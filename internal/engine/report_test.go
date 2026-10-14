package engine

import (
	"errors"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// update calls Update for the step at position of the attempt, and fails
// the test on an error. exit is nil for no exit status.
func (f fixture) update(
	t *testing.T, attempt model.AttemptID, position int32, state protocol.StepState, outcome protocol.Outcome, exit *int32,
) {
	t.Helper()
	_, err := f.engine.Runners().Update(t.Context(), &protocol.UpdateRequest{
		AttemptId: int64(attempt), Position: position, State: state, Outcome: outcome, ExitStatus: exit,
	})
	if err != nil {
		t.Fatalf("Update(%d, %s) error = %v", position, state, err)
	}
}

// log calls Log and returns the size it answered and its error.
func (f fixture) log(t *testing.T, attempt model.AttemptID, position int32, offset int64, data string) (int64, error) {
	t.Helper()
	resp, err := f.engine.Runners().Log(t.Context(), &protocol.LogRequest{
		AttemptId: int64(attempt), Position: position, Offset: offset, Data: []byte(data),
	})
	return resp.GetSize(), err
}

// checkStep fails the test unless step s is in state with outcome, nil for
// none, and exit, nil for none.
func checkStep(t *testing.T, s model.Step, state model.StepState, outcome *model.Outcome, exit *int) {
	t.Helper()
	if s.State != state {
		t.Errorf("step %d state = %s, want %s", s.Position, s.State, state)
	}
	if (s.Outcome == nil) != (outcome == nil) || (outcome != nil && *s.Outcome != *outcome) {
		t.Errorf("step %d outcome = %v, want %v", s.Position, s.Outcome, outcome)
	}
	if (s.ExitStatus == nil) != (exit == nil) || (exit != nil && *s.ExitStatus != *exit) {
		t.Errorf("step %d exit status = %v, want %v", s.Position, s.ExitStatus, exit)
	}
}

// Covers AE7.
func TestReportsOfAFailedStepFinishTheBuildWithFailure(t *testing.T) {
	f := newFixture(t)
	id, a := f.accepted(t)
	running, finished, skipped := protocol.StepState_STEP_STATE_RUNNING,
		protocol.StepState_STEP_STATE_FINISHED, protocol.StepState_STEP_STATE_SKIPPED
	success, failure := protocol.Outcome_OUTCOME_SUCCESS, protocol.Outcome_OUTCOME_FAILURE
	f.update(t, a, 1, running, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	f.update(t, a, 1, finished, success, new(int32(0)))
	f.update(t, a, 2, running, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	f.update(t, a, 2, finished, failure, new(int32(2)))
	f.update(t, a, 3, skipped, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	_, err := f.engine.Runners().Done(t.Context(), &protocol.DoneRequest{AttemptId: int64(a), Outcome: failure})
	if err != nil {
		t.Fatalf("Done() error = %v", err)
	}

	b := f.build(t, id)
	if b.State != model.BuildFinished || b.Outcome == nil || *b.Outcome != model.OutcomeFailure {
		t.Errorf("build = %s with outcome %v, want finished with failure", b.State, b.Outcome)
	}
	w := b.Workflows[0]
	if w.State != model.WorkflowFinished || w.Outcome == nil || *w.Outcome != model.OutcomeFailure {
		t.Errorf("workflow = %s with outcome %v, want finished with failure", w.State, w.Outcome)
	}
	attempt := w.Attempts[0]
	if attempt.Outcome == nil || *attempt.Outcome != model.OutcomeFailure || attempt.FinishedAt == nil {
		t.Errorf("attempt = outcome %v, finished at %v; want failure and a time", attempt.Outcome, attempt.FinishedAt)
	}
	if len(attempt.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(attempt.Steps))
	}
	checkStep(t, attempt.Steps[0], model.StepFinished, new(model.OutcomeSuccess), new(0))
	checkStep(t, attempt.Steps[1], model.StepFinished, new(model.OutcomeFailure), new(2))
	checkStep(t, attempt.Steps[2], model.StepSkipped, nil, nil)
}

func TestDoneWithSuccessFinishesTheBuildWithSuccess(t *testing.T) {
	f := newFixture(t)
	id, a := f.accepted(t)
	for position := int32(1); position <= 3; position++ {
		f.update(t, a, position, protocol.StepState_STEP_STATE_RUNNING, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
		f.update(t, a, position, protocol.StepState_STEP_STATE_FINISHED, protocol.Outcome_OUTCOME_SUCCESS, nil)
	}
	_, err := f.engine.Runners().Done(t.Context(), &protocol.DoneRequest{
		AttemptId: int64(a), Outcome: protocol.Outcome_OUTCOME_SUCCESS,
	})
	if err != nil {
		t.Fatalf("Done() error = %v", err)
	}
	b := f.build(t, id)
	if b.Outcome == nil || *b.Outcome != model.OutcomeSuccess || *b.Workflows[0].Outcome != model.OutcomeSuccess {
		t.Errorf("build outcome = %v, workflow outcome = %v; want success for both", b.Outcome, b.Workflows[0].Outcome)
	}
}

func TestDoneOfAnUnknownAttempt(t *testing.T) {
	f := newFixture(t)
	_, err := f.engine.Runners().Done(t.Context(), &protocol.DoneRequest{
		AttemptId: 42, Outcome: protocol.Outcome_OUTCOME_FAILURE,
	})
	if !errors.Is(err, model.ErrNotFound) {
		t.Errorf("Done() of an unknown attempt error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestReportsWithoutAnOutcomeOrStateAreInvalid(t *testing.T) {
	f := newFixture(t)
	_, a := f.accepted(t)
	r := f.engine.Runners()
	if _, err := r.Update(t.Context(), &protocol.UpdateRequest{AttemptId: int64(a), Position: 1}); !errors.Is(
		err, model.ErrInvalid) {
		t.Errorf("Update() with no state error = %v, want %v", err, model.ErrInvalid)
	}
	f.update(t, a, 1, protocol.StepState_STEP_STATE_RUNNING, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	_, err := r.Update(t.Context(), &protocol.UpdateRequest{
		AttemptId: int64(a), Position: 1, State: protocol.StepState_STEP_STATE_FINISHED,
	})
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Update(finished) with no outcome error = %v, want %v", err, model.ErrInvalid)
	}
	if _, err := r.Done(t.Context(), &protocol.DoneRequest{AttemptId: int64(a)}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Done() with no outcome error = %v, want %v", err, model.ErrInvalid)
	}
}

func TestUpdateOutsideTheStepsIsInvalid(t *testing.T) {
	f := newFixture(t)
	_, a := f.accepted(t)
	for _, position := range []int32{0, 4} {
		_, err := f.engine.Runners().Update(t.Context(), &protocol.UpdateRequest{
			AttemptId: int64(a), Position: position, State: protocol.StepState_STEP_STATE_RUNNING,
		})
		if !errors.Is(err, model.ErrInvalid) {
			t.Errorf("Update() at position %d error = %v, want %v", position, err, model.ErrInvalid)
		}
	}
}

func TestUpdateOfAnUnknownAttempt(t *testing.T) {
	f := newFixture(t)
	_, err := f.engine.Runners().Update(t.Context(), &protocol.UpdateRequest{
		AttemptId: 42, Position: 1, State: protocol.StepState_STEP_STATE_RUNNING,
	})
	if !errors.Is(err, model.ErrNotFound) {
		t.Errorf("Update() of an unknown attempt error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestUpdateToRunningCreatesAnEmptyLog(t *testing.T) {
	f := newFixture(t)
	_, a := f.accepted(t)
	// Step 2 is skipped: only a step that started has a log.
	f.update(t, a, 1, protocol.StepState_STEP_STATE_RUNNING, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	f.update(t, a, 1, protocol.StepState_STEP_STATE_FINISHED, protocol.Outcome_OUTCOME_SUCCESS, nil)
	f.update(t, a, 2, protocol.StepState_STEP_STATE_SKIPPED, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	if _, err := f.log(t, a, 1, 0, ""); err != nil {
		t.Errorf("Log() to a step that never wrote error = %v, want its empty log", err)
	}
	if size, err := f.logs.Size(a, 1); err != nil || size != 0 {
		t.Errorf("Size(step 1) = %d, %v; want an empty log", size, err)
	}
	if _, err := f.log(t, a, 2, 0, "x"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Log() to a skipped step error = %v, want %v", err, model.ErrInvalid)
	}
}

func TestLogAppendsByOffset(t *testing.T) {
	f := newFixture(t)
	_, a := f.accepted(t)
	f.update(t, a, 1, protocol.StepState_STEP_STATE_RUNNING, protocol.Outcome_OUTCOME_UNSPECIFIED, nil)
	chunks := []struct {
		offset int64
		data   string
		size   int64
	}{
		{0, "hello", 5},
		// Sent again: acknowledged and dropped.
		{0, "hello", 5},
		// Overlaps what the log holds: only the new bytes are written.
		{3, "lo, world\n", 13},
	}
	for _, c := range chunks {
		size, err := f.log(t, a, 1, c.offset, c.data)
		if err != nil || size != c.size {
			t.Errorf("Log(%d, %q) = %d, %v; want %d", c.offset, c.data, size, err, c.size)
		}
	}
	if _, err := f.log(t, a, 1, 20, "gap"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Log() past the log's end error = %v, want %v", err, model.ErrInvalid)
	}
	if _, err := f.log(t, a, 1, -1, "lost"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Log() at a negative offset error = %v, want %v", err, model.ErrInvalid)
	}
	got, err := f.logs.Read(a, 1, 0, 64)
	if err != nil || string(got) != "hello, world\n" {
		t.Errorf("Read() = %q, %v; want %q", got, err, "hello, world\n")
	}
}

func TestLogForAStepNeverStartedIsInvalid(t *testing.T) {
	f := newFixture(t)
	_, a := f.accepted(t)
	if _, err := f.log(t, a, 1, 0, "early"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Log() before the step started error = %v, want %v", err, model.ErrInvalid)
	}
}
