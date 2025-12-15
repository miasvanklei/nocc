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

type StreamContext struct {
	ctx context.Context
	cancelFunc context.CancelFunc
}

func CreateStreamContext() *StreamContext {
	ctx, cancelFunc := context.WithCancel(context.Background())

	return &StreamContext {
		ctx: ctx,
		cancelFunc: cancelFunc,
	}
}

// RemoteConnection represents a state of a current process related to remote execution.
// It also has methods that call grpc, so this module is close to protobuf.
// Daemon makes one RemoteConnection to one server — for server.Session creation, files uploading, obj receiving.
// If a remote is not available on daemon start (on becomes unavailable in the middle),
// then all invocations that should be sent to that remote are executed locally within a daemon.
type RemoteConnection struct {
	chanToUpload   chan fileUploadReq
	quitDaemonChan chan int
	reconnectChan  chan struct{}
	receiveStreamContext *StreamContext
	uploadStreamContext *StreamContext

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
		go remote.tryReconnectRemote()
	}
}

func (remote *RemoteConnection) tryReconnectRemote() {
	timeout := time.After(10 * time.Millisecond)
	restarttimeout := time.After(5 * time.Minute)

	remote.receiveStreamContext.cancelFunc()
	remote.uploadStreamContext.cancelFunc()
	remote.grpcClient.Clear()

	reconnect: for {
		select {
		case <-remote.quitDaemonChan:
			return
		case <-restarttimeout:
			break reconnect
		case <-timeout:
			timeout = remote.reconnectRemote(false)
			if timeout == nil {
				return
			}
		}
	}

	for {
		select {
		case <-remote.quitDaemonChan:
			return
		case <-restarttimeout:
			restarttimeout = remote.reconnectRemote(true)
			if restarttimeout == nil {
				return
			}
		}
	}
}

func (remote *RemoteConnection) reconnectRemote(start bool) <-chan time.Time {
	err  := remote.SetupConnection(start)
	if err == nil {
		logClient.Error("Reconnected stream")
		remote.isUnavailable.Store(false)
		return nil
	}
	logClient.Error("remote", remote.remoteHostPort, "unable to reconnect:", err)

	return time.After(5 * time.Second)
}

func (remote *RemoteConnection) SetupConnection(startclient bool) error {
	remote.reconnectChan = make(chan struct{})

	grpcClient, err := MakeGRPCClient(remote.remoteHostPort, remote.socksProxyAddr)
	if err != nil {
		return err
	}

	compilationServiceClient := pb.NewCompilationServiceClient(grpcClient.connection)
	if startclient {
		err = StartClientRequest(compilationServiceClient, remote.clientID)
		if err != nil {
			return err
		}
	}

	remote.grpcClient = grpcClient
	remote.compilationServiceClient = compilationServiceClient

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = remote.KeepAlive(ctx)
	if err != nil {
		return err
	}

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

func (remote *RemoteConnection) KeepAlive(ctxSmallTimeout context.Context) error {
	_, err := remote.compilationServiceClient.KeepAlive(ctxSmallTimeout, &pb.KeepAliveRequest{
		ClientID: remote.clientID,
	})

	return err
}

func (remote *RemoteConnection) VerifyAlive() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if remote.isUnavailable.Load() {
		return
	}
	err := remote.KeepAlive(ctx)
	if err != nil {
		logClient.Error("keep alive failed")
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
