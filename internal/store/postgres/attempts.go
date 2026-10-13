package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/queries"
)

// StartAttempt accepts the workflow at version, the version its hand-out
// returned: it moves the workflow from assigned to running, bumps its
// version, and opens its next attempt, numbered from 1, with a pending step
// for each of steps at positions 1 to len(steps). It returns the attempt's
// id. A workflow that is not assigned or not at version gives
// model.ErrConflict, and an unknown one model.ErrNotFound.
func (s *Store) StartAttempt(
	ctx context.Context, workflow model.WorkflowID, version int64, steps []string,
) (model.AttemptID, error) {
	var id int64
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		n, err := s.q.StartWorkflow(ctx, tx, queries.StartWorkflowParams{ID: int64(workflow), Version: version})
		if err != nil {
			return fmt.Errorf("update the workflow: %w", err)
		}
		if n == 0 {
			exists, err := s.q.WorkflowExists(ctx, tx, int64(workflow))
			return refusal(exists, err, "workflow")
		}
		id, err = s.q.InsertAttempt(ctx, tx, queries.InsertAttemptParams{
			WorkflowID: int64(workflow), AcceptedVersion: version,
		})
		if err != nil {
			return fmt.Errorf("insert the attempt: %w", err)
		}
		for i, name := range steps {
			if err := s.q.InsertStep(ctx, tx, queries.InsertStepParams{
				AttemptID: id, Position: int32(i + 1), Name: name,
			}); err != nil {
				return fmt.Errorf("insert step %d: %w", i+1, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("start an attempt of workflow %d at version %d: %w", workflow, version, err)
	}
	return model.AttemptID(id), nil
}

// AttemptWorkflow returns the workflow and the build the attempt id
// belongs to, and its number. An unknown attempt gives model.ErrNotFound.
func (s *Store) AttemptWorkflow(
	ctx context.Context, id model.AttemptID,
) (model.WorkflowID, model.BuildID, int, error) {
	var row queries.AttemptWorkflowRow
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		row, err = s.q.AttemptWorkflow(ctx, txFrom(ctx), int64(id))
		return notFound(err)
	})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("look attempt %d up: %w", id, err)
	}
	return model.WorkflowID(row.WorkflowID), model.BuildID(row.BuildID), int(row.Number), nil
}

// WorkflowDefinition returns the definition of the workflow id, as
// CreateBuild stored it. An unknown workflow gives model.ErrNotFound.
func (s *Store) WorkflowDefinition(ctx context.Context, id model.WorkflowID) ([]byte, error) {
	var definition []byte
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		definition, err = s.q.WorkflowDefinition(ctx, txFrom(ctx), int64(id))
		return notFound(err)
	})
	if err != nil {
		return nil, fmt.Errorf("read workflow %d's definition: %w", id, err)
	}
	return definition, nil
}

// notFound returns model.ErrNotFound for a lookup that found no row, and
// otherwise err.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}

// FinishAttempt ends the open attempt id with outcome. Failure closes the
// attempt's leftover steps: a pending step is skipped, and a running one
// finishes with failure and no exit status. Success is refused while a
// step is neither finished with success nor skipped, and then changes
// nothing. An attempt that has ended, or a refused success, gives
// model.ErrConflict, and an unknown attempt model.ErrNotFound.
func (s *Store) FinishAttempt(ctx context.Context, id model.AttemptID, outcome model.Outcome) error {
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		if err := s.lockOpenAttempt(ctx, tx, int64(id)); err != nil {
			return err
		}
		leftovers := s.requireSucceededSteps
		if outcome == model.OutcomeFailure {
			leftovers = s.closeLeftoverSteps
		}
		if err := leftovers(ctx, tx, int64(id)); err != nil {
			return err
		}
		err := s.q.FinishAttempt(ctx, tx, queries.FinishAttemptParams{ID: int64(id), Outcome: string(outcome)})
		if err != nil {
			return fmt.Errorf("update the attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finish attempt %d with %s: %w", id, outcome, err)
	}
	return nil
}

// closeLeftoverSteps closes the steps of the attempt id that a failure
// leaves: a pending step is skipped, and a running one finishes with
// failure and no exit status.
func (s *Store) closeLeftoverSteps(ctx context.Context, tx pgx.Tx, id int64) error {
	if err := s.q.SkipPendingSteps(ctx, tx, id); err != nil {
		return fmt.Errorf("skip the pending steps: %w", err)
	}
	if err := s.q.FailRunningSteps(ctx, tx, id); err != nil {
		return fmt.Errorf("fail the running steps: %w", err)
	}
	return nil
}

// requireSucceededSteps gives model.ErrConflict while a step of the attempt
// id is neither finished with success nor skipped.
func (s *Store) requireSucceededSteps(ctx context.Context, tx pgx.Tx, id int64) error {
	blocked, err := s.q.StepsBlockSuccess(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("read the steps: %w", err)
	}
	if blocked {
		return model.ErrConflict
	}
	return nil
}

// lockOpenAttempt locks the attempt id on tx until tx ends, so its steps
// change only while it is open. An attempt that has ended gives
// model.ErrConflict, and an unknown one model.ErrNotFound.
func (s *Store) lockOpenAttempt(ctx context.Context, tx pgx.Tx, id int64) error {
	finishedAt, err := s.q.LockAttempt(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock the attempt: %w", err)
	}
	if finishedAt != nil {
		return model.ErrConflict
	}
	return nil
}
