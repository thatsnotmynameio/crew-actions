package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// chunk is the most output one event carries.
const chunk = 64 << 10

// Follow calls send with each event of the build the request names, from
// its start, and returns once it sent the build's end. It looks at the
// build in passes: one after each write the engine notifies, and one each
// tick. A pass reads the build's snapshot, then the size of each started
// step's log, and sends what changed since the pass before, in an order
// that never shows a state before its output (KTD9). Per workflow:
//
//  1. its new state, unless it is finished;
//  2. per attempt and step, a running it missed, then its new output, in
//     chunks of at most 64 KiB, then its terminal state;
//  3. its finished, with its outcome.
//
// After every workflow comes the build's end, once the build is finished.
//
// An unknown build gives model.ErrNotFound, and an engine that stops ends
// the stream with model.ErrUnavailable. It returns send's error when send
// fails, and the context's error once ctx is done.
func (c *Clients) Follow(ctx context.Context, req *api.FollowRequest, send func(*api.FollowResponse) error) error {
	e := c.engine
	id := model.BuildID(req.GetBuildId())
	f := &follower{
		engine: e, build: id, send: send,
		workflows: map[model.WorkflowID]model.WorkflowState{}, steps: map[stepKey]sentStep{},
	}
	ticker := time.NewTicker(e.tick)
	defer ticker.Stop()
	for {
		// Subscribed before the read, so a write after it still wakes us.
		changed := e.notify.subscribe()
		ended, err := f.pass(ctx)
		if err != nil {
			return fmt.Errorf("follow build %d: %w", id, err)
		}
		if ended {
			return nil
		}
		select {
		case <-changed:
		case <-ticker.C:
		case <-e.stopped:
			return fmt.Errorf("follow build %d: the engine is stopping: %w", id, model.ErrUnavailable)
		case <-ctx.Done():
			return fmt.Errorf("follow build %d: %w", id, ctx.Err())
		}
	}
}

// follower is one Follow's stream, and what it has sent so far.
type follower struct {
	engine *Engine
	build  model.BuildID
	send   func(*api.FollowResponse) error
	// workflows holds the last state sent of each workflow.
	workflows map[model.WorkflowID]model.WorkflowState
	// steps holds what was sent of each step of each attempt.
	steps map[stepKey]sentStep
}

// stepKey names one step of one attempt.
type stepKey struct {
	attempt  model.AttemptID
	position int
}

// sentStep is what a stream has sent of one step of one attempt.
type sentStep struct {
	// running is whether its running was sent, and ended whether its
	// terminal state was.
	running, ended bool
	// offset is how many bytes of its output were sent.
	offset int64
}

// pass sends what changed in the build since the last pass, and returns
// whether it sent the build's end.
func (f *follower) pass(ctx context.Context) (bool, error) {
	b, err := f.engine.store.GetBuild(ctx, f.build)
	if err != nil {
		return false, fmt.Errorf("read the build: %w", err)
	}
	// Read after the snapshot: a runner sends a step's output before its
	// finished, so the logs hold every byte the snapshot's states imply.
	sizes, err := f.sizes(b)
	if err != nil {
		return false, err
	}
	for _, w := range b.Workflows {
		if err := f.workflow(w, sizes); err != nil {
			return false, err
		}
	}
	if b.State != model.BuildFinished {
		return false, nil
	}
	return true, f.send(&api.FollowResponse{Event: &api.FollowResponse_End{
		End: &api.BuildEnd{Outcome: outcome(b.Outcome)},
	}})
}

// sizes returns the size of the log of every step that started in the
// snapshot b. A step with no log yet holds 0 bytes.
func (f *follower) sizes(b model.Build) (map[stepKey]int64, error) {
	sizes := map[stepKey]int64{}
	for _, w := range b.Workflows {
		for _, a := range w.Attempts {
			for _, s := range a.Steps {
				if !started(s.State) {
					continue
				}
				size, err := f.engine.logs.Size(a.ID, s.Position)
				if err != nil {
					return nil, fmt.Errorf("read the size of a log: %w", err)
				}
				sizes[stepKey{attempt: a.ID, position: s.Position}] = size
			}
		}
	}
	return sizes, nil
}

