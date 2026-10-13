package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The harness: TestMain migrates the database that
// CREW_ACTIONS_TEST_DATABASE_URL names, which compose.yaml's test-postgres
// serves locally and in CI's Linux job. A test asks for a store on that
// database, or for an empty database of its own. Without the variable, as
// on GitHub's macOS runners, the tests that need Postgres skip.

// databaseURLEnv names the variable that holds the test database's URL.
const databaseURLEnv = "CREW_ACTIONS_TEST_DATABASE_URL"

var (
	// databaseURL is the URL of the migrated database. It is empty when
	// the variable is unset.
	databaseURL string
	// databases counts the empty databases tests created, to name them.
	databases atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests migrates the database the variable names, when it is set, runs
// the tests and returns their exit code. A database that cannot be reached
// or migrated fails the run: only an unset variable skips.
func runTests(m *testing.M) int {
	u := os.Getenv(databaseURLEnv)
	if u == "" {
		return m.Run()
	}
	if err := migrate(context.Background(), u); err != nil {
		fmt.Fprintf(os.Stderr, "migrate the database %s names: %v\n", databaseURLEnv, err)
		return 1
	}
	databaseURL = u
	return m.Run()
}

// migrate applies the store's migrations to the database at u.
func migrate(ctx context.Context, u string) error {
	s, err := Open(ctx, u)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Migrate(ctx)
}

// requireDatabase returns the migrated database's URL, and skips the test
// without one.
func requireDatabase(t *testing.T) string {
	t.Helper()
	if databaseURL == "" {
		t.Skipf("%s is unset, so the tests that need Postgres skip", databaseURLEnv)
	}
	return databaseURL
}

// newStore returns a store on the migrated database, with every table
// emptied, and skips the test without a database.
func newStore(t *testing.T) *Store {
	t.Helper()
	s := open(t, requireDatabase(t))
	if _, err := s.pool.Exec(t.Context(), "TRUNCATE builds, workflows, attempts, steps RESTART IDENTITY"); err != nil {
		t.Fatalf("empty the tables: %v", err)
	}
	return s
}

// open opens a store on the database at u, which the test's end closes.
func open(t *testing.T, u string) *Store {
	t.Helper()
	s, err := Open(t.Context(), u)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// emptyDatabase creates a database no migration ran on, which the test's
// end drops, and returns its URL. It skips the test without a database.
func emptyDatabase(t *testing.T) string {
	t.Helper()
	admin := requireDatabase(t)
	name := fmt.Sprintf("empty_%d", databases.Add(1))
	conn, err := pgx.Connect(t.Context(), admin)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if _, err := conn.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("connect to Postgres: %v", err)
			return
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if _, err := conn.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("parse %q: %v", admin, err)
	}
	u.Path = "/" + name
	return u.String()
}
