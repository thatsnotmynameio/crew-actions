package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// errStopped is what the hand-out returns when the engine stopped before it
// assigned a workflow.
var errStopped = errors.New("the engine is stopping")

// Next hands out the oldest pending workflow. With none, it waits for a
// write or the next tick and looks again, until its window ends, and then
// returns no workflow. It also returns none once the engine stops, and
// the context's error once ctx is done, and then assigns nothing (KTD5).
func (r *Runners) Next(ctx context.Context, _ *protocol.NextRequest) (*protocol.NextResponse, error) {
	e := r.engine
	deadline := time.NewTimer(e.window)
	defer deadline.Stop()
	ticker := time.NewTicker(e.tick)
	defer ticker.Stop()
	for {
		// Subscribed before the read, so a write after it still wakes us.
		changed := e.notify.subscribe()
		resp, err := e.handOut(ctx)
		if errors.Is(err, errStopped) {
			return &protocol.NextResponse{}, nil
		}
		if err != nil || resp != nil {
			return resp, err
		}
		select {
		case <-changed:
		case <-ticker.C:
		case <-deadline.C:
			return &protocol.NextResponse{}, nil
		case <-e.stopped:
			return &protocol.NextResponse{}, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for a workflow: %w", ctx.Err())
		}
	}
}

// handOut assigns the oldest pending workflow, in one transaction, and
// returns it, or nil when none is pending. It checks ctx and the engine's
// stop before it assigns, and returns errStopped when the engine stopped.
func (e *Engine) handOut(ctx context.Context) (*protocol.NextResponse, error) {
	var resp *protocol.NextResponse
	err := e.store.InTx(ctx, func(ctx context.Context) error {
		candidates, err := e.store.PendingWorkflows(ctx, 1)
		if err != nil {
			return fmt.Errorf("look for work: %w", err)
		}
		if len(candidates) == 0 {
			return nil
		}
		if err := e.mayAssign(ctx); err != nil {
			return err
		}
		c := candidates[0]
		version, definition, err := e.store.AssignWorkflow(ctx, c.ID, c.Version)
		if err != nil {
			return fmt.Errorf("assign the oldest: %w", err)
		}
		w, err := decode(definition)
		if err != nil {
			return err
		}
		resp = &protocol.NextResponse{WorkflowId: int64(c.ID), Version: version, Workflow: w}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hand a workflow out: %w", err)
	}
	if resp != nil {
		e.notify.broadcast()
	}
	return resp, nil
}

// mayAssign returns ctx's error once it is done, errStopped once the
// engine stopped, and nil while the caller still waits.
func (e *Engine) mayAssign(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the caller stopped waiting: %w", err)
	}
	select {
	case <-e.stopped:
		return errStopped
	default:
		return nil
	}
}

// Accept starts an attempt of the workflow Next handed out, at the version
// Next returned, with a pending step for each step of its definition. A
// workflow at another version gives model.ErrConflict, and an unknown one
// model.ErrNotFound.
func (r *Runners) Accept(ctx context.Context, req *protocol.AcceptRequest) (*protocol.AcceptResponse, error) {
	e := r.engine
	id := model.WorkflowID(req.GetWorkflowId())
	var attempt model.AttemptID
	err := e.store.InTx(ctx, func(ctx context.Context) error {
		definition, err := e.store.WorkflowDefinition(ctx, id)
		if err != nil {
			return fmt.Errorf("read its steps: %w", err)
		}
		w, err := decode(definition)
		if err != nil {
			return err
		}
		names := make([]string, len(w.GetSteps()))
		for i, s := range w.GetSteps() {
			names[i] = s.GetName()
		}
		attempt, err = e.store.StartAttempt(ctx, id, req.GetVersion(), names)
		if err != nil {
			return fmt.Errorf("start its attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("accept workflow %d: %w", id, err)
	}
	e.notify.broadcast()
	return &protocol.AcceptResponse{AttemptId: int64(attempt)}, nil
}

// decode returns the compiled workflow a definition encodes (KTD3).
func decode(definition []byte) (*protocol.Workflow, error) {
	w := &protocol.Workflow{}
	if err := proto.Unmarshal(definition, w); err != nil {
		return nil, fmt.Errorf("decode the workflow's definition: %w", err)
	}
	return w, nil
}
