package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// repository is repo's identity as the store takes it.
var repository = model.Repository{Host: repo.host, Organization: repo.organization, Name: repo.repository}

// definition is a workflow's definition, with bytes that are not text.
var definition = []byte{'{', 0x00, 0xff, 0xfe, '}'}

// createBuild creates a build of repository with one workflow per name,
// each with definition, and returns its id.
func createBuild(t *testing.T, s *Store, names ...string) model.BuildID {
	t.Helper()
	workflows := make([]model.NewWorkflow, len(names))
	for i, name := range names {
		workflows[i] = model.NewWorkflow{Name: name, Definition: definition}
	}
	id, err := s.CreateBuild(t.Context(), repository, workflows)
	if err != nil {
		t.Fatalf("CreateBuild() error = %v", err)
	}
	return id
}

// getBuild returns the snapshot of the build id.
func getBuild(t *testing.T, s *Store, id model.BuildID) model.Build {
	t.Helper()
	b, err := s.GetBuild(t.Context(), id)
	if err != nil {
		t.Fatalf("GetBuild(%d) error = %v", id, err)
	}
	return b
}

// countRows returns how many rows table holds.
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count the rows of %s: %v", table, err)
	}
	return n
}

func TestCreateBuildReadsBack(t *testing.T) {
	s := newStore(t)
	parsed, err := model.ParseRepository("https://user:token@gitlab.com/group/sub/repo.git")
	if err != nil {
		t.Fatalf("ParseRepository() error = %v", err)
	}
	id, err := s.CreateBuild(t.Context(), parsed, []model.NewWorkflow{{Name: "test", Definition: definition}})
	if err != nil {
		t.Fatalf("CreateBuild() error = %v", err)
	}
	got := getBuild(t, s, id)
	if len(got.Workflows) != 1 {
		t.Fatalf("workflows = %d, want 1", len(got.Workflows))
	}
	w := got.Workflows[0]
	if got.CreatedAt.IsZero() || w.CreatedAt.IsZero() {
		t.Errorf("created at %v and %v, want the build's and the workflow's times", got.CreatedAt, w.CreatedAt)
	}
	// The times are the database's, so want takes them from got.
	want := model.Build{
		ID: id, Repository: parsed, State: model.BuildRunning, CreatedAt: got.CreatedAt,
		Workflows: []model.Workflow{{
			ID: w.ID, Name: "test", State: model.WorkflowPending, Version: 1,
			Definition: definition, CreatedAt: w.CreatedAt,
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBuild() = %+v, want %+v", got, want)
	}
	var row model.Repository
	if err := s.pool.QueryRow(t.Context(),
		"SELECT host, organization, repository FROM workflows WHERE id = $1", w.ID).
		Scan(&row.Host, &row.Organization, &row.Name); err != nil {
		t.Fatalf("read the workflow's identity: %v", err)
	}
	if row != parsed {
		t.Errorf("workflow row's identity = %v, want %v", row, parsed)
	}
}

func TestCreateBuildRefuses(t *testing.T) {
	tests := map[string][]model.NewWorkflow{
		"no workflow": nil,
		"a repeated name": {
			{Name: "test", Definition: definition},
			{Name: "lint", Definition: definition},
			{Name: "test", Definition: definition},
		},
	}
	for name, workflows := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.CreateBuild(t.Context(), repository, workflows); err == nil {
				t.Fatal("CreateBuild() error = nil, want an error")
			}
			if b, w := countRows(t, s, "builds"), countRows(t, s, "workflows"); b != 0 || w != 0 {
				t.Errorf("rows = %d builds and %d workflows, want none", b, w)
			}
		})
	}
}

func TestGetBuildUnknown(t *testing.T) {
	s := newStore(t)
	if _, err := s.GetBuild(t.Context(), 7); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("GetBuild(7) error = %v, want %v", err, model.ErrNotFound)
	}
}

