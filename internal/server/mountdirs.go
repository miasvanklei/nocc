package server

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type MountPaths struct {
	paths   []string
	flags uintptr
}

type RoMountPaths struct {
	MountPaths
}
type RwMountPaths struct {
	MountPaths
}

func makeRoMountPaths(mountDirs ...string) RoMountPaths {
	mountFolders := makeMountPaths(mountDirs, unix.MS_RDONLY)

	return RoMountPaths{
		mountFolders,
	}
}

func makeRwMountPaths(mountDirs ...string) RwMountPaths {
	mountFolders := makeMountPaths(mountDirs, 0)

	return RwMountPaths{
		mountFolders,
	}
}

func makeMountPaths(mountDirs []string, flags uintptr) MountPaths {
	return MountPaths{
		paths:   mountDirs,
		flags: flags,
	}
}

func BindmountPaths(workingDir string, mountPaths MountPaths) error {
	mountedPaths := make([]string, 0)
	var err error
	var errorMessage string
	for _, sourcePath := range mountPaths.paths {
		targetPath := path.Join(workingDir, sourcePath)

		if err = createMountDirectory(sourcePath, targetPath); err != nil {
			errorMessage = fmt.Sprintf("failed to create mount directory %s: %v", targetPath, err)
			break
		}

		if err = bindMount(sourcePath, targetPath, mountPaths.flags); err != nil {
			errorMessage = fmt.Sprintf("failed to bind mount %s on %s: %v", sourcePath, targetPath, err)
			break
		}
		mountedPaths = append(mountedPaths, sourcePath)
	}

	if err != nil {
		logServer.Error(errorMessage)
		unmountPaths(workingDir, mountedPaths)
		return fmt.Errorf("%s", errorMessage)
	}

	return nil
}

func createMountDirectory(sourcePath string, targetPath string) error {
	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}

	if fileInfo.IsDir() {
		return os.MkdirAll(targetPath, os.ModePerm)
	}

	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
		return err
	}

	if f, err := os.Create(targetPath); err != nil {
		return err
	} else {
		return f.Close()
	}
}

func bindMount(source string, target string, flags uintptr) error {
	err := unix.Mount(source, target, "bind", unix.MS_BIND | flags, "")

	if err != nil {
		return fmt.Errorf("failed to bind mount: %w", err)
	}
	return nil
}

func UnmountPaths(workingDir string, mountPaths MountPaths) {
	unmountPaths(workingDir, mountPaths.paths)
}

func unmountPaths(workingDir string, paths []string) {
	for _, unmountPath := range paths {
		targetPath := path.Join(workingDir, unmountPath)
		err := unix.Unmount(targetPath, 0)
		if err != nil {
			logServer.Error("failed to unmount", targetPath, err)
		}
	}
}
