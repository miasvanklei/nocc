package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"nocc/internal/common"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type CompilerLauncher struct {
	serverCompilerThrottle chan struct{}
}

func MakeCompilerLauncher(maxParallelCompilerProcesses int64) (*CompilerLauncher, error) {
	if maxParallelCompilerProcesses <= 0 {
		return nil, fmt.Errorf("invalid maxParallelcompilerProcesses %d", maxParallelCompilerProcesses)
	}

	return &CompilerLauncher{
		serverCompilerThrottle: make(chan struct{}, maxParallelCompilerProcesses),
	}, nil
}

// PrepareServercompilerCmdLine prepares a command line for compiler invocation.
// Notably, options like -Wall and -fpch-preprocess are pushed as is,
// but include dirs like /home/alice/headers need to be remapped to point to server dir.
func PrepareServerCompilerCmdLine(client *Client, inputFile string, outputFile string, compilerArgs []string, compilerIDirs []string) []string {
	var cppInFile string
	// cppInFile is as-is from a client cmd line:
	// * "/abs/path" becomes "client.workingDir/abs/path"
	//    (except for system files, /usr/include left unchanged)
	// * "rel/path" (relative to clientCwd) is left as-is (becomes relative to session.compilerCwd)
	//    (for correct __FILE__ expansion and other minor specifics)
	if inputFile[0] == '/' {
		cppInFile = client.MapClientFileNameToServerAbs(inputFile)
	} else {
		cppInFile = inputFile
	}

	compilerCmdLine := make([]string, 0, len(compilerIDirs)+len(compilerArgs)+3)

	// loop through -I {dir} / -include {file} / etc. (format is guaranteed), converting client {dir} to server path
	for i := 0; i < len(compilerIDirs); i += 2 {
		arg := compilerIDirs[i]
		serverIdir := client.MapClientFileNameToServerAbs(compilerIDirs[i+1])
		compilerCmdLine = append(compilerCmdLine, arg, serverIdir)
	}

	for i := range compilerArgs {
		compilerArg := FilePrefixMapOption(compilerArgs[i], client.workingDir)

		compilerCmdLine = append(compilerCmdLine, compilerArg)
	}

	// build final string
	return append(compilerCmdLine, "-o", outputFile, cppInFile)
}

func (compilerLauncher *CompilerLauncher) LaunchPchWhenPossible(client *Client, session *Session, objFileCache *ObjFileCache) error {
	pchInvocation, err := ParsePchFile(session.pchFile)
	if err != nil {
		return err
	}

	var objCacheKey common.SHA256
	objOutputFile := client.MapClientFileNameToServerAbs(pchInvocation.OutputFile)
	objCacheKey = common.SHA256{}
	objCacheKey.FromLongHexString(pchInvocation.Hash)
	if pathInObjCache := objFileCache.LookupInCache(objCacheKey); len(pathInObjCache) != 0 {
		return os.Link(pathInObjCache, objOutputFile)
	}

	cxxCwd := client.MapClientFileNameToServerAbs(pchInvocation.Cwd)
	args := PrepareServerCompilerCmdLine(client, pchInvocation.InputFile, objOutputFile, pchInvocation.Args, pchInvocation.IDirs)
	compilerLauncher.serverCompilerThrottle <- struct{}{}
	err = launchServerCompilerForPch(cxxCwd, pchInvocation.Compiler, args)
	<-compilerLauncher.serverCompilerThrottle

	if err != nil {
		return err
	}

	if stat, err := os.Stat(objOutputFile); err == nil {
		fileNameInCacheDir := fmt.Sprintf("%s.%s", path.Base(pchInvocation.InputFile), filepath.Ext(pchInvocation.OutputFile))
		_ = objFileCache.SaveFileToCache(objOutputFile, fileNameInCacheDir, objCacheKey, stat.Size())
	}

	return nil
}

