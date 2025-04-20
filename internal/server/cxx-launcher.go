package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"nocc/internal/common"
	"os"
	"os/exec"
	"strings"
	"time"
)

var defaultMappedFolders = []string{
	"-b", "/lib",
	"-b", "/usr/lib",
	"-b", "/usr/bin",
	"-b", "/bin",
	"-b", "/etc",
}

type CompilerLauncher struct {
	serverCompilerThrottle chan struct{}
	prootMappedDirs        []string
}

func MakeCompilerLauncher(maxParallelCompilerProcesses int, objCacheDir string) (*CompilerLauncher, error) {
	if maxParallelCompilerProcesses <= 0 {
		return nil, fmt.Errorf("invalid maxParallelcompilerProcesses %d", maxParallelCompilerProcesses)
	}

	return &CompilerLauncher{
		serverCompilerThrottle: make(chan struct{}, maxParallelCompilerProcesses),
		prootMappedDirs:        append(defaultMappedFolders, "-b", objCacheDir),
	}, nil
}

func (compilerLauncher *CompilerLauncher) ExecCompiler(workingDir string, compilerName string, compileInput string, compileOutput string, compilerArgs []string) (int, int32, []byte, []byte) {
	var compilerStdoutBuffer, compilerStderrBuffer bytes.Buffer
	command := "proot"
	prootarguments := make([]string, 0, len(compilerLauncher.prootMappedDirs)+9+len(compilerArgs))

	prootarguments = append(prootarguments, "-R", workingDir)
	prootarguments = append(prootarguments, compilerLauncher.prootMappedDirs...)
	prootarguments = append(prootarguments, compilerName)
	prootarguments = append(prootarguments, compilerArgs...)
	prootarguments = append(prootarguments, "-o", compileOutput, "-c", compileInput)

	compilerCommand := exec.Command(command, prootarguments...)
	compilerCommand.Stderr = &compilerStderrBuffer
	compilerCommand.Stdout = &compilerStdoutBuffer

	// This code is blocking until the compiler ends
	compilerLauncher.serverCompilerThrottle <- struct{}{}

	start := time.Now()
	err := compilerCommand.Run()
	compilerDuration := int32(time.Since(start).Milliseconds())

	<-compilerLauncher.serverCompilerThrottle

	compilerExitCode := compilerCommand.ProcessState.ExitCode()
	compilerStdout := compilerStdoutBuffer.Bytes()
	compilerStderr := compilerStderrBuffer.Bytes()

	if len(compilerStderr) == 0 && err != nil {
		compilerStderr = fmt.Appendln(nil, err)
	}

	if compilerExitCode != 0 {
		logServer.Error(
			"The compiler exited with code", compilerExitCode,
			"\ncmdLine:", compilerName, compilerArgs,
			"\ncompilerStdout:", strings.TrimSpace(string(compilerStdout)),
			"\ncxxStderr:", strings.TrimSpace(string(compilerStderr)))
	}

	return compilerExitCode, compilerDuration, compilerStdout, compilerStderr
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
