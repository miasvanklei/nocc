package server

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
)

type MountPaths struct {
	paths   []string
	options string
}

type RoMountPaths struct {
	MountPaths
}
type RwMountPaths struct {
	MountPaths
}

func makeRoMountPaths(mountDirs ...string) RoMountPaths {
	mountFolders := makeMountPaths(mountDirs, "ro")

	return RoMountPaths{
		mountFolders,
	}
}

func makeRwMountPaths(mountDirs ...string) RwMountPaths {
	mountFolders := makeMountPaths(mountDirs, "rw")

	return RwMountPaths{
		mountFolders,
	}
}

func makeMountPaths(mountDirs []string, options string) MountPaths {
	return MountPaths{
		paths:   mountDirs,
		options: options,
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

		if err = bindMount(sourcePath, targetPath, mountPaths.options); err != nil {
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

func bindMount(source string, target string, option string) error {
	cmd := exec.Command("mount", "--bind", "-o", option, source, target)
	if err := cmd.Run(); err != nil {
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
		cmd := exec.Command("umount", targetPath)
		err := cmd.Run()
		if err != nil {
			logServer.Error("failed to unmount", targetPath, err)
		}
	}
}
