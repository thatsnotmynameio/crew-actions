// Command crew-actions is crew actions' binary. For now it only prints its
// version.
//
// Usage:
//
//	crew-actions --version
//
// It exits 0 once the version is printed, 1 when the output fails, and 2 on
// any other command line.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the release's tag, which the release build stamps.
var version string

// The exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run prints crew-actions' version for --version and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--version" {
		_, _ = fmt.Fprintln(stderr, "usage: crew-actions --version")
		return exitUsage
	}
	v := version
	if v == "" {
		v = "dev"
	}
	if _, err := fmt.Fprintln(stdout, "crew-actions", v); err != nil {
		return exitFailure
	}
	return exitOK
}
