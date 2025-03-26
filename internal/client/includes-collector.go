package client

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		ClientFileName: file.fileName,
		FileSize:       file.fileSize,
		SHA256_B0_7:    file.fileSHA256.B0_7,
		SHA256_B8_15:   file.fileSHA256.B8_15,
		SHA256_B16_23:  file.fileSHA256.B16_23,
		SHA256_B24_31:  file.fileSHA256.B24_31,
	}
}

// CollectDependentIncludes collects all dependencies for an input .cpp file USING `cxx -M`.
// It launches cxx locally — but only the preprocessor, not compilation (since compilation will be done remotely).
// The -M flag of cxx runs the preprocessor and outputs dependencies of the .cpp file.
// We need dependencies to upload them to remote.
// Since cxx knows nothing about .nocc-pch files, it will output all dependencies regardless of -fpch-preprocess flag.
// We'll manually add .nocc-pch if found, so the remote is supposed to use it, not its nested dependencies, actually.
// See https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html
func CollectDependentIncludes(invocation *Invocation, cwd string) (hFiles []*IncludedFile, cppFile IncludedFile, err error) {
	cppInFileAbs := invocation.GetCppInFileAbs(cwd)

	cxxCmdLine := make([]string, 0, len(invocation.cxxArgs)+2*invocation.cxxIDirs.Count()+4)
	cxxCmdLine = append(cxxCmdLine, invocation.cxxArgs...)
	cxxCmdLine = append(cxxCmdLine, invocation.cxxIDirs.AsCxxArgs()...)
	cxxCmdLine = append(cxxCmdLine, "-o", "-", "-M", cppInFileAbs)

	// drop "-Xclang -emit-pch", as it outputs pch regardless of -M flag
	// drop "-include-pch", since pch is generated on server side and does not exist locally
	for i, arg := range cxxCmdLine {
		if arg == "-Xclang" && i < len(cxxCmdLine)-1 && cxxCmdLine[i+1] == "-emit-pch" {
			cxxCmdLine = slices.Delete(cxxCmdLine, i, i+2)
			break
		}
		if arg == "-include-pch" && i < len(cxxCmdLine)-1 {
			cxxCmdLine = slices.Delete(cxxCmdLine, i, i+2)
			break
		}
	}

	cxxMCommand := exec.Command(invocation.cxxName, cxxCmdLine...)
	cxxMCommand.Dir = cwd
	var cxxMStdout, cxxMStderr bytes.Buffer
	cxxMCommand.Stdout = &cxxMStdout
	cxxMCommand.Stderr = &cxxMStderr
	if err = cxxMCommand.Run(); err != nil {
		if err.(*exec.ExitError) != nil {
			err = fmt.Errorf("%s exited with code %d: %s", invocation.cxxName, cxxMCommand.ProcessState.ExitCode(), cxxMStderr.String())
		}
		return
	}

	// -M outputs all dependent file names (we call them ".h files", though the extension is arbitrary).
	// We also need size and sha256 for every dependency: we'll use them to check whether they were already uploaded.
	hFilesNames := extractIncludesFromCxxMStdout(cxxMStdout.Bytes(), cppInFileAbs)
	hFiles = make([]*IncludedFile, 0, len(hFilesNames))
	preallocatedBuf := make([]byte, 32*1024)

	fillSizeAndMTime := func(dest *IncludedFile) error {
		file, err := os.Open(dest.fileName)
		if err == nil {
			var stat os.FileInfo
			stat, err = file.Stat()
			if err == nil {
				dest.fileSize = stat.Size()
				dest.fileSHA256, _, err = CalcSHA256OfFile(file, dest.fileSize, preallocatedBuf)
			}
			_ = file.Close()
		}
		return err
	}

	addHFile := func(hFileName string, searchForPch bool) error {
		if searchForPch {
			if pchFile := LocateOwnPchFile(hFileName, invocation.includesCache); pchFile != nil {
				hFiles = append(hFiles, pchFile)
				return nil
			}
		}
		hFile := &IncludedFile{fileName: hFileName}
		if err := fillSizeAndMTime(hFile); err != nil {
			return err
		}
		hFiles = append(hFiles, hFile)
		return nil
	}

	// do not parallelize here to fit the system ulimit -n (cause includes collecting is also launched in parallel)
	// it's slow, but enabling non-own include parser is for testing/bugs searching, so let it be
	searchForPch := isSourceFileName(cppInFileAbs)
	for _, hFileName := range hFilesNames {
		err = addHFile(hFileName, searchForPch)
		if err != nil {
			return
		}
	}

	cppFile = IncludedFile{fileName: cppInFileAbs}
	err = fillSizeAndMTime(&cppFile)
	return
}

