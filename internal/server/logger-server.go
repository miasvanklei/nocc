package server

import "nocc/internal/common"

// anywhere in the server code, use logServer.Info() and other methods for logging
var logServer *common.LoggerWrapper

func MakeLoggerServer(logFile string, verbosity int) error {
	var err error
	logServer, err = common.MakeLogger(logFile, verbosity, false)
	return err
}
