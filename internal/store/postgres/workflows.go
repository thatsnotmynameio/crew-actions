package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/queries"
)

// PendingWorkflows returns up to n pending workflows, oldest first, without
// changing them. It locks them and skips those another transaction holds,
// so it is meant to run inside InTx, where the locks last until the
// transaction ends.
func (s *Store) PendingWorkflows(ctx context.Context, n int) ([]model.Candidate, error) {
	var candidates []model.Candidate
	err := s.InTx(ctx, func(ctx context.Context) error {
		rows, err := s.q.PendingWorkflows(ctx, txFrom(ctx), int64(n))
		if err != nil {
			return fmt.Errorf("read the pending workflows: %w", err)
		}
		candidates = make([]model.Candidate, len(rows))
		for i, row := range rows {
			candidates[i] = model.Candidate{
				ID:        model.WorkflowID(row.ID),
				Version:   row.Version,
				Name:      row.Name,
				BuildID:   model.BuildID(row.BuildID),
				CreatedAt: row.CreatedAt,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// AssignWorkflow hands out the workflow id: it moves it from pending to
// assigned, when it is at version, and records when. It returns the
// workflow's new version and its definition. A workflow that is not pending
// or not at version gives model.ErrConflict, and an unknown one
// model.ErrNotFound.
func (s *Store) AssignWorkflow(ctx context.Context, id model.WorkflowID, version int64) (int64, []byte, error) {
	var row queries.AssignWorkflowRow
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		var err error
		row, err = s.q.AssignWorkflow(ctx, tx, queries.AssignWorkflowParams{ID: int64(id), Version: version})
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("update the workflow: %w", err)
		}
		exists, err := s.q.WorkflowExists(ctx, tx, int64(id))
		return refusal(exists, err, "workflow")
	})
	if err != nil {
		return 0, nil, fmt.Errorf("assign workflow %d at version %d: %w", id, version, err)
	}
	return row.Version, row.Definition, nil
}

// FinishWorkflow moves the workflow id from running to finished with
// outcome, bumps its version and records when. It refuses while the
// workflow has an open attempt, so the engine finishes the attempt first,
// in the same transaction. A workflow that is not running, or has an open
// attempt, gives model.ErrConflict, and an unknown one model.ErrNotFound.
func (s *Store) FinishWorkflow(ctx context.Context, id model.WorkflowID, outcome model.Outcome) error {
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		n, err := s.q.FinishWorkflow(ctx, tx, queries.FinishWorkflowParams{ID: int64(id), Outcome: string(outcome)})
		if err != nil {
			return fmt.Errorf("update the workflow: %w", err)
		}
		if n > 0 {
			return nil
		}
		exists, err := s.q.WorkflowExists(ctx, tx, int64(id))
		return refusal(exists, err, "workflow")
	})
	if err != nil {
		return fmt.Errorf("finish workflow %d with %s: %w", id, outcome, err)
	}
	return nil
}

// refusal returns the error of a transition that changed no row, given
// whether the row exists and the error of looking it up: model.ErrConflict
// for a row in another state, and model.ErrNotFound for none.
func refusal(exists bool, err error, what string) error {
	if err != nil {
		return fmt.Errorf("look the %s up: %w", what, err)
	}
	if exists {
		return model.ErrConflict
	}
	return model.ErrNotFound
}
