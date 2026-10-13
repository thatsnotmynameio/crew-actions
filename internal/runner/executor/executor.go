// Package executor runs one attempt of a workflow: it sets up a workspace,
// runs the workflow's steps in it in order, and destroys it, whatever
// happens. A failing step fails the workflow, and the steps after it are
// skipped.
//
// An Observer hears how each step ends, exactly once per step: Started then
// Finished for a step that ran, or Skipped for one that did not. Its calls
// come in step order, from the goroutine that called Run, never at the same
// time. The writer Started returns receives the step's output while the
// step runs; it must not block for long, or the step blocks with it. Its
// errors never fail the step: the first one is kept in the step's Result.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend"
	"github.com/thatsnotmynameio/crew-actions/internal/spec"
)

// Outcome is how a workflow or a step ended.
type Outcome string

// The outcomes.
const (
	Success Outcome = "success"
	Failure Outcome = "failure"
)

// Observer hears how each step of a workflow ends. A step is named by its
// index in the workflow's steps.
type Observer interface {
	// Started is called before step i runs, and returns the writer its
	// output goes to.
	Started(i int) io.Writer
	// Finished is called once step i has run.
	Finished(i int, r Result)
	// Skipped is called for a step that does not run.
	Skipped(i int)
}

// Result is how a step that ran ended.
type Result struct {
	// ExitStatus is the step's exit status, or -1 when it has none, and then
	// Err says why.
	ExitStatus int
	// Err is why the step has no exit status: it could not start, its shell
	// was killed, or it was stopped.
	Err error
	// Stopped is whether the step was stopped because the attempt's context
	// ended.
	Stopped bool
	// WriteErr is the first error of the observer's writer. It never changes
	// the step's outcome.
	WriteErr error
}

// Outcome is the step's outcome: success when it exited 0. A stopped step's
// outcome is failure, but a stopped attempt has no outcome.
func (r Result) Outcome() Outcome {
	if r.Err == nil && r.ExitStatus == 0 {
		return Success
	}
	return Failure
}

// Run runs one attempt of wf: it sets up a workspace of b, runs wf's steps
// in it, telling obs how each ends, and destroys the workspace, even when an
// observer panics.
//
// It returns the workflow's outcome, or an error when the attempt reached
// none: ctx ended, and then the error wraps ctx.Err(), or the workspace could
// not be set up. When only the workspace's removal fails, the outcome is
// returned with the removal error beside it.
func Run(ctx context.Context, wf spec.Workflow, b backend.Backend, obs Observer) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		skip(obs, 0, len(wf.Steps))
		return "", fmt.Errorf("the attempt did not start: %w", err)
	}
	ws, err := b.Setup()
	if err != nil {
		skip(obs, 0, len(wf.Steps))
		return "", fmt.Errorf("set up the workspace: %w", err)
	}
	ran := false
	defer func() {
		// An observer panicked: the workspace goes all the same.
		if !ran {
			_ = ws.Destroy()
		}
	}()
	outcome, err := run(ctx, wf.Steps, ws, obs)
	ran = true
	if derr := ws.Destroy(); derr != nil {
		err = errors.Join(err, fmt.Errorf("destroy the workspace: %w", derr))
	}
	return outcome, err
}

// run runs steps in ws, in order, until one fails or ctx ends.
func run(ctx context.Context, steps []spec.Step, ws backend.Workspace, obs Observer) (Outcome, error) {
	for i, step := range steps {
		if err := ctx.Err(); err != nil {
			skip(obs, i, len(steps))
			return "", fmt.Errorf("the attempt was cancelled: %w", err)
		}
		r := runStep(ctx, ws, step, obs.Started(i))
		obs.Finished(i, r)
		if r.Stopped {
			skip(obs, i+1, len(steps))
			return "", fmt.Errorf("the attempt was stopped: %w", ctx.Err())
		}
		if r.Outcome() == Failure {
			skip(obs, i+1, len(steps))
			return Failure, nil
		}
	}
	return Success, nil
}

// runStep runs step in ws, its output going to out.
func runStep(ctx context.Context, ws backend.Workspace, step spec.Step, out io.Writer) Result {
	w := &keepingWriter{w: out}
	status, err := ws.Run(ctx, step.Commands, w)
	r := Result{ExitStatus: status, WriteErr: w.err}
	if err != nil {
		r.ExitStatus = -1
		r.Err = err
		r.Stopped = ctx.Err() != nil && errors.Is(err, ctx.Err())
	}
	return r
}

// skip tells obs that steps from to the one before to were skipped.
func skip(obs Observer, from, to int) {
	for i := from; i < to; i++ {
		obs.Skipped(i)
	}
}

// keepingWriter passes its writes to w, always reports a full write, and
// keeps w's first error. A step's output is copied from a pipe, and the copy
// stops at the first error, so a failing writer would leave the step blocked
// on a full pipe. Being one comparable value, it also gives the step's stdout
// and stderr one pipe, so their output stays in order.
type keepingWriter struct {
	w   io.Writer
	err error
}

func (k *keepingWriter) Write(p []byte) (int, error) {
	if _, err := k.w.Write(p); err != nil && k.err == nil {
		k.err = err
	}
	return len(p), nil
}
