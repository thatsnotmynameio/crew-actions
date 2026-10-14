package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/internal/testdb"
)

// The harness: a test asks for a store on a migrated database of its own,
// copied from the template testdb makes on the server
// CREW_ACTIONS_TEST_DATABASE_URL names, or for an empty database of its
// own on that server. compose.yaml's test-postgres serves it locally and
// in CI's Linux job. Without the variable, as on GitHub's macOS runners,
// the tests that need Postgres skip.

// databases counts the empty databases tests created, to name them.
var databases atomic.Int64

// migrate applies the store's migrations to the database at u.
func migrate(ctx context.Context, u string) error {
	s, err := Open(ctx, u)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Migrate(ctx)
}

// newStore returns a store on a migrated database of its own, which the
// test's end drops, and skips the test without a server.
func newStore(t *testing.T) *Store {
	t.Helper()
	dir, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return open(t, testdb.New(t, dir, migrate))
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
// end drops, and returns its URL. It skips the test without a server.
func emptyDatabase(t *testing.T) string {
	t.Helper()
	admin := testdb.Server(t)
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
	u, err := testdb.WithDatabase(admin, name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStoresOfParallelTestsAreIndependent(t *testing.T) {
	t.Parallel()
	// Each test opens its store, then inserts a build, then counts the
	// builds, and waits for the other at each step, so on a shared
	// database each would see the other's build.
	var opened, inserted sync.WaitGroup
	opened.Add(2)
	inserted.Add(2)
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			opened.Done()
			opened.Wait()
			createBuild(t, s, "test")
			inserted.Done()
			inserted.Wait()
			if got := countBuilds(t, s); got != 1 {
				t.Errorf("builds = %d, want only the test's own", got)
			}
		})
	}
}
