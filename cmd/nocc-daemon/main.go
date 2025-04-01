package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"

	sdaemon "github.com/coreos/go-systemd/v22/daemon"
	"nocc/internal/client"
	"nocc/internal/common"
)

func failedStart(err any) {
	_, _ = fmt.Fprintln(os.Stderr, "[nocc]", err)
	os.Exit(1)
}

func failedStartDaemon(err any) {
	_, _ = fmt.Fprintln(os.Stdout, "daemon not started:", err)
	os.Exit(1)
}

func readNoccServersFile(envNoccServersFilename string) (remoteNoccHosts []string) {
	contents, err := os.ReadFile(envNoccServersFilename)
	if err != nil {
		failedStart(err)
	}
	lines := bytes.Split(contents, []byte{'\n'})
	remoteNoccHosts = make([]string, 0, len(lines))

	for _, line := range lines {
		hostAndComment := bytes.SplitN(bytes.TrimSpace(line), []byte{'#'}, 2)
		if len(hostAndComment) > 0 && len(hostAndComment[0]) > 0 {
			trimmedHost := string(bytes.Trim(hostAndComment[0], " ;,"))
			remoteNoccHosts = append(remoteNoccHosts, trimmedHost)
		}
	}
	return
}

func parseNoccServersEnv(envNoccServers string) (remoteNoccHosts []string) {
	hosts := strings.Split(envNoccServers, ";")
	remoteNoccHosts = make([]string, 0, len(hosts))
	for _, host := range hosts {
		if trimmedHost := strings.TrimSpace(host); len(trimmedHost) != 0 {
			remoteNoccHosts = append(remoteNoccHosts, trimmedHost)
		}
	}
	return
}

func main() {
	showVersionAndExit := common.CmdEnvBool("Show version and exit.", false,
		"version", "")
	showVersionAndExitShort := common.CmdEnvBool("Show version and exit.", false,
		"v", "")
	noccServers := common.CmdEnvString("Remote nocc servers — a list of 'host:port' delimited by ';'.\nIf not set, nocc will read NOCC_SERVERS_FILENAME.", "",
		"", "NOCC_SERVERS")
	socksProxyAddr := common.CmdEnvString("A socks5 proxy address for all remote connections.", "",
		"", "NOCC_SOCKS_PROXY")
	noccServersFilename := common.CmdEnvString("A file with nocc servers — a list of 'host:port', one per line (with optional comments starting with '#').\nUsed if NOCC_SERVERS is unset.", "",
		"", "NOCC_SERVERS_FILENAME")
	logFileName := common.CmdEnvString("A filename to log, nothing by default.\nErrors are duplicated to stderr always.", "",
		"", "NOCC_LOG_FILENAME")
	logVerbosity := common.CmdEnvInt("Logger verbosity level for INFO (-1 off, default 0, max 2).\nErrors are logged always.", 0,
		"", "NOCC_LOG_VERBOSITY")
	localCompilerQueueSize := common.CmdEnvInt("Amount of parallel processes when remotes aren't available and compiler is launched locally.\nBy default, it's a number of CPUs on the current machine.", int64(runtime.NumCPU()),
		"", "NOCC_LOCAL_COMPILER_QUEUE_SIZE")

	common.ParseCmdFlagsCombiningWithEnv()

	var remoteNoccHosts []string
	if *noccServers != "" {
		remoteNoccHosts = parseNoccServersEnv(*noccServers)
	} else if *noccServersFilename != "" {
		remoteNoccHosts = readNoccServersFile(*noccServersFilename)
	}

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if err := client.MakeLoggerClient(*logFileName, *logVerbosity, *logFileName != "stderr"); err != nil {
		failedStartDaemon(err)
	}

	daemon, err := client.MakeDaemon(remoteNoccHosts, *socksProxyAddr, *localCompilerQueueSize)
	if err != nil {
		failedStartDaemon(err)
	}
	err = daemon.StartListeningUnixSocket()
	if err != nil {
		failedStartDaemon(err)
	}

	daemon.ServeUntilNobodyAlive()
	_, _ = sdaemon.SdNotify(false, sdaemon.SdNotifyStopping)
	return
}
