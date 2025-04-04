package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"nocc/internal/common"
	"nocc/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NoccServer stores all server's state and serves grpc requests.
// Remember, that in practice, the nocc-server process is started on different physical nodes (shards),
// and nocc clients balance between them based on .cpp basename.
type NoccServer struct {
	pb.UnimplementedCompilationServiceServer
	GRPCServer *grpc.Server

	Cron *Cron

	ActiveClients    *ClientsStorage
	CompilerLauncher *CompilerLauncher

	SrcFileCache *SrcFileCache
	ObjFileCache *ObjFileCache
}

func launchCompilerOnServerOnReadySessions(noccServer *NoccServer, client *Client) {
	for _, session := range client.GetSessionsNotStartedCompilation() {
		session.StartCompilingObjIfPossible(client, noccServer.CompilerLauncher, noccServer.ObjFileCache)
	}
}

// StartGRPCListening is an entrypoint called from main() of nocc-server.
// It either returns an error or starts processing grpc requests and never ends.
func (s *NoccServer) StartGRPCListening(listenAddr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	go s.Cron.StartCron()

	logServer.Info(0, "nocc-server started")

	var rLimit syscall.Rlimit
	_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	logServer.Info(0, "env:", "listenAddr", listenAddr, "; ulimit -n", rLimit.Cur, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())

	return listener, s.GRPCServer.Serve(listener)
}

// QuitServerGracefully closes all active clients and stops accepting new connections.
// After it, StartGRPCListening returns, and main() continues.
func (s *NoccServer) QuitServerGracefully() {
	logServer.Info(0, "graceful stop...")

	s.Cron.StopCron()
	s.ActiveClients.StopAllClients()
	s.GRPCServer.GracefulStop()
}

// StartClient is a grpc handler.
// When a nocc-daemon starts, it sends this query — before starting any session.
// So, one client == one running nocc-daemon. All clients have unique clientID.
// When a nocc-daemon exits, it sends StopClient (or when it dies unexpectedly, a client is deleted after timeout).
func (s *NoccServer) StartClient(_ context.Context, in *pb.StartClientRequest) (*pb.StartClientReply, error) {
	client, err := s.ActiveClients.OnClientConnected(in.ClientID)
	if err != nil {
		return nil, err
	}

	logServer.Info(0, "new client", "clientID", client.clientID, "version", in.ClientVersion, "; nClients", s.ActiveClients.ActiveCount())

	return &pb.StartClientReply{}, nil
}

