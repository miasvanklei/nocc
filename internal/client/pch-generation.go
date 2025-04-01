package client

import (
	"os"

	"nocc/internal/common"
)

// GenerateOwnPch collects all dependencies for own .nocc-pch generation.
// When we need to generate .gch/.pch on a client side, we generate .nocc-pch INSTEAD.
// This file is later discovered as a dependency, and after being uploaded, is compiled to real .gch/.pch on remote.
// See comments above common.OwnPch.
func GenerateOwnPch(daemon *Daemon, cwd string, invocation *Invocation) (*common.OwnPch, error) {
	ownPch := &common.OwnPch{
		OwnPchFile:  common.ReplaceFileExt(invocation.objOutFile, ".nocc-pch"),
		OrigHFile:   invocation.cppInFile,
		OrigPchFile: invocation.objOutFile,
		CxxName:     invocation.compilerName,
		CxxArgs:     invocation.compilerArgs,
		CxxIDirs:    append(invocation.compilerIDirs.AsCompilerArgs(), invocation.includesCache.defIDirs.AsCompilerArgs()...),
	}
	_ = os.Remove(ownPch.OwnPchFile) // if a previous version exists

	hFiles, inHFile, _, err := CollectDependentIncludes(invocation)
	if err != nil {
		return nil, err
	}

	ownPch.AddDepInclude(inHFile.fileName, inHFile.fileSize, inHFile.fileSHA256)
	for _, hFile := range hFiles {
		ownPch.AddDepInclude(hFile.fileName, hFile.fileSize, hFile.fileSHA256)
	}
	ownPch.CalcPchHash()

	return ownPch, nil
}
