package postgres

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/queries"
)

// The step primitives move the step at a position of an open attempt. An
// attempt that has ended, or a step in another state, gives
// model.ErrConflict, and an unknown attempt or position model.ErrNotFound.

// StartStep moves the step at position of the attempt from pending to
// running, and records when.
func (s *Store) StartStep(ctx context.Context, attempt model.AttemptID, position int) error {
	err := s.moveStep(ctx, attempt, position, func(ctx context.Context, tx pgx.Tx, position int32) (int64, error) {
		return s.q.StartStep(ctx, tx, queries.StartStepParams{AttemptID: int64(attempt), Position: position})
	})
	if err != nil {
		return fmt.Errorf("start step %d of attempt %d: %w", position, attempt, err)
	}
	return nil
}

// FinishStep moves the step at position of the attempt from running to
// finished with outcome, and records when and exitStatus, which is nil
// when the step reported none.
func (s *Store) FinishStep(
	ctx context.Context, attempt model.AttemptID, position int, outcome model.Outcome, exitStatus *int,
) error {
	err := s.moveStep(ctx, attempt, position, func(ctx context.Context, tx pgx.Tx, position int32) (int64, error) {
		var exit *int32
		if exitStatus != nil {
			status, err := toInt32(*exitStatus, "exit status")
			if err != nil {
				return 0, err
			}
			exit = &status
		}
		return s.q.FinishStep(ctx, tx, queries.FinishStepParams{
			AttemptID: int64(attempt), Position: position, Outcome: string(outcome), ExitStatus: exit,
		})
	})
	if err != nil {
		return fmt.Errorf("finish step %d of attempt %d with %s: %w", position, attempt, outcome, err)
	}
	return nil
}

// SkipStep moves the step at position of the attempt from pending to
// skipped, and records when.
func (s *Store) SkipStep(ctx context.Context, attempt model.AttemptID, position int) error {
	err := s.moveStep(ctx, attempt, position, func(ctx context.Context, tx pgx.Tx, position int32) (int64, error) {
		return s.q.SkipStep(ctx, tx, queries.SkipStepParams{AttemptID: int64(attempt), Position: position})
	})
	if err != nil {
		return fmt.Errorf("skip step %d of attempt %d: %w", position, attempt, err)
	}
	return nil
}

// moveStep runs update, which moves the step at position of the attempt
// and returns how many rows it changed, while it holds the attempt's lock.
// A position no step can have gives model.ErrNotFound.
func (s *Store) moveStep(
	ctx context.Context, attempt model.AttemptID, position int,
	update func(ctx context.Context, tx pgx.Tx, position int32) (int64, error),
) error {
	p, err := toInt32(position, "position")
	if err != nil {
		return model.ErrNotFound
	}
	return s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		if err := s.lockOpenAttempt(ctx, tx, int64(attempt)); err != nil {
			return err
		}
		n, err := update(ctx, tx, p)
		if err != nil {
			return fmt.Errorf("update the step: %w", err)
		}
		if n > 0 {
			return nil
		}
		exists, err := s.q.StepExists(ctx, tx, queries.StepExistsParams{AttemptID: int64(attempt), Position: p})
		return refusal(exists, err, "step")
	})
}

// toInt32 returns n as the int32 of an integer column, and an error naming
// what when it does not fit, rather than storing another number.
func toInt32(n int, what string) (int32, error) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("%s %d does not fit an integer column", what, n)
	}
	return int32(n), nil
}
