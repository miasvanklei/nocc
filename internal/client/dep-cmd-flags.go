package client

import (
	"path"

	"nocc/internal/common"
)

// DepCmdFlags contains flags from the command line to generate .o.d file.
// CMake and make sometimes invoke the compiler like
// > g++ -MD -MT example.dir/1.cpp.o -MF example.dir/1.cpp.o.d -o example.dir/1.cpp.o -c 1.cpp
// This means: along with an object file (1.cpp.o), generate a dependency file (named 1.cpp.o.d here).
// A dependency file is a text file with include list found at any depth.
// Probably, it's used by CMake to track recompilation tree on that files change.
//
// nocc detects options like -MD and emits a depfile on a client side, after having collected all includes.
// Moreover, these options are stripped off invocation.compilerArgs and are not sent to the remote at all.
//
// Some options are supported and handled (-MF {file} / -MT {target} / ...).
// Some are unsupported (-M / -MG / ....). When they occur, nocc falls back to local compilation.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html.
type DepCmdFlags struct {
	flagMF      string // -MF {abs filename} (pre-resolved at cwd)
	flagMT      string // -MT/-MQ (target name)
	flagMD      bool   // -MD (like -MF {def file})
	flagMMD     bool   // -MMD (mention only user header files, not system header files)
	flagMP      bool   // -MP (add a phony target for each dependency other than the main file)
}

func (deps *DepCmdFlags) SetCmdFlagMF(absFilename string) {
	deps.flagMF = absFilename
}

func (deps *DepCmdFlags) SetCmdFlagMT(mtTarget string) {
	if len(deps.flagMT) > 0 {
		deps.flagMT += " \\\n "
	}
	deps.flagMT += mtTarget
}

func (deps *DepCmdFlags) SetCmdFlagMQ(mqTarget string) {
	if len(deps.flagMT) > 0 {
		deps.flagMT += " \\\n "
	}
	deps.flagMT += quoteMakefileTarget(mqTarget)
}

func (deps *DepCmdFlags) SetCmdFlagMD() {
	deps.flagMD = true
}

func (deps *DepCmdFlags) SetCmdFlagMMD() {
	deps.flagMMD = true
}

func (deps *DepCmdFlags) SetCmdFlagMP() {
	deps.flagMP = true
}

// ShouldGenerateDepFile determines whether to output .o.d file besides .o compilation
func (deps *DepCmdFlags) ShouldGenerateDepFile() bool {
	return deps.flagMD || deps.flagMMD || deps.flagMF != ""
}

// GenerateAndSaveDepFile is called if a .o.d file generation is needed.
// Prior to this, all dependencies (hFiles) are already known (via compiler -M).
// So, here we need only to satisfy depfile format rules.
func (deps *DepCmdFlags) GenerateAndSaveDepFile(invocation *Invocation, hFiles []*IncludedFile) (string, error) {
	targetName := deps.flagMT
	if len(targetName) == 0 {
		targetName = deps.calcDefaultTargetName(invocation)
	}

	depFileName := deps.calcOutputDepFileName(invocation)
	depListMainTarget := deps.calcDepListFromHFiles(invocation, hFiles)
	depTargets := []DepFileTarget{
		{targetName, depListMainTarget},
	}

	if deps.flagMP {
		// > This option instructs CPP to add a phony target for each dependency other than the main file,
		// > causing each to depend on nothing.
		for idx, depStr := range depListMainTarget {
			if idx > 0 { // 0 is cppInFile
				depTargets = append(depTargets, DepFileTarget{escapeMakefileSpaces(depStr), nil})
			}
		}
	}

	depFile := DepFile{
		DTargets: depTargets,
	}

	return depFileName, invocation.WriteFile(depFileName, depFile.WriteToBytes())
}

// calcDefaultTargetName returns targetName if no -MT and similar options passed
func (deps *DepCmdFlags) calcDefaultTargetName(invocation *Invocation) string {
	// g++ documentation doesn't satisfy its actual behavior, the implementation seems to be just
	// (remember, that objOutFile is not a full path, it's a relative as specified in cmd line)
	return invocation.objOutFile
}

// calcOutputDepFileName returns a name of generated .o.d file based on cmd flags
func (deps *DepCmdFlags) calcOutputDepFileName(invocation *Invocation) string {
	// the -MF option determines the file name
	if deps.flagMF != "" {
		return deps.flagMF
	}

	// without -MF, a file name is constructed in such a way: (a quote from the gcc documentation)
	// > The driver determines file based on whether an -o option is given.
	// > If it is, the driver uses its argument but with a suffix of .d,
	// > otherwise ... (it's not applicable to nocc, as it requires -o anyway)
	if invocation.objOutFile != "" {
		return common.ReplaceFileExt(invocation.objOutFile, ".d")
	}
	return common.ReplaceFileExt(path.Base(invocation.cppInFile), ".d")
}

// calcDepListFromHFiles fills DepFileTarget.TargetDepList
func (deps *DepCmdFlags) calcDepListFromHFiles(invocation *Invocation, hFiles []*IncludedFile) []string {
	depList := make([]string, 0, 1+len(hFiles))
	depList = append(depList, quoteMakefileTarget(invocation.cppInFile))
	for _, hFile := range hFiles {
		depList = append(depList, quoteMakefileTarget(hFile.fileName))
	}

	return depList
}

// quoteMakefileTarget escapes any characters which are special to Make
func quoteMakefileTarget(targetName string) (escaped string) {
	for i := range len(targetName) {
		switch targetName[i] {
		case ' ':
		case '\t':
			for j := i - 1; j >= 0 && targetName[j] == '\\'; j-- {
				escaped += string('\\') // escape the preceding backslashes
			}
			escaped += string('\\') // escape the space/tab
		case '$':
			escaped += string('$')
		case '#':
			escaped += string('\\')
		}
		escaped += string(targetName[i])
	}
	return
}
