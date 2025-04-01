package client

import (
	"bytes"
	"fmt"
	"os/exec"
	"syscall"
)

// LocalCxxLaunch describes an invocation when it's executed locally, not remotely.
// When some remotes are not available, files that were calculated to be compiled on that remotes,
// fall back to local compilation.
// Note, that local compilation is performed within a daemon instead of passing it to C++ wrappers.
// This is done in order to maintain a single queue.
// (`nocc` is typically launched with a very huge number of concurrent processes, and if network is broken,
// this queue makes a huge bunch of `nocc` invocations to be throttled to a limited number of local cxx processes).
type LocalCxxLaunch struct {
	cwd     string
	compiler string
	cmdLine []string
	uid int
	gid int
}

func (localCxx *LocalCxxLaunch) RunCxxLocally() (exitCode int, stdout []byte, stderr []byte) {
	logClient.Info(0, "compile locally", localCxx.cmdLine)

	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand := exec.Command(localCxx.compiler, localCxx.cmdLine...)
	cxxCommand.Dir = localCxx.cwd
	cxxCommand.Stdout = &cxxStdout
	cxxCommand.Stderr = &cxxStderr
	cxxCommand.SysProcAttr = &syscall.SysProcAttr{}
	cxxCommand.SysProcAttr.Credential = &syscall.Credential{
	    Uid: uint32(localCxx.uid),
	    Gid: uint32(localCxx.gid),
	}

	err := cxxCommand.Run()

	exitCode = cxxCommand.ProcessState.ExitCode()
	stdout = cxxStdout.Bytes()
	stderr = cxxStderr.Bytes()

	if len(stderr) == 0 && err != nil {
		stderr = fmt.Appendln(nil, err)
	}
	
	return
}
