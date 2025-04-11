package client

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	sdaemon "github.com/coreos/go-systemd/v22/daemon"
	"golang.org/x/sys/unix"
)

// DaemonUnixSockListener is created when `nocc-daemon` starts.
// It listens to a unix socket from `nocc` invocations (from a lightweight C++ wrapper).
// Request/response transferred via this socket are represented as simple C-style strings with \0 delimiters, see below.
type DaemonUnixSockListener struct {
	activeConnections atomic.Int32
	lastTimeAlive     time.Time
	netListener       net.Listener
}

type DaemonSockRequest struct {
	SessionId uint32
	Uid      int
	Gid      int
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
		activeConnections: atomic.Int32{},
		lastTimeAlive:     time.Now(),
	}
}

func (listener *DaemonUnixSockListener) StartListeningUnixSocket() (err error) {
	listeners, err := activation.Listeners()
	if err != nil {
		return
	}
	if len(listeners) == 0 {
		return fmt.Errorf("no socket to listen to")
	}
	
	listener.netListener = listeners[0]
	return
}

func (listener *DaemonUnixSockListener) StartAcceptingConnections(daemon *Daemon) {
	_, _ = sdaemon.SdNotify(false, sdaemon.SdNotifyReady)
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
			nActive := listener.activeConnections.Load()
			if nActive == 0 && time.Since(listener.lastTimeAlive).Seconds() > 15 {
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
	uid, gid := getConnectedUser(conn)

	slice, err := bufio.NewReaderSize(conn, 128*1024).ReadSlice(0)
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
		SessionId: daemon.totalInvocations.Add(1),
		Uid:      uid,
		Gid:      gid,
		Cwd:      reqParts[0],
		Compiler: reqParts[1],
		CmdLine:  reqParts[2:],
	}

	listener.activeConnections.Add(1)
	response := daemon.HandleInvocation(request)
	listener.activeConnections.Add(-1)
	listener.lastTimeAlive = time.Now()

	listener.respondOk(conn, &response)
}

func getConnectedUser(conn net.Conn) (uid int, gid int) {
	unixConn := conn.(*net.UnixConn)
	f, _ := unixConn.File()
	pcred, _ := unix.GetsockoptUcred(int(f.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	f.Close()

	return int(pcred.Uid), int(pcred.Gid)
}

func (listener *DaemonUnixSockListener) respondOk(conn net.Conn, resp *DaemonSockResponse) {
	_, _ = conn.Write(fmt.Appendf(nil, "%d\b%s\b%s\000", resp.ExitCode, resp.Stdout, resp.Stderr))
	_ = conn.Close()
}

func (listener *DaemonUnixSockListener) respondErr(conn net.Conn) {
	_, _ = conn.Write([]byte("\000"))
	_ = conn.Close()
}
