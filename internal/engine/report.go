package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// Update records the state of the step at the request's position, counted
// from 1, in its attempt: running starts it and creates its log, finished
// finishes it with the outcome and exit status, and skipped skips it. A
// position outside the attempt's steps gives model.ErrInvalid, a step or
// an attempt in another state model.ErrConflict, and an unknown attempt
// model.ErrNotFound.
func (r *Runners) Update(ctx context.Context, req *protocol.UpdateRequest) (*protocol.UpdateResponse, error) {
	e := r.engine
	attempt, position := model.AttemptID(req.GetAttemptId()), int(req.GetPosition())
	if err := e.moveStep(ctx, attempt, position, req); err != nil {
		return nil, fmt.Errorf("update step %d of attempt %d: %w",
			position, attempt, e.positionRefusal(ctx, attempt, position, err))
	}
	e.notify.broadcast()
	return &protocol.UpdateResponse{}, nil
}

// moveStep moves the step at position of the attempt to the request's
// state.
func (e *Engine) moveStep(
	ctx context.Context, attempt model.AttemptID, position int, req *protocol.UpdateRequest,
) error {
	state, err := protocol.StepStateToModel(req.GetState())
	if err != nil {
		return fmt.Errorf("read its state: %w", err)
	}
	switch state {
	case model.StepRunning:
		return e.startStep(ctx, attempt, position)
	case model.StepFinished:
		return e.finishStep(ctx, attempt, position, req)
	default: // skipped, the only other state a runner reports
		return e.skipStep(ctx, attempt, position)
	}
}

// startStep starts the step at position of the attempt, then creates its
// log, so a log exists only for a step that really started (KTD7).
func (e *Engine) startStep(ctx context.Context, attempt model.AttemptID, position int) error {
	if err := e.store.StartStep(ctx, attempt, position); err != nil {
		return fmt.Errorf("start it: %w", err)
	}
	if err := e.logs.Create(attempt, position); err != nil {
		return fmt.Errorf("create its log: %w", err)
	}
	return nil
}

// finishStep finishes the step at position of the attempt with the
// request's outcome and exit status.
func (e *Engine) finishStep(
	ctx context.Context, attempt model.AttemptID, position int, req *protocol.UpdateRequest,
) error {
	outcome, err := protocol.OutcomeToModel(req.GetOutcome())
	if err != nil {
		return fmt.Errorf("read its outcome: %w", err)
	}
	var exit *int
	if req.ExitStatus != nil {
		exit = new(int(req.GetExitStatus()))
	}
	if err := e.store.FinishStep(ctx, attempt, position, outcome, exit); err != nil {
		return fmt.Errorf("finish it: %w", err)
	}
	return nil
}

// skipStep skips the step at position of the attempt.
func (e *Engine) skipStep(ctx context.Context, attempt model.AttemptID, position int) error {
	if err := e.store.SkipStep(ctx, attempt, position); err != nil {
		return fmt.Errorf("skip it: %w", err)
	}
	return nil
}

// positionRefusal returns err, a step primitive's error, except that a
// step the attempt does not have gives model.ErrInvalid: the store says
// model.ErrNotFound both for it and for an unknown attempt.
func (e *Engine) positionRefusal(ctx context.Context, attempt model.AttemptID, position int, err error) error {
	if !errors.Is(err, model.ErrNotFound) {
		return err
	}
	_, _, _, lookErr := e.store.AttemptWorkflow(ctx, attempt) //nolint:dogsled // only whether it exists matters
	if errors.Is(lookErr, model.ErrNotFound) {
		return err
	}
	if lookErr != nil {
		return fmt.Errorf("look the attempt up: %w", lookErr)
	}
	return model.Invalid(fmt.Sprintf( //nolint:wrapcheck // model.Invalid makes this error
		"attempt %d has no step at position %d", attempt, position))
}

// Log appends a chunk of the output of the step at the request's position
// to its log, by offset, and returns the size the log holds. A step that
// never started, a negative offset or one past the log's end gives
// model.ErrInvalid.
func (r *Runners) Log(_ context.Context, req *protocol.LogRequest) (*protocol.LogResponse, error) {
	e := r.engine
	attempt, position := model.AttemptID(req.GetAttemptId()), int(req.GetPosition())
	var size int64
	var err error
	if req.GetOffset() < 0 {
		err = model.Invalid(fmt.Sprintf("offset %d is negative", req.GetOffset()))
	} else {
		size, err = e.logs.Append(attempt, position, req.GetOffset(), req.GetData())
	}
	if err != nil {
		return nil, fmt.Errorf("log step %d of attempt %d: %w", position, attempt, err)
	}
	e.notify.broadcast()
	return &protocol.LogResponse{Size: size}, nil
}

// Done ends the attempt with the request's outcome, and its workflow and
// build with it. An attempt that has ended gives model.ErrConflict, and an
// unknown one model.ErrNotFound.
func (r *Runners) Done(ctx context.Context, req *protocol.DoneRequest) (*protocol.DoneResponse, error) {
	e := r.engine
	attempt := model.AttemptID(req.GetAttemptId())
	if err := e.finish(ctx, attempt, req.GetOutcome()); err != nil {
		return nil, fmt.Errorf("finish attempt %d: %w", attempt, err)
	}
	e.notify.broadcast()
	return &protocol.DoneResponse{}, nil
}

// finish ends the attempt, its workflow and its build with o, in one
// transaction. A build has one workflow, so it takes the workflow's
// outcome.
func (e *Engine) finish(ctx context.Context, attempt model.AttemptID, o protocol.Outcome) error {
	outcome, err := protocol.OutcomeToModel(o)
	if err != nil {
		return fmt.Errorf("read its outcome: %w", err)
	}
	err = e.store.InTx(ctx, func(ctx context.Context) error {
		workflow, build, _, err := e.store.AttemptWorkflow(ctx, attempt)
		if err != nil {
			return fmt.Errorf("look it up: %w", err)
		}
		if err := e.store.FinishAttempt(ctx, attempt, outcome); err != nil {
			return fmt.Errorf("end it: %w", err)
		}
		if err := e.store.FinishWorkflow(ctx, workflow, outcome); err != nil {
			return fmt.Errorf("finish its workflow: %w", err)
		}
		if err := e.store.FinishBuild(ctx, build, outcome); err != nil {
			return fmt.Errorf("finish its build: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finish it with %s: %w", outcome, err)
	}
	return nil
}