// LaunchCompilerWhenPossible launches the C++ compiler on a server managing a waiting queue.
// The purpose of a waiting queue is not to over-utilize server resources at peak times.
// Currently, amount of max parallel C++ processes is an option provided at start up
// (it other words, it's not dynamic, nocc-server does not try to analyze CPU/memory).
func (compilerLauncher *CompilerLauncher) LaunchCompilerWhenPossible(client *Client, session *Session, objFileCache *ObjFileCache) {

	session.OutputFile = objFileCache.GenerateObjOutFileName(client, session)
	compilerCmdLine := PrepareServerCompilerCmdLine(client, session.InputFile, session.OutputFile, session.compilerArgs, session.compilerIDirs)
	logServer.Info(1, "launch compiler #", "sessionID", session.sessionID, "clientID", client.clientID, compilerCmdLine)

	compilerLauncher.serverCompilerThrottle <- struct{}{} // blocking
	session.LaunchServerCompilerForCpp(client, compilerCmdLine, objFileCache) // blocking until compiler ends
	<-compilerLauncher.serverCompilerThrottle

	// save to obj cache (to be safe, only if compiler output is empty)
	if !session.objCacheKey.IsEmpty() {
		if session.compilerExitCode == 0 && len(session.compilerStdout) == 0 && len(session.compilerStderr) == 0 {
			if stat, err := os.Stat(session.OutputFile); err == nil {
				_ = objFileCache.SaveFileToCache(session.OutputFile, path.Base(session.InputFile)+".o", session.objCacheKey, stat.Size())
			}
		}
	}

	client.PushToClientReadyChannel(session)
}

func launchServerCompilerForPch(cwd string, compilerName string, args []string) error {
	var cxxStdout, cxxStderr bytes.Buffer
	compilerCommand := exec.Command(compilerName, args...)
	compilerCommand.Dir = cwd
	compilerCommand.Stderr = &cxxStderr
	compilerCommand.Stdout = &cxxStdout

	logServer.Info(1, "launch cxx for pch compilation", "cwd", cwd)
	_ = compilerCommand.Run()

	compilerExitCode := compilerCommand.ProcessState.ExitCode()

	if compilerExitCode != 0 {
		logServer.Error("the C++ compiler exited with code", compilerExitCode,
			"\ncmdLine:", compilerName, args,
			"\ncxxStdout:", strings.TrimSpace(cxxStdout.String()),
			"\ncxxStderr:", strings.TrimSpace(cxxStderr.String()))
		return fmt.Errorf("could not compile pch: the C++ compiler exited with code %d\n%s", compilerExitCode, cxxStdout.String()+cxxStderr.String())
	}

	return nil
}

func (session *Session) LaunchServerCompilerForCpp(client *Client, compilerCmdLine []string, objFileCache *ObjFileCache) {
	compilerCommand := exec.Command(session.compilerName, compilerCmdLine...)
	compilerCommand.Dir = session.compilerCwd
	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand.Stderr = &compilerStderr
	compilerCommand.Stdout = &compilerStdout

	start := time.Now()
	err := compilerCommand.Run()

	session.compilerDuration = int32(time.Since(start).Milliseconds())
	session.compilerExitCode = int32(compilerCommand.ProcessState.ExitCode())
	session.compilerStdout = compilerStdout.Bytes()
	session.compilerStderr = compilerStderr.Bytes()
	if len(session.compilerStderr) == 0 && err != nil {
		session.compilerStderr = fmt.Appendln(nil, err)
	}

	if session.compilerExitCode != 0 {
		logServer.Error("the C++ compiler exited with code", session.compilerExitCode,
			"sessionID", session.sessionID, session.InputFile,
			"\ncompilerCwd:", session.compilerCwd,
			"\ncompilerCmdLine:", session.compilerName, compilerCmdLine,
			"\ncompilerStdout:", strings.TrimSpace(string(session.compilerStdout)),
			"\ncompilerStderr:", strings.TrimSpace(string(session.compilerStderr)))
	} else if session.compilerDuration > 30000 {
		logServer.Info(0, "compiled very heavy file", "sessionID", session.sessionID, "compilerDuration", session.compilerDuration, session.InputFile)
	}

	session.compilerStdout = patchStdoutDropServerPaths(client, session.compilerStdout)
	session.compilerStderr = patchStdoutDropServerPaths(client, session.compilerStderr)
}

// patchStdoutDropServerPaths replaces /tmp/nocc/cpp/clients/clientID/path/to/file.cpp with /path/to/file.cpp.
// It's very handy to send back stdout/stderr without server paths.
func patchStdoutDropServerPaths(client *Client, stdout []byte) []byte {
	if len(stdout) == 0 {
		return stdout
	}

	return bytes.ReplaceAll(stdout, []byte(client.workingDir), []byte{})
}

func ParsePchFile(pchFile *fileInClientDir) (pchCompilation *common.PCHInvocation, err error) {
	file, err := os.Open(pchFile.serverFileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	pchCompilation = &common.PCHInvocation{}
	bytes, _ := io.ReadAll(file)
	err = json.Unmarshal(bytes, pchCompilation)

	return
}
