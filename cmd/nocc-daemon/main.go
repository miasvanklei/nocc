package main

import (
	"fmt"
	"os"

	sdaemon "github.com/coreos/go-systemd/v22/daemon"
	"nocc/internal/client"
	"nocc/internal/common"
)

func failedStartDaemon(err any) {
	_, _ = fmt.Fprintln(os.Stdout, "daemon not started:", err)
	os.Exit(1)
}

func main() {
	showVersionAndExit := common.CmdEnvBool("Show version and exit.", false,
		"version")
	showVersionAndExitShort := common.CmdEnvBool("Show version and exit.", false,
		"v")

	configuration, err := ParseConfiguration("/etc/nocc/daemon.toml")
	if err != nil {
		failedStartDaemon("Failed to parse configuration: " + err.Error())
	}

	common.ParseCmdFlagsCombiningWithEnv()

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if err := client.MakeLoggerClient(configuration.LogFileName, configuration.LogLevel); err != nil {
		failedStartDaemon(err)
	}

	daemon, err := client.MakeDaemon(configuration.Servers, configuration.SocksProxyAddr, configuration.CompilerQueueSize)
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
