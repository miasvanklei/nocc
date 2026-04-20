package client

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nocc/internal/common"
	"nocc/pb"
)

// IncludedFile is a dependency for a .cpp compilation (a resolved #include directive, a pch file, a .cpp itself).
// Actually, fileName extension is not .h always: it could be .h/.hpp/.inc/.inl/.nocc-pch/etc.
type IncludedFile struct {
	fileName      string        // full path, starts with /
	fileSize      int64         // size in bytes
	fileSHA256    common.SHA256 // hash of contents; for KPHP, it's //crc from the header; for pch, hash of deps
	isSymlink     bool          // true if file is a symlink
	symlinkTarget string        // symlink target if isSymlink
}

func (file *IncludedFile) ToPbFileMetadata() *pb.FileMetadata {
	return &pb.FileMetadata{
		FileName:         file.fileName,
		IsSymlink:        file.isSymlink,
		SymlinkTarget:    file.symlinkTarget,
		FileSize:         file.fileSize,
		SHA256_B0_7:      file.fileSHA256.B0_7,
		SHA256_B8_15:     file.fileSHA256.B8_15,
		SHA256_B16_23:    file.fileSHA256.B16_23,
		SHA256_B24_31:    file.fileSHA256.B24_31,
	}
}

// CollectDependentIncludes collects all dependencies for an input .cpp file USING `compiler -M`.
// It launches compiler locally — but only the preprocessor, not compilation (since compilation will be done remotely).
// The -M flag of compiler runs the preprocessor and outputs dependencies of the .cpp file.
// We need dependencies to upload them to remote.
// Since compiler knows nothing about .nocc-pch files, it will output all dependencies regardless of -fpch-preprocess flag.
// We'll manually add .nocc-pch if found, so the remote is supposed to use it, not its nested dependencies, actually.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html
func CollectDependentIncludes(invocation *Invocation) (requiredFiles []*IncludedFile, cppFile *IncludedFile, pchFile *IncludedFile, err error) {
	compilerCmdLine := make([]string, 0, len(invocation.compilerArgs)+4)
	compilerCmdLine = append(compilerCmdLine, invocation.compilerArgs...)
	compilerCmdLine = append(compilerCmdLine, "-o", "-", "-M", invocation.cppInFile)

	localLaunch := LocalCompilerLaunch{
		cwd:      invocation.cwd,
		compiler: invocation.compilerName,
		cmdLine:  compilerCmdLine,
		uid:      invocation.uid,
		gid:      invocation.gid,
	}

	exitcode, compilerStdout, compilerStderr := localLaunch.RunCompilerLocally()

	if exitcode != 0 {
		err = fmt.Errorf("%s %s exited with code %d: %s", invocation.compilerName, compilerCmdLine, exitcode, string(compilerStderr))
		return
	}

	// -M outputs all dependent file names (we call them ".h files", though the extension is arbitrary).
	// We also need size and sha256 for every dependency: we'll use them to check whether they were already uploaded.
	hFilesNames := extractIncludesFromCompilerMStdout(invocation.cwd, compilerStdout, invocation.cppInFile)
	requiredFiles = make([]*IncludedFile, 0, len(hFilesNames))

	for hFileName := range hFilesNames {
		var requiredHFiles []*IncludedFile
		requiredHFiles, err = createRequiredIncludeFiles(hFilesNames, hFileName)
		if err != nil {
			return
		}
		requiredFiles = append(requiredFiles, requiredHFiles...)
	}

	for _, requiredFile := range requiredFiles {
		if pchFile != nil {
			break
		}

		searchForPch := isHeaderFileName(requiredFile.fileName)
		if searchForPch {
			pchFile, _ = createIncludedFileWithBuffer(requiredFile.fileName + ".nocc-pch")
		}
	}

	cppFile, err = createIncludedFileWithBuffer(invocation.cppInFile)

	return
}

func createRequiredIncludeFiles(hFilesNames map[string]struct{}, hFileName string) (requiredFiles []*IncludedFile, err error) {
	symlinkTarget := getSymbolicLink(hFileName)
	var hfile *IncludedFile
	requiredFiles = make([]*IncludedFile, 0, 2)

	if symlinkTarget == nil {
			hfile, err = createIncludedFileWithBuffer(hFileName)
		if err != nil {
			return
		}
		requiredFiles = append(requiredFiles, hfile)

		return
	}

	requiredFiles = append(requiredFiles, &IncludedFile{fileName: hFileName, isSymlink: true, symlinkTarget: *symlinkTarget})

	// If the file is a symlink, and the file it points to is not in the list of required header files,
	// then the file it points to is also a required file.
	if _, ok := hFilesNames[*symlinkTarget]; !ok {
		hfile, err = createIncludedFileWithBuffer(*symlinkTarget)
		if err != nil {
			return
		}

		requiredFiles = append(requiredFiles, hfile)
	}

	return
}

func getSymbolicLink(fileName string) *string {
	stat, err := os.Lstat(fileName)
	if err != nil {
		return nil
	}
	if stat.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(fileName)
		if err != nil {
			return nil
		}
		return &target
	}
	return nil
}

func createIncludedFileWithBuffer(fileName string) (*IncludedFile, error) {
	preallocatedBuf := make([]byte, 32*1024)
	dest := IncludedFile{fileName: fileName}

	file, err := os.Open(dest.fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	dest.fileSize = stat.Size()
	dest.fileSHA256, _, err = common.CalcSHA256OfFile(file, dest.fileSize, preallocatedBuf)
	return &dest, err
}

func extractIncludesFromCompilerMStdout(cwd string, compilerMStdout []byte, cppInFile string) map[string]struct{} {
	scanner := bufio.NewScanner(bytes.NewReader(compilerMStdout))
	scanner.Split(bufio.ScanWords)
	hFilesNames := map[string]struct{}{}
	for scanner.Scan() {
		line := scanner.Text()

		if line == "\\" || line == cppInFile || strings.HasSuffix(line, ".o") || strings.HasSuffix(line, ".o:") {
			continue
		}
		hFileName := common.PathAbs(cwd, line)
		hFilesNames[hFileName] = struct{}{}
	}
	return hFilesNames
}
