package client

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	invokedUnsupported = iota
	invokedForLocalCompiling
	invokedForCompilingCpp
	invokedForCompilingPch
	invokedForLinking
)

// Invocation describes one `nocc` invocation inside a daemon.
// When `nocc g++ ...` is called, it pipes cmdLine to a background Daemon, which serves them in parallel.
// If this invocation is to compile .cpp to .o, it maps bidirectionally to server.Session.
type Invocation struct {
	invokeType int   // one of the constants above
	err        error // any error occurred while parsing/uploading/compiling/receiving

	uid        int
	gid        int
	createTime time.Time // used for local timeout
	sessionID  uint32    // incremental while a daemon is alive

	cwd string // working directory, where nocc was launched

	// cmdLine is parsed to the following fields:
	cppInFile     string       // input file as specified in cmd line (.cpp for compilation, .h for pch generation)
	objOutFile    string       // output file as specified in cmd line (.o for compilation, .gch/.pch for pch generation)
	compilerName  string       // g++ / clang / etc.
	compilerArgs  []string     // args like -Wall, -fpch-preprocess, -I{dir} and many more
	depsFlags     DepCmdFlags // -MD -MF file and others, used for .d files generation (not passed to server)

	waitUploads atomic.Int32 // files still waiting for upload to finish; 0 releases wgUpload; see Invocation.DoneUploadFile
	doneRecv    atomic.Int32 // 1 if o file received or failed receiving; 1 releases wgRecv; see Invocation.DoneRecvObj
	wgUpload    sync.WaitGroup
	wgRecv      sync.WaitGroup

	// when remote compilation starts, the server starts a server.Session (with the same sessionID)
	// after it finishes, we have these fields filled (and objOutFile saved)
	compilerExitCode int
	compilerStdout   []byte
	compilerStderr   []byte
	compilerDuration int32

	summary *InvocationSummary
}

func isSourceFileName(fileName string) bool {
	return isCsourceFileName(fileName) ||
		isCXXSourceFileName(fileName) ||
		isObjCSourceFileName(fileName) ||
		isObjCXXSourceFileName(fileName)
}

func isCsourceFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".c") ||
		strings.HasSuffix(fileName, ".i")
}

func isCXXSourceFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".cpp") ||
		strings.HasSuffix(fileName, ".cxx") ||
		strings.HasSuffix(fileName, ".cc") ||
		strings.HasSuffix(fileName, ".C") ||
		strings.HasSuffix(fileName, ".CC") ||
		strings.HasSuffix(fileName, ".cp") ||
		strings.HasSuffix(fileName, ".CPP") ||
		strings.HasSuffix(fileName, ".c++") ||
		strings.HasSuffix(fileName, ".C++") ||
		strings.HasSuffix(fileName, ".CXX") ||
		strings.HasSuffix(fileName, ".ii")
}

func isObjCSourceFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".m") ||
		strings.HasSuffix(fileName, ".mi")
}

func isObjCXXSourceFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".mm") ||
		strings.HasSuffix(fileName, ".M") ||
		strings.HasSuffix(fileName, ".mii")
}

func isHeaderFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".h") ||
		strings.HasSuffix(fileName, ".H") ||
		strings.HasSuffix(fileName, ".hh") ||
		strings.HasSuffix(fileName, ".hxx") ||
		strings.HasSuffix(fileName, ".hpp")
}

func pathAbs(cwd string, relPath string) string {
	if relPath[0] == '/' {
		return relPath
	}
	return filepath.Join(cwd, relPath)
}

