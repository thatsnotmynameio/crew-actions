package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The harness: TestMain starts one Postgres 18 container for the package
// and migrates its database. A test asks for a store on that database, or
// for an empty database of its own. Without Docker, as on GitHub's macOS
// runners, the tests that need Postgres skip.

// image is the Postgres the tests run against, by major only.
const image = "postgres:18"

var (
	// databaseURL is the URL of the container's migrated database. It is
	// empty when Docker is unavailable.
	databaseURL string
	// skipReason says why there is no database.
	skipReason string
	// databases counts the empty databases tests created, to name them.
	databases atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests starts and migrates Postgres when Docker is there, runs the
// tests and returns their exit code. A container that fails to start with
// Docker running fails the run: only a missing Docker skips.
func runTests(m *testing.M) int {
	ctx := context.Background()
	if err := dockerHealth(ctx); err != nil {
		skipReason = fmt.Sprintf("Docker is unavailable, so the tests that need Postgres skip: %v", err)
		return m.Run()
	}
	ctr, err := tcpostgres.Run(ctx, image, tcpostgres.BasicWaitStrategies())
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintf(os.Stderr, "stop Postgres: %v\n", err)
		}
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Postgres: %v\n", err)
		return 1
	}
	u, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Postgres' URL: %v\n", err)
		return 1
	}
	if err := migrate(ctx, u); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	databaseURL = u
	return m.Run()
}

// dockerHealth returns why Docker cannot run containers, or nil. Finding
// no Docker host panics inside testcontainers, so a panic is an answer too.
func dockerHealth(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("find Docker: %w", err)
	}
	if err := provider.Health(ctx); err != nil {
		return fmt.Errorf("check Docker: %w", err)
	}
	return nil
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
// without Docker.
func requireDatabase(t *testing.T) string {
	t.Helper()
	if databaseURL == "" {
		t.Skip(skipReason)
	}
	return databaseURL
}

// newStore returns a store on the migrated database, with every table
// emptied, and skips the test without Docker.
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
// end drops, and returns its URL. It skips the test without Docker.
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
