package server

import (
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

	InputFile  string // as-is from a client cmd line (relative to compilerCwd on a server-side)
	OutputFile string // inside /tmp/nocc/obj/compiler-out, or directly in /tmp/nocc/obj/obj-cache if taken from cache
	compilerCwd     string // cwd for the compiler on a server-side (= client.workingDir + clientCwd)
	compilerName    string // g++ / clang / etc.
	compilerCmdLine []string

	client *Client
	files  []*fileInClientDir

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
		sessionID: in.SessionID,
		files:     make([]*fileInClientDir, len(in.RequiredFiles)),
		compilerName:   in.Compiler,
		InputFile: in.InputFile, // as specified in a client cmd line invocation (relative to in.Cwd or abs on a client file system)
	}

	for index, meta := range in.RequiredFiles {
		fileSHA256 := common.SHA256{B0_7: meta.SHA256_B0_7, B8_15: meta.SHA256_B8_15, B16_23: meta.SHA256_B16_23, B24_31: meta.SHA256_B24_31}
		file, err := client.StartUsingFileInSession(meta.FileName, meta.FileSize, fileSHA256)
		newSession.files[index] = file
		// the only reason why a session can't be created is a dependency conflict:
		// previously, a client reported that clientFileName has sha256=v1, and now it sends sha256=v2
		if err != nil {
			return nil, err
		}
	}

	// note, that we don't add newSession to client.sessions: it's just created, not registered
	// (so, it won't be enumerated in a loop inside GetSessionsNotStartedCompilation until registered)

	return newSession, nil
}

// PrepareServercompilerCmdLine prepares a command line for compiler invocation.
// Notably, options like -Wall and -fpch-preprocess are pushed as is,
// but include dirs like /home/alice/headers need to be remapped to point to server dir.
func (session *Session) PrepareServerCompilerCmdLine(noccServer *NoccServer, clientCwd string, compilerArgs []string, compilerIDirs []string) {
	session.OutputFile = noccServer.ObjFileCache.GenerateObjOutFileName(session)

	var inputFile string
	// old clients that don't send this field (they send abs cppInFile)
	// todo delete later, after upgrading all clients
	if clientCwd == "" {
		inputFile = session.client.MapClientFileNameToServerAbs(session.InputFile)
		session.compilerCwd = session.client.workingDir
	} else {
		// session.cppInFile is as-is from a client cmd line:
		// * "/abs/path" becomes "client.workingDir/abs/path"
		//    (except for system files, /usr/include left unchanged)
		// * "rel/path" (relative to clientCwd) is left as-is (becomes relative to session.compilerCwd)
		//    (for correct __FILE__ expansion and other minor specifics)
		if session.InputFile[0] == '/' {
			inputFile = session.client.MapClientFileNameToServerAbs(session.InputFile)
		} else {
			inputFile = session.InputFile
		}
		session.compilerCwd = session.client.MapClientFileNameToServerAbs(clientCwd)
	}

	compilerCmdLine := make([]string, 0, len(compilerIDirs)+len(compilerArgs)+3)

	// loop through -I {dir} / -include {file} / etc. (format is guaranteed), converting client {dir} to server path
	for i := 0; i < len(compilerIDirs); i += 2 {
		arg := compilerIDirs[i]
		serverIdir := session.client.MapClientFileNameToServerAbs(compilerIDirs[i+1])
		compilerCmdLine = append(compilerCmdLine, arg, serverIdir)
	}

	for i := 0; i < len(compilerArgs); i++ {
		compilerArg := FilePrefixMapOption(compilerArgs[i], session.client.workingDir)

		compilerCmdLine = append(compilerCmdLine, compilerArg)
	}
	// build final string
	session.compilerCmdLine = append(compilerCmdLine, "-o", session.OutputFile, inputFile)
}

// StartCompilingObjIfPossible executes compiler if all dependent files (.cpp/.h/.nocc-pch/etc.) are ready.
// They have either been uploaded by the client or already taken from src cache.
// Note, that it's called for sessions that don't exist in obj cache.
func (session *Session) StartCompilingObjIfPossible(noccServer *NoccServer) {
	for _, file := range session.files {
		if file.state.Load() != fsFileStateUploaded {
			return
		}
	}

	if session.compilationStarted.Swap(1) == 0 {
		go noccServer.CompilerLauncher.LaunchCompilerWhenPossible(noccServer, session)
	}
}

func (session *Session) PushToClientReadyChannel() {
	// a client could have disconnected while compiler was working, then chanDisconnected is closed
	select {
	case <-session.client.chanDisconnected:
	case session.client.chanReadySessions <- session:
		// note, that if this chan is full, this 'case' (and this function call) is blocking
	}
}