// addAttempts gives workflow a finished attempt 2 and an open attempt 1,
// with a finished step at position 0 and a pending one at position 1,
// and returns them as GetBuild should, without their start times. Attempt
// 2 and position 1 are inserted first, so only the reads' order puts them
// after attempt 1 and position 0.
func addAttempts(t *testing.T, s *Store, workflow int64) []model.Attempt {
	t.Helper()
	second, err := insertAttempt(t, s, workflow, 2, repo)
	if err != nil {
		t.Fatal(err)
	}
	var secondFinishedAt time.Time
	if err := s.pool.QueryRow(t.Context(),
		"UPDATE attempts SET outcome = 'failure', finished_at = now() WHERE id = $1 RETURNING finished_at",
		second).Scan(&secondFinishedAt); err != nil {
		t.Fatalf("finish attempt 2: %v", err)
	}
	first, err := insertAttempt(t, s, workflow, 1, repo)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := insertStep(t, s, first, 1, repo)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := insertStep(t, s, first, 0, repo)
	if err != nil {
		t.Fatal(err)
	}
	var startedAt, finishedAt time.Time
	if err := s.pool.QueryRow(t.Context(),
		`UPDATE steps SET state = 'finished', outcome = 'success', exit_status = 0,
		 started_at = now(), finished_at = now() WHERE id = $1 RETURNING started_at, finished_at`,
		finished).Scan(&startedAt, &finishedAt); err != nil {
		t.Fatalf("finish the step at position 0: %v", err)
	}
	return []model.Attempt{
		{
			ID: model.AttemptID(first), Number: 1, AcceptedVersion: 1,
			Steps: []model.Step{
				{
					ID: model.StepID(finished), Position: 0, Name: "test", State: model.StepFinished,
					Outcome: new(model.OutcomeSuccess), ExitStatus: new(0), StartedAt: &startedAt, FinishedAt: &finishedAt,
				},
				{ID: model.StepID(pending), Position: 1, Name: "test", State: model.StepPending},
			},
		},
		{
			ID: model.AttemptID(second), Number: 2, AcceptedVersion: 1,
			Outcome: new(model.OutcomeFailure), FinishedAt: &secondFinishedAt,
		},
	}
}

func TestGetBuildReadsAttemptsAndSteps(t *testing.T) {
	s := newStore(t)
	id := createBuild(t, s, "test", "lint")
	want := getBuild(t, s, id)
	attempts := addAttempts(t, s, int64(want.Workflows[0].ID))

	got := getBuild(t, s, id)
	if n := len(got.Workflows[0].Attempts); n != len(attempts) {
		t.Fatalf("test's attempts = %d, want %d", n, len(attempts))
	}
	// The attempts' start times are the database's, so want takes them
	// from got.
	for i := range attempts {
		attempts[i].StartedAt = got.Workflows[0].Attempts[i].StartedAt
	}
	want.Workflows[0].Attempts = attempts
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBuild() = %+v, want %+v", got, want)
	}
}

// exitStatus is the exit status the tests' steps report for outcome.
func exitStatus(outcome model.Outcome) *int {
	if outcome == model.OutcomeFailure {
		return new(1)
	}
	return new(0)
}

// runSteps runs the steps of the attempt a as the engine will: it starts
// each in order and finishes it with outcomes' outcome, and skips the rest
// once one fails. It returns the attempt's outcome.
func runSteps(ctx context.Context, s *Store, a model.AttemptID, outcomes []model.Outcome) (model.Outcome, error) {
	for i, outcome := range outcomes {
		position := i + 1
		if err := s.StartStep(ctx, a, position); err != nil {
			return "", err
		}
		if err := s.FinishStep(ctx, a, position, outcome, exitStatus(outcome)); err != nil {
			return "", err
		}
		if outcome != model.OutcomeFailure {
			continue
		}
		for skipped := position + 1; skipped <= len(outcomes); skipped++ {
			if err := s.SkipStep(ctx, a, skipped); err != nil {
				return "", err
			}
		}
		return model.OutcomeFailure, nil
	}
	return model.OutcomeSuccess, nil
}

