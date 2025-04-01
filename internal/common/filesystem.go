package common

import (
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strconv"
)

func MkdirForFile(fileName string) error {
	if err := os.MkdirAll(filepath.Dir(fileName), os.ModePerm); err != nil {
		return err
	}
	return nil
}

func OpenTempFile(fullPath string, uid int, gid int) (f *os.File, err error) {
	fileNameTmp := fullPath + "." + strconv.Itoa(rand.Int())
	fileTmp, err := os.OpenFile(fileNameTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, os.ModePerm)
	_ = fileTmp.Chown(uid, gid)
	return fileTmp, err
}

func WriteFile(name string, data []byte, uid int, gid int) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	_ = f.Chown(uid, gid)
	
	if err != nil {
		return err
	}

	_, err = f.Write(data)
	if err1 := f.Close(); err1 != nil && err == nil {
		err = err1
	}

	return err
}

func ReplaceFileExt(fileName string, newExt string) string {
	logExt := path.Ext(fileName)
	return fileName[0:len(fileName)-len(logExt)] + newExt
}
