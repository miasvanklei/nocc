package server

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
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

	InputFile    string // as-is from a client cmd line (relative to compilerCwd on a server-side)
	OutputFile   string // inside ${ObjCacheDir}/compiler-out, or directly in ${ObjCacheDir}/obj-cache if taken from cache
	compilerName string   // g++ / clang / etc.
	compilerArgs []string // all args for the compiler, including -I/-isystem/-L

	files   []*fileInClientDir
	pchFile *fileInClientDir

	objCacheKey        common.SHA256
	objCacheExists     bool
	compilationStarted atomic.Int32

	compilerExitCode int
	compilerStdout   []byte
	compilerStderr   []byte
	compilerDuration int32
	interrupted      bool

	interruptchan chan struct{}
}

func CreateNewSession(in *pb.StartCompilationSessionRequest, client *Client) (*Session, error) {
	newSession := &Session{
		sessionID:     in.SessionID,
		compilerName:  in.Compiler,
		compilerArgs:  in.CompilerArgs,
		InputFile:     in.InputFile,
		files:         make([]*fileInClientDir, len(in.RequiredFiles)),
		interruptchan: make(chan struct{}),
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
	return client.StartUsingFileInSession(meta.FileName, meta.FileSize, meta.IsSymlink, meta.SymlinkTarget, fileSHA256)
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
		go session.LaunchCompilerWhenPossible(client, compilerLauncher, objFileCache)
	}
}

func (session *Session) StartCompilingPchIfPossible(client *Client, compilerLauncher *CompilerLauncher, objFileCache *ObjFileCache) {
	if session.pchFile.state.Load() == fsFileStatePchCompiled {
		logServer.Info(1, "pch file already compiled", session.sessionID)
		session.LaunchCompilerWhenPossible(client, compilerLauncher, objFileCache)
	} else if session.pchFile.state.CompareAndSwap(fsFileStateUploaded, fsFileStatePchCompiling) {
		logServer.Info(1, "compiling pch file", session.pchFile.serverFileName)

		interrupted, err := session.LaunchPchWhenPossible(client, compilerLauncher, objFileCache)
		if interrupted {
			session.pchFile.state.Store(fsFileStatePchCompileInterrupted)
		} else if err == nil {
			session.pchFile.state.Store(fsFileStatePchCompiled)
			logServer.Info(1, "pch file compiled", session.pchFile.serverFileName)
		} else {
			logServer.Error(err.Error())
			session.pchFile.state.Store(fsFileStatePchCompileError)
		}

		for _, session := range client.GetSessionsNotStartedCompilation() {
			session.StartCompilingObjIfPossible(client, compilerLauncher, objFileCache)
		}
	} else if session.pchFile.state.Load() == fsFileStatePchCompileError {
		logServer.Error("pch file compilation failed, not continuing")
		session.compilerStderr = fmt.Appendln(nil, fmt.Errorf("compilation of pch file %s failed, not continuing", session.pchFile.serverFileName))
		session.compilerExitCode = -1
		client.PushToClientReadyChannel(session)
	} else if session.pchFile.state.Load() == fsFileStatePchCompileInterrupted {
		session.interrupted = true
		client.PushToClientReadyChannel(session)
	}

}

// LaunchCompilerWhenPossible launches the compiler on a server managing a waiting queue.
// The purpose of a waiting queue is not to over-utilize server resources at peak times.
// Currently, amount of max parallel processes is an option provided at start up
// (it other words, it's not dynamic, nocc-server does not try to analyze CPU/memory).
func (session *Session) LaunchCompilerWhenPossible(client *Client, compilerLauncher *CompilerLauncher, objFileCache *ObjFileCache) {
	if session.compilationStarted.Swap(1) != 0 {
		return
	}

	session.OutputFile = objFileCache.GenerateObjOutFileName(client, session)

	logServer.Info(1, "launch compiler #", "sessionID", session.sessionID, "clientID", client.clientID, session.compilerArgs)

	request := &CompilerLaunchRequest{
		workingDir:    client.workingDir,
		chanDisconnected: client.chanDisconnected,
		compilerName:     session.compilerName,
		compileInput:     session.InputFile,
		compileOutput:    session.OutputFile,
		compilerArgs:     session.compilerArgs,
		interruptchan:    session.interruptchan,
	}

	response, err := compilerLauncher.ExecCompiler(request)
	if err != nil {
		session.compilerExitCode = -1
		session.compilerStderr = fmt.Appendln(nil, err)
		client.PushToClientReadyChannel(session)
		return
	}

	if response.interrupted {
		session.interrupted = true
		client.PushToClientReadyChannel(session)
		return
	}

	session.compilerExitCode = response.exitcode
	session.compilerDuration = response.duration
	session.compilerStdout = response.stdout
	session.compilerStderr = response.stderr

	if session.compilerDuration > 30000 {
		logServer.Info(0, "compiled very heavy file", "sessionID", session.sessionID, "compilerDuration", session.compilerDuration, session.InputFile)
	}

	// save to obj cache only if compilation was successful
	if !session.objCacheKey.IsEmpty() {
		if session.compilerExitCode == 0 {
			if stat, err := os.Stat(session.OutputFile); err == nil {
				_ = objFileCache.SaveFileToCache(session.OutputFile, path.Base(session.InputFile)+".o", session.objCacheKey, stat.Size())
			}
		}
	}

	client.PushToClientReadyChannel(session)
}

func (session *Session) LaunchPchWhenPossible(client *Client, compilerLauncher *CompilerLauncher, objFileCache *ObjFileCache) (bool, error) {
	pchInvocation, err := ParsePchFile(session.pchFile)
	if err != nil {
		return false, err
	}

	var objCacheKey common.SHA256
	clientOutputFile := client.MapClientFileNameToServerAbs(pchInvocation.OutputFile)
	objCacheKey = common.SHA256{}
	objCacheKey.FromLongHexString(pchInvocation.Hash)
	if pathInObjCache := objFileCache.LookupInCache(objCacheKey); len(pathInObjCache) != 0 {
		logServer.Info(0, "pch already compiled", clientOutputFile, "sessionID", session.sessionID)
		return false, os.Link(pathInObjCache, clientOutputFile)
	}

	request := &CompilerLaunchRequest{
		workingDir:    client.workingDir,
		chanDisconnected: client.chanDisconnected,
		compilerName:     pchInvocation.Compiler,
		compileInput:     pchInvocation.InputFile,
		compileOutput:    clientOutputFile,
		compilerArgs:     pchInvocation.Args,
		interruptchan:    session.interruptchan,
	}

	response, err := compilerLauncher.ExecCompiler(request)
	if err != nil {
		return false, err
	}

	if response.interrupted {
		return true, nil
	}

	if response.exitcode != 0 {
		return false, fmt.Errorf("failed to compile pch file %s", pchInvocation.InputFile)
	}

	if stat, err := os.Stat(clientOutputFile); err == nil {
		fileNameInCacheDir := fmt.Sprintf("%s.%s", path.Base(pchInvocation.InputFile), filepath.Ext(pchInvocation.OutputFile))
		_ = objFileCache.SaveFileToCache(clientOutputFile, fileNameInCacheDir, objCacheKey, stat.Size())
	}

	return false, nil
}
