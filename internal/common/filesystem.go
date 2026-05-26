package common

import (
	"path"
	"strings"
)

func ReplaceFileExt(fileName string, newExt string) string {
	logExt := path.Ext(fileName)
	return fileName[0:len(fileName)-len(logExt)] + newExt
}

func PathAbs(cwd string, relPath string) string {
	if relPath[0] == '/' {
		return relPath
	}
	return strings.Join([]string{cwd, relPath}, "/")
}
