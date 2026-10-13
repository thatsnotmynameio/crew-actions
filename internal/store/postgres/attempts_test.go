package postgres

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// assigned creates a build with one workflow and hands it out, and returns
// the build's id, the workflow's id and the version the hand-out returned.
func assigned(t *testing.T, s *Store) (model.BuildID, model.WorkflowID, int64) {
	t.Helper()
	id := createBuild(t, s, "test")
	w := getBuild(t, s, id).Workflows[0]
	version, _, err := s.AssignWorkflow(t.Context(), w.ID, w.Version)
	if err != nil {
		t.Fatalf("AssignWorkflow() error = %v", err)
	}
	return id, w.ID, version
}

// started creates a build with one workflow, hands it out and starts an
// attempt of it with steps, and returns the build's, the workflow's and
// the attempt's ids.
func started(t *testing.T, s *Store, steps ...string) (model.BuildID, model.WorkflowID, model.AttemptID) {
	t.Helper()
	id, w, version := assigned(t, s)
	a, err := s.StartAttempt(t.Context(), w, version, steps)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	return id, w, a
}

// lastAttempt returns the last attempt of the build id's first workflow.
func lastAttempt(t *testing.T, s *Store, id model.BuildID) model.Attempt {
	t.Helper()
	attempts := getBuild(t, s, id).Workflows[0].Attempts
	if len(attempts) == 0 {
		t.Fatal("the workflow has no attempt")
	}
	return attempts[len(attempts)-1]
}

func TestStartAttempt(t *testing.T) {
	s := newStore(t)
	id, w, version := assigned(t, s)
	a, err := s.StartAttempt(t.Context(), w, version, []string{"checkout", "build", "test"})
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	got := getBuild(t, s, id).Workflows[0]
	if got.State != model.WorkflowRunning || got.Version != version+1 {
		t.Errorf("workflow = %s at version %d, want running at version %d", got.State, got.Version, version+1)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(got.Attempts))
	}
	attempt := got.Attempts[0]
	if attempt.StartedAt.IsZero() {
		t.Error("attempt's start time is zero, want the database's")
	}
	// The start time is the database's, so want takes it from got.
	want := model.Attempt{
		ID: a, Number: 1, AcceptedVersion: version, StartedAt: attempt.StartedAt,
		Steps: []model.Step{
			{ID: attempt.Steps[0].ID, Position: 1, Name: "checkout", State: model.StepPending},
			{ID: attempt.Steps[1].ID, Position: 2, Name: "build", State: model.StepPending},
			{ID: attempt.Steps[2].ID, Position: 3, Name: "test", State: model.StepPending},
		},
	}
	if !reflect.DeepEqual(attempt, want) {
		t.Errorf("attempt = %+v, want %+v", attempt, want)
	}
}

func TestStartAttemptNumbersAfterTheLast(t *testing.T) {
	s := newStore(t)
	id, w, a := started(t, s, "test")
	if err := s.FinishAttempt(t.Context(), a, model.OutcomeFailure); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	// Part 5 hands a lost workflow out again; this stands in for it.
	var version int64
	if err := s.pool.QueryRow(t.Context(),
		"UPDATE workflows SET state = 'assigned', version = version + 1 WHERE id = $1 RETURNING version",
		w).Scan(&version); err != nil {
		t.Fatalf("assign the workflow again: %v", err)
	}
	if _, err := s.StartAttempt(t.Context(), w, version, []string{"test"}); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if got := lastAttempt(t, s, id); got.Number != 2 || got.AcceptedVersion != version {
		t.Errorf("attempt = number %d at version %d, want number 2 at version %d",
			got.Number, got.AcceptedVersion, version)
	}
}

func TestStartAttemptConflicts(t *testing.T) {
	tests := map[string]struct {
		// prepare creates the build whose workflow StartAttempt is called on.
		prepare func(t *testing.T, s *Store) model.BuildID
		// version is the version StartAttempt is called with, relative to
		// the workflow's.
		version int64
	}{
		"a stale version": {
			prepare: func(t *testing.T, s *Store) model.BuildID {
				t.Helper()
				id, _, _ := assigned(t, s)
				return id
			},
			version: -1,
		},
		"a pending workflow": {
			prepare: func(t *testing.T, s *Store) model.BuildID {
				t.Helper()
				return createBuild(t, s, "test")
			},
			version: 0,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id := tt.prepare(t, s)
			before := getBuild(t, s, id)
			w := before.Workflows[0]
			_, err := s.StartAttempt(t.Context(), w.ID, w.Version+tt.version, []string{"test"})
			if !errors.Is(err, model.ErrConflict) {
				t.Errorf("StartAttempt() error = %v, want %v", err, model.ErrConflict)
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflict = %+v, want %+v", after, before)
			}
		})
	}
}

