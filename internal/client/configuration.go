package client

import (
	"runtime"
	"fmt"

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
		LogLevel:          0,
		InvocationTimeout: 15 * 60, // 15 minutes
		ConnectionTimeout: 15,      // 15 seconds
		ClientID:          "",
	}

	if _, err := toml.DecodeFile(filePath, &config); err != nil {
		return nil, err
	}

	if err := detectDuplicateServers(config.Servers); err != nil {
		return nil, err
        }

	return &config, nil
}

func detectDuplicateServers(servers []string) error {
	mapDuplicate := make(map[string]bool)

	for _, server := range servers {
		if mapDuplicate[server] {
			return fmt.Errorf("Duplicate entry found: %s", server)
		}
	}

	return nil
}
