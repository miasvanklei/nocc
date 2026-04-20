package common

import (
	"path"
	"path/filepath"
)

func ReplaceFileExt(fileName string, newExt string) string {
	logExt := path.Ext(fileName)
	return fileName[0:len(fileName)-len(logExt)] + newExt
}

func PathAbs(cwd string, relPath string) string {
	var absPath string
	if relPath[0] == '/' {
		absPath = relPath
	} else {
		absPath = filepath.Join(cwd, relPath)
	}
	return filepath.Clean(absPath)
}
