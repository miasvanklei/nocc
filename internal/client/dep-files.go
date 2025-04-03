package client

import (
	"bytes"
	"fmt"
	"strings"
)

// DepFileTarget is one target in .o.d file:
// targetName: dep dep dep
// in a text file, deps are separated by spaces or slash+newlines
type DepFileTarget struct {
	TargetName    string
	TargetDepList []string
}

// DepFile represents a .o.d file after being parsed or at the moment of being generated
type DepFile struct {
	DTargets []DepFileTarget
}

// WriteToBytes outputs a filled dFile as .o.d representation
func (dFile *DepFile) WriteToBytes() []byte {
	b := bytes.Buffer{}

	for _, dTarget := range dFile.DTargets {
		if b.Len() > 0 {
			b.WriteRune('\n')
		}
		fmt.Fprintf(&b, "%s:", dTarget.TargetName) // note that necessary escaping should be pre-done
		if len(dTarget.TargetDepList) > 0 {
			fmt.Fprintf(&b, " %s", escapeMakefileSpaces(dTarget.TargetDepList[0]))
			for _, hDepFileName := range dTarget.TargetDepList[1:] {
				fmt.Fprintf(&b, " \\\n  %s", escapeMakefileSpaces(hDepFileName))
			}
		}
		b.WriteRune('\n')
	}

	return b.Bytes()
}

// escapeMakefileSpaces outputs a string which slashed spaces
func escapeMakefileSpaces(depItemName string) string {
	depItemName = strings.ReplaceAll(depItemName, "\n", "\\\n")
	depItemName = strings.ReplaceAll(depItemName, " ", "\\ ")
	depItemName = strings.ReplaceAll(depItemName, ":", "\\:")
	return depItemName
}
