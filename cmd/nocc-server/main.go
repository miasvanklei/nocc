package main

import (
	"fmt"
	"os"
	"path"
	"time"

	"nocc/internal/common"
	"nocc/internal/server"
	"nocc/pb"

	"google.golang.org/grpc"
)

func failedStart(message string, err error) {
	_, _ = fmt.Fprintln(os.Stderr, fmt.Sprint("failed to start nocc-server: ", message, ": ", err))
	os.Exit(1)
}

// prepareEmptyDir ensures that serverDir exists and is empty
// it's executed on server launch
// as a consequence, all file caches are lost on restart
func prepareEmptyDir(parentDir string, subdir string) string {
	// if /tmp/nocc/cpp/src-cache already exists, it means, that it contains files from a previous launch
	// to start up as quickly as possible, do the following:
	// 1) rename it to /tmp/nocc/cpp/src-cache.old
	// 2) clear it recursively in the background
	serverDir := path.Join(parentDir, subdir)
	if _, err := os.Stat(serverDir); err == nil {
		oldDirRenamed := fmt.Sprintf("%s.old.%d", serverDir, time.Now().Unix())
		if err := os.Rename(serverDir, oldDirRenamed); err != nil {
			failedStart("can't rename "+serverDir, err)
		}
		go func() {
			_ = os.RemoveAll(oldDirRenamed)
		}()
	}

	if err := os.MkdirAll(serverDir, os.ModePerm); err != nil {
		failedStart("can't create "+serverDir, err)
	}
	return serverDir
}

func main() {
	var err error

	showVersionAndExit := common.CmdEnvBool("Show version and exit", false,
		"version")
	showVersionAndExitShort := common.CmdEnvBool("Show version and exit", false,
		"v")

	configuration, err := ParseConfiguration("/etc/nocc/server.conf")
	if err != nil {
		failedStart("Failed to parse configuration", err)
	}

	common.ParseCmdFlagsCombiningWithEnv()

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if err = server.MakeLoggerServer(configuration.LogFileName, configuration.LogLevel); err != nil {
		failedStart("Can't init logger", err)
	}

	s := &server.NoccServer{}

	s.ActiveClients, err = server.MakeClientsStorage(prepareEmptyDir(configuration.SrcCacheDir, "clients"))
	if err != nil {
		failedStart("Failed to init clients hashtable", err)
	}

	s.CompilerLauncher, err = server.MakeCompilerLauncher(configuration.CompilerQueueSize, configuration.ObjCacheDir)
	if err != nil {
		failedStart("Failed to init compiler launcher", err)
	}

	s.SrcFileCache, err = server.MakeSrcFileCache(prepareEmptyDir(configuration.SrcCacheDir, "src-cache"), configuration.SrcCacheSize)
	if err != nil {
		failedStart("Failed to init src file cache", err)
	}

	s.ObjFileCache, err = server.MakeObjFileCache(prepareEmptyDir(configuration.ObjCacheDir, "obj-cache"), prepareEmptyDir(configuration.ObjCacheDir, "compiler-out"), configuration.ObjCacheSize)
	if err != nil {
		failedStart("Failed to init obj file cache", err)
	}

	s.GRPCServer = grpc.NewServer()
	pb.RegisterCompilationServiceServer(s.GRPCServer, s)

	s.Cron, err = server.MakeCron(s)
	if err != nil {
		failedStart("Failed to init cron", err)
	}

	listener, err := s.StartGRPCListening(configuration.ListenAddr)
	if err != nil {
		failedStart("Failed to listen", err)
	}

	s.GRPCServer.Stop()
	_ = listener.Close()
}
