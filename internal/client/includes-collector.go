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
	"slices"
)

// IncludedFile is a dependency for a .cpp compilation (a resolved #include directive, a pch file, a .cpp itself).
// Actually, fileName extension is not .h always: it could be .h/.hpp/.inc/.inl/.nocc-pch/etc.
type IncludedFile struct {
	fileName   string        // full path, starts with /
	fileSize   int64         // size in bytes
	fileSHA256 common.SHA256 // hash of contents; for KPHP, it's //crc from the header; for pch, hash of deps
}

func (file *IncludedFile) ToPbFileMetadata() *pb.FileMetadata {
	return &pb.FileMetadata{
		FileName:      file.fileName,
		FileSize:      file.fileSize,
		SHA256_B0_7:   file.fileSHA256.B0_7,
		SHA256_B8_15:  file.fileSHA256.B8_15,
		SHA256_B16_23: file.fileSHA256.B16_23,
		SHA256_B24_31: file.fileSHA256.B24_31,
	}
}

// CollectDependentIncludes collects all dependencies for an input .cpp file USING `compiler -M`.
// It launches compiler locally — but only the preprocessor, not compilation (since compilation will be done remotely).
// The -M flag of compiler runs the preprocessor and outputs dependencies of the .cpp file.
// We need dependencies to upload them to remote.
// Since compiler knows nothing about .nocc-pch files, it will output all dependencies regardless of -fpch-preprocess flag.
// We'll manually add .nocc-pch if found, so the remote is supposed to use it, not its nested dependencies, actually.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html
func CollectDependentIncludes(invocation *Invocation) (hFiles []*IncludedFile, cppFile *IncludedFile, pchFile *IncludedFile, err error) {
	cppInFileAbs := invocation.GetCppInFileAbs()

	compilerCmdLine := make([]string, 0, len(invocation.compilerArgs)+2*invocation.compilerIDirs.Count()+4)
	compilerCmdLine = append(compilerCmdLine, invocation.compilerArgs...)
	compilerCmdLine = append(compilerCmdLine, invocation.compilerIDirs.AsCompilerArgs()...)
	compilerCmdLine = append(compilerCmdLine, "-o", "-", "-M", cppInFileAbs)

	// drop "-Xclang -emit-pch", as it outputs pch regardless of -M flag
	for i, arg := range compilerCmdLine {
		if arg == "-Xclang" && i < len(compilerCmdLine)-1 && compilerCmdLine[i+1] == "-emit-pch" {
			compilerCmdLine = slices.Delete(compilerCmdLine, i, i+2)
			break
		}
	}

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
	hFilesNames := extractIncludesFromCompilerMStdout(compilerStdout, cppInFileAbs)
	hFiles = make([]*IncludedFile, 0, len(hFilesNames))
	preallocatedBuf := make([]byte, 32*1024)

	fillSizeAndMTime := func(fileName string) (*IncludedFile, error) {
		file, err := os.Open(fileName)
		if err == nil {
			defer file.Close()
			var stat os.FileInfo
			stat, err = file.Stat()
			if err == nil {
				dest := IncludedFile{fileName: fileName}
				dest.fileSize = stat.Size()
				dest.fileSHA256, _, err = common.CalcSHA256OfFile(file, dest.fileSize, preallocatedBuf)

				return &dest, nil
			}
		}

		return nil, err
	}

	addHFile := func(hFileName string, searchForPch bool) error {
		if searchForPch {
			if pchFile == nil {
				pchFile, _ = fillSizeAndMTime(common.ReplaceFileExt(hFileName, ".nocc-pch"))
			}
		}
		hFile, err := fillSizeAndMTime(hFileName)
		if err != nil {
			return err
		}
		hFiles = append(hFiles, hFile)
		return nil
	}

	for _, hFileName := range hFilesNames {
		searchForPch := isHeaderFileName(hFileName)
		err = addHFile(hFileName, searchForPch)
		if err != nil {
			return
		}
	}

	cppFile, err = fillSizeAndMTime(cppInFileAbs)
	return
}

func extractIncludesFromCompilerMStdout(compilerMStdout []byte, cppInFile string) []string {
	scanner := bufio.NewScanner(bytes.NewReader(compilerMStdout))
	scanner.Split(bufio.ScanWords)
	hFilesNames := make([]string, 0, 16)
	for scanner.Scan() {
		line := scanner.Text()

		if line == "\\" || line == cppInFile || strings.HasSuffix(line, ".o") || strings.HasSuffix(line, ".o:") {
			continue
		}
		hFileName, _ := filepath.Abs(line)
		hFilesNames = append(hFilesNames, hFileName)
	}
	return hFilesNames
}
