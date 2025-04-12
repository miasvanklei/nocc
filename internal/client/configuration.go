package client

import (
	"runtime"

	"github.com/BurntSushi/toml"
)

type Configuration struct {
	ClientID          string
	SocksProxyAddr    string
	CompilerQueueSize int
	Servers           []string
	LogFileName       string
	LogLevel          int
	InvocationTimeout int
	ConnectionTimeout int
}

func ParseConfiguration(filePath string) (*Configuration, error) {
	config := Configuration{
		CompilerQueueSize: runtime.NumCPU(),
		Servers:           []string{"localhost:43210"},
		LogFileName:       "stderr",
		LogLevel:          1,
		InvocationTimeout: 10 * 60, // 10 minutes
		ConnectionTimeout: 15,      // 15 seconds
		ClientID:          "",
	}
	if _, err := toml.DecodeFile(filePath, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
