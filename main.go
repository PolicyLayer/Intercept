// Package main is the entry point for the intercept CLI. Build-time variables
// version and commit are injected via ldflags.
package main

import (
	"os"

	"github.com/policylayer/intercept/cmd"
)

// version and commit are set at build time via -ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

// main initialises the version string and delegates to the Cobra command tree.
func main() {
	cmd.SetVersion(version, commit)
	if len(os.Args) == 1 {
		cmd.PrintHelp()
		return
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
