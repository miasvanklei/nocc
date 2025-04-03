package client

import (
	"bytes"
	"fmt"
	"os/exec"
	"syscall"
)

// LocalCompilerLaunch describes an invocation when it's executed locally, not remotely.
// When some remotes are not available, files that were calculated to be compiled on that remotes,
// fall back to local compilation.
// Note, that local compilation is performed within a daemon instead of passing it to C++ wrappers.
// This is done in order to maintain a single queue.
// (`nocc` is typically launched with a very huge number of concurrent processes, and if network is broken,
// this queue makes a huge bunch of `nocc` invocations to be throttled to a limited number of local compiler processes).
type LocalCompilerLaunch struct {
	cwd      string
	compiler string
	cmdLine  []string
	uid      int
	gid      int
}

func (localcompiler *LocalCompilerLaunch) RunCompilerLocally() (exitCode int, stdout []byte, stderr []byte) {
	logClient.Info(0, "compile locally", localcompiler.cmdLine)

	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand := exec.Command(localcompiler.compiler, localcompiler.cmdLine...)
	compilerCommand.Dir = localcompiler.cwd
	compilerCommand.Stdout = &compilerStdout
	compilerCommand.Stderr = &compilerStderr
	compilerCommand.SysProcAttr = &syscall.SysProcAttr{}
	compilerCommand.SysProcAttr.Credential = &syscall.Credential{
		Uid: uint32(localcompiler.uid),
		Gid: uint32(localcompiler.gid),
	}

	err := compilerCommand.Run()

	exitCode = compilerCommand.ProcessState.ExitCode()
	stdout = compilerStdout.Bytes()
	stderr = compilerStderr.Bytes()

	if len(stderr) == 0 && err != nil {
		stderr = fmt.Appendln(nil, err)
	}

	return
}
