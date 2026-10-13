package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/queries"
)

// CreateBuild creates a running build of repo with workflows, each pending
// at version 1 and under the build's identity, and returns the build's id.
// It refuses a build without workflows and two workflows of one name, and
// then writes nothing.
func (s *Store) CreateBuild(
	ctx context.Context, repo model.Repository, workflows []model.NewWorkflow,
) (model.BuildID, error) {
	if len(workflows) == 0 {
		return 0, errors.New("create a build: it has no workflow")
	}
	names := make(map[string]bool, len(workflows))
	for _, w := range workflows {
		if names[w.Name] {
			return 0, fmt.Errorf("create a build: two workflows are named %q", w.Name)
		}
		names[w.Name] = true
	}
	var id int64
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx := txFrom(ctx)
		var err error
		id, err = s.q.InsertBuild(ctx, tx, queries.InsertBuildParams{
			Host: repo.Host, Organization: repo.Organization, Repository: repo.Name,
		})
		if err != nil {
			return fmt.Errorf("create a build: %w", err)
		}
		for _, w := range workflows {
			if err := s.q.InsertWorkflow(ctx, tx, queries.InsertWorkflowParams{
				BuildID: id, Host: repo.Host, Organization: repo.Organization, Repository: repo.Name,
				Name: w.Name, Definition: w.Definition,
			}); err != nil {
				return fmt.Errorf("create build %d's workflow %q: %w", id, w.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return model.BuildID(id), nil
}

// GetBuild returns the snapshot of the build id, with all its workflows,
// every attempt and every attempt's steps, ordered by id, number and
// position. An unknown id gives model.ErrNotFound.
func (s *Store) GetBuild(ctx context.Context, id model.BuildID) (model.Build, error) {
	var b model.Build
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		b, err = s.getBuild(ctx, txFrom(ctx), int64(id))
		return err
	})
	if err != nil {
		return model.Build{}, fmt.Errorf("get build %d: %w", id, err)
	}
	return b, nil
}

// getBuild reads the build id on tx. It reads the children before their
// parents: no row is deleted, so every row read has its parent in a later
// read, even when another transaction writes in between.
func (s *Store) getBuild(ctx context.Context, tx pgx.Tx, id int64) (model.Build, error) {
	row, err := s.q.GetBuild(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Build{}, model.ErrNotFound
	}
	if err != nil {
		return model.Build{}, fmt.Errorf("read the build: %w", err)
	}
	steps, err := s.steps(ctx, tx, id)
	if err != nil {
		return model.Build{}, err
	}
	attempts, err := s.attempts(ctx, tx, id, steps)
	if err != nil {
		return model.Build{}, err
	}
	workflows, err := s.workflows(ctx, tx, id, attempts)
	if err != nil {
		return model.Build{}, err
	}
	state, stateErr := model.ParseBuildState(row.State)
	outcome, outcomeErr := parseNullable(model.ParseOutcome, row.Outcome)
	if err := errors.Join(stateErr, outcomeErr); err != nil {
		return model.Build{}, fmt.Errorf("read the build: %w", err)
	}
	return model.Build{
		ID:         model.BuildID(row.ID),
		Repository: model.Repository{Host: row.Host, Organization: row.Organization, Name: row.Repository},
		State:      state,
		Outcome:    outcome,
		CreatedAt:  row.CreatedAt,
		FinishedAt: row.FinishedAt,
		Workflows:  workflows,
	}, nil
}

// workflows reads the build's workflows, ordered by id, each with its
// attempts from attempts, which are keyed by workflow id.
func (s *Store) workflows(
	ctx context.Context, tx pgx.Tx, build int64, attempts map[int64][]model.Attempt,
) ([]model.Workflow, error) {
	rows, err := s.q.ListWorkflows(ctx, tx, build)
	if err != nil {
		return nil, fmt.Errorf("read the workflows: %w", err)
	}
	workflows := make([]model.Workflow, len(rows))
	for i, row := range rows {
		state, stateErr := model.ParseWorkflowState(row.State)
		outcome, outcomeErr := parseNullable(model.ParseOutcome, row.Outcome)
		if err := errors.Join(stateErr, outcomeErr); err != nil {
			return nil, fmt.Errorf("read workflow %d: %w", row.ID, err)
		}
		workflows[i] = model.Workflow{
			ID:         model.WorkflowID(row.ID),
			Name:       row.Name,
			State:      state,
			Outcome:    outcome,
			Version:    row.Version,
			Definition: row.Definition,
			CreatedAt:  row.CreatedAt,
			AssignedAt: row.AssignedAt,
			FinishedAt: row.FinishedAt,
			Attempts:   attempts[row.ID],
		}
	}
	return workflows, nil
}

// attempts reads the build's attempts, keyed by workflow id and ordered by
// number, each with its steps from steps, which are keyed by attempt id.
func (s *Store) attempts(
	ctx context.Context, tx pgx.Tx, build int64, steps map[int64][]model.Step,
) (map[int64][]model.Attempt, error) {
	rows, err := s.q.ListAttempts(ctx, tx, build)
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	attempts := make(map[int64][]model.Attempt)
	for _, row := range rows {
		outcome, err := parseNullable(model.ParseOutcome, row.Outcome)
		if err != nil {
			return nil, fmt.Errorf("read attempt %d: %w", row.ID, err)
		}
		attempts[row.WorkflowID] = append(attempts[row.WorkflowID], model.Attempt{
			ID:              model.AttemptID(row.ID),
			Number:          int(row.Number),
			AcceptedVersion: row.AcceptedVersion,
			Outcome:         outcome,
			StartedAt:       row.StartedAt,
			FinishedAt:      row.FinishedAt,
			Steps:           steps[row.ID],
		})
	}
	return attempts, nil
}

// steps reads the build's steps, keyed by attempt id and ordered by
// position.
func (s *Store) steps(ctx context.Context, tx pgx.Tx, build int64) (map[int64][]model.Step, error) {
	rows, err := s.q.ListSteps(ctx, tx, build)
	if err != nil {
		return nil, fmt.Errorf("read the steps: %w", err)
	}
	steps := make(map[int64][]model.Step)
	for _, row := range rows {
		state, stateErr := model.ParseStepState(row.State)
		outcome, outcomeErr := parseNullable(model.ParseOutcome, row.Outcome)
		if err := errors.Join(stateErr, outcomeErr); err != nil {
			return nil, fmt.Errorf("read step %d: %w", row.ID, err)
		}
		var exit *int
		if row.ExitStatus != nil {
			status := int(*row.ExitStatus)
			exit = &status
		}
		steps[row.AttemptID] = append(steps[row.AttemptID], model.Step{
			ID:         model.StepID(row.ID),
			Position:   int(row.Position),
			Name:       row.Name,
			State:      state,
			Outcome:    outcome,
			ExitStatus: exit,
			StartedAt:  row.StartedAt,
			FinishedAt: row.FinishedAt,
		})
	}
	return steps, nil
}

// parseNullable returns nil for a NULL column, and otherwise what parse
// returns for its value.
func parseNullable[T any](parse func(string) (T, error), s *string) (*T, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // NULL is no value, and no error
	}
	v, err := parse(*s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
