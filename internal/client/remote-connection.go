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

	remoteHostPort string
	remoteHost     string // for console output and logs, just IP is more pretty
	isUnavailable  atomic.Bool

	grpcClient               *GRPCClient
	compilationServiceClient pb.CompilationServiceClient

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

func MakeRemoteConnection(daemon *Daemon, remoteHostPort string, socksProxyAddr string) (*RemoteConnection, error) {
	grpcClient, err := MakeGRPCClient(remoteHostPort, socksProxyAddr)

	remote := &RemoteConnection{
		quitDaemonChan:           daemon.quitDaemonChan,
		remoteHostPort:           remoteHostPort,
		remoteHost:               ExtractRemoteHostWithoutPort(remoteHostPort),
		grpcClient:               grpcClient,
		clientID:                 daemon.clientID,
		chanToUpload:             make(chan fileUploadReq, 50),
		compilationServiceClient: pb.NewCompilationServiceClient(grpcClient.connection),
	}

	if err != nil {
		return remote, err
	}

	return remote, nil
}

func (remote *RemoteConnection) StartFileMonitoring(daemon *Daemon) {
	go remote.CreateUploadStream()
	go remote.CreateReceiveStream(daemon.FindInvocationBySessionID)
}

func (remote *RemoteConnection) StartClientRequest() error {
	ctxConnect, cancelFunc := context.WithTimeout(context.Background(), 5000*time.Millisecond)
	defer cancelFunc()
	_, err := remote.compilationServiceClient.StartClient(ctxConnect, &pb.StartClientRequest{
		ClientID:      remote.clientID,
		ClientVersion: common.GetVersion(),
	})

	return err
}

func (remote *RemoteConnection) OnRemoteBecameUnavailable(reason error) {
	if !remote.isUnavailable.Swap(true) {
		logClient.Error("remote", remote.remoteHostPort, "became unavailable:", reason)
	}
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
	remote.grpcClient.Clear()
}
