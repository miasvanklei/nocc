package main

import (
	"runtime"

	"github.com/BurntSushi/toml"
)

type Configuration struct {
	ListenAddr        []string
	CompilerQueueSize int
	LogFileName       string
	LogLevel          int
	SrcCacheDir       string
	ObjCacheDir       string
	SrcCacheSize      int64
	ObjCacheSize      int64
	CompilerDirs      []string
}

func ParseConfiguration(filePath string) (*Configuration, error) {
	config := Configuration{
		ListenAddr:        []string{"localhost:43210"},
		CompilerQueueSize: runtime.NumCPU(),
		LogFileName:       "stderr",
		LogLevel:          0,
		SrcCacheDir:       "/var/tmp/nocc/cpp",
		ObjCacheDir:       "/var/tmp/nocc/obj",
		SrcCacheSize:      8 * 1024 * 1024 * 1024,
		ObjCacheSize:      4 * 1024 * 1024 * 1024,
	}
	if _, err := toml.DecodeFile(filePath, &config); err != nil {
		return nil, err
	}
	return &config, nil
}
