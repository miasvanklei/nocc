package main

import (
	"fmt"
	"os"

	"nocc/internal/client"
	"nocc/internal/common"

	sdaemon "github.com/coreos/go-systemd/v22/daemon"
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

	configuration, err := client.ParseConfiguration("/etc/nocc/daemon.conf")
	if err != nil {
		failedStartDaemon("Failed to parse configuration: " + err.Error())
	}

	common.ParseCmdFlagsCombiningWithEnv()

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if err := client.MakeLoggerClient(configuration); err != nil {
		failedStartDaemon(err)
	}

	daemon, err := client.MakeDaemon(configuration)
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
