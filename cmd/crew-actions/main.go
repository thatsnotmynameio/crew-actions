// Command crew-actions is crew actions' binary: the engine, the runner, and
// run, which runs a workflow file by hand and follows its build.
//
// Usage:
//
//	crew-actions engine [--addr 127.0.0.1:7700] [--log-dir dir]
//	crew-actions runner [--engine http://127.0.0.1:7700]
//	crew-actions run [--engine http://127.0.0.1:7700] [--repo url|host/organization/repository] file
//	crew-actions --version
//
// The engine reads its database's URL from CREW_ACTIONS_DATABASE_URL, and
// listens on loopback only.
//
// It exits 0 on success, 1 when the build run follows fails or an
// operation fails, 2 on a usage error or when run refuses a file or an
// identity, with no build created, and 130 when run is interrupted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
)

// version is the release's tag, which the release build stamps.
var version string

// The exit codes.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130
)

// defaultEngineURL is where the runner and run reach the engine, which
// listens on defaultAddr.
const (
	defaultAddr      = "127.0.0.1:7700"
	defaultEngineURL = "http://" + defaultAddr
)

// usage is the text a usage error prints.
const usage = `usage:
  crew-actions engine [--addr 127.0.0.1:7700] [--log-dir dir]
  crew-actions runner [--engine http://127.0.0.1:7700]
  crew-actions run [--engine http://127.0.0.1:7700] [--repo url|host/organization/repository] file
  crew-actions --version

The engine reads its database's URL from CREW_ACTIONS_DATABASE_URL.

Exit status: 0 on success, 1 when the build fails or an operation fails,
2 on a usage error or a refused file or identity (no build was created),
130 when run is interrupted (the build continues).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the subcommand args name until it ends or ctx does, and returns
// the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "")
	}
	switch args[0] {
	case "--version":
		if len(args) != 1 {
			return usageError(stderr, "--version takes no argument")
		}
		return printVersion(stdout)
	case "engine":
		return engineCommand(ctx, args[1:], stdout, stderr)
	case "runner":
		return runnerCommand(ctx, args[1:], stderr)
	case "run":
		return runCommand(ctx, args[1:], stdout, stderr)
	default:
		return usageError(stderr, "unknown subcommand "+args[0])
	}
}

// printVersion prints crew-actions' version.
func printVersion(stdout io.Writer) int {
	v := version
	if v == "" {
		v = "dev"
	}
	if _, err := fmt.Fprintln(stdout, "crew-actions", v); err != nil {
		return exitFailure
	}
	return exitOK
}

// usageError prints problem, when there is one, then the usage, and returns
// the usage error's exit code.
func usageError(stderr io.Writer, problem string) int {
	if problem != "" {
		_, _ = fmt.Fprintln(stderr, "crew-actions:", problem)
	}
	_, _ = fmt.Fprint(stderr, usage)
	return exitUsage
}

// newFlags returns the flag set of the subcommand name, which prints its
// errors to stderr.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("crew-actions "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseFlags parses args with fs. It returns false, with the exit code to
// end with, when the command stops there: on -h, which exits 0, or on a
// usage error.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK, false
	case err != nil:
		return usageError(stderr, ""), false
	default:
		return 0, true
	}
}

// checkEngineURL returns an error unless u is an http or https URL with a
// host, as --engine needs.
func checkEngineURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("--engine %s is not an http:// or https:// URL", u)
	}
	return nil
}

// errorf prints one line of what went wrong to stderr.
func errorf(stderr io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(stderr, "crew-actions: "+format+"\n", args...)
}
