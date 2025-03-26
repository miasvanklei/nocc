package client

import "regexp"

// IncludeDirs represents a part of the command-line related to include dirs (absolute paths).
type IncludeDirs struct {
	dirsI       []string // -I dir
	dirsIquote  []string // -iquote dir
	dirsIsystem []string // -isystem dir
	filesI      []string // -include file
	filePCH     *string  // -include-pch file
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

func (dirs *IncludeDirs) IsEmpty() bool {
	isEmpty := len(dirs.dirsI) == 0 && len(dirs.dirsIquote) == 0 && len(dirs.dirsIsystem) == 0 && len(dirs.filesI) == 0

	if dirs.filePCH != nil {
		return false
	}

	return isEmpty
}

func (dirs *IncludeDirs) Count() int {
	count := len(dirs.dirsI) + len(dirs.dirsIquote) + len(dirs.dirsIsystem) + len(dirs.filesI)

	if dirs.filePCH != nil {
		return count + 1
	}

	return count
}

func (dirs *IncludeDirs) AsIncArgs(compiler string) []string {
	cxxIArgs := make([]string, 0, 2)

	re := regexp.MustCompile(`\+\+(?:-\d+)?$`)
	if !dirs.stdincxx && re.MatchString(compiler) {
		cxxIArgs = append(cxxIArgs, "-nostdinc++")
	}

	if !dirs.stdincxx {
		cxxIArgs = append(cxxIArgs, "-nostdinc")
	}

	return cxxIArgs
}

func (dirs *IncludeDirs) AsCxxArgs() []string {
	cxxIArgs := make([]string, 0, 2*dirs.Count())

	for _, dir := range dirs.dirsI {
		cxxIArgs = append(cxxIArgs, "-I", dir)
	}

	for _, dir := range dirs.dirsIquote {
		cxxIArgs = append(cxxIArgs, "-iquote", dir)
	}

	if !dirs.stdinc && !dirs.stdincxx {
		for _, dir := range dirs.dirsIsystem {
			cxxIArgs = append(cxxIArgs, "-isystem", dir)
		}
	}

	if dirs.filePCH != nil {
		cxxIArgs = append(cxxIArgs, "-include-pch", *dirs.filePCH)
	}

	for _, file := range dirs.filesI {
		cxxIArgs = append(cxxIArgs, "-include", file)
	}

	return cxxIArgs
}

func (dirs *IncludeDirs) MergeWith(other IncludeDirs) {
	dirs.dirsI = append(dirs.dirsI, other.dirsI...)
	dirs.dirsIquote = append(dirs.dirsIquote, other.dirsIquote...)
	dirs.dirsIsystem = append(dirs.dirsIsystem, other.dirsIsystem...)
	dirs.filesI = append(dirs.filesI, other.filesI...)
}
