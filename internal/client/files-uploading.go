package client

import (
	"context"
	"io"
	"os"
	"time"

	"nocc/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fileUploadReq struct {
	clientID   string
	invocation *Invocation
	file       *pb.FileMetadata
	fileIndex  uint32
}

func (rc *RemoteConnection) CreateUploadStream() {
	rc.reconnectWaitGroup.Add(1)
	rc.runUploadStream()
	rc.reconnectWaitGroup.Done()
}

func (rc *RemoteConnection) runUploadStream() {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	stream, err := rc.compilationServiceClient.UploadFileStream(ctx)

	if err != nil {
		rc.OnRemoteBecameUnavailable(err)

		return
	}

	invocation, err := rc.monitorClientChanForFileUploading(stream)
	if err != nil {
		// when a daemon stops listening, all streams are automatically closed
		select {
		case <-rc.quitDaemonChan:
			return
		case <-rc.reconnectChan:
			return
		default:
			break
		}

		// if something goes completely wrong and stream recreation fails, mark this remote as unavailable
		// see FilesReceiving for a comment about this error code
		if st, ok := status.FromError(err); ok {
			if st.Code() == codes.Unauthenticated {
				rc.OnRemoteBecameUnavailable(err)
				return
			}
		}

		// if some error occurred, the stream could be left in the middle of uploading
		// the easiest solution is to close this stream and to reopen a new one
		// if the server became inaccessible, recreation would fail
		logClient.Error("recreate upload stream:", err)
		time.Sleep(100 * time.Millisecond)

		go rc.CreateUploadStream()

		// theoretically, we could implement retries: if something does wrong with the network,
		// then retry uploading (by pushing req to fu.chanToUpload)
		// to do this correctly, we need to distinguish network errors vs file errors (and don't retry then)
		// for now, there are no retries: if something fails, this invocation will be executed locally
		invocation.DoneUploadFile(err)
	}
}

// monitorClientChanForFileUploading listens to chanToUpload and uploads it via stream.
// One grpc stream is used to upload multiple files consecutively.
func (rc *RemoteConnection) monitorClientChanForFileUploading(stream pb.CompilationService_UploadFileStreamClient) (*Invocation, error) {
	chunkBuf := make([]byte, 64*1024) // reusable chunk for file reading, exists until stream close

	for {
		select {
		case <-rc.quitDaemonChan:
			return nil, nil
		case <-rc.reconnectChan:
			return nil, nil

		case req := <-rc.chanToUpload:
			logClient.Info(2, "start uploading", req.file.FileSize, req.file.FileName)
			if req.file.FileSize > 64*1024 {
				logClient.Info(1, "upload large file", req.file.FileSize, req.file.FileName)
			}

			invocation := req.invocation
			err := uploadFileByChunks(stream, chunkBuf, req.file.FileName, req.clientID, invocation.sessionID, req.fileIndex)

			// such complexity of error handling prevents hanging sessions and proper stream recreation
			if err != nil {
				return invocation, err
			}

			invocation.summary.nFilesSent++
			invocation.summary.nBytesSent += int(req.file.FileSize)
			invocation.DoneUploadFile(nil)
			// continue listening, reuse the same stream to upload new files
		}
	}
}

// uploadFileByChunks is an actual implementation of piping a local client file to a server stream.
// See server.receiveUploadedFileByChunks.
func uploadFileByChunks(stream pb.CompilationService_UploadFileStreamClient, chunkBuf []byte, clientFileName string, clientID string, sessionID uint32, fileIndex uint32) error {
	fd, err := os.Open(clientFileName)
	if err != nil {
		return err
	}
	defer fd.Close()

	var n int
	var sentChunks = 0 // used to correctly handle empty files (when Read returns EOF immediately)
	for {
		n, err = fd.Read(chunkBuf)
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF && sentChunks != 0 {
			break
		}
		sentChunks++

		err = stream.Send(&pb.UploadFileChunkRequest{
			ClientID:  clientID,
			SessionID: sessionID,
			FileIndex: fileIndex,
			ChunkBody: chunkBuf[:n],
		})
		if err != nil {
			return err
		}
	}

	// when a file uploaded succeeds, the server sends just an empty confirmation packet
	// if the server couldn't save an uploaded file, it would return an error (and the stream will be recreated)
	_, err = stream.Recv()
	return err
}
