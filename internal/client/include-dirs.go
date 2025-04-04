package client

// IncludeDirs represents a part of the command-line related to include dirs (absolute paths).
type IncludeDirs struct {
	dirsI       []string // -I dir
	dirsIquote  []string // -iquote dir
	dirsIsystem []string // -isystem dir
	filesI      []string // -include file
	includePch  string   // -include-pch file
	stdinc      bool     // -nostdinc
	stdincxx    bool     // -nostdinc++
}

func MakeIncludeDirs() *IncludeDirs {
	return &IncludeDirs{
		dirsI:       make([]string, 0, 2),
		dirsIquote:  make([]string, 0, 2),
		dirsIsystem: make([]string, 0, 2),
		filesI:      make([]string, 0),
	}
}

func (dirs *IncludeDirs) IsEmpty() bool {
	return len(dirs.dirsI) == 0 && len(dirs.dirsIquote) == 0 && len(dirs.dirsIsystem) == 0 && len(dirs.filesI) == 0
}

func (dirs *IncludeDirs) Count() int {
	return len(dirs.dirsI) + len(dirs.dirsIquote) + len(dirs.dirsIsystem) + len(dirs.filesI)
}

func (dirs *IncludeDirs) AsCompilerArgs() []string {
	iArgs := make([]string, 0, 2*dirs.Count())

	for _, dir := range dirs.dirsI {
		iArgs = append(iArgs, "-I", dir)
	}

	for _, dir := range dirs.dirsIquote {
		iArgs = append(iArgs, "-iquote", dir)
	}

	if dirs.includePch != "" {
		iArgs = append(iArgs, "-include-pch", dirs.includePch)
	}

	for _, dir := range dirs.dirsIsystem {
		iArgs = append(iArgs, "-isystem", dir)
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
