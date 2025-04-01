package common

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	pchContentsDepIncludesSeparator = "#=======#'\"\\/\"'#=======#"
)

type ownPchDepInclude struct {
	fileName   string
	fileSize   int64
	fileSHA256 SHA256
}

// OwnPch describes a .nocc-pch file.
// It's a nocc precompiled header generated INSTEAD OF .gch/.pch on a client side —
// and compiled on a server side into a real .gch/.pch.
//
// How does the own pch mechanism work:
//
// When a client command to generate pch is executed,
// > nocc g++ -x c++-header -o all-headers.h.gch all-headers.h
// then nocc emits all-headers.h.nocc-pch, whereas all-headers.h.gch is not produced at all.
// This is a text file containing all dependencies required to be compiled on any remote.
// See client.GenerateOwnPch.
//
// When a client collects sources and sees #include "all-headers.h", it discovers all-headers.h.nocc-pch
// and uploads it like a regular dependency (then all-headers.h itself is not uploaded at all, by the way).
//
// When all-headers.h.nocc-pch is uploaded, the remote compiles it,
// resulting in all-headers.h and all-headers.h.gch again, but stored on remote (until nocc-server restart).
// After it has been uploaded and compiled once, all other cpp files depending on this .nocc-pch
// will use already compiled .gch that is hard linked into client working dir.
// See server.PchCompilation.
//
// Note, that a hash of pch is calculated based on dependencies and cxx args.
// It means, that equal build agents will generate the same hash,
// and the pch would be uploaded and compiled remotely only once.
//
// The original .gch/.pch on client side is NOT generated, because it's useless if everything works ok.
// If remote compilation of some cpp fails for any reason, nocc will fall back to local compilation.
// In this case, local compilation will be done without precompiled header, as it doesn't exist.
type OwnPch struct {
	OwnPchFile  string
	OrigHFile   string
	OrigPchFile string
	PchHash     SHA256
	CxxName     string
	CxxArgs     []string
	CxxIDirs    []string
	DepIncludes []ownPchDepInclude
}


// ExtractAllDepsToRootDir is called on the server side to recreate a client file structure.
func (ownPch *OwnPch) ExtractAllDepsToRootDir(rootDir string) error {
	_ = os.MkdirAll(rootDir, os.ModePerm)

	ownPchFile := ownPch.OwnPchFile
	contents, err := os.ReadFile(ownPchFile)
	if err != nil {
		return err
	}

	ownPch.DepIncludes = make([]ownPchDepInclude, 0, 64)

	sepPos := bytes.Index(contents, []byte(pchContentsDepIncludesSeparator))
	for sepPos != -1 {
		dep := ownPchDepInclude{}
		namePos := sepPos + len(pchContentsDepIncludesSeparator) + 1
		sizeOffset := bytes.IndexByte(contents[namePos:], '\\')
		nlOffset := bytes.IndexByte(contents[namePos:], '\n')
		if nlOffset == -1 || sizeOffset == -1 || sizeOffset > nlOffset {
			return fmt.Errorf("corrupted pch file %q", ownPchFile)
		}

		dep.fileName = string(contents[namePos : namePos+sizeOffset-1])
		pchHexStr := ""
		if n, _ := fmt.Sscanf(string(contents[namePos+sizeOffset:namePos+nlOffset+1]), "\\%d %s\n", &dep.fileSize, &pchHexStr); n != 2 {
			return fmt.Errorf("corrupted pch file %q", ownPchFile)
		}
		if dep.fileSHA256.FromLongHexString(pchHexStr); dep.fileSHA256.IsEmpty() {
			return fmt.Errorf("corrupted pch file %q", ownPchFile)
		}
		ownPch.DepIncludes = append(ownPch.DepIncludes, dep)

		startCPos := namePos + nlOffset + 1
		endOffset := bytes.Index(contents[startCPos:], []byte(pchContentsDepIncludesSeparator))

		var depC []byte
		if endOffset == -1 {
			depC = contents[startCPos:]
			sepPos = -1
		} else {
			depC = contents[startCPos : startCPos+endOffset]
			sepPos = startCPos + endOffset
		}

		serverFileName := path.Join(rootDir, dep.fileName)
		if err = MkdirForFile(serverFileName); err != nil {
			return err
		}
		if err = os.WriteFile(serverFileName, depC, os.ModePerm); err != nil {
			return err
		}
	}

	return MkdirForFile(path.Join(rootDir, ownPch.OrigPchFile))
}

func (ownPch *OwnPch) DebugDepsStr() string {
	pchDepsStr := ""
	for _, dep := range ownPch.DepIncludes {
		pchDepsStr += fmt.Sprintf("%s %d %s, ", path.Base(dep.fileName), dep.fileSize, dep.fileSHA256.ToShortHexString())
	}
	return pchDepsStr
}

func ParseOwnPchFile(ownPchFile string) (*OwnPch, error) {
	file, err := os.Open(ownPchFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	headContents := make([]byte, 32*1024)
	_, _ = io.ReadFull(file, headContents)
	sepPos := bytes.Index(headContents, []byte(pchContentsDepIncludesSeparator))
	if sepPos == -1 {
		return nil, fmt.Errorf("corrupted pch file %q", ownPchFile)
	}

	ownPch := OwnPch{
		OwnPchFile: ownPchFile,
	}

	headLines := strings.Split(string(headContents[:sepPos]), "\n")
	for _, line := range headLines {
		if strings.HasPrefix(line, "PCH_HASH=") {
			ownPch.PchHash.FromLongHexString(line[9:])
		}
		if strings.HasPrefix(line, "ORIG_HDR=") {
			ownPch.OrigHFile = line[9:]
		}
		if strings.HasPrefix(line, "ORIG_PCH=") {
			ownPch.OrigPchFile = line[9:]
		}
		if strings.HasPrefix(line, "CXX_NAME=") {
			ownPch.CxxName = line[9:]
		}
		if strings.HasPrefix(line, "CXX_ARGS=") {
			ownPch.CxxArgs = strings.Split(line[9:], " ")
		}
		if strings.HasPrefix(line, "CXX_DIRS=") {
			ownPch.CxxIDirs = strings.Split(line[9:], " ")
		}
	}

	if len(ownPch.CxxName) == 0 || len(ownPch.CxxArgs) == 0 || len(ownPch.OrigPchFile) == 0 || ownPch.PchHash.IsEmpty() {
		return nil, fmt.Errorf("corrupted pch file %q", ownPchFile)
	}
	return &ownPch, nil
}
