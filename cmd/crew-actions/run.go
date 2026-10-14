package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/proc"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect"
)

// submitTimeout bounds the submit, which runs once.
const submitTimeout = 30 * time.Second

// runCommand runs `crew-actions run`: it submits a workflow file with its
// repository's identity, then follows the build to its end, and exits with
// its outcome.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", stderr)
	engineURL := fs.String("engine", defaultEngineURL, "the engine's `URL`")
	// A plain string, parsed after the flags, so flag never repeats it.
	repoFlag := fs.String("repo", "",
		"the build's repository, as a `URL` or host/organization/repository (default: the origin remote)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "run takes one workflow file")
	}
	if err := checkEngineURL(*engineURL); err != nil {
		return usageError(stderr, err.Error())
	}
	file := fs.Arg(0)
	content, err := os.ReadFile(file) //nolint:gosec // the file to run is the one the user names
	if err != nil {
		errorf(stderr, "%v", err)
		return exitUsage
	}
	var git proc.Group
	repo, err := repository(ctx, *repoFlag, git.Run)
	if err != nil {
		errorf(stderr, "%v", err)
		return exitUsage
	}
	return submitAndFollow(ctx, connect.NewBuildsClient(*engineURL), repo, file, content, stdout, stderr)
}

// submitAndFollow submits the workflow file, whose content is content, as a
// build of repo, prints the build's id, and follows the build to its end.
// The submit runs once, on a context of its own, so an interrupt during it
// still learns the build's id.
func submitAndFollow(
	ctx context.Context, builds api.Builds, repo model.Repository, file string, content []byte,
	stdout, stderr io.Writer,
) int {
	submitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), submitTimeout)
	resp, err := builds.Submit(submitCtx, &api.SubmitRequest{
		Host: repo.Host, Organization: repo.Organization, Repository: repo.Name,
		FileName: filepath.Base(file), Content: content,
	})
	cancel()
	switch {
	case errors.Is(err, model.ErrInvalid):
		errorf(stderr, "%v", err)
		return exitUsage
	case errors.Is(err, model.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		errorf(stderr, "submit %s: %v; whether the engine created a build is unknown", file, err)
		return exitFailure
	case err != nil:
		errorf(stderr, "submit %s: %v", file, err)
		return exitFailure
	}
	id := resp.GetBuildId()
	if _, err := fmt.Fprintf(stdout, "build %d of %s\n", id, repo); err != nil {
		return exitFailure
	}
	return follow(ctx, builds, id, stdout, stderr)
}

// follow prints the build id's events until its end, and returns the exit
// code of its outcome. An interrupt stops it following, and the build goes
// on.
func follow(ctx context.Context, builds api.Builds, id int64, stdout, stderr io.Writer) int {
	r := newRenderer(stdout)
	outcome, err := api.Follow(ctx, builds, id, r.handle, func() {
		errorf(stderr, "the engine is unavailable; reconnecting every second")
	})
	switch {
	case ctx.Err() != nil:
		errorf(stderr, "stopped following; build %d continues on the engine", id)
		return exitInterrupted
	case errors.Is(err, model.ErrNotFound):
		errorf(stderr, "build %d: the engine does not know it", id)
		return exitFailure
	case err != nil:
		errorf(stderr, "follow build %d: %v", id, err)
		return exitFailure
	}
	if _, err := fmt.Fprintf(stdout, "build %d finished with %s\n", id, outcomeName(outcome)); err != nil {
		return exitFailure
	}
	if outcome != api.Outcome_OUTCOME_SUCCESS {
		return exitFailure
	}
	return exitOK
}
