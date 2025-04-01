package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

type CompilerLauncher struct {
	serverCompilerThrottle chan struct{}

	nSessionsReadyButWaiting int64
	nSessionsNowCompiling    int64

	totalCalls           int64
	totalDurationMs      int64
	more10secCount       int64
	more30secCount       int64
	nonZeroExitCodeCount int64
}

func MakeCompilerLauncher(maxParallelCompilerProcesses int64) (*CompilerLauncher, error) {
	if maxParallelCompilerProcesses <= 0 {
		return nil, fmt.Errorf("invalid maxParallelcompilerProcesses %d", maxParallelCompilerProcesses)
	}

	return &CompilerLauncher{
		serverCompilerThrottle: make(chan struct{}, maxParallelCompilerProcesses),
	}, nil
}

// LaunchCompilerWhenPossible launches the C++ compiler on a server managing a waiting queue.
// The purpose of a waiting queue is not to over-utilize server resources at peak times.
// Currently, amount of max parallel C++ processes is an option provided at start up
// (it other words, it's not dynamic, nocc-server does not try to analyze CPU/memory).
func (compilerLauncher *CompilerLauncher) LaunchCompilerWhenPossible(noccServer *NoccServer, session *Session) {
	atomic.AddInt64(&compilerLauncher.nSessionsReadyButWaiting, 1)
	compilerLauncher.serverCompilerThrottle <- struct{}{} // blocking

	atomic.AddInt64(&compilerLauncher.nSessionsReadyButWaiting, -1)
	curParallelCount := atomic.AddInt64(&compilerLauncher.nSessionsNowCompiling, 1)

	logServer.Info(1, "launch compiler #", curParallelCount, "sessionID", session.sessionID, "clientID", session.client.clientID, session.compilerCmdLine)
	compilerLauncher.launchServerCompilerForCpp(session, noccServer) // blocking until compiler ends

	atomic.AddInt64(&compilerLauncher.nSessionsNowCompiling, -1)
	atomic.AddInt64(&compilerLauncher.totalCalls, 1)
	atomic.AddInt64(&compilerLauncher.totalDurationMs, int64(session.compilerDuration))

	if session.compilerExitCode != 0 {
		atomic.AddInt64(&compilerLauncher.nonZeroExitCodeCount, 1)
	} else if session.compilerDuration > 30000 {
		atomic.AddInt64(&compilerLauncher.more30secCount, 1)
	} else if session.compilerDuration > 10000 {
		atomic.AddInt64(&compilerLauncher.more10secCount, 1)
	}

	<-compilerLauncher.serverCompilerThrottle
	session.PushToClientReadyChannel()
}

func (compilerLauncher *CompilerLauncher) GetNowCompilingSessionsCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.nSessionsNowCompiling)
}

func (compilerLauncher *CompilerLauncher) GetWaitingInQueueSessionsCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.nSessionsReadyButWaiting)
}

func (compilerLauncher *CompilerLauncher) GetTotalcompilerCallsCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.totalCalls)
}

func (compilerLauncher *CompilerLauncher) GetTotalcompilerDurationMilliseconds() int64 {
	return atomic.LoadInt64(&compilerLauncher.totalDurationMs)
}

func (compilerLauncher *CompilerLauncher) GetMore10secCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.more10secCount)
}

func (compilerLauncher *CompilerLauncher) GetMore30secCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.more30secCount)
}

func (compilerLauncher *CompilerLauncher) GetNonZeroExitCodeCount() int64 {
	return atomic.LoadInt64(&compilerLauncher.nonZeroExitCodeCount)
}

func (compilerLauncher *CompilerLauncher) launchServerCompilerForCpp(session *Session, noccServer *NoccServer) {
	compilerCommand := exec.Command(session.compilerName, session.compilerCmdLine...)
	compilerCommand.Dir = session.compilerCwd
	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand.Stderr = &compilerStderr
	compilerCommand.Stdout = &compilerStdout

	start := time.Now()
	err := compilerCommand.Run()

	session.compilerDuration = int32(time.Since(start).Milliseconds())
	session.compilerExitCode = int32(compilerCommand.ProcessState.ExitCode())
	session.compilerStdout = compilerStdout.Bytes()
	session.compilerStderr = compilerStderr.Bytes()
	if len(session.compilerStderr) == 0 && err != nil {
		session.compilerStderr = []byte(fmt.Sprintln(err))
	}

	if session.compilerExitCode != 0 {
		logServer.Error("the C++ compiler exited with code", session.compilerExitCode, "sessionID", session.sessionID, session.InputFile, "\ncompilerCwd:", session.compilerCwd, "\ncompilerCmdLine:", session.compilerName, session.compilerCmdLine, "\ncompilerStdout:", strings.TrimSpace(string(session.compilerStdout)), "\ncompilerStderr:", strings.TrimSpace(string(session.compilerStderr)))
	} else if session.compilerDuration > 30000 {
		logServer.Info(0, "compiled very heavy file", "sessionID", session.sessionID, "compilerDuration", session.compilerDuration, session.InputFile)
	}

	// save to obj cache (to be safe, only if compiler output is empty)
	if !session.objCacheKey.IsEmpty() {
		if session.compilerExitCode == 0 && len(session.compilerStdout) == 0 && len(session.compilerStderr) == 0 {
			if stat, err := os.Stat(session.OutputFile); err == nil {
				_ = noccServer.ObjFileCache.SaveFileToCache(session.OutputFile, path.Base(session.InputFile)+".o", session.objCacheKey, stat.Size())
			}
		}
	}

	session.compilerStdout = compilerLauncher.patchStdoutDropServerPaths(session.client, session.compilerStdout)
	session.compilerStderr = compilerLauncher.patchStdoutDropServerPaths(session.client, session.compilerStderr)
}

func (compilerLauncher *CompilerLauncher) launchServercompilerForPch(compilerName string, compilerCmdLine []string, rootDir string) error {
	compilerCommand := exec.Command(compilerName, compilerCmdLine...)
	compilerCommand.Dir = rootDir
	var compilerStdout, compilerStderr bytes.Buffer
	compilerCommand.Stderr = &compilerStderr
	compilerCommand.Stdout = &compilerStdout

	logServer.Info(1, "launch compiler for pch compilation", "rootDir", rootDir)
	_ = compilerCommand.Run()

	compilerExitCode := compilerCommand.ProcessState.ExitCode()

	if compilerExitCode != 0 {
		logServer.Error("the C++ compiler exited with code pch", compilerExitCode, "\ncmdLine:", compilerName, compilerCmdLine, "\ncompilerStdout:", strings.TrimSpace(compilerStdout.String()), "\ncompilerStderr:", strings.TrimSpace(compilerStderr.String()))
		return fmt.Errorf("could not compile pch: the C++ compiler exited with code %d\n%s", compilerExitCode, compilerStdout.String()+compilerStderr.String())
	}

	return nil
}

// patchStdoutDropServerPaths replaces /tmp/nocc/cpp/clients/clientID/path/to/file.cpp with /path/to/file.cpp.
// It's very handy to send back stdout/stderr without server paths.
func (compilerLauncher *CompilerLauncher) patchStdoutDropServerPaths(client *Client, stdout []byte) []byte {
	if len(stdout) == 0 {
		return stdout
	}

	return bytes.ReplaceAll(stdout, []byte(client.workingDir), []byte{})
}