func TestStartAttemptUnknown(t *testing.T) {
	s := newStore(t)
	if _, err := s.StartAttempt(t.Context(), 7, 2, nil); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("StartAttempt(7) error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestFinishAttemptWithFailureClosesLeftoverSteps(t *testing.T) {
	s := newStore(t)
	id, _, a := started(t, s, "build", "test")
	if err := s.StartStep(t.Context(), a, 1); err != nil {
		t.Fatalf("StartStep(1) error = %v", err)
	}
	if err := s.FinishAttempt(t.Context(), a, model.OutcomeFailure); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	got := lastAttempt(t, s, id)
	if got.Outcome == nil || *got.Outcome != model.OutcomeFailure || got.FinishedAt == nil {
		t.Errorf("attempt = outcome %v, finished at %v; want failure and a time", got.Outcome, got.FinishedAt)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(got.Steps))
	}
	checkStep(t, got.Steps[0], model.StepFinished, new(model.OutcomeFailure), nil, true)
	checkStep(t, got.Steps[1], model.StepSkipped, nil, nil, false)
}

func TestFinishAttemptWithSuccessConflicts(t *testing.T) {
	tests := map[string]func(t *testing.T, s *Store, a model.AttemptID){
		"a pending step": func(*testing.T, *Store, model.AttemptID) {},
		"a running step": func(t *testing.T, s *Store, a model.AttemptID) {
			t.Helper()
			if err := s.StartStep(t.Context(), a, 1); err != nil {
				t.Fatalf("StartStep(1) error = %v", err)
			}
		},
		"a failed step": func(t *testing.T, s *Store, a model.AttemptID) {
			t.Helper()
			if err := s.StartStep(t.Context(), a, 1); err != nil {
				t.Fatalf("StartStep(1) error = %v", err)
			}
			if err := s.FinishStep(t.Context(), a, 1, model.OutcomeFailure, new(1)); err != nil {
				t.Fatalf("FinishStep(1) error = %v", err)
			}
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id, _, a := started(t, s, "test")
			prepare(t, s, a)
			before := getBuild(t, s, id)
			if err := s.FinishAttempt(t.Context(), a, model.OutcomeSuccess); !errors.Is(err, model.ErrConflict) {
				t.Errorf("FinishAttempt() error = %v, want %v", err, model.ErrConflict)
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflict = %+v, want %+v", after, before)
			}
		})
	}
}

func TestFinishAttemptTwiceConflicts(t *testing.T) {
	s := newStore(t)
	_, _, a := started(t, s, "test")
	if err := s.FinishAttempt(t.Context(), a, model.OutcomeFailure); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	if err := s.FinishAttempt(t.Context(), a, model.OutcomeFailure); !errors.Is(err, model.ErrConflict) {
		t.Errorf("second FinishAttempt() error = %v, want %v", err, model.ErrConflict)
	}
}

func TestFinishAttemptUnknown(t *testing.T) {
	s := newStore(t)
	if err := s.FinishAttempt(t.Context(), 7, model.OutcomeFailure); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("FinishAttempt(7) error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestAttemptWorkflow(t *testing.T) {
	s := newStore(t)
	id, w, a := started(t, s, "test")
	gotWorkflow, gotBuild, gotNumber, err := s.AttemptWorkflow(t.Context(), a)
	if err != nil {
		t.Fatalf("AttemptWorkflow() error = %v", err)
	}
	if gotWorkflow != w || gotBuild != id || gotNumber != 1 {
		t.Errorf("AttemptWorkflow() = workflow %d, build %d, number %d; want %d, %d and 1",
			gotWorkflow, gotBuild, gotNumber, w, id)
	}
}

func TestAttemptWorkflowUnknown(t *testing.T) {
	s := newStore(t)
	if _, _, _, err := s.AttemptWorkflow(t.Context(), 7); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("AttemptWorkflow(7) error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestWorkflowDefinition(t *testing.T) {
	s := newStore(t)
	w := getBuild(t, s, createBuild(t, s, "test")).Workflows[0]
	got, err := s.WorkflowDefinition(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if !bytes.Equal(got, definition) {
		t.Errorf("WorkflowDefinition() = %q, want %q", got, definition)
	}
}

func TestWorkflowDefinitionUnknown(t *testing.T) {
	s := newStore(t)
	if _, err := s.WorkflowDefinition(t.Context(), 7); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("WorkflowDefinition(7) error = %v, want %v", err, model.ErrNotFound)
	}
}
