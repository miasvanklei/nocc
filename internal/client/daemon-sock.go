package client

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// DaemonUnixSockListener is created when `nocc-daemon` starts.
// It listens to a unix socket from `nocc` invocations (from a lightweight C++ wrapper).
// Request/response transferred via this socket are represented as simple C-style strings with \0 delimiters, see below.
type DaemonUnixSockListener struct {
	activeConnections int32
	lastTimeAlive     time.Time
	netListener       net.Listener
}

type DaemonSockRequest struct {
	Cwd      string
	Compiler string
	CmdLine  []string
}

type DaemonSockResponse struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

func MakeDaemonRpcListener() *DaemonUnixSockListener {
	return &DaemonUnixSockListener{
		activeConnections: 0,
		lastTimeAlive:     time.Now(),
	}
}

func (listener *DaemonUnixSockListener) StartListeningUnixSocket(daemonUnixSock string) (err error) {
	_ = os.Remove(daemonUnixSock)
	listener.netListener, err = net.Listen("unix", daemonUnixSock)
	return
}

func (listener *DaemonUnixSockListener) StartAcceptingConnections(daemon *Daemon) {
	for {
		conn, err := listener.netListener.Accept()
		if err != nil {
			select {
			case <-daemon.quitDaemonChan:
				return
			default:
				logClient.Error("daemon accept error:", err)
			}
		} else {
			listener.lastTimeAlive = time.Now()
			go listener.onRequest(conn, daemon) // `nocc` invocation
		}
	}
}

func (listener *DaemonUnixSockListener) EnterInfiniteLoopUntilQuit(daemon *Daemon) {
	for {
		select {
		case <-daemon.quitDaemonChan:
			_ = listener.netListener.Close() // Accept() will return an error immediately
			return

		case <-time.After(5 * time.Second):
			nActive := atomic.LoadInt32(&listener.activeConnections)
			isDisconnected := daemon.serverStatus.Load() == int32(StateDisconnected)
			if !isDisconnected && nActive == 0 && time.Since(listener.lastTimeAlive).Seconds() > 15 {
				daemon.QuitDaemonGracefully("no connections receiving anymore")
			}
		}
	}
}

// onRequest parses a string-encoded message from `nocc` C++ client and calls Daemon.HandleInvocation.
// After the request has been fully processed (.o is written), we answer back, and `nocc` client dies.
// Request message format:
// "{Cwd}\b{Compiler}\b{CmdLine...}\0"
// Response message format:
// "{ExitCode}\b{Stdout}\b{Stderr}\0"
func (listener *DaemonUnixSockListener) onRequest(conn net.Conn, daemon *Daemon) {
	slice, err := bufio.NewReaderSize(conn, 64*1024).ReadSlice(0)
	if err != nil {
		logClient.Error("couldn't read from socket", err)
		listener.respondErr(conn)
		return
	}
	reqParts := strings.Split(string(slice[0:len(slice)-1]), "\b") // -1 to strip off the trailing '\0'
	if len(reqParts) < 3 {
		logClient.Error("couldn't read from socket", reqParts)
		listener.respondErr(conn)
		return
	}
	request := DaemonSockRequest{
		Cwd:      reqParts[0],
		Compiler: reqParts[1],
		CmdLine:  strings.Split(reqParts[2], " "),
	}

	atomic.AddInt32(&listener.activeConnections, 1)

	if daemon.serverStatus.CompareAndSwap(int32(StateDisconnected), int32(StateConnecting)) {
		go daemon.ConnectToRemoteHosts()
		<-daemon.connectedServerchan
	} else if daemon.serverStatus.Load() == int32(StateConnecting) {
		<-daemon.connectedServerchan
	}

	response := daemon.HandleInvocation(request)

	atomic.AddInt32(&listener.activeConnections, -1)
	listener.lastTimeAlive = time.Now()

	listener.respondOk(conn, &response)
}

func (listener *DaemonUnixSockListener) respondOk(conn net.Conn, resp *DaemonSockResponse) {
	_, _ = conn.Write(fmt.Appendf(nil, "%d\b%s\b%s\000", resp.ExitCode, resp.Stdout, resp.Stderr))
	_ = conn.Close()
}

func (listener *DaemonUnixSockListener) respondErr(conn net.Conn) {
	_, _ = conn.Write([]byte("\000"))
	_ = conn.Close()
}