// StartCompilationSession is a grpc handler.
// A client sends this request providing sha256 of a .cpp file name and all its dependencies (.h/.nocc-pch/etc.).
// A server responds, what dependencies are missing (needed to be uploaded from the client).
// See comments in server.Session.
func (s *NoccServer) StartCompilationSession(_ context.Context, in *pb.StartCompilationSessionRequest) (*pb.StartCompilationSessionReply, error) {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client == nil {
		logServer.Error("unauthenticated client on session start", "clientID", in.ClientID)
		return nil, status.Errorf(codes.Unauthenticated, "clientID %s not found; probably, the server was restarted just now", in.ClientID)
	}

	session, err := CreateNewSession(in, client)
	if err != nil {
		logServer.Error("failed to open session", "clientID", client.clientID, "sessionID", in.SessionID, err)
		return nil, err
	}

	// optimistic path: this .o has already been compiled earlier and exists in obj cache
	// then we don't need to upload files from the client (and even don't need to link them from src cache)
	// respond that we are waiting 0 files, and the client would immediately request for a compiled obj
	// it's mostly a moment of optimization: avoid calling os.Link from src cache to working dir
	session.objCacheKey = s.ObjFileCache.MakeObjCacheKey(session.compilerName, in.Args, session.files, session.InputFile)
	if pathInObjCache := s.ObjFileCache.LookupInCache(session.objCacheKey); len(pathInObjCache) != 0 {
		session.objCacheExists = true
		session.OutputFile = pathInObjCache // stream back this file directly
		session.compilationStarted.Store(1) // client.GetSessionsNotStartedCompilation() will not return it

		logServer.Info(0, "started", "sessionID", session.sessionID, "clientID", client.clientID, "from obj cache", session.InputFile)
		client.RegisterCreatedSession(session)
		client.PushToClientReadyChannel(session)

		return &pb.StartCompilationSessionReply{}, nil
	}

	// otherwise, we detect files that don't exist in src cache and request a client to upload them
	// before restoring from src cache, ensure that all client dirs structure is mirrored to workingDir
	client.MkdirAllForSession(session)

	// here we deal with concurrency:
	// one nocc client creates multiple sessions that depend on equal h files
	// our goal is to let the client upload file X only once:
	// the first session is responded "need X to be uploaded", whereas other sessions just wait
	// note, that if X is in src-cache, it's just hard linked from there to serverFileName
	fileIndexesToUpload := make([]uint32, 0, len(session.files))
	for index, file := range session.files {
		if file.state.CompareAndSwap(fsFileStateJustCreated, fsFileStateUploading) {
			file.uploadStartTime = time.Now()

			if s.SrcFileCache.CreateHardLinkFromCache(file.serverFileName, file.fileSHA256) {
				logServer.Info(2, "file", file.serverFileName, "is in src-cache, no need to upload")
				file.state.Store(fsFileStateUploaded)

				continue
			}

			logServer.Info(1, "fs created->uploading", "sessionID", session.sessionID, client.MapServerAbsToClientFileName(file.serverFileName))
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))
		} else if file.state.CompareAndSwap(fsFileStateUploading, fsFileStateUploading) {
			if !client.IsFileUploadHanged(file) { // this file is already requested to be uploaded
				continue
			}

			file.uploadStartTime = time.Now()

			logServer.Error("fs uploading->uploading", "sessionID", session.sessionID, file.serverFileName, "(re-requested because previous upload hanged)")
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))
		} else if file.state.CompareAndSwap(fsFileStateUploadError, fsFileStateUploading) {
			file.uploadStartTime = time.Now()

			logServer.Error("fs error->uploading", "sessionID", session.sessionID, file.serverFileName, "(re-requested because previous upload error)")
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))
		}
	}

	logServer.Info(0, "started", "sessionID", session.sessionID, "clientID", client.clientID, "waiting", len(fileIndexesToUpload), "uploads", session.InputFile)
	client.RegisterCreatedSession(session)
	launchCompilerOnServerOnReadySessions(s, client) // other sessions could also be waiting for files in src-cache

	return &pb.StartCompilationSessionReply{
		FileIndexesToUpload: fileIndexesToUpload,
	}, nil
}

// UploadFileStream handles a grpc stream created on a client start.
// When a client needs to upload a file, a client pushes it to the stream: so, a client is the initiator.
// Multiple .h/.cpp files are transferred over a single stream, one by one.
// This stream is alive until any error happens. On upload error, it's closed. A client recreates it on demand.
// See client.FilesUploading.
func (s *NoccServer) UploadFileStream(stream pb.CompilationService_UploadFileStreamServer) error {
	for {
		firstChunk, err := stream.Recv()
		if err != nil {
			if !errors.Is(stream.Context().Err(), context.Canceled) {
				logServer.Error("stream receive error:", err.Error())
			}
			return err
		}

		client := s.ActiveClients.GetClient(firstChunk.ClientID)
		if client == nil {
			logServer.Error("unauthenticated client on upload stream", "clientID", firstChunk.ClientID)
			return status.Errorf(codes.Unauthenticated, "client %s not found", firstChunk.ClientID)
		}
		client.lastSeen = time.Now()

		session := client.GetSession(firstChunk.SessionID)
		if session == nil || firstChunk.FileIndex >= uint32(len(session.files)) {
			logServer.Error("bad sessionID/fileIndex on upload", "clientID", client.clientID, "sessionID", firstChunk.SessionID)
			return fmt.Errorf("unknown sessionID %d with index %d", firstChunk.SessionID, firstChunk.FileIndex)
		}

		file := session.files[firstChunk.FileIndex]
		clientFileName := client.MapServerAbsToClientFileName(file.serverFileName)

		if file.fileSize > 256*1024 {
			logServer.Info(0, "start receiving large file", file.fileSize, "sessionID", session.sessionID, clientFileName)
		}

		if err := receiveUploadedFileByChunks(s, stream, firstChunk, int(file.fileSize), file.serverFileName); err != nil {
			file.state.Store(fsFileStateUploadError)
			logServer.Error("fs uploading->error", "sessionID", session.sessionID, clientFileName, err)
			return fmt.Errorf("can't receive file %q: %v", clientFileName, err)
		}

		logServer.Info(2, "received", file.fileSize, "bytes", "sessionID", session.sessionID, clientFileName)
		if file.fileSize > 256*1024 {
			logServer.Info(0, "large file received", file.fileSize, "sessionID", session.sessionID, clientFileName)
		}

		file.state.Store(fsFileStateUploaded)
		logServer.Info(1, "fs uploading->uploaded", "sessionID", session.sessionID, clientFileName)
		launchCompilerOnServerOnReadySessions(s, client) // other sessions could also be waiting for this file, we should check all
		_ = stream.Send(&pb.UploadFileReply{})
		_ = s.SrcFileCache.SaveFileToCache(file.serverFileName, path.Base(file.serverFileName), file.fileSHA256, file.fileSize)

		// start waiting for the next file over the same stream
	}
}

