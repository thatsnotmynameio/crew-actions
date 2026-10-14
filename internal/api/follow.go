package api

import (
	"context"
	"errors"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// reconnectEvery is how long Follow waits, after the engine was unavailable,
// before it calls it again.
const reconnectEvery = time.Second

// Follow follows the build buildID through builds to its end, and returns
// the build's outcome. It calls handle with each event once, in the order the
// engine sent it, and returns handle's error when handle fails.
//
// When the engine is unavailable, Follow calls notice, once per outage, and
// calls the engine again every second. Each stream starts from the build's
// start, so Follow drops what it already handed on: a step's state and
// output are known by attempt and position, and an output event that repeats
// bytes already handed on comes to handle with only the bytes past them.
// Follow returns any other error of the engine's, such as model.ErrNotFound,
// and the context's error when ctx ends.
func Follow(
	ctx context.Context, builds Builds, buildID int64, handle func(*FollowResponse) error, notice func(),
) (Outcome, error) {
	f := newFollower(handle)
	req := &FollowRequest{BuildId: buildID}
	for {
		err := builds.Follow(ctx, req, f.send)
		if !errors.Is(err, model.ErrUnavailable) {
			if err != nil {
				return 0, err //nolint:wrapcheck // the engine's sentinels and handle's error reach the caller as they are
			}
			return f.outcome, nil
		}
		if !f.outage {
			f.outage = true
			notice()
		}
		if err := wait(ctx, reconnectEvery); err != nil {
			return 0, err
		}
	}
}

// wait returns after d, or with the context's error when ctx ends first.
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller compares it with its context's error
	case <-timer.C:
		return nil
	}
}

// step is a step of one attempt of the build's workflow.
type step struct {
	attempt, position int32
}

// stepState is a state of one step.
type stepState struct {
	step  step
	state StepState
}

// follower hands a build's events on to handle, and remembers what it
// already handed on.
type follower struct {
	handle func(*FollowResponse) error
	// outcome is the build's, once its end was handed on.
	outcome Outcome
	// outage is true from a call the engine was unavailable for until a
	// stream sends an event.
	outage bool
	// workflows holds each workflow's last state, by name. Only a change
	// is handed on, so a workflow sent back to the queue shows its states
	// again.
	workflows map[string]WorkflowState
	// steps holds the states of each step.
	steps map[stepState]bool
	// output holds how many bytes of each step's output were handed on.
	output map[step]int64
}

func newFollower(handle func(*FollowResponse) error) *follower {
	return &follower{
		handle:    handle,
		workflows: map[string]WorkflowState{},
		steps:     map[stepState]bool{},
		output:    map[step]int64{},
	}
}

// send hands on what of e was not handed on yet.
func (f *follower) send(e *FollowResponse) error {
	f.outage = false
	e = f.next(e)
	if e == nil {
		return nil
	}
	if end := e.GetEnd(); end != nil {
		f.outcome = end.GetOutcome()
	}
	return f.handle(e)
}

// next records e and returns what of it to hand on: e itself, an output
// event trimmed to its new bytes, or nil when all of it was handed on.
func (f *follower) next(e *FollowResponse) *FollowResponse {
	switch ev := e.GetEvent().(type) {
	case *FollowResponse_Workflow:
		name, state := ev.Workflow.GetName(), ev.Workflow.GetState()
		if f.workflows[name] == state {
			return nil
		}
		f.workflows[name] = state
	case *FollowResponse_Step:
		s := stepState{step{ev.Step.GetAttempt(), ev.Step.GetPosition()}, ev.Step.GetState()}
		if f.steps[s] {
			return nil
		}
		f.steps[s] = true
	case *FollowResponse_Output:
		return f.nextOutput(e, ev.Output)
	}
	return e
}

// nextOutput records o, the output of e, and returns what of e to hand on:
// e itself, an event with only o's bytes past those already handed on, or
// nil when there are none.
func (f *follower) nextOutput(e *FollowResponse, o *OutputEvent) *FollowResponse {
	s := step{o.GetAttempt(), o.GetPosition()}
	got, data := f.output[s], o.GetData()
	end := o.GetOffset() + int64(len(data))
	if end <= got {
		return nil
	}
	f.output[s] = end
	skip := got - o.GetOffset()
	if skip <= 0 {
		return e
	}
	return &FollowResponse{Event: &FollowResponse_Output{Output: &OutputEvent{
		Attempt: o.GetAttempt(), Position: o.GetPosition(), Offset: got, Data: data[skip:],
	}}}
}
