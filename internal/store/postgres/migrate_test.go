package postgres

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// The SQLSTATE codes of the constraints the schema enforces.
const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	checkViolation      = "23514"
)

// identity is a repository's identity, which every row carries.
type identity struct {
	host, organization, repository string
}

// repo is the repository the tests' rows belong to.
var repo = identity{"github.com", "thatsnotmynameio", "crew-actions"}

// chain is one build with a workflow, an open attempt and a pending step.
type chain struct {
	build, workflow, attempt, step int64
}

// insert runs query, which inserts a row and returns its id, and returns
// that id. Its error says it was inserting what.
func insert(t *testing.T, s *Store, what, query string, args ...any) (int64, error) {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(t.Context(), query, args...).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert %s: %w", what, err)
	}
	return id, nil
}

// insertBuild inserts a running build of id and returns the row's id.
func insertBuild(t *testing.T, s *Store, id identity) (int64, error) {
	t.Helper()
	return insert(t, s, "a build",
		`INSERT INTO builds (host, organization, repository, state)
		 VALUES ($1, $2, $3, 'running') RETURNING id`,
		id.host, id.organization, id.repository)
}

// insertWorkflow inserts a pending workflow of build named name, under id,
// and returns the row's id.
func insertWorkflow(t *testing.T, s *Store, build int64, name string, id identity) (int64, error) {
	t.Helper()
	return insert(t, s, "a workflow",
		`INSERT INTO workflows (build_id, host, organization, repository, name, state, definition)
		 VALUES ($1, $2, $3, $4, $5, 'pending', '\x7b7d') RETURNING id`,
		build, id.host, id.organization, id.repository, name)
}

// insertAttempt inserts an open attempt of workflow with number, under id,
// and returns the row's id.
func insertAttempt(t *testing.T, s *Store, workflow int64, number int, id identity) (int64, error) {
	t.Helper()
	return insert(t, s, "an attempt",
		`INSERT INTO attempts (workflow_id, host, organization, repository, number, accepted_version)
		 VALUES ($1, $2, $3, $4, $5, 1) RETURNING id`,
		workflow, id.host, id.organization, id.repository, number)
}

// insertStep inserts a pending step of attempt at position, under id, and
// returns the row's id.
func insertStep(t *testing.T, s *Store, attempt int64, position int, id identity) (int64, error) {
	t.Helper()
	return insert(t, s, "a step",
		`INSERT INTO steps (attempt_id, host, organization, repository, position, name, state)
		 VALUES ($1, $2, $3, $4, $5, 'test', 'pending') RETURNING id`,
		attempt, id.host, id.organization, id.repository, position)
}

// newChain inserts a build of repo with a workflow, an open attempt and a
// pending step.
func newChain(t *testing.T, s *Store) chain {
	t.Helper()
	var c chain
	var err error
	if c.build, err = insertBuild(t, s, repo); err != nil {
		t.Fatal(err)
	}
	if c.workflow, err = insertWorkflow(t, s, c.build, "test", repo); err != nil {
		t.Fatal(err)
	}
	if c.attempt, err = insertAttempt(t, s, c.workflow, 1, repo); err != nil {
		t.Fatal(err)
	}
	if c.step, err = insertStep(t, s, c.attempt, 0, repo); err != nil {
		t.Fatal(err)
	}
	return c
}

// update sets columns on the chain's row in table.
func (c chain) update(t *testing.T, s *Store, table, set string) error {
	t.Helper()
	id := map[string]int64{"builds": c.build, "workflows": c.workflow, "attempts": c.attempt, "steps": c.step}[table]
	if _, err := s.pool.Exec(t.Context(), "UPDATE "+table+" SET "+set+" WHERE id = $1", id); err != nil {
		return fmt.Errorf("update %s: %w", table, err)
	}
	return nil
}

// wantViolation fails the test unless err is Postgres refusing a row on
// constraint, with the SQLSTATE code.
func wantViolation(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want %s on %s", err, code, constraint)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("error = %s on %q (%v), want %s on %q", pgErr.Code, pgErr.ConstraintName, err, code, constraint)
	}
}

