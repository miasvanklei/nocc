package server

import (
	"fmt"
	"os"
	"os/exec"
	"path"
)

type MountFolders struct {
	folders []string
	options string
}

type RoMountFolders struct {
	MountFolders
}
type RwMountFolders struct {
	MountFolders
}

func makeRoMountDirs(mountDirs ...string) RoMountFolders {
	mountFolders := makeMountDirs(mountDirs, "ro")

	return RoMountFolders {
		mountFolders,
	}
}

func makeRwMountDirs(mountDirs ...string) RwMountFolders {
	mountFolders := makeMountDirs(mountDirs, "rw")

	return RwMountFolders {
		mountFolders,
	}
}

func makeMountDirs(mountDirs []string, options string) MountFolders {
	return MountFolders{
		folders: mountDirs,
		options: options,
	}
}

func bindmountFolders(workingDir string, mountFolders MountFolders) error {
	for _, folder := range mountFolders.folders {
		mountDir := path.Join(workingDir, folder)
		if err := os.MkdirAll(mountDir, os.ModePerm); err != nil {
			return fmt.Errorf("can't create client working directory: %v", err)
		}
		if err := bindMount(folder, mountDir, mountFolders.options); err != nil {
			return fmt.Errorf("failed to bind mount %s: %w", folder, err)
		}
	}
	return nil
}

func bindMount(source string, target string, option string) error {
	cmd := exec.Command("mount", "--bind", "-o", option, source, target)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bind mount: %w", err)
	}
	return nil
}

func unmountFolders(workingDir string, mountFolders MountFolders) {
	for _, folder := range mountFolders.folders {
		mountDir := path.Join(workingDir, folder)
		cmd := exec.Command("umount", mountDir)
		err := cmd.Run()
		if err != nil {
			logServer.Error("failed to unmount", mountDir, err)
		}
	}
}