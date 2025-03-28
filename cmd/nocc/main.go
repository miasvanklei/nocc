package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type DaemonSockResponse struct {
	exitcode int
	stdout   string
	stderr   string
}

func main() {
	c, err := net.Dial("unix", "/run/nocc-daemon.sock")
	exitOnError(err)
	defer c.Close()

	compiler, args := splitCompilerAndArgs(os.Args)

	cwd := get_cwd()

	sendRequest(c, cwd, compiler, args)
	response := readResponse(c, compiler, args)

	os.Stdout.WriteString(response.stdout)
	os.Stderr.WriteString(response.stderr)
	os.Exit(response.exitcode)
}

func exitOnError(err error) {
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
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

func getPath() []string {
	return strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
}

func getPathCompiler(compiler string) (path_compiler string, err error) {
	path_current_program, _ := os.Executable()

	for _, path := range getPath() {
		path_compiler = filepath.Join(path, compiler)
		if path_current_program == path_compiler {
			continue
		}
		if _, err = os.Stat(path_compiler); err == nil {
			return
		}
	}

	err = fmt.Errorf("compiler: %s not found in PATH", compiler)
	return
}

func executeLocally(compiler string, arguments []string, error string) {
	os.Stderr.WriteString(error)
	path_compiler, err := getPathCompiler(compiler)

	exitOnError(err)

	cmnd := exec.Command(path_compiler, arguments...)

	err = cmnd.Run()

	exitOnError(err)

	os.Exit(0)
}

func sendRequest(conn net.Conn, current_path string, compiler string, arguments []string) {
	_, err := conn.Write(fmt.Appendf(nil, "%s\b%s\b%s\000", current_path, compiler, strings.Join(arguments, " ")))
	if err != nil {
		executeLocally(compiler, arguments, err.Error())
	}
}

func readResponse(conn net.Conn, compiler string, arguments []string) (daemonSockResponse DaemonSockResponse) {
	slice, err := bufio.NewReaderSize(conn, 128*1024).ReadSlice(0)
	if err != nil {
		executeLocally(compiler, arguments, "Couldn't read from socket\n")
		return
	}

	responseParts := strings.Split(string(slice[0:len(slice)-1]), "\b") // -1 to strip off the trailing '\0'

	if len(responseParts) != 3 {
		executeLocally(compiler, arguments, "Received more than 3 parts in response\n")
	}

	exitcode, err := strconv.Atoi(responseParts[0])
	if err != nil {
		executeLocally(compiler, arguments, err.Error())
	}

	daemonSockResponse = DaemonSockResponse{
		exitcode: exitcode,
		stdout:   responseParts[1],
		stderr:   responseParts[2],
	}

	return
}
