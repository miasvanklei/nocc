package client

import "nocc/internal/common"

// anywhere in the client code, use logClient.Info() and other methods for logging
var logClient *common.LoggerWrapper

func MakeLoggerClient(configuration *Configuration) error {
	var err error
	logClient, err = common.MakeLogger(configuration.LogFileName, configuration.LogLevel, true)
	return err
}