// RecvCompiledObjStream handles a grpc stream created on a client start.
// When a .o file on the server is ready, it to the stream: so, a server is the initiator.
// Multiple .o files are transferred over a single stream, one by one.
// This stream is alive until any error happens. On error, it's closed. A client recreates it.
// See client.FilesReceiving.
func (s *NoccServer) RecvCompiledObjStream(in *pb.OpenReceiveStreamRequest, stream pb.CompilationService_RecvCompiledObjStreamServer) error {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client == nil {
		logServer.Error("unauthenticated client on recv stream", "clientID", in.ClientID)
		return status.Errorf(codes.Unauthenticated, "client %s not found", in.ClientID)
	}
	chunkBuf := make([]byte, 64*1024) // reusable chunk for file reading, exists until stream close

	// errors occur very rarely (if a client disconnects or something strange happens)
	// the easiest solution is just to close this stream
	// if a client is alive, it will open a new stream
	// if a trailer "sessionID" won't reach a client,
	// it would still think that a session is in the process of remote compilation
	// and will clear it after some timeout
	onError := func(sessionID uint32, format string, a ...interface{}) error {
		stream.SetTrailer(metadata.Pairs("sessionID", strconv.Itoa(int(sessionID))))
		err := fmt.Errorf(format, a...)
		logServer.Error(err)
		return err
	}

	for {
		select {
		case <-client.chanDisconnected:
			return nil

		case session := <-client.chanReadySessions:
			client.lastSeen = time.Now()

			if session.compilerExitCode != 0 {
				err := stream.Send(&pb.RecvCompiledObjChunkReply{
					SessionID:        session.sessionID,
					CompilerExitCode: session.compilerExitCode,
					CompilerStdout:   session.compilerStdout,
					CompilerStderr:   session.compilerStderr,
					CompilerDuration: session.compilerDuration,
				})
				if err != nil {
					return onError(session.sessionID, "can't send obj non-0 reply sessionID %d clientID %s %v", session.sessionID, client.clientID, err)
				}
			} else {
				logServer.Info(0, "send obj file", "sessionID", session.sessionID, "clientID", client.clientID, "compilerDuration", session.compilerDuration, session.OutputFile)
				err := sendObjFileByChunks(stream, chunkBuf, session)
				if err != nil {
					return onError(session.sessionID, "can't send obj file %s sessionID %d clientID %s %v", session.OutputFile, session.sessionID, client.clientID, err)
				}
			}

			client.CloseSession(session)
			logServer.Info(2, "close", "sessionID", session.sessionID, "clientID", client.clientID)
			// start waiting for the next ready session
		}
	}
}

// StopClient is a grpc handler. See StartClient for comments.
func (s *NoccServer) StopClient(_ context.Context, in *pb.StopClientRequest) (*pb.StopClientReply, error) {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client != nil {
		logServer.Info(0, "client disconnected", "clientID", client.clientID, "; nClients", s.ActiveClients.ActiveCount()-1)
		// removing working dir could take some time, but respond immediately
		go s.ActiveClients.DeleteClient(client)
	}

	return &pb.StopClientReply{}, nil
}