func (invocation *Invocation) ParseCmdLineInvocation(cmdLine []string) {
	parseArgFile := func(key string, arg string, argIndex *int) (string, bool) {
		if arg == key { // -I /path
			if *argIndex+1 < len(cmdLine) {
				*argIndex++
				if cmdLine[*argIndex] == "-Xclang" { // -Xclang -include -Xclang {file}
					*argIndex++
				}
				return cmdLine[*argIndex], true
			} else {
				invocation.err = fmt.Errorf("unsupported command-line: no argument after %s", arg)
				return "", false
			}
		} else if strings.HasPrefix(arg, key) { // -I/path
			return arg[len(key):], true
		}
		return "", false
	}

	parseArgStr := func(args []string, key string, argIndex *int) string {
		if args[*argIndex] == key {
			if *argIndex+1 < len(args) {
				*argIndex++
				return args[*argIndex]
			} else {
				invocation.err = fmt.Errorf("unsupported command-line: no argument after %s", args[*argIndex])
				return ""
			}
		}
		return ""
	}

	parsePreprocessorArg := func (args []string, argIndex *int) bool {
		if mfFile := parseArgStr(args, "-MD", argIndex); mfFile != "" {
			invocation.depsFlags.SetCmdFlagMD()
			invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, mfFile))
			return true
		} else if mfFile := parseArgStr(args, "-MMD", argIndex); mfFile != "" {
			invocation.depsFlags.SetCmdFlagMMD()
			invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, mfFile))
			return true
		}

		return false
	}

	for i := 0; i < len(cmdLine); i++ {
		arg := cmdLine[i]
		if len(arg) == 0 {
			continue
		}
		if arg[0] == '-' {
			if oFile, ok := parseArgFile("-o", arg, &i); ok {
				invocation.objOutFile = pathAbs(invocation.cwd, oFile)
				continue
			} else if dir, ok := parseArgFile("-I", arg, &i); ok {
				invocation.compilerArgs = append(invocation.compilerArgs, "-I", pathAbs(invocation.cwd, dir))
				continue
			} else if dir, ok := parseArgFile("-iquote", arg, &i); ok {
				invocation.compilerArgs = append(invocation.compilerArgs, "-iquote", pathAbs(invocation.cwd, dir))
				continue
			} else if dir, ok := parseArgFile("-isystem", arg, &i); ok {
				invocation.compilerArgs = append(invocation.compilerArgs, "-isystem", pathAbs(invocation.cwd, dir))
				continue
			} else if iFile, ok := parseArgFile("-include-pch", arg, &i); ok {
				invocation.compilerArgs = append(invocation.compilerArgs, "-include-pch", pathAbs(invocation.cwd, iFile))
				continue
			} else if iFile, ok := parseArgFile("-include", arg, &i); ok {
				invocation.compilerArgs = append(invocation.compilerArgs, "-include", pathAbs(invocation.cwd, iFile))
				continue
			} else if arg == "-x" {
				xArg := cmdLine[i+1]
				if xArg == "c-header" || xArg == "c++-header" || xArg == "objective-c-header" || xArg == "objective-c++-header" {
					invocation.depsFlags.SetCmdFlagEmitPCH()
					invocation.compilerArgs = append(invocation.compilerArgs, arg, xArg)
					i++
					continue
				}
			} else if arg == "-I-" || arg == "-E" || strings.HasPrefix(arg, "-iprefix") || strings.HasPrefix(arg, "-idirafter") || strings.HasPrefix(arg, "--sysroot") {
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if mfFile := parseArgStr(cmdLine, "-MF", &i); mfFile != "" {
				invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, mfFile))
				continue
			} else if strArg := parseArgStr(cmdLine, "-MT", &i); strArg != "" {
				invocation.depsFlags.SetCmdFlagMT(strArg)
				continue
			} else if strArg := parseArgStr(cmdLine, "-MQ", &i); strArg != "" {
				invocation.depsFlags.SetCmdFlagMQ(strArg)
				continue
			} else if arg == "-MD" {
				invocation.depsFlags.SetCmdFlagMD()
				continue
			} else if arg == "-MMD" {
				invocation.depsFlags.SetCmdFlagMMD()
				continue
			} else if arg == "-MP" {
				invocation.depsFlags.SetCmdFlagMP()
				continue
			} else if arg == "-M" || arg == "-MM" || arg == "-MG" || arg == "-MMD" {
				// these dep flags are unsupported yet, cmake doesn't use them
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if arg == "-Xclang" && i < len(cmdLine)-1 { // "-Xclang {xArg}" — leave as is, unless we need to parse arg
				xArg := cmdLine[i+1]
				if xArg == "-I" || xArg == "-iquote" || xArg == "-isystem" || xArg == "-include" || xArg == "-include-pch" {
					continue // like "-Xclang" doesn't exist
				}
				invocation.compilerArgs = append(invocation.compilerArgs, arg, xArg)
				i++
				continue
			} else if arg == "-march=native" {
				invocation.err = fmt.Errorf("-march=native can't be launched remotely")
				return
			} else if strings.HasPrefix(arg, "-Wp") {
				wArgs := strings.Split(arg, ",")
				for j := 1; j < len(wArgs); j++ {
					if !parsePreprocessorArg(wArgs, &j) {
						cmdLine = append(cmdLine, wArgs[j])
					}
				}
				continue
			}
		} else if isSourceFileName(arg) || isHeaderFileName(arg) {
			if invocation.cppInFile != "" {
				invocation.err = fmt.Errorf("unsupported command-line: multiple input source files")
				return
			}
			invocation.cppInFile = filepath.Clean(pathAbs(invocation.cwd, arg))
			continue
		}

		invocation.compilerArgs = append(invocation.compilerArgs, arg)
	}

	if invocation.err != nil {
		return
	}

	// conftest.* built by autoconf is always done locally
	if strings.Contains(invocation.objOutFile, "/dev/null") || strings.HasPrefix(invocation.cppInFile, "conftest") || strings.HasPrefix(invocation.cppInFile, "tmp.conftest.") {
		invocation.invokeType = invokedForLocalCompiling
	} else if invocation.depsFlags.flagEmitPCH {
		invocation.invokeType = invokedForCompilingPch
	} else if invocation.cppInFile != "" && invocation.objOutFile != "" {
		invocation.invokeType = invokedForCompilingCpp
	} else if invocation.objOutFile != "" {
		invocation.invokeType = invokedForLinking
	} else {
		invocation.err = fmt.Errorf("unsupported command-line: no output file specified")
	}
}

