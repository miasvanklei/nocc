package client

import (
	"fmt"
	"math/rand"
	"nocc/internal/common"
	"os"
	"path"
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
	hascOption   bool              // -c
	cppInFile    string            // input file as specified in cmd line (.cpp for compilation, .h for pch generation)
	objOutFile   string            // output file as specified in cmd line (.o for compilation, .gch/.pch for pch generation)
	compilerName string            // g++ / clang / etc.
	compilerArgs []string          // args like -Wall, -fpch-preprocess, -I{dir} and many more
	fOptionFiles map[string]string // -frandomize-layout-seed-file={file} and others
	depsFlags    DepCmdFlags       // -MD -MF file and others, used for .d files generation (not passed to server)

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
		strings.HasSuffix(fileName, ".ii") ||
		strings.HasSuffix(fileName, ".S") ||
		strings.HasSuffix(fileName, ".s")
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
	var absPath string
	if relPath[0] == '/' {
		absPath = relPath
	} else {
		absPath = filepath.Join(cwd, relPath)
	}
	return filepath.Clean(absPath)
}

func (invocation *Invocation) ParseCmdLineInvocation(cmdLine []string) {
	for i := 0; i < len(cmdLine); i++ {
		arg := cmdLine[i]
		if len(arg) == 0 {
			continue
		}
		if arg[0] == '-' {
			if arg == "-c" {
				invocation.hascOption = true
				continue
			} else if parseFileResult := invocation.parseArgFile(cmdLine, "-o", &i); parseFileResult != nil {
				if parseFileResult.value == "/dev/null" {
					invocation.invokeType = invokedForLocalCompiling
				}
				invocation.objOutFile = pathAbs(invocation.cwd, parseFileResult.value)
				continue
			} else if args := invocation.parseIncludeArgs(cmdLine, &i); args != nil {
				invocation.compilerArgs = append(invocation.compilerArgs, args...)
				continue
			} else if args := invocation.parseFOption(cmdLine, &i); args != nil {
				invocation.compilerArgs = append(invocation.compilerArgs, args...)
				invocation.fOptionFiles[args[0]] = args[1]
				continue
			} else if arg == "-x" {
				xArg := cmdLine[i+1]
				if xArg == "c-header" || xArg == "c++-header" || xArg == "objective-c-header" || xArg == "objective-c++-header" {
					invocation.invokeType = invokedForCompilingPch
					invocation.compilerArgs = append(invocation.compilerArgs, arg, xArg)
					i++
					continue
				}
			} else if arg == "-I-" || arg == "-E" {
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if parseFileResult := invocation.parseArgFile(cmdLine, "-MF", &i); parseFileResult != nil {
				invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, parseFileResult.value))
				continue
			} else if parseFileResult := invocation.parseArgFile(cmdLine, "-MT", &i); parseFileResult != nil {
				invocation.depsFlags.SetCmdFlagMT(parseFileResult.value)
				continue
			} else if parseFileResult := invocation.parseArgFile(cmdLine, "-MQ", &i); parseFileResult != nil {
				invocation.depsFlags.SetCmdFlagMQ(parseFileResult.value)
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
			} else if arg == "-M" || arg == "-MM" || arg == "-MG" {
				// these dep flags are unsupported yet, cmake doesn't use them
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if arg == "-march=native" {
				invocation.err = fmt.Errorf("-march=native can't be launched remotely")
				return
			} else if strings.HasPrefix(arg, "-Wp") {
				wArgs := strings.Split(arg, ",")
				for j := 1; j < len(wArgs); j++ {
					if !invocation.parsePreprocessorArg(wArgs, &j) {
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

			// Best effort for compiling configure tests locally
			determineLocalCompiling(invocation, arg)

			invocation.cppInFile = pathAbs(invocation.cwd, arg)
			continue
		}

		invocation.compilerArgs = append(invocation.compilerArgs, arg)
	}

	if invocation.err != nil || invocation.invokeType != invokedUnsupported {
		return
	}

	if invocation.hascOption && invocation.cppInFile != "" {
		if invocation.objOutFile == "" {
			outputFilename := common.ReplaceFileExt(path.Base(invocation.cppInFile), ".o")
			invocation.objOutFile = filepath.Join(invocation.cwd, outputFilename)
		}
		invocation.invokeType = invokedForCompilingCpp
	} else if invocation.cppInFile != "" && invocation.objOutFile != "" {
		invocation.invokeType = invokedForLinking
	} else {
		invocation.err = fmt.Errorf("unsupported command-line: no output file specified")
	}
}

func (invocation *Invocation) parsePreprocessorArg(args []string, argIndex *int) bool {
	if parseFileResult := invocation.parseArgFile(args, "-MD", argIndex); parseFileResult != nil {
		invocation.depsFlags.SetCmdFlagMD()
		invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, parseFileResult.value))
		return true
	} else if parseFileResult := invocation.parseArgFile(args, "-MMD", argIndex); parseFileResult != nil {
		invocation.depsFlags.SetCmdFlagMMD()
		invocation.depsFlags.SetCmdFlagMF(pathAbs(invocation.cwd, parseFileResult.value))
		return true
	}

	return false
}

func (invocation *Invocation) parseFOption(args []string, argIndex *int) []string {
	fOptions := []string{"-frandomize-layout-seed-file="}

	for _, key := range fOptions {
		if parseFileResult := invocation.parseArgFile(args, key, argIndex); parseFileResult != nil {
			return append(parseFileResult.args, pathAbs(invocation.cwd, parseFileResult.value))
		}
	}

	return nil
}

func (invocation *Invocation) parseIncludeArgs(args []string, argIndex *int) []string {
	includefolderKeys := []string{"-I", "-iquote", "-isystem"}
	includefileKeys := []string{"-include-pch", "-include"}

	for _, key := range includefolderKeys {
		if parseFileResult := invocation.parseArgFile(args, key, argIndex); parseFileResult != nil {
			dir := pathAbs(invocation.cwd, parseFileResult.value)
			return append(parseFileResult.args, dir)
		}
	}

	for _, key := range includefileKeys {
		if parseFileResult := invocation.parseArgFile(args, key, argIndex); parseFileResult != nil {
			dir := parseFileResult.value
			if strings.HasPrefix(dir, "./") || strings.HasPrefix(dir, "../") {
				dir = pathAbs(invocation.cwd, dir)
			}

			return append(parseFileResult.args, dir)
		}
	}

	return nil
}

type parseFileResult struct {
	args  []string
	value string
}

func (invocation *Invocation) parseArgFile(args []string, key string, argIndex *int) *parseFileResult {
	arg := args[*argIndex]

	if arg == key {
		if *argIndex+1 < len(args) {
			*argIndex++
			if args[*argIndex] == "-Xclang" { // -Xclang -include -Xclang {file}
				*argIndex++
				return &parseFileResult{args: []string{key, "-Xclang"}, value: args[*argIndex]}
			}
			return &parseFileResult{args: []string{key}, value: args[*argIndex]}
		} else {
			invocation.err = fmt.Errorf("unsupported command-line: no argument after %s", arg)
			return nil
		}
	} else if strings.HasPrefix(arg, key) {
		return &parseFileResult{args: []string{key}, value: arg[len(key):]}
	}

	return nil
}

func determineLocalCompiling(invocation *Invocation, arg string) {
	shouldCompileLocally :=
		strings.Contains(invocation.cwd, "TryCompile-") || // cmake
			strings.Contains(invocation.cwd, "meson-private") || // meson
			strings.Contains(invocation.cwd, ".conf_check") || // waf
			strings.Contains(arg, "ffconf.") || // ffmpeg
			strings.Contains(arg, "cgo-gcc-input") || // go
			strings.HasPrefix(filepath.Base(arg), "conftest") || // autoconf
			strings.HasPrefix(arg, "tmp.conftest.") // autoconf

	if shouldCompileLocally {
		invocation.invokeType = invokedForLocalCompiling
	}
}

func CreateInvocation(req DaemonSockRequest) *Invocation {
	invocation := &Invocation{
		uid:          req.Uid,
		gid:          req.Gid,
		createTime:   time.Now(),
		sessionID:    req.SessionId,
		cwd:          req.Cwd,
		compilerName: req.Compiler,
		compilerArgs: make([]string, 0, len(req.CmdLine)),
		fOptionFiles: make(map[string]string, 1),
		summary:      MakeInvocationSummary(),
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