// workflow sends what changed in the workflow w, whose started steps' logs
// hold sizes.
func (f *follower) workflow(w model.Workflow, sizes map[stepKey]int64) error {
	if w.State != model.WorkflowFinished && f.workflows[w.ID] != w.State {
		if err := f.sendWorkflow(w); err != nil {
			return err
		}
	}
	for _, a := range w.Attempts {
		for _, s := range a.Steps {
			if err := f.step(a, s, sizes[stepKey{attempt: a.ID, position: s.Position}]); err != nil {
				return err
			}
		}
	}
	if w.State == model.WorkflowFinished && f.workflows[w.ID] != w.State {
		return f.sendWorkflow(w)
	}
	return nil
}

// sendWorkflow sends the state of the workflow w.
func (f *follower) sendWorkflow(w model.Workflow) error {
	f.workflows[w.ID] = w.State
	return f.send(&api.FollowResponse{Event: &api.FollowResponse_Workflow{Workflow: &api.WorkflowEvent{
		Name: w.Name, State: api.WorkflowStateFromModel(w.State), Outcome: outcome(w.Outcome),
	}}})
}

// step sends what changed in the step s of the attempt a, whose log holds
// size bytes: a running it missed, its new output, then its terminal state.
func (f *follower) step(a model.Attempt, s model.Step, size int64) error {
	key := stepKey{attempt: a.ID, position: s.Position}
	sent := f.steps[key]
	defer func() { f.steps[key] = sent }()
	if started(s.State) && !sent.running {
		sent.running = true
		if err := f.sendStep(a, model.Step{Position: s.Position, Name: s.Name, State: model.StepRunning}); err != nil {
			return err
		}
	}
	for sent.offset < size {
		data, err := f.engine.logs.Read(a.ID, s.Position, sent.offset, int(min(size-sent.offset, chunk)))
		if err != nil {
			return fmt.Errorf("read a log: %w", err)
		}
		if len(data) == 0 {
			// The log lost the bytes its size counted; the next pass looks again.
			break
		}
		err = f.send(&api.FollowResponse{Event: &api.FollowResponse_Output{Output: &api.OutputEvent{
			Attempt: int32Of(a.Number), Position: int32Of(s.Position), Offset: sent.offset, Data: data,
		}}})
		if err != nil {
			return err
		}
		sent.offset += int64(len(data))
	}
	if (s.State == model.StepFinished || s.State == model.StepSkipped) && !sent.ended {
		sent.ended = true
		return f.sendStep(a, s)
	}
	return nil
}

// sendStep sends the state of the step s of the attempt a.
func (f *follower) sendStep(a model.Attempt, s model.Step) error {
	event := &api.StepEvent{
		Attempt: int32Of(a.Number), Position: int32Of(s.Position), Name: s.Name,
		State: api.StepStateFromModel(s.State), Outcome: outcome(s.Outcome),
	}
	if s.ExitStatus != nil {
		event.ExitStatus = new(int32Of(*s.ExitStatus))
	}
	return f.send(&api.FollowResponse{Event: &api.FollowResponse_Step{Step: event}})
}

// started reports whether a step in state s has started, and so may have
// output.
func started(s model.StepState) bool {
	return s == model.StepRunning || s == model.StepFinished
}

// int32Of returns n, which the store read from an integer column, as the
// int32 it was stored as.
func int32Of(n int) int32 {
	return int32(n) //nolint:gosec // an integer column holds an int32, so n fits
}

// outcome returns the API's outcome for o, and OUTCOME_UNSPECIFIED for
// none.
func outcome(o *model.Outcome) api.Outcome {
	if o == nil {
		return api.Outcome_OUTCOME_UNSPECIFIED
	}
	return api.OutcomeFromModel(*o)
}
