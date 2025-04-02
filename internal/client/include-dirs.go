package client

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
)

// IncludeDirs represents a part of the command-line related to include dirs (absolute paths).
type IncludeDirs struct {
	dirsI       []string // -I dir
	dirsIquote  []string // -iquote dir
	dirsIsystem []string // -isystem dir
	filesI      []string // -include file
	stdinc      bool     // -nostdinc
	stdincxx    bool     // -nostdinc++
}

func MakeIncludeDirs() IncludeDirs {
	return IncludeDirs{
		dirsI:       make([]string, 0, 2),
		dirsIquote:  make([]string, 0, 2),
		dirsIsystem: make([]string, 0, 2),
		filesI:      make([]string, 0),
	}
}

// GetDefaultIncludeDirsOnLocal retrieves default include dirs on a local machine.
// This is done by -Wp,-v option for a no input file.
// This result is cached once nocc-daemon is started.
func (defIncludeDirs IncludeDirs) GetDefaultIncludeDirsOnLocal(compileName string, lang string) error {
	compilerWpCommand := exec.Command(compileName, "-Wp,-v", "-x", lang, "/dev/null", "-fsyntax-only")
	var compilerWpStderr bytes.Buffer
	compilerWpCommand.Stderr = &compilerWpStderr
	if err := compilerWpCommand.Run(); err != nil {
		return err
	}

	defIncludeDirs.parseCompilerDefaultIncludeDirsFromWpStderr(compilerWpStderr.String())

	return nil
}

// parseCompilerDefaultIncludeDirsFromWpStderr parses output of a C++ compiler with -Wp,-v option.
func (defIncludeDirs IncludeDirs) parseCompilerDefaultIncludeDirsFromWpStderr(compilerWpStderr string) {
	const (
		dirsIStart      = "#include <...>"
		dirsIquoteStart = "#include \"...\""
		dirsEnd         = "End of search list"

		stateUnknown      = 0
		stateInDirsIquote = 1
		stateInDirsI      = 2
	)

	state := stateUnknown

	for _, line := range strings.Split(compilerWpStderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, dirsIquoteStart) {
			state = stateInDirsIquote
		} else if strings.HasPrefix(line, dirsIStart) {
			state = stateInDirsI
		} else if strings.HasPrefix(line, dirsEnd) {
			return
		} else if strings.HasPrefix(line, "/") {
			if strings.HasSuffix(line, "(framework directory)") {
				continue
			}
			switch state {
			case stateInDirsIquote:
				defIncludeDirs.dirsIquote = append(defIncludeDirs.dirsIquote, line)
			case stateInDirsI:
				if strings.HasPrefix(line, "/usr/") || strings.HasPrefix(line, "/Library/") {
					normalizedPath, err := filepath.Abs(line)
					if err != nil {
						logClient.Error("can't normalize path:", line)
						continue
					}
					defIncludeDirs.dirsIsystem = append(defIncludeDirs.dirsIsystem, normalizedPath)
				} else {
					defIncludeDirs.dirsI = append(defIncludeDirs.dirsI, line)
				}
			}
		}
	}
}

func (dirs *IncludeDirs) IsEmpty() bool {
	return len(dirs.dirsI) == 0 && len(dirs.dirsIquote) == 0 && len(dirs.dirsIsystem) == 0 && len(dirs.filesI) == 0
}

func (dirs *IncludeDirs) Count() int {
	return len(dirs.dirsI) + len(dirs.dirsIquote) + len(dirs.dirsIsystem) + len(dirs.filesI)
}

func (dirs *IncludeDirs) AsIncArgs(filename string) []string {
	iArgs := make([]string, 0, 2)

	if !dirs.stdincxx {
		if isCsourceFileName(filename) || isObjCSourceFileName(filename) {
			iArgs = append(iArgs, "-nostdinc")
		} else {
			iArgs = append(iArgs, "-nostdinc++")
		}
	}

	return iArgs
}

func (dirs *IncludeDirs) AsCompilerArgs() []string {
	iArgs := make([]string, 0, 2*dirs.Count())

	for _, dir := range dirs.dirsI {
		iArgs = append(iArgs, "-I", dir)
	}

	for _, dir := range dirs.dirsIquote {
		iArgs = append(iArgs, "-iquote", dir)
	}

	if !dirs.stdinc && !dirs.stdincxx {
		for _, dir := range dirs.dirsIsystem {
			iArgs = append(iArgs, "-isystem", dir)
		}
	}

	for _, file := range dirs.filesI {
		iArgs = append(iArgs, "-include", file)
	}

	return iArgs
}

func (dirs *IncludeDirs) MergeWith(other *IncludeDirs) {
	dirs.dirsI = append(dirs.dirsI, other.dirsI...)
	dirs.dirsIquote = append(dirs.dirsIquote, other.dirsIquote...)
	dirs.dirsIsystem = append(dirs.dirsIsystem, other.dirsIsystem...)
	dirs.filesI = append(dirs.filesI, other.filesI...)
}
