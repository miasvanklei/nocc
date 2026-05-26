package common

import (
	"context"
	"os/exec"
	"syscall"
)

func CreateCompilerCommand(command string, arguments []string, interruptFunc func(cancel context.CancelFunc, ctx context.Context)) (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	compilerCommand := exec.CommandContext(ctx, command, arguments...)
	compilerCommand.Cancel = func() error {
		return compilerCommand.Process.Signal(syscall.SIGTERM)
	}

	go interruptFunc(cancel, ctx)

	return compilerCommand, ctx, cancel
}