func CreateInvocation(req DaemonSockRequest) *Invocation {
	invocation := &Invocation{
		uid:           req.Uid,
		gid:           req.Gid,
		createTime:    time.Now(),
		sessionID:     req.SessionId,
		cwd:           req.Cwd,
		compilerName:  req.Compiler,
		compilerArgs:  make([]string, 0, len(req.CmdLine)),
		summary:       MakeInvocationSummary(),
	}

	return invocation
}

func (invocation *Invocation) DoneRecvObj(err error) {
	if invocation.doneRecv.Swap(1) == 0 {
		if err != nil {
			invocation.err = err
		}
		invocation.wgRecv.Done()
	}
}

func (invocation *Invocation) DoneUploadFile(err error) {
	if err != nil {
		invocation.err = err
	}
	invocation.waitUploads.Add(-1)
	invocation.wgUpload.Done() // will end up after all required files uploaded/failed
}

func (invocation *Invocation) ForceInterrupt(err error) {
	logClient.Error("force interrupt", "sessionID", invocation.sessionID, "remoteHost", invocation.summary.remoteHost, invocation.cppInFile, err)
	// release invocation.wgUpload
	for invocation.waitUploads.Load() != 0 {
		invocation.DoneUploadFile(err)
	}
	// release invocation.wgDone
	invocation.DoneRecvObj(err)
}

func (invocation *Invocation) OpenTempFile(fullPath string) (f *os.File, err error) {
	fileNameTmp := fullPath + "." + strconv.Itoa(rand.Int())
	fileTmp, err := os.OpenFile(fileNameTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, os.ModePerm)
	_ = fileTmp.Chown(invocation.uid, invocation.gid)
	return fileTmp, err
}

func (invocation *Invocation) WriteFile(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	_ = f.Chown(invocation.uid, invocation.gid)

	if err != nil {
		return err
	}

	_, err = f.Write(data)
	if err1 := f.Close(); err1 != nil && err == nil {
		err = err1
	}

	return err
}
