package server

import (
	"fmt"
	"sync/atomic"

	"nocc/internal/common"
	"nocc/pb"
)

// Session is created when a client requests to compile a .cpp file.
// It's a server representation of client.Invocation.
// A lifetime of one Session is the following:
// 1) a session is created, provided a .cpp file and all .h/.nocc-pch/etc. dependencies
// 2) files that don't exist on this server are uploaded by the client
// 3) the C++ compiler (compiler) is launched
// 4) the client downloads .o
// 5) the session is closed automatically
// Steps 2-5 can be skipped if a compiled .o already exists in ObjFileCache.
type Session struct {
	sessionID uint32

	InputFile     string // as-is from a client cmd line (relative to compilerCwd on a server-side)
	OutputFile    string // inside /tmp/nocc/obj/compiler-out, or directly in /tmp/nocc/obj/obj-cache if taken from cache
	compilerCwd   string // cwd for the compiler on a server-side (= client.workingDir + clientCwd)
	compilerName  string // g++ / clang / etc.
	compilerArgs  []string
	compilerIDirs []string

	files   []*fileInClientDir
	pchFile *fileInClientDir

	objCacheKey        common.SHA256
	objCacheExists     bool
	compilationStarted atomic.Int32

	compilerExitCode int32
	compilerStdout   []byte
	compilerStderr   []byte
	compilerDuration int32
}

func CreateNewSession(in *pb.StartCompilationSessionRequest, client *Client) (*Session, error) {
	newSession := &Session{
		sessionID:     in.SessionID,
		files:         make([]*fileInClientDir, len(in.RequiredFiles)),
		compilerName:  in.Compiler,
		InputFile:     in.InputFile,
		compilerCwd:   in.Cwd,
		compilerArgs:  in.Args,
		compilerIDirs: in.IDirs,
	}

	for index, meta := range in.RequiredFiles {
		file, err := startUsingFileInSession(client, meta)
		if err != nil {
			return nil, err
		}
		newSession.files[index] = file
	}

	// if the client sends a pch file, we need to start using it in the session
	if in.RequiredPchFile != nil {
		file, err := startUsingFileInSession(client, in.RequiredPchFile)
		if err != nil {
			return nil, err
		}
		newSession.pchFile = file
		newSession.files = append(newSession.files, file)
	}

	// note, that we don't add newSession to client.sessions: it's just created, not registered
	// (so, it won't be enumerated in a loop inside GetSessionsNotStartedCompilation until registered)

	return newSession, nil
}

// the only reason why a session can't be created is a dependency conflict:
// previously, a client reported that clientFileName has sha256=v1, and now it sends sha256=v2
func startUsingFileInSession(client *Client, meta *pb.FileMetadata) (*fileInClientDir, error) {
	fileSHA256 := common.SHA256{B0_7: meta.SHA256_B0_7, B8_15: meta.SHA256_B8_15, B16_23: meta.SHA256_B16_23, B24_31: meta.SHA256_B24_31}
	return client.StartUsingFileInSession(meta.FileName, meta.FileSize, fileSHA256)
}

// StartCompilingObjIfPossible executes compiler if all dependent files (.cpp/.h/.nocc-pch/etc.) are ready.
// They have either been uploaded by the client or already taken from src cache.
// Note, that it's called for sessions that don't exist in obj cache.
func (session *Session) StartCompilingObjIfPossible(client *Client, compilerLauncher *CompilerLauncher, objFileCache *ObjFileCache) {
	for _, file := range session.files {
		if file.state.Load() == fsFileStateUploading {
			return
		}
	}

	if session.pchFile != nil {
		go session.StartCompilingPchIfPossible(client, compilerLauncher, objFileCache)
	} else {
		go compilerLauncher.LaunchCompilerWhenPossible(client, session, objFileCache)
	}
}

func (session *Session) StartCompilingPchIfPossible(client *Client, compilerLauncher *CompilerLauncher, objFileCache *ObjFileCache) {
	if session.pchFile.state.Load() == fsFileStatePchCompiled {
		logServer.Info(1, "pch file already compiled", session.sessionID)
		compilerLauncher.LaunchCompilerWhenPossible(client, session, objFileCache)
	} else if session.pchFile.state.CompareAndSwap(fsFileStateUploaded, fsFileStatePchCompiling) {
		logServer.Info(1, "compiling pch file", session.pchFile.serverFileName)

		err := compilerLauncher.LaunchPchWhenPossible(client, session, objFileCache)
		if err == nil {
			session.pchFile.state.Store(fsFileStatePchCompiled)
			logServer.Error("pch file compiled", session.pchFile.serverFileName)
		} else {
			logServer.Error("failed to compile pch file")
			session.pchFile.state.Store(fsFileStatePchCompileError)
		}

		for _, session := range client.GetSessionsNotStartedCompilation() {
			session.StartCompilingObjIfPossible(client, compilerLauncher, objFileCache)
		}
	} else if session.pchFile.state.Load() == fsFileStatePchCompileError {
		logServer.Error("pch file compilation failed, not continuing")
		session.compilerStderr = fmt.Appendln(nil, fmt.Errorf("pch file compilation failed, not continuing"))
		session.compilerExitCode = -1
		client.PushToClientReadyChannel(session)
	}
}
