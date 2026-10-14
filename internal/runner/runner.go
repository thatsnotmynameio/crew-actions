// Package runner is the process that runs the work: it asks the engine for a
// workflow, runs it through the executor, one at a time, and reports each
// step's state and output, then the attempt's outcome, over the protocol.
//
// A runner calls next, then accept with the version next returned, then, for
// each step in order, update with running, log with the step's output and
// update with finished, or update with skipped, then done. A step's finished
// comes only once its whole output has been logged, so finished also says
// that no more output follows.
//
// While idle, a next that fails because the engine is unavailable is tried
// again every second. During a workflow, a failed call cancels the attempt:
// its running step is stopped, done is not sent, and the runner reports the
// error on its stderr and asks for work again. When the runner's context
// ends, its running step is stopped and Run returns without done.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/executor"
	"github.com/thatsnotmynameio/crew-actions/internal/spec"
)

const (
	// nextWindow is how long the engine holds a next with no work before it
	// answers with none.
	nextWindow = 25 * time.Second
	// nextMargin is how much longer than nextWindow a next waits for its
	// answer, so the engine's window always ends first.
	nextMargin = 10 * time.Second
	// idleRetry is how long an idle runner waits before it tries next again
	// on an unavailable engine.
	idleRetry = time.Second
)

// Runner asks Engine for work and runs it on Backend. Engine and Backend
// must be set.
type Runner struct {
	// Engine is the engine the runner calls.
	Engine protocol.Engine
	// Backend sets up the workspaces the steps run in.
	Backend backend.Backend
	// Stderr receives what goes wrong on the runner's side; nil means
	// os.Stderr.
	Stderr io.Writer

	stderrMu sync.Mutex
}

// Run runs workflows, one at a time, until ctx ends, and then returns nil. It
// returns an error only when a next fails for a reason other than an
// unavailable engine.
func (r *Runner) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		resp, err := r.next(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		if resp.GetWorkflow() != nil {
			r.take(ctx, resp)
		}
	}
	return nil
}

// next asks the engine for work, trying again every idleRetry while the
// engine is unavailable.
func (r *Runner) next(ctx context.Context) (*protocol.NextResponse, error) {
	for {
		call, cancel := context.WithTimeout(ctx, nextWindow+nextMargin)
		resp, err := r.Engine.Next(call, &protocol.NextRequest{})
		cancel()
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, model.ErrUnavailable) {
			return nil, fmt.Errorf("ask for work: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("ask for work: %w", ctx.Err())
		case <-time.After(idleRetry):
		}
	}
}

// take accepts the workflow next handed out and runs it. A workflow another
// runner took is dropped.
func (r *Runner) take(ctx context.Context, work *protocol.NextResponse) {
	accepted, err := r.Engine.Accept(ctx, &protocol.AcceptRequest{
		WorkflowId: work.GetWorkflowId(), Version: work.GetVersion(),
	})
	if errors.Is(err, model.ErrConflict) || ctx.Err() != nil {
		return
	}
	if err != nil {
		r.reportf("accept workflow %d: %v", work.GetWorkflowId(), err)
		return
	}
	r.attempt(ctx, accepted.GetAttemptId(), workflow(work.GetWorkflow()))
}

// attempt runs wf as attempt id and reports how it ends.
func (r *Runner) attempt(ctx context.Context, id int64, wf spec.Workflow) {
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	obs := &observer{ctx: actx, cancel: cancel, engine: r.Engine, attempt: id, reportf: r.reportf}
	outcome, err := executor.Run(actx, wf, r.Backend, obs)
	switch {
	case ctx.Err() != nil:
		return
	case obs.failed() != nil:
		r.reportf("attempt %d: %v", id, obs.failed())
		return
	case outcome == "":
		// The workspace could not be set up: the attempt fails.
		r.reportf("attempt %d: %v", id, err)
		outcome = executor.Failure
	case err != nil:
		r.reportf("attempt %d: %v", id, err)
	}
	if _, err := r.Engine.Done(actx, &protocol.DoneRequest{
		AttemptId: id, Outcome: protocol.OutcomeFromModel(modelOutcome(outcome)),
	}); err != nil {
		r.reportf("attempt %d: done: %v", id, err)
	}
}

// reportf writes one line to the runner's stderr.
func (r *Runner) reportf(format string, args ...any) {
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	w := r.Stderr
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, "crew-actions: "+format+"\n", args...)
}

// workflow is the workflow next handed out, as the executor runs it.
func workflow(wf *protocol.Workflow) spec.Workflow {
	out := spec.Workflow{Name: wf.GetName()}
	for _, s := range wf.GetSteps() {
		out.Steps = append(out.Steps, spec.Step{Name: s.GetName(), Commands: s.GetCommands()})
	}
	return out
}

// modelOutcome is the model's outcome for the executor's o.
func modelOutcome(o executor.Outcome) model.Outcome {
	if o == executor.Success {
		return model.OutcomeSuccess
	}
	return model.OutcomeFailure
}
