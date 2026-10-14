package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// waitFor is how long a test waits for an event or a stream's end.
const waitFor = 5 * time.Second

// quietFor is how long a test watches a stream to see it send nothing.
const quietFor = 300 * time.Millisecond

// stream is a Follow running in the background.
type stream struct {
	events chan *api.FollowResponse
	done   chan error
	// cancel cancels the Follow's context.
	cancel context.CancelFunc
}

// follow starts following the build id.
func (f fixture) follow(t *testing.T, id model.BuildID) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	s := &stream{events: make(chan *api.FollowResponse, 1024), done: make(chan error, 1), cancel: cancel}
	go func() {
		s.done <- f.engine.Clients().Follow(ctx, &api.FollowRequest{BuildId: int64(id)},
			func(r *api.FollowResponse) error {
				s.events <- r
				return nil
			})
	}()
	return s
}

// next returns the stream's next n events, described.
func (s *stream) next(t *testing.T, n int) []string {
	t.Helper()
	got := make([]string, 0, n)
	for range n {
		select {
		case r := <-s.events:
			got = append(got, describe(r))
		case err := <-s.done:
			t.Fatalf("Follow() returned %v after %q, want %d events", err, got, n)
		case <-time.After(waitFor):
			t.Fatalf("Follow() sent %q, want %d events", got, n)
		}
	}
	return got
}

// quiet fails the test if the stream sends an event or ends soon.
func (s *stream) quiet(t *testing.T) {
	t.Helper()
	select {
	case r := <-s.events:
		t.Errorf("Follow() sent %q, want nothing yet", describe(r))
	case err := <-s.done:
		t.Errorf("Follow() returned %v, want it still open", err)
	case <-time.After(quietFor):
	}
}

// end waits for Follow to return, and returns its error and every event
// it sent that the test has not read yet.
func (s *stream) end(t *testing.T) ([]*api.FollowResponse, error) {
	t.Helper()
	select {
	case err := <-s.done:
		// Follow sent every event before it returned.
		var rest []*api.FollowResponse
		for len(s.events) > 0 {
			rest = append(rest, <-s.events)
		}
		return rest, err
	case <-time.After(waitFor):
		t.Fatal("Follow() did not return")
		return nil, nil
	}
}

// describeAll describes events.
func describeAll(events []*api.FollowResponse) []string {
	got := make([]string, len(events))
	for i, r := range events {
		got[i] = describe(r)
	}
	return got
}

// describe returns one line for the event r, such as
// "step 1.2 build finished failure exit 2" for attempt 1's step 2.
func describe(r *api.FollowResponse) string {
	switch {
	case r.GetWorkflow() != nil:
		w := r.GetWorkflow()
		return strings.TrimSpace(fmt.Sprintf("workflow %s %s %s", w.GetName(),
			lower(w.GetState(), "WORKFLOW_STATE_"), lower(w.GetOutcome(), "OUTCOME_")))
	case r.GetStep() != nil:
		s := r.GetStep()
		line := strings.TrimSpace(fmt.Sprintf("step %d.%d %s %s %s", s.GetAttempt(), s.GetPosition(), s.GetName(),
			lower(s.GetState(), "STEP_STATE_"), lower(s.GetOutcome(), "OUTCOME_")))
		if s.ExitStatus != nil {
			line += fmt.Sprintf(" exit %d", s.GetExitStatus())
		}
		return line
	case r.GetOutput() != nil:
		o := r.GetOutput()
		return fmt.Sprintf("output %d.%d @%d %q", o.GetAttempt(), o.GetPosition(), o.GetOffset(), o.GetData())
	case r.GetEnd() != nil:
		return "end " + lower(r.GetEnd().GetOutcome(), "OUTCOME_")
	default:
		return "no event"
	}
}

// lower returns the name of the enum value v without prefix, in lower
// case, and nothing for its unspecified value.
func lower(v fmt.Stringer, prefix string) string {
	name := strings.TrimPrefix(v.String(), prefix)
	if name == "UNSPECIFIED" {
		return ""
	}
	return strings.ToLower(name)
}

// merged returns events with the consecutive output chunks of one step
// joined, and fails the test when a chunk does not start where the one
// before it ended.
func merged(t *testing.T, events []*api.FollowResponse) []*api.FollowResponse {
	t.Helper()
	var out []*api.FollowResponse
	for _, r := range events {
		o := r.GetOutput()
		if o == nil || len(out) == 0 || out[len(out)-1].GetOutput() == nil {
			out = append(out, r)
			continue
		}
		last := out[len(out)-1].GetOutput()
		if last.GetAttempt() != o.GetAttempt() || last.GetPosition() != o.GetPosition() {
			out = append(out, r)
			continue
		}
		if end := last.GetOffset() + int64(len(last.GetData())); o.GetOffset() != end {
			t.Fatalf("output of step %d.%d at %d, want it at %d", o.GetAttempt(), o.GetPosition(), o.GetOffset(), end)
		}
		data := slices.Concat(last.GetData(), o.GetData())
		out[len(out)-1] = &api.FollowResponse{Event: &api.FollowResponse_Output{Output: &api.OutputEvent{
			Attempt: last.GetAttempt(), Position: last.GetPosition(), Offset: last.GetOffset(), Data: data,
		}}}
	}
	return out
}

