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

var defaultMappedFolders = []string{
	"-b", "/lib",
	"-b", "/usr/lib",
	"-b", "/usr/bin",
	"-b", "/bin",
	"-b", "/etc",
	"-b", "/tmp/nocc",
}

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
func PrepareServerCompilerCmdLine(client *Client, inputFile string, outputFile string, compilerArgs []string, compilerIDirs []string) []string {
	compilerCmdLine := make([]string, 0, len(compilerIDirs)+len(compilerArgs)+3)

	for i := 0; i < len(compilerIDirs); i += 2 {
		compilerCmdLine = append(compilerCmdLine, compilerIDirs[i], compilerIDirs[i+1])
	}

	for _, compilerArg := range compilerArgs {
		compilerCmdLine = append(compilerCmdLine, compilerArg)
	}

	// build final string
	return append(compilerCmdLine, "-o", outputFile, inputFile)
}

func (compilerLauncher *CompilerLauncher) LaunchPchWhenPossible(client *Client, session *Session, objFileCache *ObjFileCache) error {
	pchInvocation, err := ParsePchFile(session.pchFile)
	if err != nil {
		return err
	}

	var objCacheKey common.SHA256
	clientOutputFile := client.MapClientFileNameToServerAbs(pchInvocation.OutputFile)
	objCacheKey = common.SHA256{}
	objCacheKey.FromLongHexString(pchInvocation.Hash)
	if pathInObjCache := objFileCache.LookupInCache(objCacheKey); len(pathInObjCache) != 0 {
		return os.Link(pathInObjCache, clientOutputFile)
	}

	args := PrepareServerCompilerCmdLine(client, pchInvocation.InputFile, pchInvocation.OutputFile, pchInvocation.Args, pchInvocation.IDirs)

	// This code is blocking until the compiler ends
	compilerLauncher.serverCompilerThrottle <- struct{}{}
	err = launchServerCompilerForPch(client.workingDir, pchInvocation.Cwd, pchInvocation.Compiler, args)
	<-compilerLauncher.serverCompilerThrottle

	if err != nil {
		return err
	}

	if stat, err := os.Stat(clientOutputFile); err == nil {
		fileNameInCacheDir := fmt.Sprintf("%s.%s", path.Base(pchInvocation.InputFile), filepath.Ext(pchInvocation.OutputFile))
		_ = objFileCache.SaveFileToCache(clientOutputFile, fileNameInCacheDir, objCacheKey, stat.Size())
	}

	return nil
}

// LaunchCompilerWhenPossible launches the C++ compiler on a server managing a waiting queue.
// The purpose of a waiting queue is not to over-utilize server resources at peak times.
// Currently, amount of max parallel C++ processes is an option provided at start up
// (it other words, it's not dynamic, nocc-server does not try to analyze CPU/memory).
func (compilerLauncher *CompilerLauncher) LaunchCompilerWhenPossible(client *Client, session *Session, objFileCache *ObjFileCache) {
	if session.compilationStarted.Swap(1) != 0 {
		return
	}

	session.OutputFile = objFileCache.GenerateObjOutFileName(client, session)
	compilerCmdLine := PrepareServerCompilerCmdLine(client, session.InputFile, session.OutputFile, session.compilerArgs, session.compilerIDirs)
	logServer.Info(1, "launch compiler #", "sessionID", session.sessionID, "clientID", client.clientID, compilerCmdLine)

	// this code is blocking until the compiler ends
	compilerLauncher.serverCompilerThrottle <- struct{}{}
	session.LaunchServerCompilerForCpp(client.workingDir, compilerCmdLine, objFileCache)
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

func launchServerCompilerForPch(workingDir string, compilerCwd string, compilerName string, args []string) error {
	command := "proot"
	prootarguments := []string{
		"-R", workingDir,
		"-w", compilerCwd,
	}

	prootarguments = append(prootarguments, defaultMappedFolders...)
	prootarguments = append(prootarguments, compilerName)
	prootarguments = append(prootarguments, args...)

	var cxxStdout, cxxStderr bytes.Buffer
	compilerCommand := exec.Command(command, args...)
	compilerCommand.Stderr = &cxxStderr
	compilerCommand.Stdout = &cxxStdout

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

func (session *Session) LaunchServerCompilerForCpp(workingDir string, compilerCmdLine []string, objFileCache *ObjFileCache) {
	command := "proot"
	prootarguments := []string{
		"-R", workingDir,
		"-w", session.compilerCwd,
	}

	prootarguments = append(prootarguments, defaultMappedFolders...)
	prootarguments = append(prootarguments, session.compilerName)
	prootarguments = append(prootarguments, compilerCmdLine...)

	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand := exec.Command(command, prootarguments...)
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
