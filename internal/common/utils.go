package common

import (
	"context"
	"os/exec"
	"syscall"
)

func CreateCompilerCommand(interruptchan chan struct{}, command string, arguments []string) (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	compilerCommand := exec.CommandContext(ctx, command, arguments...)
	compilerCommand.Cancel = func() error {
		return compilerCommand.Process.Signal(syscall.SIGTERM)
	}

	go func() {
		select {
		case <-interruptchan:
			cancel()
		case <-ctx.Done():
		}
	}()

	return compilerCommand, ctx, cancel
}
