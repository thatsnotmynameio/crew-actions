package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// stream is one call of Follow that a fake scripts: the events it sends,
// then the error it returns.
type stream struct {
	events []*FollowResponse
	err    error
}

// fakeBuilds is an api.Builds whose Follow plays its streams in order, one
// per call, and records when each call came.
type fakeBuilds struct {
	streams []stream
	calls   []time.Time
}

func (f *fakeBuilds) Submit(context.Context, *SubmitRequest) (*SubmitResponse, error) {
	return nil, errors.New("not scripted")
}

func (f *fakeBuilds) Follow(ctx context.Context, req *FollowRequest, send func(*FollowResponse) error) error {
	if req.GetBuildId() != 42 {
		return fmt.Errorf("Follow(%d), want build 42", req.GetBuildId())
	}
	f.calls = append(f.calls, time.Now())
	if len(f.calls) > len(f.streams) {
		<-ctx.Done()
		return ctx.Err() //nolint:wrapcheck // Follow returns its context's error as it is
	}
	s := f.streams[len(f.calls)-1]
	for _, e := range s.events {
		if err := send(e); err != nil {
			return err
		}
	}
	return s.err
}

func workflowEvent(state WorkflowState) *FollowResponse {
	return &FollowResponse{Event: &FollowResponse_Workflow{Workflow: &WorkflowEvent{Name: "build", State: state}}}
}

func stepEvent(attempt, position int32, state StepState) *FollowResponse {
	return &FollowResponse{Event: &FollowResponse_Step{Step: &StepEvent{
		Attempt: attempt, Position: position, Name: fmt.Sprintf("step %d", position), State: state,
	}}}
}

func outputEvent(attempt, position int32, offset int64, data string) *FollowResponse {
	return &FollowResponse{Event: &FollowResponse_Output{Output: &OutputEvent{
		Attempt: attempt, Position: position, Offset: offset, Data: []byte(data),
	}}}
}

func endEvent(outcome Outcome) *FollowResponse {
	return &FollowResponse{Event: &FollowResponse_End{End: &BuildEnd{Outcome: outcome}}}
}

// follow runs Follow against f for build 42 and returns its result, the
// events its callback got and how many times it noticed an outage.
func follow(ctx context.Context, f *fakeBuilds) (Outcome, []*FollowResponse, int, error) {
	var got []*FollowResponse
	notices := 0
	outcome, err := Follow(ctx, f, 42,
		func(e *FollowResponse) error {
			got = append(got, e)
			return nil
		},
		func() { notices++ })
	return outcome, got, notices, err
}

// checkEvents fails t unless got holds the events of want, in order.
func checkEvents(t *testing.T, got, want []*FollowResponse) {
	t.Helper()
	if !slices.EqualFunc(got, want, func(a, b *FollowResponse) bool { return proto.Equal(a, b) }) {
		t.Errorf("callback got %d events:\n%v\nwant %d:\n%v", len(got), got, len(want), want)
	}
}

func TestFollowResumesAfterAnOutageWithoutRepeating(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		running := []*FollowResponse{
			workflowEvent(WorkflowState_WORKFLOW_STATE_PENDING),
			workflowEvent(WorkflowState_WORKFLOW_STATE_RUNNING),
			stepEvent(1, 1, StepState_STEP_STATE_RUNNING),
			outputEvent(1, 1, 0, "hel"),
		}
		f := &fakeBuilds{streams: []stream{
			{events: running, err: fmt.Errorf("%w: the engine stopped", model.ErrUnavailable)},
			{err: model.ErrUnavailable},
			{events: []*FollowResponse{
				workflowEvent(WorkflowState_WORKFLOW_STATE_RUNNING),
				stepEvent(1, 1, StepState_STEP_STATE_RUNNING),
				outputEvent(1, 1, 0, "hello\n"),
				stepEvent(1, 1, StepState_STEP_STATE_FINISHED),
				stepEvent(1, 2, StepState_STEP_STATE_RUNNING),
				outputEvent(1, 2, 0, "bye\n"),
				stepEvent(1, 2, StepState_STEP_STATE_FINISHED),
				workflowEvent(WorkflowState_WORKFLOW_STATE_FINISHED),
				endEvent(Outcome_OUTCOME_SUCCESS),
			}},
		}}

		outcome, got, notices, err := follow(t.Context(), f)
		if err != nil || outcome != Outcome_OUTCOME_SUCCESS {
			t.Fatalf("Follow = %v, %v, want OUTCOME_SUCCESS, nil", outcome, err)
		}
		checkEvents(t, got, append(slices.Clone(running),
			outputEvent(1, 1, 3, "lo\n"),
			stepEvent(1, 1, StepState_STEP_STATE_FINISHED),
			stepEvent(1, 2, StepState_STEP_STATE_RUNNING),
			outputEvent(1, 2, 0, "bye\n"),
			stepEvent(1, 2, StepState_STEP_STATE_FINISHED),
			workflowEvent(WorkflowState_WORKFLOW_STATE_FINISHED),
			endEvent(Outcome_OUTCOME_SUCCESS),
		))
		if notices != 1 {
			t.Errorf("notice called %d times for one outage, want 1", notices)
		}
		if len(f.calls) != 3 {
			t.Fatalf("Follow called %d times, want 3", len(f.calls))
		}
		for i := 1; i < len(f.calls); i++ {
			if gap := f.calls[i].Sub(f.calls[i-1]); gap != time.Second {
				t.Errorf("call %d came %v after the one before, want 1s", i+1, gap)
			}
		}
	})
}

func TestFollowNoticesEachOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeBuilds{streams: []stream{
			{err: model.ErrUnavailable},
			{events: []*FollowResponse{workflowEvent(WorkflowState_WORKFLOW_STATE_PENDING)}, err: model.ErrUnavailable},
			{events: []*FollowResponse{endEvent(Outcome_OUTCOME_FAILURE)}},
		}}

		outcome, _, notices, err := follow(t.Context(), f)
		if err != nil || outcome != Outcome_OUTCOME_FAILURE {
			t.Fatalf("Follow = %v, %v, want OUTCOME_FAILURE, nil", outcome, err)
		}
		if notices != 2 {
			t.Errorf("notice called %d times for two outages, want 2", notices)
		}
	})
}

func TestFollowDeliversASecondAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := []*FollowResponse{
			workflowEvent(WorkflowState_WORKFLOW_STATE_RUNNING),
			stepEvent(1, 1, StepState_STEP_STATE_RUNNING),
			outputEvent(1, 1, 0, "first\n"),
			stepEvent(1, 1, StepState_STEP_STATE_FINISHED),
		}
		second := []*FollowResponse{
			stepEvent(2, 1, StepState_STEP_STATE_RUNNING),
			outputEvent(2, 1, 0, "second\n"),
			stepEvent(2, 1, StepState_STEP_STATE_FINISHED),
			workflowEvent(WorkflowState_WORKFLOW_STATE_FINISHED),
			endEvent(Outcome_OUTCOME_SUCCESS),
		}
		requeued := []*FollowResponse{
			workflowEvent(WorkflowState_WORKFLOW_STATE_PENDING),
			workflowEvent(WorkflowState_WORKFLOW_STATE_RUNNING),
		}
		f := &fakeBuilds{streams: []stream{
			{events: first, err: model.ErrUnavailable},
			{events: slices.Concat(first, requeued, second)},
		}}

		outcome, got, _, err := follow(t.Context(), f)
		if err != nil || outcome != Outcome_OUTCOME_SUCCESS {
			t.Fatalf("Follow = %v, %v, want OUTCOME_SUCCESS, nil", outcome, err)
		}
		checkEvents(t, got, slices.Concat(first, requeued, second))
	})
}

func TestFollowReturnsNotFoundOnReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeBuilds{streams: []stream{
			{events: []*FollowResponse{workflowEvent(WorkflowState_WORKFLOW_STATE_PENDING)}, err: model.ErrUnavailable},
			{err: model.ErrNotFound},
		}}

		if _, _, _, err := follow(t.Context(), f); !errors.Is(err, model.ErrNotFound) {
			t.Errorf("Follow error = %v, want one matching model.ErrNotFound", err)
		}
	})
}

func TestFollowReturnsWhenCancelledDuringTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &fakeBuilds{streams: []stream{{err: model.ErrUnavailable}}}
		done := make(chan error, 1)
		go func() {
			_, _, _, err := follow(ctx, f)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Follow error = %v, want context.Canceled", err)
		}
		if len(f.calls) != 1 {
			t.Errorf("Follow called %d times, want 1: it reconnected after its context ended", len(f.calls))
		}
	})
}

func TestFollowReturnsTheCallbacksError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeBuilds{streams: []stream{{events: []*FollowResponse{endEvent(Outcome_OUTCOME_SUCCESS)}}}}
		stop := errors.New("stdout closed")
		_, err := Follow(t.Context(), f, 42, func(*FollowResponse) error { return stop }, func() {})
		if !errors.Is(err, stop) {
			t.Errorf("Follow error = %v, want the callback's", err)
		}
	})
}
