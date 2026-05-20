package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"nocc/internal/common"
)

type ServerState int

const (
	StateDisconnected ServerState = iota
	StateConnected
	StateConnecting
)

// Daemon is created once, in a separate process `nocc-daemon`, which is listening for connections via unix socket.
// `nocc-daemon` is created by the first `nocc` invocation.
// `nocc` is invoked from cmake/kphp. It's a lightweight C++ wrapper that pipes command-line invocation to a daemon.
// The daemon keeps grpc connections to all servers and stores includes cache in memory.
// `nocc-daemon` quits in 15 seconds after it stops receiving new connections.
// (the next `nocc` invocation will spawn the daemon again)
type Daemon struct {
	startTime      time.Time
	quitDaemonChan chan int

	clientID string

	listener              *DaemonUnixSockListener
	remoteConnections     []*RemoteConnection
	remoteNoccHosts       []string
	socksProxyAddr        string
	localCompilerThrottle chan struct{}

	disableLocalCompiler bool

	totalInvocations  atomic.Uint32
	activeInvocations map[uint32]*Invocation
	invocationTimeout time.Duration
	connectionTimeout time.Duration

	mu sync.RWMutex
}

// detectClientID returns a clientID for current daemon launch.
// It's either controlled by env NOCC_CLIENT_ID or a random set of chars
// (it means, that after a daemon dies and launches again after some time, it becomes a new client for the server).
func detectClientID(clientID string) string {
	if clientID != "" {
		return clientID
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

	b := make([]rune, 8)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

func MakeDaemon(configuration *Configuration) (*Daemon, error) {
	daemon := &Daemon{
		startTime:             time.Now(),
		quitDaemonChan:        make(chan int),
		clientID:              detectClientID(configuration.ClientID),
		remoteConnections:     make([]*RemoteConnection, len(configuration.Servers)),
		remoteNoccHosts:       configuration.Servers,
		socksProxyAddr:        configuration.SocksProxyAddr,
		localCompilerThrottle: make(chan struct{}, configuration.CompilerQueueSize),
		disableLocalCompiler:  configuration.CompilerQueueSize == 0,
		activeInvocations:     make(map[uint32]*Invocation, 300),
		invocationTimeout:     time.Duration(configuration.InvocationTimeout) * time.Second,
		connectionTimeout:     time.Duration(configuration.ConnectionTimeout) * time.Second,
	}

	daemon.ConnectToRemoteHosts()

	return daemon, nil
}

func (daemon *Daemon) ConnectToRemoteHosts() {
	wg := sync.WaitGroup{}
	wg.Add(len(daemon.remoteNoccHosts))

	for index, remoteHostPort := range daemon.remoteNoccHosts {
		go func(index int, remoteHostPort string) {
			remote := MakeRemoteConnection(daemon, remoteHostPort, daemon.socksProxyAddr)
			daemon.remoteConnections[index] = remote
			err := remote.SetupConnection(true)

			if err != nil {
				remote.OnRemoteBecameUnavailable(err)
				logClient.Error("error connecting to", remoteHostPort, err)
			}

			wg.Done()
		}(index, remoteHostPort)
	}
	wg.Wait()
}

func (daemon *Daemon) StartListeningUnixSocket() error {
	daemon.listener = MakeDaemonRpcListener()
	return daemon.listener.StartListeningUnixSocket()
}

func (daemon *Daemon) ServeUntilNobodyAlive() {
	logClient.Info(0, "nocc-daemon started in", time.Since(daemon.startTime).Milliseconds(), "ms")

	var rLimit syscall.Rlimit
	_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	logClient.Info(0, "env:", "clientID", daemon.clientID, "; num servers", len(daemon.remoteConnections), "; ulimit -n", rLimit.Cur, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())

	go daemon.PeriodicallyInterruptHangedInvocations()
	go daemon.listener.StartAcceptingConnections(daemon)
	daemon.listener.EnterInfiniteLoopUntilQuit(daemon)
}

func (daemon *Daemon) KeepAlive() {
	for _, remote := range daemon.remoteConnections {
		go remote.VerifyAlive()
	}
}

func (daemon *Daemon) QuitDaemonGracefully(reason string) {
	logClient.Info(0, "daemon quit:", reason)

	defer func() { _ = recover() }()
	close(daemon.quitDaemonChan)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, remote := range daemon.remoteConnections {
		remote.SendStopClient(ctx)
		remote.Clear()
	}

	daemon.mu.Lock()
	for _, invocation := range daemon.activeInvocations {
		invocation.ForceInterrupt(fmt.Errorf("daemon quit: %v", reason))
	}
	daemon.mu.Unlock()
}

func (daemon *Daemon) HandleInvocation(req DaemonSockRequest) (*DaemonSockResponse, error) {
	response, err := daemon.HandleCompilation(req)

	if err != nil {
		return nil, err
	}

	return &DaemonSockResponse{
		ExitCode: response.exitCode,
		Stdout:   response.stdout,
		Stderr:   response.stderr,
	}, nil
}

func (daemon *Daemon) HandleCompilation(req DaemonSockRequest) (*CompilerLaunchResponse, error) {
	invocation := CreateInvocation(req)
	invocation.ParseCmdLineInvocation(req.CmdLine)

	switch invocation.invokeType {
	default:
		return daemon.InvokeLocalCompilation(req, errors.New("unexpected invokeType after parsing"))

	case invokedForLocalCompiling:
		return daemon.InvokeLocalCompilation(req, nil)
	case invokedUnsupported:
		// if command-line has unsupported options or is non-well-formed,
		// invocation.err describes a human-readable reason
		return daemon.InvokeLocalCompilation(req, invocation.err)

	case invokedForLinking:
		logClient.Info(1, "fallback to local compiler for linking")
		return daemon.InvokeLocalCompilation(req, nil)

	case invokedForCompilingPch:
		logClient.Info(1, "compiling pch locally")
		return daemon.invokePCHCompilation(req, invocation)

	case invokedForCompilingCpp:
		logClient.Info(1, "compiling remotely", invocation.cppInFile)
		result, err := daemon.invokeForRemoteCompiling(invocation)

		if err == nil && (result.interrupted || result.exitCode == 0) {
			return result, nil
		}

		result, err = daemon.InvokeLocalCompilation(req, err)

		if result.exitCode == 0 {
			message := fmt.Sprintf("compiling %s remotely on %s failed, but succeeded locally\n", invocation.cppInFile, invocation.summary.remoteHost)
			logClient.Error(message)
		}

		return result, err
	}
}

func (daemon *Daemon) invokePCHCompilation(req DaemonSockRequest, invocation *Invocation) (*CompilerLaunchResponse, error) {
	response, err := daemon.InvokeLocalCompilation(req, nil)
	sha256PCH, _ := common.GetFileSHA256(invocation.objOutFile)

	pchinvocation := common.PCHInvocation{
		Hash:       sha256PCH.ToLongHexString(),
		Compiler:   req.Compiler,
		InputFile:  invocation.cppInFile,
		OutputFile: invocation.objOutFile,
		Args:       invocation.compilerArgs,
	}

	bytes, _ := json.Marshal(&pchinvocation)
	_ = invocation.WriteFile(common.ReplaceFileExt(invocation.objOutFile, ".nocc-pch"), bytes)

	return response, err
}

func (daemon *Daemon) invokeForRemoteCompiling(invocation *Invocation) (*CompilerLaunchResponse, error) {
	if len(daemon.remoteConnections) == 0 {
		return nil, fmt.Errorf("no remote hosts set; use NOCC_SERVERS env var to provide servers")
	}

	remote := daemon.chooseRemoteConnectionForCppCompilation(invocation.cppInFile)

	invocation.summary.remoteHost = remote.remoteHost

	if remote.isUnavailable.Load() {
		return nil, fmt.Errorf("remote %s is unavailable", remote.remoteHost)
	}

	daemon.mu.Lock()
	daemon.activeInvocations[invocation.sessionID] = invocation
	daemon.mu.Unlock()

	response, err := CompileCppRemotely(daemon, remote, invocation)

	daemon.mu.Lock()
	delete(daemon.activeInvocations, invocation.sessionID)
	daemon.mu.Unlock()

	logClient.Info(1, "summary:", invocation.summary.ToLogString(invocation))

	return response, err
}

func (daemon *Daemon) InvokeLocalCompilation(req DaemonSockRequest, reason error) (*CompilerLaunchResponse, error) {
	if reason != nil {
		logClient.Error("compiling locally: ", reason)
	}

	daemon.localCompilerThrottle <- struct{}{}
	compilerLaunchRequest := CompilerLaunchRequest{req.Cwd, req.Compiler, req.CmdLine, req.Uid, req.Gid, req.InterruptChan}
	response, err := compilerLaunchRequest.RunCompilerLocally()
	<-daemon.localCompilerThrottle

	return response, err
}

func (daemon *Daemon) FindInvocationBySessionID(sessionID uint32) *Invocation {
	daemon.mu.RLock()
	invocation := daemon.activeInvocations[sessionID]
	daemon.mu.RUnlock()
	return invocation
}

func (daemon *Daemon) PeriodicallyInterruptHangedInvocations() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM)

	for {
		select {
		case <-daemon.quitDaemonChan:
			return

		case sig := <-signals:
			if sig == syscall.SIGKILL {
				logClient.Info(0, "got sigkill, exit(9)")
				os.Exit(9)
			}
			if sig == syscall.SIGTERM {
				daemon.QuitDaemonGracefully("got sigterm")
			}

		case <-time.After(10 * time.Second):
			daemon.mu.Lock()
			for _, invocation := range daemon.activeInvocations {
				if time.Since(invocation.createTime) > daemon.invocationTimeout {
					invocation.ForceInterrupt(fmt.Errorf("interrupt sessionID %d (%s) after %d sec timeout, reached step %s", invocation.sessionID, invocation.summary.remoteHost, int(time.Since(invocation.createTime).Seconds()), invocation.summary.timings[len(invocation.summary.timings)-1].stepName))
				}
			}
			daemon.mu.Unlock()
		}
	}
}

func (daemon *Daemon) chooseRemoteConnectionForCppCompilation(cppInFile string) *RemoteConnection {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Base(cppInFile)))
	return daemon.remoteConnections[int(hasher.Sum32())%len(daemon.remoteConnections)]
}
