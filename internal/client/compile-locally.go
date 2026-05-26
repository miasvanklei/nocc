package client

import (
	"bytes"
	"context"
	"nocc/internal/common"
	"syscall"
)

// CompilerLaunchRequest describes an invocation when it's executed locally, not remotely.
// When some remotes are not available, files that were calculated to be compiled on that remotes,
// fall back to local compilation.
// Note, that local compilation is performed within a daemon instead of passing it to C++ wrappers.
// This is done in order to maintain a single queue.
// (`nocc` is typically launched with a very huge number of concurrent processes, and if network is broken,
// this queue makes a huge bunch of `nocc` invocations to be throttled to a limited number of local compiler processes).
type CompilerLaunchRequest struct {
	cwd           string
	compiler      string
	cmdLine       []string
	uid           int
	gid           int
	interruptChan chan struct{}
}

type CompilerLaunchResponse struct {
	interrupted bool
	exitCode    int
	stdout      []byte
	stderr      []byte
}

func (request *CompilerLaunchRequest) RunCompilerLocally() (*CompilerLaunchResponse, error) {
	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand, ctx, cancel :=
		common.CreateCompilerCommand(request.compiler, request.cmdLine, func(cancel context.CancelFunc, ctx context.Context) {
			select {
			case <-request.interruptChan:
				cancel()
			case <-ctx.Done():
			}
		})

	defer cancel()
	compilerCommand.Dir = request.cwd
	compilerCommand.Stdout = &compilerStdout
	compilerCommand.Stderr = &compilerStderr
	compilerCommand.SysProcAttr = &syscall.SysProcAttr{}
	compilerCommand.SysProcAttr.Credential = &syscall.Credential{
		Uid: uint32(request.uid),
		Gid: uint32(request.gid),
	}

	err := compilerCommand.Run()

	if ctx.Err() != nil {
		return &CompilerLaunchResponse{
			interrupted: true,
		}, nil
	}

	response := &CompilerLaunchResponse{
		exitCode: compilerCommand.ProcessState.ExitCode(),
		stdout:   compilerStdout.Bytes(),
		stderr:   compilerStderr.Bytes(),
	}

	return response, err
}
