package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nocc/internal/common"
	"os"
	"strings"
	"syscall"
	"time"
)

type CompilerLauncher struct {
	serverCompilerThrottle chan struct{}
}

type CompilerLaunchRequest struct {
	workingDir       string
	compilerName     string
	compileInput     string
	compileOutput    string
	compilerArgs     []string
	interruptchan    chan struct{}
	chanDisconnected chan struct{}
}

type CompilerLaunchResponse struct {
	interrupted bool
	exitcode    int
	duration    int32
	stdout      []byte
	stderr      []byte
}

func MakeCompilerLauncher(maxParallelCompilerProcesses int) (*CompilerLauncher, error) {
	if maxParallelCompilerProcesses <= 0 {
		return nil, fmt.Errorf("invalid maxParallelcompilerProcesses %d", maxParallelCompilerProcesses)
	}

	return &CompilerLauncher{
		serverCompilerThrottle: make(chan struct{}, maxParallelCompilerProcesses),
	}, nil
}

func (compilerLauncher *CompilerLauncher) ExecCompiler(request *CompilerLaunchRequest) (*CompilerLaunchResponse, error) {
	var compilerStdoutBuffer, compilerStderrBuffer bytes.Buffer
	compilerCmd := make([]string, 0, 5+len(request.compilerArgs))
	compilerCmd = append(compilerCmd, request.compilerArgs...)
	compilerCmd = append(compilerCmd, "-o", request.compileOutput, "-c", request.compileInput)
	compilerCmd = append(compilerCmd, "-Wno-missing-include-dirs") // This is needed to avoid errors about missing include dirs in the chroot environment

	compilerCommand, ctx, cancel :=
		common.CreateCompilerCommand(request.compilerName, compilerCmd, func(cancel context.CancelFunc, ctx context.Context) {
			select {
			case <-request.interruptchan:
				cancel()
			case <-request.chanDisconnected:
				cancel()
			case <-ctx.Done():
			}
		})

	compilerCommand.SysProcAttr = &syscall.SysProcAttr{
		Chroot: request.workingDir,
	}
	compilerCommand.Dir = "/"
	compilerCommand.Stderr = &compilerStderrBuffer
	compilerCommand.Stdout = &compilerStdoutBuffer
	defer cancel()

	// This code is blocking until the compiler ends
	compilerLauncher.serverCompilerThrottle <- struct{}{}

	start := time.Now()
	err := compilerCommand.Run()
	compilerDuration := int32(time.Since(start).Milliseconds())

	<-compilerLauncher.serverCompilerThrottle

	compilerExitCode := compilerCommand.ProcessState.ExitCode()
	compilerStdout := compilerStdoutBuffer.Bytes()
	compilerStderr := compilerStderrBuffer.Bytes()

	if ctx.Err() != nil {
		return &CompilerLaunchResponse{
			interrupted: true,
		}, nil
	}

	if err != nil || compilerExitCode != 0 {
		logServer.Error(
			"The compiler exited with code", compilerExitCode,
			"\ncmdLine:", request.compilerName, request.compilerArgs,
			"\ncompilerStdout:", strings.TrimSpace(string(compilerStdout)),
			"\ncxxStderr:", strings.TrimSpace(string(compilerStderr)))
	}

	return &CompilerLaunchResponse{
		exitcode: compilerExitCode,
		duration: compilerDuration,
		stdout:   compilerStdout,
		stderr:   compilerStderr,
	}, nil
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
