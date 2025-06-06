package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"slices"
)

func main() {
	compiler, args := splitCompilerAndArgs(os.Args)
	if shouldCompileLocally(args) {
		executeLocally(compiler, args, "")
	}

	c, err := net.Dial("unix", "/run/nocc-daemon.sock")
	exitOnError(err)
	defer c.Close()

	cwd := get_cwd()

	sendRequest(c, cwd, compiler, args)
	exitCode := readResponse(c, compiler, args)

	os.Stderr.Close()
	os.Exit(exitCode)
}

// We compile locally under the following conditions:
// - the user specified "-", or "-E"
// - the user did not specify or "-c"
func shouldCompileLocally(args []string) bool {
	return slices.Contains(args, "-") || slices.Contains(args, "-E") || !slices.Contains(args, "-c")
}

func exitOnError(err error) {
	if err != nil {
		os.Stderr.WriteString("[nocc] " + err.Error() + "\n")
		os.Stderr.Close()
		os.Exit(1)
	}
}

func get_cwd() string {
	cwd, err := os.Getwd()
	exitOnError(err)
	return cwd
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

func getCompiler(compiler string) (string, error) {
	path_current_program, _ := os.Executable()

	for _, path := range getPaths() {
		path_compiler := filepath.Join(path, compiler)
		real_path, err := filepath.EvalSymlinks(path_compiler)
		if err != nil || path_current_program == real_path {
			continue
		}

		return path_compiler, nil
	}

	err := fmt.Errorf("compiler: %s not found in PATH", compiler)
	return "", err
}

func executeLocally(compiler string, arguments []string, error string) {
	if error != "" {
		os.Stderr.WriteString("[nocc] " + error + "\n")
	}

	path_compiler, err := getCompiler(compiler)
	if err != nil {
		exitOnError(err)
	}

	var compilerStdout, compilerStderr bytes.Buffer
	cmd := exec.Command(path_compiler, arguments...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = &compilerStdout
	cmd.Stderr = &compilerStderr
	err = cmd.Run()

	os.Stdout.Write(compilerStdout.Bytes())
	os.Stderr.Write(compilerStderr.Bytes())

	if err != nil {
		exitOnError(err)
	}

	os.Exit(0)
}

func sendRequest(conn net.Conn, current_path string, compiler string, arguments []string) {
	_, err := conn.Write(fmt.Appendf(nil, "%s\b%s\b%s\000", current_path, compiler, strings.Join(arguments, "\b")))
	if err != nil {
		executeLocally(compiler, arguments, err.Error())
	}
}

func readResponse(conn net.Conn, compiler string, arguments []string) int {
	slice, err := bufio.NewReaderSize(conn, 128*1024).ReadSlice(0)
	if err != nil {
		executeLocally(compiler, arguments, "Couldn't read from socket\n")
	}

	responseParts := strings.Split(string(slice[0:len(slice)-1]), "\b") // -1 to strip off the trailing '\0'

	if len(responseParts) != 3 {
		executeLocally(compiler, arguments, "Received more than 3 parts in response\n")
	}

	exitcode, err := strconv.Atoi(responseParts[0])
	if err != nil {
		executeLocally(compiler, arguments, err.Error())
	}

	os.Stdout.WriteString(responseParts[1])
	os.Stderr.WriteString(responseParts[2])

	return exitcode
}
