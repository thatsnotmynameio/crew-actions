package postgres

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// createWorkflows creates n builds one after the other, each with one
// workflow, and returns the workflows' ids, oldest first.
func createWorkflows(t *testing.T, s *Store, n int) []model.WorkflowID {
	t.Helper()
	ids := make([]model.WorkflowID, n)
	for i := range ids {
		ids[i] = getBuild(t, s, createBuild(t, s, "test")).Workflows[0].ID
	}
	return ids
}

// candidateIDs returns the ids of candidates, in order.
func candidateIDs(candidates []model.Candidate) []model.WorkflowID {
	ids := make([]model.WorkflowID, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	return ids
}

func TestPendingWorkflowsOldestFirst(t *testing.T) {
	s := newStore(t)
	ids := createWorkflows(t, s, 3)
	got, err := s.PendingWorkflows(t.Context(), 2)
	if err != nil {
		t.Fatalf("PendingWorkflows(2) error = %v", err)
	}
	if want := ids[:2]; !reflect.DeepEqual(candidateIDs(got), want) {
		t.Fatalf("PendingWorkflows(2) = %v, want %v", candidateIDs(got), want)
	}
	b := getBuild(t, s, got[0].BuildID)
	w := b.Workflows[0]
	want := model.Candidate{ID: w.ID, Version: 1, Name: "test", BuildID: b.ID, CreatedAt: w.CreatedAt}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("PendingWorkflows(2)[0] = %+v, want %+v", got[0], want)
	}
}

func TestPendingWorkflowsSkipsLockedOnes(t *testing.T) {
	s := newStore(t)
	ids := createWorkflows(t, s, 3)
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		held, err := s.PendingWorkflows(ctx, 1)
		if err != nil {
			return err
		}
		if want := ids[:1]; !reflect.DeepEqual(candidateIDs(held), want) {
			t.Errorf("first transaction's PendingWorkflows(1) = %v, want %v", candidateIDs(held), want)
		}
		// t.Context() carries no transaction, so this InTx begins a second
		// one while the first holds its lock.
		//nolint:contextcheck // the second transaction needs a context without the first
		return s.InTx(t.Context(), func(ctx context.Context) error {
			next, err := s.PendingWorkflows(ctx, 1)
			if err != nil {
				return err
			}
			if want := ids[1:2]; !reflect.DeepEqual(candidateIDs(next), want) {
				t.Errorf("second transaction's PendingWorkflows(1) = %v, want %v", candidateIDs(next), want)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}
}

func TestAssignWorkflow(t *testing.T) {
	s := newStore(t)
	id := createBuild(t, s, "test")
	w := getBuild(t, s, id).Workflows[0]
	version, got, err := s.AssignWorkflow(t.Context(), w.ID, w.Version)
	if err != nil {
		t.Fatalf("AssignWorkflow() error = %v", err)
	}
	if version != w.Version+1 {
		t.Errorf("AssignWorkflow() version = %d, want %d", version, w.Version+1)
	}
	if !bytes.Equal(got, definition) {
		t.Errorf("AssignWorkflow() definition = %x, want %x", got, definition)
	}
	after := getBuild(t, s, id).Workflows[0]
	if after.State != model.WorkflowAssigned || after.Version != version || after.AssignedAt == nil {
		t.Errorf("workflow = %s at version %d, assigned at %v; want assigned at version %d, a time",
			after.State, after.Version, after.AssignedAt, version)
	}
	pending, err := s.PendingWorkflows(t.Context(), 1)
	if err != nil {
		t.Fatalf("PendingWorkflows(1) error = %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("PendingWorkflows(1) = %v, want none once assigned", candidateIDs(pending))
	}
}

func TestAssignWorkflowConflicts(t *testing.T) {
	tests := map[string]struct {
		// assign assigns the workflow first when true.
		assign bool
		// version is the version AssignWorkflow is called with, relative to
		// the workflow's.
		version int64
	}{
		"a stale version":  {assign: false, version: -1},
		"a newer version":  {assign: false, version: 1},
		"already assigned": {assign: true, version: 0},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id := createBuild(t, s, "test")
			w := getBuild(t, s, id).Workflows[0]
			if tt.assign {
				if _, _, err := s.AssignWorkflow(t.Context(), w.ID, w.Version); err != nil {
					t.Fatalf("AssignWorkflow() error = %v", err)
				}
			}
			before := getBuild(t, s, id)
			current := before.Workflows[0].Version
			_, _, err := s.AssignWorkflow(t.Context(), w.ID, current+tt.version)
			if !errors.Is(err, model.ErrConflict) {
				t.Errorf("AssignWorkflow() error = %v, want %v", err, model.ErrConflict)
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflict = %+v, want %+v", after, before)
			}
		})
	}
}

func TestAssignWorkflowUnknown(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.AssignWorkflow(t.Context(), 7, 1); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("AssignWorkflow(7) error = %v, want %v", err, model.ErrNotFound)
	}
}
