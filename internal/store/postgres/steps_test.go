package postgres

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// checkStep checks that step is in state with outcome and exitStatus, that
// it has a start time exactly when started, and a finish time exactly when
// it has ended.
func checkStep(
	t *testing.T, step model.Step, state model.StepState, outcome *model.Outcome, exitStatus *int, started bool,
) {
	t.Helper()
	got := model.Step{State: step.State, Outcome: step.Outcome, ExitStatus: step.ExitStatus}
	want := model.Step{State: state, Outcome: outcome, ExitStatus: exitStatus}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("step %d = %s, outcome %v, exit status %v; want %s, outcome %v, exit status %v",
			step.Position, step.State, step.Outcome, step.ExitStatus, state, outcome, exitStatus)
	}
	ended := state == model.StepFinished || state == model.StepSkipped
	if (step.StartedAt != nil) != started || (step.FinishedAt != nil) != ended {
		t.Errorf("step %d started at %v and finished at %v, want a start time %t and a finish time %t",
			step.Position, step.StartedAt, step.FinishedAt, started, ended)
	}
}

func TestStepsMoveThroughTheirStates(t *testing.T) {
	s := newStore(t)
	id, _, a := started(t, s, "build", "test", "deploy")
	if err := s.StartStep(t.Context(), a, 1); err != nil {
		t.Fatalf("StartStep(1) error = %v", err)
	}
	checkStep(t, lastAttempt(t, s, id).Steps[0], model.StepRunning, nil, nil, true)
	if err := s.FinishStep(t.Context(), a, 1, model.OutcomeSuccess, nil); err != nil {
		t.Fatalf("FinishStep(1) error = %v", err)
	}
	if err := s.StartStep(t.Context(), a, 2); err != nil {
		t.Fatalf("StartStep(2) error = %v", err)
	}
	if err := s.FinishStep(t.Context(), a, 2, model.OutcomeFailure, new(3)); err != nil {
		t.Fatalf("FinishStep(2) error = %v", err)
	}
	if err := s.SkipStep(t.Context(), a, 3); err != nil {
		t.Fatalf("SkipStep(3) error = %v", err)
	}
	steps := lastAttempt(t, s, id).Steps
	checkStep(t, steps[0], model.StepFinished, new(model.OutcomeSuccess), nil, true)
	checkStep(t, steps[1], model.StepFinished, new(model.OutcomeFailure), new(3), true)
	checkStep(t, steps[2], model.StepSkipped, nil, nil, false)
}

// stepCalls are the step primitives, each on the step at position of a.
var stepCalls = map[string]func(t *testing.T, s *Store, a model.AttemptID, position int) error{
	"StartStep": func(t *testing.T, s *Store, a model.AttemptID, position int) error {
		t.Helper()
		return s.StartStep(t.Context(), a, position)
	},
	"FinishStep": func(t *testing.T, s *Store, a model.AttemptID, position int) error {
		t.Helper()
		return s.FinishStep(t.Context(), a, position, model.OutcomeSuccess, new(0))
	},
	"SkipStep": func(t *testing.T, s *Store, a model.AttemptID, position int) error {
		t.Helper()
		return s.SkipStep(t.Context(), a, position)
	},
}

// failedAttempt starts an attempt with two steps, starts the first and
// finishes the attempt with failure, and returns the build's and the
// attempt's ids.
func failedAttempt(t *testing.T, s *Store) (model.BuildID, model.AttemptID) {
	t.Helper()
	id, _, a := started(t, s, "build", "test")
	if err := s.StartStep(t.Context(), a, 1); err != nil {
		t.Fatalf("StartStep(1) error = %v", err)
	}
	if err := s.FinishAttempt(t.Context(), a, model.OutcomeFailure); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	return id, a
}

func TestStepCallsOnAFinishedAttemptConflict(t *testing.T) {
	for name, call := range stepCalls {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id, a := failedAttempt(t, s)
			before := getBuild(t, s, id)
			for position := 1; position <= 2; position++ {
				if err := call(t, s, a, position); !errors.Is(err, model.ErrConflict) {
					t.Errorf("%s(%d) error = %v, want %v", name, position, err, model.ErrConflict)
				}
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflicts = %+v, want %+v", after, before)
			}
		})
	}
}

func TestStepTransitionsConflict(t *testing.T) {
	tests := map[string]struct {
		// start starts the step first when true.
		start bool
		call  string
	}{
		"StartStep on a running step":  {start: true, call: "StartStep"},
		"SkipStep on a running step":   {start: true, call: "SkipStep"},
		"FinishStep on a pending step": {start: false, call: "FinishStep"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id, _, a := started(t, s, "test")
			if tt.start {
				if err := s.StartStep(t.Context(), a, 1); err != nil {
					t.Fatalf("StartStep(1) error = %v", err)
				}
			}
			before := getBuild(t, s, id)
			if err := stepCalls[tt.call](t, s, a, 1); !errors.Is(err, model.ErrConflict) {
				t.Errorf("%s(1) error = %v, want %v", tt.call, err, model.ErrConflict)
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflict = %+v, want %+v", after, before)
			}
		})
	}
}

func TestStepCallsUnknown(t *testing.T) {
	for name, call := range stepCalls {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			_, _, a := started(t, s, "test")
			for _, position := range []int{2, math.MaxInt32 + 1} {
				if err := call(t, s, a, position); !errors.Is(err, model.ErrNotFound) {
					t.Errorf("%s(%d) error = %v, want %v", name, position, err, model.ErrNotFound)
				}
			}
			if err := call(t, s, a+1, 1); !errors.Is(err, model.ErrNotFound) {
				t.Errorf("%s on attempt %d error = %v, want %v", name, a+1, err, model.ErrNotFound)
			}
		})
	}
}

func TestFinishStepRefusesAnExitStatusTheColumnCannotHold(t *testing.T) {
	s := newStore(t)
	id, _, a := started(t, s, "test")
	if err := s.StartStep(t.Context(), a, 1); err != nil {
		t.Fatalf("StartStep(1) error = %v", err)
	}
	before := getBuild(t, s, id)
	if err := s.FinishStep(t.Context(), a, 1, model.OutcomeFailure, new(math.MaxInt32+1)); err == nil {
		t.Error("FinishStep() error = nil, want an error")
	}
	if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
		t.Errorf("build after the refusal = %+v, want %+v", after, before)
	}
}