// tables returns the names of the store's tables in s's database, sorted.
func tables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.pool.Query(t.Context(),
		`SELECT table_name::text FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		 ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	return names
}

// applied returns how many migrations goose recorded as applied in s's
// database.
func applied(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM goose_db_version WHERE version_id > 0").Scan(&n); err != nil {
		t.Fatalf("count the applied migrations: %v", err)
	}
	return n
}

// theTables are the tables the first migration creates.
var theTables = []string{"attempts", "builds", "steps", "workflows"}

func TestMigrateCreatesTheTablesOnce(t *testing.T) {
	s := open(t, emptyDatabase(t))
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if got := tables(t, s); !slices.Equal(got, theTables) {
		t.Errorf("tables = %v, want %v", got, theTables)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	if got := applied(t, s); got != 1 {
		t.Errorf("applied migrations after a second Migrate = %d, want 1", got)
	}
}

func TestMigrateConcurrently(t *testing.T) {
	u := emptyDatabase(t)
	stores := []*Store{open(t, u), open(t, u)}
	// goose's version table exists, as it does for every migration after
	// the first, so both calls find the migration pending at once. Creating
	// it in the race would delay one call until the other is done.
	db := stdlib.OpenDBFromPool(stores[0].pool)
	defer func() { _ = db.Close() }()
	p, err := newProvider(db)
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}
	if _, err := p.HasPending(t.Context()); err != nil {
		t.Fatalf("HasPending() error = %v", err)
	}
	errs := make([]error, len(stores))
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Go(func() { errs[i] = s.Migrate(t.Context()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate() %d error = %v", i, err)
		}
	}
	if got := applied(t, stores[0]); got != 1 {
		t.Errorf("applied migrations = %d, want 1", got)
	}
	if got := tables(t, stores[0]); !slices.Equal(got, theTables) {
		t.Errorf("tables = %v, want %v", got, theTables)
	}
}

func TestMigrateDownAndUp(t *testing.T) {
	s := open(t, emptyDatabase(t))
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	db := stdlib.OpenDBFromPool(s.pool)
	defer func() { _ = db.Close() }()
	p, err := newProvider(db)
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}
	if _, err := p.DownTo(t.Context(), 0); err != nil {
		t.Fatalf("DownTo(0) error = %v", err)
	}
	if got := tables(t, s); len(got) != 0 {
		t.Errorf("tables after migrating down = %v, want none", got)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() after migrating down error = %v", err)
	}
	newChain(t, s)
}

func TestChildIdentityFollowsItsParent(t *testing.T) {
	s := newStore(t)
	c := newChain(t, s)
	// Each insert would be valid under repo's identity: the workflow's name
	// and the step's position are free, and the attempt's workflow has no
	// open attempt.
	other, err := insertWorkflow(t, s, c.build, "other", repo)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		insert     func(identity) error
		constraint string
	}{
		{"workflow", func(id identity) error {
			_, err := insertWorkflow(t, s, c.build, "another", id)
			return err
		}, "workflows_build_fkey"},
		{"attempt", func(id identity) error {
			_, err := insertAttempt(t, s, other, 1, id)
			return err
		}, "attempts_workflow_fkey"},
		{"step", func(id identity) error {
			_, err := insertStep(t, s, c.attempt, 1, id)
			return err
		}, "steps_attempt_fkey"},
	}
	others := map[string]identity{
		"host":         {"gitlab.com", repo.organization, repo.repository},
		"organization": {repo.host, "someone-else", repo.repository},
		"repository":   {repo.host, repo.organization, "crew"},
	}
	for _, tt := range tests {
		for field, id := range others {
			t.Run(tt.name+" with another "+field, func(t *testing.T) {
				wantViolation(t, tt.insert(id), foreignKeyViolation, tt.constraint)
			})
		}
	}
}

func TestIdentityIsNotEmpty(t *testing.T) {
	s := newStore(t)
	tests := []struct {
		id         identity
		constraint string
	}{
		{identity{"", repo.organization, repo.repository}, "builds_host_check"},
		{identity{repo.host, "", repo.repository}, "builds_organization_check"},
		{identity{repo.host, repo.organization, ""}, "builds_repository_check"},
	}
	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			_, err := insertBuild(t, s, tt.id)
			wantViolation(t, err, checkViolation, tt.constraint)
		})
	}
}

func TestOutcomeAndFinishedAtOnlyOnceFinished(t *testing.T) {
	s := newStore(t)
	c := newChain(t, s)
	tests := []struct {
		name, table, set, constraint string
	}{
		{"running build with an outcome", "builds",
			"outcome = 'success'", "builds_outcome_when_finished"},
		{"finished build without an outcome", "builds",
			"state = 'finished', finished_at = now()", "builds_outcome_when_finished"},
		{"finished build without finished_at", "builds",
			"state = 'finished', outcome = 'success'", "builds_finished_at_when_finished"},
		{"pending workflow with an outcome", "workflows",
			"outcome = 'success'", "workflows_outcome_when_finished"},
		{"finished workflow without an outcome", "workflows",
			"state = 'finished', finished_at = now()", "workflows_outcome_when_finished"},
		{"running workflow with finished_at", "workflows",
			"state = 'running', finished_at = now()", "workflows_finished_at_when_finished"},
		{"open attempt with an outcome", "attempts",
			"outcome = 'failure'", "attempts_outcome_when_finished"},
		{"finished attempt without an outcome", "attempts",
			"finished_at = now()", "attempts_outcome_when_finished"},
		{"pending step with an outcome", "steps",
			"outcome = 'success'", "steps_outcome_when_finished"},
		{"finished step without an outcome", "steps",
			"state = 'finished', finished_at = now()", "steps_outcome_when_finished"},
		{"skipped step with an outcome", "steps",
			"state = 'skipped', finished_at = now(), outcome = 'success'", "steps_outcome_when_finished"},
		{"skipped step without finished_at", "steps",
			"state = 'skipped'", "steps_finished_at_when_ended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantViolation(t, c.update(t, s, tt.table, tt.set), checkViolation, tt.constraint)
		})
	}
}

func TestEveryRowFinishes(t *testing.T) {
	s := newStore(t)
	c := newChain(t, s)
	finished := "state = 'finished', outcome = 'success', finished_at = now()"
	for _, table := range []string{"steps", "workflows", "builds"} {
		if err := c.update(t, s, table, finished); err != nil {
			t.Error(err)
		}
	}
	if err := c.update(t, s, "attempts", "outcome = 'success', finished_at = now()"); err != nil {
		t.Error(err)
	}
}

func TestOutcomeIsSuccessOrFailure(t *testing.T) {
	s := newStore(t)
	c := newChain(t, s)
	tests := []struct {
		table, set string
	}{
		{"builds", "state = 'finished', outcome = 'lost', finished_at = now()"},
		{"workflows", "state = 'finished', outcome = 'lost', finished_at = now()"},
		{"attempts", "outcome = 'lost', finished_at = now()"},
		{"steps", "state = 'finished', outcome = 'lost', finished_at = now()"},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			wantViolation(t, c.update(t, s, tt.table, tt.set), checkViolation, tt.table+"_outcome_check")
		})
	}
}

func TestOneOpenAttemptPerWorkflow(t *testing.T) {
	s := newStore(t)
	c := newChain(t, s)
	_, err := insertAttempt(t, s, c.workflow, 2, repo)
	wantViolation(t, err, uniqueViolation, "attempts_one_open_per_workflow")

	if err := c.update(t, s, "attempts", "outcome = 'failure', finished_at = now()"); err != nil {
		t.Fatal(err)
	}
	if _, err := insertAttempt(t, s, c.workflow, 2, repo); err != nil {
		t.Errorf("open a second attempt once the first finished: %v", err)
	}
}
