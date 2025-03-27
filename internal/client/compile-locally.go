package client

import (
	"bytes"
	"fmt"
	"os/exec"
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
}

func (localCxx *LocalCxxLaunch) RunCxxLocally() (exitCode int, stdout []byte, stderr []byte) {
	logClient.Info(0, "compile locally", localCxx.cmdLine)

	cxxCommand := exec.Command(localCxx.compiler, localCxx.cmdLine...)
	cxxCommand.Dir = localCxx.cwd
	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand.Stdout = &cxxStdout
	cxxCommand.Stderr = &cxxStderr
	err := cxxCommand.Run()

	exitCode = cxxCommand.ProcessState.ExitCode()
	stdout = cxxStdout.Bytes()
	stderr = cxxStderr.Bytes()
	if len(stderr) == 0 && err != nil {
		stderr = fmt.Appendln(nil, err)
	}
	return
}
