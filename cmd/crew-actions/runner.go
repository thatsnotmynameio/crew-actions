package main

import (
	"context"
	"io"

	"github.com/thatsnotmynameio/crew-actions/internal/proc"
	"github.com/thatsnotmynameio/crew-actions/internal/runner"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend/local"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect"
)

// runnerCommand runs `crew-actions runner`: it asks the engine for work and
// runs one workflow at a time, in a new directory under the system's
// temporary directory, until ctx ends.
func runnerCommand(ctx context.Context, args []string, stderr io.Writer) int {
	fs := newFlags("runner", stderr)
	engineURL := fs.String("engine", defaultEngineURL, "the engine's `URL`")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "runner takes no argument")
	}
	if err := checkEngineURL(*engineURL); err != nil {
		return usageError(stderr, err.Error())
	}
	r := &runner.Runner{
		Engine:  connect.NewEngineClient(*engineURL),
		Backend: &local.Backend{Group: &proc.Group{}},
		Stderr:  stderr,
	}
	if err := r.Run(ctx); err != nil {
		errorf(stderr, "runner: %v", err)
		return exitFailure
	}
	return exitOK
}
