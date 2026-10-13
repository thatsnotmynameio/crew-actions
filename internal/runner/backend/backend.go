// Package backend declares where an attempt's steps run. A backend sets up a
// workspace for an attempt; the attempt's steps run in it, one after
// another, and it is destroyed when the attempt ends. Its implementations,
// such as local, are its subpackages.
package backend

import (
	"context"
	"io"
)

// Backend sets up the workspaces in which attempts run.
type Backend interface {
	// Setup sets up a new, empty workspace for one attempt.
	Setup() (Workspace, error)
}

// Workspace is where one attempt's steps run, all of them in the same
// place, so what a step leaves there the next one finds.
type Workspace interface {
	// Run runs a step's commands, in order, and returns the step's exit
	// status: 0, or the status of the command that failed, after which the
	// step's other commands do not run. The step's stdout and stderr both go
	// to out, in the order it printed them. An error means the step has no
	// exit status: it could not start, it was killed by a signal, or it was
	// stopped because ctx ended, in which case the error wraps ctx.Err().
	Run(ctx context.Context, commands []string, out io.Writer) (int, error)
	// Destroy removes the workspace and everything in it. Destroying a
	// workspace already gone does nothing.
	Destroy() error
}
