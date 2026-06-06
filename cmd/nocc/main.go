package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

type CompilationStatus int32

const (
	StatusNotStarted CompilationStatus = iota
	StatusRunning
	StatusInterrupted
)

func main() {
	exitCode := realMain()
	_ = os.Stderr.Close()
	os.Exit(exitCode)
}

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	compiler, args := splitCompilerAndArgs(os.Args)
	if shouldCompileLocally(args) {
		exitCode, err := executeLocally(compiler, args, nil)
		if err != nil {
			return exitOnError(err)
		}
		return exitCode
	}

	conn, err := net.Dial("unix", "/run/nocc-daemon.sock")
	if err == nil {
		defer conn.Close()
		return runCompilationInDaemon(ctx, conn, compiler, args)
	}

	exitCode, err := executeLocally(compiler, args, err)
	if err != nil {
		return exitOnError(err)
	}
	return exitCode
}

func runCompilationInDaemon(ctx context.Context, conn net.Conn, compiler string, args []string) int {
	var compilationStatus atomic.Int32
	normalExitchan := make(chan struct{})
	defer close(normalExitchan)

	cwd, err := os.Getwd()
	if err != nil {
		return exitOnError(err)
	}

	go waitForInterruption(ctx, conn, &compilationStatus, normalExitchan)
	if err := sendRequest(conn, &compilationStatus, cwd, compiler, args); err != nil {
		return exitOnError(err)
	}

	exitCode, err := readResponse(conn)
	if err != nil {
		return exitOnError(err)
	}
	return exitCode
}

// We compile locally under the following conditions:
// - the user specified "-", or "-E"
// - the user did not specify or "-c"
// - the user specified "/dev/null" as an input file
func shouldCompileLocally(args []string) bool {
	return slices.Contains(args, "-") || slices.Contains(args, "-E") || !slices.Contains(args, "-c") || slices.Contains(args, "/dev/null")
}

func exitOnError(err error) int {
	_, _ = os.Stderr.WriteString("[nocc] " + err.Error() + "\n")
	return 1
}

func splitCompilerAndArgs(args []string) (compiler string, arguments []string) {
	compiler = filepath.Base(args[0])

	if compiler == "nocc" {
		compiler = filepath.Base(args[1])
		arguments = args[2:]
	} else {
		arguments = args[1:]
	}

	return
}

func getPaths() []string {
	return strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
}

func getCompiler(compiler string) (*string, error) {
	pathCurrentProgram, _ := os.Executable()

	for _, path := range getPaths() {
		pathCompiler := filepath.Join(path, compiler)
		realPath, err := filepath.EvalSymlinks(pathCompiler)
		if err != nil || pathCurrentProgram == realPath {
			continue
		}

		return &pathCompiler, nil
	}

	err := fmt.Errorf("compiler: %s not found in PATH", compiler)
	return nil, err
}

func executeLocally(compiler string, arguments []string, err error) (int, error) {
	if err != nil {
		_, _ = os.Stderr.WriteString("[nocc] " + err.Error() + "\n")
	}

	pathCompiler, err := getCompiler(compiler)
	if err != nil {
		return 1, err
	}

	var compilerStdout, compilerStderr bytes.Buffer
	cmd := exec.Command(*pathCompiler, arguments...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = &compilerStdout
	cmd.Stderr = &compilerStderr
	err = cmd.Run()

	_, _ = os.Stdout.Write(compilerStdout.Bytes())
	_, _ = os.Stderr.Write(compilerStderr.Bytes())

	exitCode := cmd.ProcessState.ExitCode()

	return exitCode, err
}

func waitForInterruption(ctx context.Context, conn net.Conn, compilationStatus *atomic.Int32, normalExitchan chan struct{}) {
	select {
	case <-ctx.Done():
		if compilationStatus.CompareAndSwap(int32(StatusRunning), int32(StatusInterrupted)) {
			_, _ = conn.Write(fmt.Appendf(nil, "\000"))
		}
		return
	case <-normalExitchan:
		return
	}
}

func sendRequest(conn net.Conn, compilationStatus *atomic.Int32, currentPath string, compiler string, arguments []string) (err error) {
	if compilationStatus.CompareAndSwap(int32(StatusNotStarted), int32(StatusRunning)) {
		_, err = conn.Write(fmt.Appendf(nil, "%s\b%s\b%s\000", currentPath, compiler, strings.Join(arguments, "\b")))
	}

	return
}

func readResponse(conn net.Conn) (int, error) {
	response, err := bufio.NewReaderSize(conn, 128*1024).ReadString(0x00)
	if err != nil {
		return 1, err
	}

	responseParts := strings.Split(string(response[0:len(response)-1]), "\b") // -1 to strip off the trailing '\0'

	if len(responseParts) != 3 {
		return 1, fmt.Errorf("received more than 3 parts in response")
	}

	exitcode, err := strconv.Atoi(responseParts[0])
	if err != nil {
		return 1, err
	}

	_, _ = os.Stdout.WriteString(responseParts[1])
	_, _ = os.Stderr.WriteString(responseParts[2])

	return exitcode, nil
}
