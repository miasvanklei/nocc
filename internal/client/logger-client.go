package client

import "nocc/internal/common"

// anywhere in the client code, use logClient.Info() and other methods for logging
var logClient *common.LoggerWrapper

func MakeLoggerClient(logFile string, verbosity int) error {
	var err error
	logClient, err = common.MakeLogger(logFile, verbosity, true)
	return err
}
