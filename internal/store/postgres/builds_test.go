package postgres

import (
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
