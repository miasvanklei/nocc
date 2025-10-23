package client

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"nocc/internal/common"
	"nocc/pb"
)

// RemoteConnection represents a state of a current process related to remote execution.
// It also has methods that call grpc, so this module is close to protobuf.
// Daemon makes one RemoteConnection to one server — for server.Session creation, files uploading, obj receiving.
// If a remote is not available on daemon start (on becomes unavailable in the middle),
// then all invocations that should be sent to that remote are executed locally within a daemon.
type RemoteConnection struct {
	chanToUpload   chan fileUploadReq
	quitDaemonChan chan int
	reconnectChan  chan struct{}

	socksProxyAddr string
	remoteHostPort string
	remoteHost     string // for console output and logs, just IP is more pretty
	isUnavailable  atomic.Bool

	grpcClient               *GRPCClient
	compilationServiceClient pb.CompilationServiceClient
	findInvocation           func(uint32) *Invocation

	clientID     string // = Daemon.clientID
	hostUserName string // = Daemon.hostUserName
}

func ExtractRemoteHostWithoutPort(remoteHostPort string) (remoteHost string) {
	remoteHost = remoteHostPort
	if idx := strings.Index(remoteHostPort, ":"); idx != -1 {
		remoteHost = remoteHostPort[:idx]
	}
	return
}

func MakeRemoteConnection(daemon *Daemon, remoteHostPort string, socksProxyAddr string) *RemoteConnection {
	remote := &RemoteConnection{
		quitDaemonChan: daemon.quitDaemonChan,
		socksProxyAddr: socksProxyAddr,
		remoteHostPort: remoteHostPort,
		remoteHost:     ExtractRemoteHostWithoutPort(remoteHostPort),
		clientID:       daemon.clientID,
		chanToUpload:   make(chan fileUploadReq, 50),
		findInvocation: daemon.FindInvocationBySessionID,
	}

	return remote
}

func (remote *RemoteConnection) startFileMonitoring() {
	go remote.CreateUploadStream()
	go remote.CreateReceiveStream()
}

func StartClientRequest(csc pb.CompilationServiceClient, clientID string) error {
	ctxConnect, cancelFunc := context.WithTimeout(context.Background(), 5000*time.Millisecond)
	defer cancelFunc()
	_, err := csc.StartClient(ctxConnect, &pb.StartClientRequest{
		ClientID:      clientID,
		ClientVersion: common.GetVersion(),
	})

	return err
}

func (remote *RemoteConnection) OnRemoteBecameUnavailable(reason error) {
	if !remote.isUnavailable.Swap(true) {
		close(remote.reconnectChan)
		logClient.Error("remote", remote.remoteHostPort, "became unavailable:", reason)
		go remote.reconnectRemote()
	}
}

func (remote *RemoteConnection) reconnectRemote() {
	timeout := time.After(10 * time.Millisecond)
	for {
		select {
		case <-remote.quitDaemonChan:
			return

		case <-timeout:
			err := remote.SetupConnection()
			if err == nil {
				remote.isUnavailable.Store(false)
				return
			}
			timeout = time.After(10 * time.Second)
			logClient.Error("remote", remote.remoteHostPort, "unable to reconnect:", err)
		}
	}
}

func (remote *RemoteConnection) SetupConnection() error {
	remote.reconnectChan = make(chan struct{})

	grpcClient, err := MakeGRPCClient(remote.remoteHostPort, remote.socksProxyAddr)
	compilationServiceClient := pb.NewCompilationServiceClient(grpcClient.connection)
	if err != nil {
		return err
	}

	err = StartClientRequest(compilationServiceClient, remote.clientID)
	if err != nil {
		return err
	}

	remote.grpcClient = grpcClient
	remote.compilationServiceClient = compilationServiceClient

	remote.startFileMonitoring()
	return nil
}

// StartCompilationSession starts a session on the remote:
// one `nocc` Invocation for cpp compilation == one server.Session, by design.
// As an input, we send metadata about all dependencies needed for a .cpp to be compiled (.h/.nocc-pch/etc.).
// As an output, the remote responds with files that are missing and needed to be uploaded.
func (remote *RemoteConnection) StartCompilationSession(invocation *Invocation, requiredFiles []*pb.FileMetadata, requiredPchFile *pb.FileMetadata) ([]uint32, error) {
	if remote.isUnavailable.Load() {
		return nil, fmt.Errorf("remote %s is unavailable", remote.remoteHost)
	}

	startSessionReply, err := remote.compilationServiceClient.StartCompilationSession(
		remote.grpcClient.callContext,
		&pb.StartCompilationSessionRequest{
			ClientID:        remote.clientID,
			SessionID:       invocation.sessionID,
			InputFile:       invocation.cppInFile,
			Compiler:        invocation.compilerName,
			CompilerArgs:    invocation.compilerArgs,
			RequiredFiles:   requiredFiles,
			RequiredPchFile: requiredPchFile,
		})

	if err != nil {
		return nil, err
	}

	return startSessionReply.FileIndexesToUpload, nil
}

func (remote *RemoteConnection) StartUploadingFileToRemote(invocation *Invocation, file *pb.FileMetadata, fileIndex uint32) {
	remote.chanToUpload <- fileUploadReq{
		clientID:   remote.clientID,
		invocation: invocation,
		file:       file,
		fileIndex:  fileIndex,
	}
}

// UploadFilesToRemote uploads files to the remote in parallel and finishes after all of them are done.
func (remote *RemoteConnection) UploadFilesToRemote(invocation *Invocation, requiredFiles []*pb.FileMetadata, fileIndexesToUpload []uint32) error {
	invocation.waitUploads.Store(int32(len(fileIndexesToUpload)))
	invocation.wgUpload.Add(int(invocation.waitUploads.Load()))

	for _, fileIndex := range fileIndexesToUpload {
		remote.StartUploadingFileToRemote(invocation, requiredFiles[fileIndex], fileIndex)
	}

	invocation.wgUpload.Wait()
	return invocation.err
}

// WaitForCompiledObj returns when the resulting .o file is compiled on remote, downloaded and saved on client.
// We don't send any request here, just wait: after all uploads finish, the remote starts compiling .cpp.
// When .o is ready, the remote pushes it to a receiving stream, and wgRecv is done.
// If compilation exits with non-zero code, the same stream is used to send error details.
// See FilesReceiving.
func (remote *RemoteConnection) WaitForCompiledObj(invocation *Invocation) (exitCode int, stdout []byte, stderr []byte, err error) {
	invocation.wgRecv.Wait()

	return invocation.compilerExitCode, invocation.compilerStdout, invocation.compilerStderr, invocation.err
}

func (remote *RemoteConnection) KeepAlive(ctxSmallTimeout context.Context) {
	if remote.isUnavailable.Load() {
		return
	}

	_, err := remote.compilationServiceClient.KeepAlive(ctxSmallTimeout, &pb.KeepAliveRequest{
		ClientID: remote.clientID,
	})

	if err != nil {
		logClient.Error("keep alive failed:", err)
		remote.OnRemoteBecameUnavailable(err)
	}
}

func (remote *RemoteConnection) SendStopClient(ctxSmallTimeout context.Context) {
	if remote.isUnavailable.Load() {
		return
	}
	_, _ = remote.compilationServiceClient.StopClient(
		ctxSmallTimeout,
		&pb.StopClientRequest{
			ClientID: remote.clientID,
		})
}

func (remote *RemoteConnection) Clear() {
	remote.compilationServiceClient = nil
	if remote.grpcClient != nil {
		remote.grpcClient.Clear()
	}
}