// runToTheEnd runs the steps of the attempt a of the workflow w of the
// build id with runSteps, then finishes the attempt, the workflow and the
// build with the attempt's outcome, all in one transaction, as the engine
// will compose them.
func runToTheEnd(
	t *testing.T, s *Store, id model.BuildID, w model.WorkflowID, a model.AttemptID, outcomes ...model.Outcome,
) {
	t.Helper()
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		outcome, err := runSteps(ctx, s, a, outcomes)
		if err != nil {
			return err
		}
		if err := s.FinishAttempt(ctx, a, outcome); err != nil {
			return err
		}
		if err := s.FinishWorkflow(ctx, w, outcome); err != nil {
			return err
		}
		return s.FinishBuild(ctx, id, outcome)
	})
	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}
}

// wantFinished returns got as it should read once finished with outcome,
// for a workflow handed out at version: the build, the workflow and its
// one attempt finished with outcome, and each step finished with steps'
// outcome, exit status 0 for success and 1 for failure, or skipped past
// the end of steps. Ids, names and times come from got, and the test
// checks that the times are set.
func wantFinished(
	t *testing.T, got model.Build, version int64, outcome model.Outcome, steps ...model.Outcome,
) model.Build {
	t.Helper()
	if len(got.Workflows) != 1 || len(got.Workflows[0].Attempts) != 1 {
		t.Fatalf("build = %+v, want one workflow with one attempt", got)
	}
	w := got.Workflows[0]
	a := w.Attempts[0]
	if got.FinishedAt == nil || w.FinishedAt == nil || a.FinishedAt == nil {
		t.Errorf("finished at %v, %v and %v, want the build's, the workflow's and the attempt's times",
			got.FinishedAt, w.FinishedAt, a.FinishedAt)
	}
	wantSteps := make([]model.Step, len(a.Steps))
	for i, step := range a.Steps {
		want := model.Step{
			ID: step.ID, Position: i + 1, Name: step.Name, State: model.StepSkipped, FinishedAt: step.FinishedAt,
		}
		if i < len(steps) {
			want.State, want.Outcome, want.ExitStatus = model.StepFinished, &steps[i], exitStatus(steps[i])
			want.StartedAt = step.StartedAt
			if step.StartedAt == nil {
				t.Errorf("step %d's start time is nil", i+1)
			}
		}
		if step.FinishedAt == nil {
			t.Errorf("step %d's finish time is nil", i+1)
		}
		wantSteps[i] = want
	}
	// Starting the attempt and finishing the workflow each bump its version.
	return model.Build{
		ID: got.ID, Repository: repository, State: model.BuildFinished, Outcome: &outcome,
		CreatedAt: got.CreatedAt, FinishedAt: got.FinishedAt,
		Workflows: []model.Workflow{{
			ID: w.ID, Name: "test", State: model.WorkflowFinished, Outcome: &outcome, Version: version + 2,
			Definition: definition, CreatedAt: w.CreatedAt, AssignedAt: w.AssignedAt, FinishedAt: w.FinishedAt,
			Attempts: []model.Attempt{{
				ID: a.ID, Number: 1, AcceptedVersion: version, Outcome: &outcome,
				StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, Steps: wantSteps,
			}},
		}},
	}
}

// startThree hands out a build's one workflow and starts an attempt of it
// with three steps, and returns the build's, the workflow's and the
// attempt's ids and the version the hand-out returned.
func startThree(t *testing.T, s *Store) (model.BuildID, model.WorkflowID, model.AttemptID, int64) {
	t.Helper()
	id, w, version := assigned(t, s)
	a, err := s.StartAttempt(t.Context(), w, version, []string{"checkout", "build", "test"})
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	return id, w, a, version
}