// GetDefaultIncludeDirsOnLocal retrieves default include dirs on a local machine.
// This is done by -Wp,-v option for a no input file.
// This result is cached once nocc-daemon is started.
func GetDefaultIncludeDirsOnLocal(compileName string, lang string) (IncludeDirs, error) {
	cxxWpCommand := exec.Command(compileName, "-Wp,-v", "-x", lang, "/dev/null", "-fsyntax-only")
	var cxxWpStderr bytes.Buffer
	cxxWpCommand.Stderr = &cxxWpStderr
	if err := cxxWpCommand.Run(); err != nil {
		return IncludeDirs{}, err
	}

	return parseCxxDefaultIncludeDirsFromWpStderr(cxxWpStderr.String()), nil
}

func GetDefaultCxxIncludeDirsOnLocal(cxxName string) (IncludeDirs, error) {
	return GetDefaultIncludeDirsOnLocal(cxxName, "c++")
}

func GetDefaultCIncludeDirsOnLocal(cName string) (IncludeDirs, error) {
	return GetDefaultIncludeDirsOnLocal(cName, "c")
}

// CalcSHA256OfFile reads the opened file up to end and returns its sha256 and contents.
func CalcSHA256OfFile(file *os.File, fileSize int64, preallocatedBuf []byte) (common.SHA256, []byte, error) {
	var buffer []byte
	if fileSize > int64(len(preallocatedBuf)) {
		buffer = make([]byte, fileSize)
	} else {
		buffer = preallocatedBuf[:fileSize]
	}
	_, err := io.ReadFull(file, buffer)
	if err != nil {
		return common.SHA256{}, nil, err
	}

	hasher := sha256.New()
	_, _ = hasher.Write(buffer)
	return common.MakeSHA256Struct(hasher), buffer, nil
}

// CalcSHA256OfFileName is like CalcSHA256OfFile but for a file name, not descriptor.
func CalcSHA256OfFileName(fileName string, preallocatedBuf []byte) (common.SHA256, []byte, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return common.SHA256{}, nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return common.SHA256{}, nil, err
	}

	return CalcSHA256OfFile(file, stat.Size(), preallocatedBuf)
}

// LocateOwnPchFile finds a .nocc-pch file next to .h.
// The results are cached: if a file doesn't exist, it won't be looked up again until daemon is alive.
func LocateOwnPchFile(hFileName string, includesCache *IncludesCache) *IncludedFile {
	basehFileName := hFileName
	cutHFileName, hasSuffix := strings.CutSuffix(hFileName, ".pch")
	if hasSuffix {
		basehFileName = cutHFileName
	}
	ownPchFile := basehFileName + ".nocc-pch"
	pchCached, exists := includesCache.GetHFileInfo(ownPchFile)
	if !exists {
		if stat, err := os.Stat(ownPchFile); err == nil {
			ownPch, err := common.ParseOwnPchFile(ownPchFile)
			if err == nil {
				includesCache.AddHFileInfo(ownPchFile, stat.Size(), ownPch.PchHash, []string{})
			} else {
				logClient.Error(err)
				includesCache.AddHFileInfo(ownPchFile, -1, common.SHA256{}, []string{})
			}
		} else {
			includesCache.AddHFileInfo(ownPchFile, -1, common.SHA256{}, []string{})
		}
		pchCached, _ = includesCache.GetHFileInfo(ownPchFile)
	}

	if pchCached.fileSize == -1 {
		return nil
	}
	return &IncludedFile{ownPchFile, pchCached.fileSize, pchCached.fileSHA256}
}

// parseCxxDefaultIncludeDirsFromWpStderr parses output of a C++ compiler with -Wp,-v option.
func parseCxxDefaultIncludeDirsFromWpStderr(cxxWpStderr string) IncludeDirs {
	const (
		dirsIStart      = "#include <...>"
		dirsIquoteStart = "#include \"...\""
		dirsEnd         = "End of search list"

		stateUnknown      = 0
		stateInDirsIquote = 1
		stateInDirsI      = 2
	)

	state := stateUnknown
	defIncludeDirs := MakeIncludeDirs()
	for _, line := range strings.Split(cxxWpStderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, dirsIquoteStart) {
			state = stateInDirsIquote
		} else if strings.HasPrefix(line, dirsIStart) {
			state = stateInDirsI
		} else if strings.HasPrefix(line, dirsEnd) {
			return defIncludeDirs
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
	return defIncludeDirs
}


func extractIncludesFromCxxMStdout(cxxMStdout []byte, cppInFile string) []string {
	scanner := bufio.NewScanner(bytes.NewReader(cxxMStdout))
	scanner.Split(bufio.ScanWords)
	hFilesNames := make([]string, 0, 16)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "#pragma" && scanner.Scan() && scanner.Text() == "GCC" && scanner.Scan() && scanner.Text() == "pch_preprocess" && scanner.Scan() {
			pchFileName := strings.Trim(scanner.Text(), "\"")
			pchFileName, _ = filepath.Abs(pchFileName)
			hFilesNames = append(hFilesNames, pchFileName)
			continue
		}

		if line == "\\" || line == cppInFile || strings.HasSuffix(line, ".o") || strings.HasSuffix(line, ".o:") {
			continue
		}
		hFileName, _ := filepath.Abs(line)
		hFilesNames = append(hFilesNames, hFileName)
	}
	return hFilesNames
}
