package main

import (
	"runtime"

	"github.com/BurntSushi/toml"
)

type Configuration struct {
	SocksProxyAddr    string
	CompilerQueueSize int
	Servers           []string
	LogFileName       string
	LogLevel          int
}

func ParseConfiguration(filePath string) (*Configuration, error) {
	config := Configuration{
		CompilerQueueSize: runtime.NumCPU(),
		Servers:           []string{"localhost:43210"},
		LogFileName:       "stderr",
		LogLevel:          1,
	}
	if _, err := toml.DecodeFile(filePath, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