// checkEvents fails the test unless got equals want.
func checkEvents(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("events =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// ranEvents are the events of the attempt run sends, from its first step
// on.
var ranEvents = []string{
	"step 1.1 checkout running",
	`output 1.1 @0 "on branch main\n"`,
	"step 1.1 checkout finished success exit 0",
	"step 1.2 build running",
	`output 1.2 @0 "vet: bad\n"`,
	"step 1.2 build finished failure exit 2",
	"step 1.3 test skipped",
	"workflow test finished failure",
	"end failure",
}

// run reports the attempt a through the engine, as a runner would, to the
// end that ranEvents describes, writing each step's output in two chunks.
func (f fixture) run(t *testing.T, a model.AttemptID) {
	t.Helper()
	running, finished := protocol.StepState_STEP_STATE_RUNNING, protocol.StepState_STEP_STATE_FINISHED
	none, success, failure := protocol.Outcome_OUTCOME_UNSPECIFIED, protocol.Outcome_OUTCOME_SUCCESS,
		protocol.Outcome_OUTCOME_FAILURE
	f.update(t, a, 1, running, none, nil)
	f.mustLog(t, a, 1, 0, "on branch ")
	f.mustLog(t, a, 1, 10, "main\n")
	f.update(t, a, 1, finished, success, new(int32(0)))
	f.update(t, a, 2, running, none, nil)
	f.mustLog(t, a, 2, 0, "vet: ")
	f.mustLog(t, a, 2, 5, "bad\n")
	f.update(t, a, 2, finished, failure, new(int32(2)))
	f.update(t, a, 3, protocol.StepState_STEP_STATE_SKIPPED, none, nil)
	if _, err := f.engine.Runners().Done(t.Context(), &protocol.DoneRequest{
		AttemptId: int64(a), Outcome: failure,
	}); err != nil {
		t.Fatalf("Done() error = %v", err)
	}
}

// mustLog calls Log and fails the test on an error.
func (f fixture) mustLog(t *testing.T, a model.AttemptID, position int32, offset int64, data string) {
	t.Helper()
	if _, err := f.log(t, a, position, offset, data); err != nil {
		t.Fatalf("Log(%d, %d) error = %v", position, offset, err)
	}
}

// The quiet writes skip the notifier, so a stream sees them only at its
// next pass. With a long tick, that pass comes only when the test wakes it.

// quietly runs a write that skips the notifier, and fails the test on its
// error.
func quietly(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("quiet write error = %v", err)
	}
}

// quietLog appends data at offset to the log of the step at position of
// the attempt.
func (f fixture) quietLog(t *testing.T, a model.AttemptID, position int, offset int64, data string) {
	t.Helper()
	_, err := f.logs.Append(a, position, offset, []byte(data))
	quietly(t, err)
}

// wake wakes every stream, as an engine's write does.
func (f fixture) wake() {
	f.engine.notify.broadcast()
}

func TestFollowAFinishedBuildReplaysItThenEnds(t *testing.T) {
	f := newFixture(t)
	id, a := f.accepted(t)
	f.run(t, a)
	events, err := f.follow(t, id).end(t)
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	checkEvents(t, describeAll(merged(t, events)), ranEvents)
}

func TestFollowSendsEveryStepBeforeTheWorkflowFinishedInOnePass(t *testing.T) {
	f := newFixture(t)
	f.engine.tick = time.Minute
	id, a := f.accepted(t)
	f.update(t, a, 1, protocol.StepState_STEP_STATE_RUNNING, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	s := f.follow(t, id)
	checkEvents(t, s.next(t, 2), []string{"workflow test running", "step 1.1 checkout running"})
	s.quiet(t)

	ctx := t.Context()
	f.quietLog(t, a, 1, 0, "on branch main\n")
	quietly(t, f.store.FinishStep(ctx, a, 1, model.OutcomeSuccess, new(0)))
	quietly(t, f.engine.startStep(ctx, a, 2))
	f.quietLog(t, a, 2, 0, "vet: bad\n")
	quietly(t, f.store.FinishStep(ctx, a, 2, model.OutcomeFailure, new(2)))
	quietly(t, f.store.SkipStep(ctx, a, 3))
	quietly(t, f.engine.finish(ctx, a, protocol.Outcome_OUTCOME_FAILURE))
	f.wake()

	events, err := s.end(t)
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	checkEvents(t, describeAll(events), ranEvents[1:])
}

func TestFollowSendsAStepThatRanBetweenPassesAsRunningOutputFinished(t *testing.T) {
	f := newFixture(t)
	f.engine.tick = time.Minute
	id, a := f.accepted(t)
	s := f.follow(t, id)
	checkEvents(t, s.next(t, 1), []string{"workflow test running"})
	s.quiet(t)

	ctx := t.Context()
	quietly(t, f.engine.startStep(ctx, a, 1))
	f.quietLog(t, a, 1, 0, "on branch main\n")
	quietly(t, f.store.FinishStep(ctx, a, 1, model.OutcomeSuccess, new(0)))
	f.wake()

	checkEvents(t, s.next(t, 3), ranEvents[:3])
	s.quiet(t)
	s.cancel()
	if _, err := s.end(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Follow() error = %v, want %v", err, context.Canceled)
	}
}

func TestFollowSendsOutputAppendedWhileItIsOpenBeforeTheStepFinished(t *testing.T) {
	f := newFixture(t)
	id, a := f.accepted(t)
	s := f.follow(t, id)
	checkEvents(t, s.next(t, 1), []string{"workflow test running"})
	f.run(t, a)
	events, err := s.end(t)
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	checkEvents(t, describeAll(merged(t, events)), ranEvents)
}

func TestFollowSendsLargeOutputInChunks(t *testing.T) {
	f := newFixture(t)
	id, a := f.accepted(t)
	running, finished := protocol.StepState_STEP_STATE_RUNNING, protocol.StepState_STEP_STATE_FINISHED
	none, success := protocol.Outcome_OUTCOME_UNSPECIFIED, protocol.Outcome_OUTCOME_SUCCESS
	f.update(t, a, 1, running, none, nil)
	output := bytes.Repeat([]byte("0123456789abcdef"), 150<<10/16)
	f.mustLog(t, a, 1, 0, string(output))
	f.update(t, a, 1, finished, success, new(int32(0)))
	f.update(t, a, 2, protocol.StepState_STEP_STATE_SKIPPED, none, nil)
	f.update(t, a, 3, protocol.StepState_STEP_STATE_SKIPPED, none, nil)
	if _, err := f.engine.Runners().Done(t.Context(), &protocol.DoneRequest{
		AttemptId: int64(a), Outcome: success,
	}); err != nil {
		t.Fatalf("Done() error = %v", err)
	}

	events, err := f.follow(t, id).end(t)
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	var offsets []int64
	var got []byte
	for _, r := range events {
		if o := r.GetOutput(); o != nil {
			if len(o.GetData()) > 64<<10 {
				t.Errorf("chunk at %d holds %d bytes, want at most 64 KiB", o.GetOffset(), len(o.GetData()))
			}
			offsets = append(offsets, o.GetOffset())
			got = append(got, o.GetData()...)
		}
	}
	if want := []int64{0, 64 << 10, 128 << 10}; !slices.Equal(offsets, want) {
		t.Errorf("chunk offsets = %v, want %v", offsets, want)
	}
	if !bytes.Equal(got, output) {
		t.Errorf("output = %d bytes, want the %d logged", len(got), len(output))
	}
}

func TestFollowARunningStepWithNoLogYetGoesOn(t *testing.T) {
	f := newFixture(t)
	f.engine.tick = time.Minute
	id, a := f.accepted(t)
	// Started in the store alone, so the step has no log file yet.
	quietly(t, f.store.StartStep(t.Context(), a, 1))
	s := f.follow(t, id)
	checkEvents(t, s.next(t, 2), []string{"workflow test running", "step 1.1 checkout running"})
	s.quiet(t)

	quietly(t, f.logs.Create(a, 1))
	f.mustLog(t, a, 1, 0, "late\n")
	checkEvents(t, s.next(t, 1), []string{`output 1.1 @0 "late\n"`})
}

func TestFollowAnUnknownBuild(t *testing.T) {
	f := newFixture(t)
	err := f.engine.Clients().Follow(t.Context(), &api.FollowRequest{BuildId: 42},
		func(r *api.FollowResponse) error {
			t.Errorf("Follow() sent %q for an unknown build", describe(r))
			return nil
		})
	if !errors.Is(err, model.ErrNotFound) {
		t.Errorf("Follow() of an unknown build error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestStoppingTheEngineEndsAnOpenFollow(t *testing.T) {
	f := newFixture(t)
	id, _ := f.accepted(t)
	s := f.follow(t, id)
	checkEvents(t, s.next(t, 1), []string{"workflow test running"})
	f.stop()
	if _, err := s.end(t); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Follow() error = %v, want %v", err, model.ErrUnavailable)
	}
}

func TestFollowReturnsTheErrorOfSend(t *testing.T) {
	f := newFixture(t)
	id, _ := f.accepted(t)
	lost := errors.New("the client went away")
	sent := 0
	err := f.engine.Clients().Follow(t.Context(), &api.FollowRequest{BuildId: int64(id)},
		func(*api.FollowResponse) error {
			sent++
			return lost
		})
	if !errors.Is(err, lost) || sent != 1 {
		t.Errorf("Follow() = %v after %d events, want %v after 1", err, sent, lost)
	}
}