func TestBuildRunsToSuccess(t *testing.T) {
	s := newStore(t)
	id, w, a, version := startThree(t, s)
	runToTheEnd(t, s, id, w, a, model.OutcomeSuccess, model.OutcomeSuccess, model.OutcomeSuccess)

	got := getBuild(t, s, id)
	want := wantFinished(t, got, version, model.OutcomeSuccess,
		model.OutcomeSuccess, model.OutcomeSuccess, model.OutcomeSuccess)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBuild() = %+v, want %+v", got, want)
	}
}

func TestBuildFailsAtTheSecondStep(t *testing.T) {
	s := newStore(t)
	id, w, a, version := startThree(t, s)
	runToTheEnd(t, s, id, w, a, model.OutcomeSuccess, model.OutcomeFailure, model.OutcomeSuccess)

	got := getBuild(t, s, id)
	want := wantFinished(t, got, version, model.OutcomeFailure, model.OutcomeSuccess, model.OutcomeFailure)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetBuild() = %+v, want %+v", got, want)
	}
}

func TestFinishBuildConflicts(t *testing.T) {
	tests := map[string]func(t *testing.T, s *Store, id model.BuildID){
		"an unfinished workflow": func(*testing.T, *Store, model.BuildID) {},
		"already finished": func(t *testing.T, s *Store, id model.BuildID) {
			t.Helper()
			w := getBuild(t, s, id).Workflows[0]
			version, _, err := s.AssignWorkflow(t.Context(), w.ID, w.Version)
			if err != nil {
				t.Fatalf("AssignWorkflow() error = %v", err)
			}
			a, err := s.StartAttempt(t.Context(), w.ID, version, nil)
			if err != nil {
				t.Fatalf("StartAttempt() error = %v", err)
			}
			runToTheEnd(t, s, id, w.ID, a)
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			id := createBuild(t, s, "test")
			prepare(t, s, id)
			before := getBuild(t, s, id)
			if err := s.FinishBuild(t.Context(), id, model.OutcomeFailure); !errors.Is(err, model.ErrConflict) {
				t.Errorf("FinishBuild() error = %v, want %v", err, model.ErrConflict)
			}
			if after := getBuild(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("build after the conflict = %+v, want %+v", after, before)
			}
		})
	}
}

func TestFinishBuildLeavesAnUnfinishedBuildRunning(t *testing.T) {
	s := newStore(t)
	id := createBuild(t, s, "test", "lint")
	if err := s.FinishBuild(t.Context(), id, model.OutcomeSuccess); !errors.Is(err, model.ErrConflict) {
		t.Errorf("FinishBuild() error = %v, want %v", err, model.ErrConflict)
	}
	if got := getBuild(t, s, id); got.State != model.BuildRunning || got.Outcome != nil || got.FinishedAt != nil {
		t.Errorf("build = %s, outcome %v, finished at %v; want running, none and none",
			got.State, got.Outcome, got.FinishedAt)
	}
}

func TestFinishBuildUnknown(t *testing.T) {
	s := newStore(t)
	if err := s.FinishBuild(t.Context(), 7, model.OutcomeSuccess); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("FinishBuild(7) error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestAnErrorInTxUndoesFinishAttempt(t *testing.T) {
	s := newStore(t)
	id, _, a := started(t, s, "test")
	before := getBuild(t, s, id)
	errStop := errors.New("stop")
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		if err := s.FinishAttempt(ctx, a, model.OutcomeFailure); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("InTx() error = %v, want %v", err, errStop)
	}
	got := getBuild(t, s, id)
	if !reflect.DeepEqual(got, before) {
		t.Errorf("build after the rollback = %+v, want %+v", got, before)
	}
	if w := got.Workflows[0]; w.State != model.WorkflowRunning || w.Attempts[0].FinishedAt != nil {
		t.Errorf("workflow = %s, attempt finished at %v; want running and open", w.State, w.Attempts[0].FinishedAt)
	}
}
