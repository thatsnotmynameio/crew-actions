package testdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
)

// migrateStore applies the store's migrations to the database at u.
func migrateStore(ctx context.Context, u string) error {
	s, err := postgres.Open(ctx, u)
	if err != nil {
		return fmt.Errorf("apply the store's migrations: %w", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return fmt.Errorf("apply the store's migrations: %w", err)
	}
	return nil
}

// newStoreDatabase calls New with the store's migrations.
func newStoreDatabase(t *testing.T) string {
	t.Helper()
	migrations, err := postgres.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return New(t, migrations, migrateStore)
}

// databaseID returns the id of the database at u.
func databaseID(t *testing.T, u string) string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), u)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var id string
	if err := conn.QueryRow(t.Context(), "SELECT id FROM database_identity").Scan(&id); err != nil {
		t.Fatalf("read the database id: %v", err)
	}
	return id
}

func TestNewDatabasesHaveIdsOfTheirOwn(t *testing.T) {
	if a, b := databaseID(t, newStoreDatabase(t)), databaseID(t, newStoreDatabase(t)); a == b {
		t.Errorf("database id = %q in both databases, want two ids", a)
	}
}

func TestNewDropsTheDatabaseAtCleanup(t *testing.T) {
	var name string
	t.Run("new", func(t *testing.T) {
		u, err := url.Parse(newStoreDatabase(t))
		if err != nil {
			t.Fatalf("parse New()'s URL: %v", err)
		}
		name = strings.TrimPrefix(u.Path, "/")
	})
	if name == "" {
		t.Skip("New made no database")
	}
	conn, err := pgx.Connect(t.Context(), Server(t))
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var exists bool
	if err := conn.QueryRow(t.Context(),
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("look the database up: %v", err)
	}
	if exists {
		t.Errorf("database %s exists after the test that made it ended", name)
	}
}

// templateDatabase returns a template name of the test's own, so the
// template other tests copy is left alone, and drops the database of that
// name at the test's end.
func templateDatabase(t *testing.T, server string) string {
	t.Helper()
	name := "crew_actions_template_test_" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		if err := exec(context.Background(), server, "ALTER DATABASE "+name+" WITH IS_TEMPLATE false"); err != nil {
			t.Errorf("unmark database %s: %v", name, err)
		}
		if err := exec(context.Background(), server, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return name
}

func TestEnsureTemplateRebuildsAnUnfinishedTemplate(t *testing.T) {
	server := Server(t)
	name := templateDatabase(t, server)
	// What a test binary that died while migrating the template leaves.
	if err := exec(t.Context(), server, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	if err := ensureTemplate(t.Context(), server, name, migrateStore); err != nil {
		t.Fatalf("ensureTemplate() error = %v", err)
	}
	conn, err := pgx.Connect(t.Context(), server)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var finished bool
	if err := conn.QueryRow(t.Context(),
		"SELECT datistemplate FROM pg_database WHERE datname = $1", name).Scan(&finished); err != nil {
		t.Fatalf("look the template up: %v", err)
	}
	if !finished {
		t.Errorf("database %s is not marked as a template", name)
	}
	u, err := WithDatabase(server, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec(t.Context(), u, "SELECT id FROM database_identity"); err != nil {
		t.Errorf("the rebuilt template has no database_identity: %v", err)
	}
}

// unreachable is the URL of a server that is not there.
const unreachable = "postgres://postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=5"

func TestHelpersReportFailures(t *testing.T) {
	tests := map[string]func(t *testing.T) error{
		"an unreachable server's template": func(t *testing.T) error {
			t.Helper()
			return ensureTemplate(t.Context(), unreachable, "test", migrateStore)
		},
		"an unreachable server's statement": func(t *testing.T) error {
			t.Helper()
			return exec(t.Context(), unreachable, "SELECT 1")
		},
		"a malformed URL": func(t *testing.T) error {
			t.Helper()
			_, err := WithDatabase("postgres://postgres@127.0.0.1:port/postgres", "test")
			return err
		},
		"migrating a template on a malformed URL": func(t *testing.T) error {
			t.Helper()
			return migrateTemplate(t.Context(), "postgres://postgres@127.0.0.1:port/postgres", "test", migrateStore)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			if err := run(t); err == nil {
				t.Error("error = nil, want an error")
			}
		})
	}
}

func TestExecReportsAFailedStatement(t *testing.T) {
	if err := exec(t.Context(), Server(t), "SELECT 1 / 0"); err == nil {
		t.Error("exec(SELECT 1 / 0) error = nil, want an error")
	}
}
