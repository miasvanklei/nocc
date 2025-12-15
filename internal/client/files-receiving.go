package client

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"nocc/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (rc *RemoteConnection) CreateReceiveStream() {
	rc.receiveStreamContext = CreateStreamContext()
	rc.runReceiveStream()
}

func (rc *RemoteConnection) runReceiveStream() {
	defer rc.receiveStreamContext.cancelFunc()

	stream, err := rc.compilationServiceClient.RecvCompiledObjStream(rc.receiveStreamContext.ctx,
		&pb.OpenReceiveStreamRequest{ClientID: rc.clientID},
	)

	if err != nil {
		rc.OnRemoteBecameUnavailable(err)
		return
	}

	needRecreateStream, err := rc.monitorRemoteStreamForObjReceiving(stream)

	if err == nil {
		return
	}

	if !needRecreateStream {
		// when a daemon stops listening, all streams are automatically closed
		select {
		case <-rc.quitDaemonChan:
			return
		case <-rc.reconnectChan:
			return
		default:
			break
		}

		// grpc stream creation doesn't wait for ack, that's why
		// if a stream couldn't be created at all, we know this only on Recv() failure
		if st, ok := status.FromError(err); ok {
			if st.Code() == codes.Unauthenticated {
				rc.OnRemoteBecameUnavailable(err)
				return
			}
		}

		// if something weird occurs, the server fails to send a chunk to a stream
		// it closes the stream and includes metadata to trailer
		// here, on the client size, we mark this invocation as errored, they'll be compiled locally
		// this prevents invocations from hanging — at least when a network works as expected
		mdSession := stream.Trailer().Get("sessionID")
		if len(mdSession) == 1 {
			sessionID, _ := strconv.Atoi(mdSession[0])
			invocation := rc.findInvocation(uint32(sessionID))
			if invocation != nil {
				invocation.DoneRecvObj(err, false)
			}
		}
	}

	// NB: there are rpc errors that are not visible to the server-side (like codes.ResourceExhausted)
	// in this case, the server thinks that .o was sent, but the client gets an error without metadata
	// such invocations will be cleared later, see PeriodicallyInterruptHangedInvocations()
	logClient.Error("recreate recv stream:", err)
	time.Sleep(100 * time.Millisecond)

	go rc.CreateReceiveStream()
}

// monitorRemoteStreamForObjReceiving listens to a grpc receiving stream and handles .o files sent by a remote.
// When a next .o is ready on remote, it sends it to a stream.
// One stream is used to receive multiple .o files consecutively.
// If compilation exits with non-zero code, the same stream is used to send error details.
// See RemoteConnection.WaitForCompiledObj.
func (rc *RemoteConnection) monitorRemoteStreamForObjReceiving(stream pb.CompilationService_RecvCompiledObjStreamClient) (bool, error) {
	for {
		// when a daemon stops listening, all streams are automatically closed
		select {
		case <-rc.quitDaemonChan:
			return false, nil
		case <-rc.reconnectChan:
			return false, nil
		default:
		}

		firstChunk, err := stream.Recv()

		if err != nil {
			return false, err
		}

		invocation := rc.findInvocation(firstChunk.SessionID)
		if invocation == nil {
			logClient.Error("can't find invocation for obj", "sessionID", firstChunk.SessionID)
			continue
		}

		invocation.compilerExitCode = int(firstChunk.CompilerExitCode)
		invocation.compilerStdout = firstChunk.CompilerStdout
		invocation.compilerStderr = firstChunk.CompilerStderr
		invocation.compilerDuration = firstChunk.CompilerDuration
		invocation.summary.nBytesReceived += int(firstChunk.FileSize)

		// non-zero exitCode means either a bug in the source code or a compiler errror
		if firstChunk.CompilerExitCode != 0 {
			invocation.DoneRecvObj(nil, false)
			continue
		}

		needRecreateStream, err := receiveObjFileByChunks(stream, invocation, int(firstChunk.FileSize))
		invocation.DoneRecvObj(err, false)

		if err != nil {
			return needRecreateStream, err
		}

		// continue waiting for next .o files pushed by the remote over the same stream
	}
}

// receiveObjFileByChunks is an actual implementation of saving a server stream to a local client .o file.
// See server.sendObjFileByChunks.
func receiveObjFileByChunks(stream pb.CompilationService_RecvCompiledObjStreamClient, invocation *Invocation, fileSize int) (bool, error) {
	var errWrite error
	var errRecv error
	var receivedBytes int

	fileTmp, errWrite := invocation.OpenTempFile(invocation.objOutFile)

	var nextChunk *pb.RecvCompiledObjChunkReply
	for receivedBytes < fileSize {
		nextChunk, errRecv = stream.Recv()
		if errRecv != nil { // EOF is also unexpected
			break
		}
		if errWrite == nil {
			_, errWrite = fileTmp.Write(nextChunk.ChunkBody)
		}
		if nextChunk.SessionID != invocation.sessionID {
			errRecv = fmt.Errorf("inconsistent stream, chunks mismatch")
			break
		}
		receivedBytes += len(nextChunk.ChunkBody)
	}

	if fileTmp != nil {
		_ = fileTmp.Close()
		if errWrite == nil {
			errWrite = os.Rename(fileTmp.Name(), invocation.objOutFile)
		}
		_ = os.Remove(fileTmp.Name())
	}

	switch {
	case errRecv != nil:
		return true, errRecv// "true" to recreate recv stream
	case errWrite != nil:
		return false, errWrite // "false" means that the stream is ok, there was just a problem of saving a file
	default:
		return false, nil
	}
}
