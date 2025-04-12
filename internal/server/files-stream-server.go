package server

import (
	"fmt"
	"io"
	"os"

	"nocc/pb"
)

// receiveUploadedFileByChunks is an actual implementation of piping a client stream to a local server file.
// See client.uploadFileByChunks.
func receiveUploadedFileByChunks(noccServer *NoccServer, stream pb.CompilationService_UploadFileStreamServer, firstChunk *pb.UploadFileChunkRequest, expectedBytes int, serverFileName string) (err error) {
	receivedBytes := len(firstChunk.ChunkBody)

	// we write to a tmp file and rename it to serverFileName after saving
	// it prevents races from concurrent writing to the same file
	// (this situation is possible on a slow network when a file was requested several times)
	fileTmp, err := noccServer.SrcFileCache.MakeTempFileForUploadSaving(serverFileName)
	if err == nil {
		_, err = fileTmp.Write(firstChunk.ChunkBody)
	}

	var nextChunk *pb.UploadFileChunkRequest
	for receivedBytes < expectedBytes && err == nil {
		nextChunk, err = stream.Recv()
		if err != nil { // EOF is also unexpected
			break
		}
		_, err = fileTmp.Write(nextChunk.ChunkBody)
		if nextChunk.SessionID != firstChunk.SessionID || nextChunk.FileIndex != firstChunk.FileIndex {
			err = fmt.Errorf("inconsistent stream, chunks mismatch")
		}
		receivedBytes += len(nextChunk.ChunkBody)
	}

	if fileTmp != nil {
		_ = fileTmp.Close()
		if err == nil {
			err = os.Rename(fileTmp.Name(), serverFileName)
		}
		if err != nil {
			_ = os.Remove(fileTmp.Name())
		}
	}
	return
}

// sendObjFileByChunks is an actual implementation of piping a local server file to a client stream.
// See client.receiveObjFileByChunks.
func sendObjFileByChunks(stream pb.CompilationService_RecvCompiledObjStreamServer, chunkBuf []byte, session *Session) error {
	fd, err := os.Open(session.OutputFile)
	if err != nil {
		return err
	}
	defer fd.Close()
	stat, err := fd.Stat()
	if err != nil {
		return err
	}

	err = stream.Send(&pb.RecvCompiledObjChunkReply{
		SessionID:        session.sessionID,
		CompilerExitCode: session.compilerExitCode,
		CompilerStdout:   session.compilerStdout,
		CompilerStderr:   session.compilerStderr,
		CompilerDuration: session.compilerDuration,
		FileSize:         stat.Size(),
	})
	if err != nil {
		return err
	}

	var n int
	for {
		n, err = fd.Read(chunkBuf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		err = stream.Send(&pb.RecvCompiledObjChunkReply{
			SessionID: session.sessionID,
			ChunkBody: chunkBuf[:n],
		})
		if err != nil {
			return err
		}
	}

	// after sending a compiled obj, the client doesn't respond in any way,
	// so we don't call stream.Recv(), the stream is already ready to send other objs
	return nil
}

func sendFailureMessage(stream pb.CompilationService_RecvCompiledObjStreamServer, session *Session) error {
	return stream.Send(&pb.RecvCompiledObjChunkReply{
		SessionID:        session.sessionID,
		CompilerExitCode: int32(session.compilerExitCode),
		CompilerStdout:   session.compilerStdout,
		CompilerStderr:   session.compilerStderr,
		CompilerDuration: session.compilerDuration,
	})
}
