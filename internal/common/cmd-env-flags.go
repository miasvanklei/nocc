// This module provides integration of the flag package with environment variables.
// The purpose to launch either `nocc-server -log-filename fn.log` or `NOCC_LOG_FILENAME=fn.log nocc-server`.
// See usages of CmdEnvString and others.

package common

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type cmdLineArg interface {
	flag.Value
	isFlagSet() bool
	getCmdName() string
	getDescription() string
}

var allCmdLineArgs []cmdLineArg

type cmdLineArgBool struct {
	cmdName string
	usage   string

	isSet bool
	def   bool
	value bool
}

func (s *cmdLineArgBool) String() string {
	return strconv.FormatBool(s.value)
}

func (s *cmdLineArgBool) Set(v string) error {
	s.isSet = true
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	s.value = b
	return nil
}

func (s *cmdLineArgBool) IsBoolFlag() bool {
	return true
}

func (s *cmdLineArgBool) getDescription() string {
	return s.usage
}

func (s *cmdLineArgBool) isFlagSet() bool {
	return s.isSet
}

func (s *cmdLineArgBool) getCmdName() string {
	return s.cmdName
}

func initCmdFlag(s cmdLineArg, cmdName string, usage string) {
	if cmdName != "" { // only env var makes sense
		flag.Var(s, cmdName, usage)
	}
}

func customPrintUsage() {
	fmt.Printf("Usage of %s:\n\n", os.Args[0])
	for _, f := range allCmdLineArgs {
		if f.getCmdName() == "v" { // don't print "-v" (shortcut for -version)
			continue
		}

		valueHint := ""
		if f.getCmdName() == "version" {
			valueHint = " / -v"
		}
		if f.getCmdName() != "" {
			fmt.Printf("  -%s%s\n", f.getCmdName(), valueHint)
		}
		fmt.Print("    \t")
		fmt.Print(strings.ReplaceAll(f.getDescription(), "\n", "\n    \t"))
		fmt.Print("\n\n")
	}
}

func CmdEnvBool(usage string, def bool, cmdFlagName string) *bool {
	var sf = &cmdLineArgBool{cmdFlagName, usage, false, def, def}
	allCmdLineArgs = append(allCmdLineArgs, sf)
	initCmdFlag(sf, cmdFlagName, usage)
	return &sf.value
}

func ParseCmdFlagsCombiningWithEnv() {
	flag.Usage = customPrintUsage
	flag.Parse()
}
