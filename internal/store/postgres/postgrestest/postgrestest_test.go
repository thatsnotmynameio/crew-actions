package postgrestest

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
)

// open opens a store on the database at u, which the test's end closes.
func open(t *testing.T, u string) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(t.Context(), u)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// databaseID returns s's database id.
func databaseID(t *testing.T, s *postgres.Store) string {
	t.Helper()
	id, err := s.DatabaseID(t.Context())
	if err != nil {
		t.Fatalf("DatabaseID() error = %v", err)
	}
	return id
}

func TestNewDatabasesAreIndependent(t *testing.T) {
	first, second := open(t, New(t)), open(t, New(t))
	repo := model.Repository{Host: "github.com", Organization: "thatsnotmynameio", Name: "crew-actions"}
	id, err := first.CreateBuild(t.Context(), repo, []model.NewWorkflow{{Name: "test", Definition: []byte("{}")}})
	if err != nil {
		t.Fatalf("CreateBuild() error = %v", err)
	}
	if _, err := first.GetBuild(t.Context(), id); err != nil {
		t.Errorf("GetBuild(%d) in the first database error = %v", id, err)
	}
	if _, err := second.GetBuild(t.Context(), id); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("GetBuild(%d) in the second database error = %v, want %v", id, err, model.ErrNotFound)
	}
	if a, b := databaseID(t, first), databaseID(t, second); a == b {
		t.Errorf("DatabaseID() = %q in both databases, want two ids", a)
	}
}

func TestNewDropsTheDatabaseAtCleanup(t *testing.T) {
	var name string
	t.Run("new", func(t *testing.T) {
		u, err := url.Parse(New(t))
		if err != nil {
			t.Fatalf("parse New()'s URL: %v", err)
		}
		name = strings.TrimPrefix(u.Path, "/")
	})
	if name == "" {
		t.Skip("New made no database")
	}
	conn, err := pgx.Connect(t.Context(), os.Getenv(databaseURLEnv))
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

func TestEnsureTemplateRebuildsAnUnfinishedTemplate(t *testing.T) {
	server := os.Getenv(databaseURLEnv)
	if server == "" {
		t.Skipf("%s is unset, so the tests that need Postgres skip", databaseURLEnv)
	}
	// A name of its own, so the template other tests copy is left alone.
	name := "crew_actions_template_test_" + strings.ToLower(rand.Text())
	// What a test binary that died while migrating the template leaves.
	if err := exec(t.Context(), server, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := exec(context.Background(), server, "ALTER DATABASE "+name+" WITH IS_TEMPLATE false"); err != nil {
			t.Errorf("unmark database %s: %v", name, err)
		}
		if err := exec(context.Background(), server, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	if err := ensureTemplate(t.Context(), server, name); err != nil {
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
	u, err := withDatabase(server, name)
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
			return ensureTemplate(t.Context(), unreachable, "test")
		},
		"an unreachable server's statement": func(t *testing.T) error {
			t.Helper()
			return exec(t.Context(), unreachable, "SELECT 1")
		},
		"a malformed URL": func(t *testing.T) error {
			t.Helper()
			_, err := withDatabase("postgres://postgres@127.0.0.1:port/postgres", "test")
			return err
		},
		"migrating an unreachable database": func(t *testing.T) error {
			t.Helper()
			return migrate(t.Context(), unreachable, "test")
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
	server := os.Getenv(databaseURLEnv)
	if server == "" {
		t.Skipf("%s is unset, so the tests that need Postgres skip", databaseURLEnv)
	}
	if err := exec(t.Context(), server, "SELECT 1 / 0"); err == nil {
		t.Error("exec(SELECT 1 / 0) error = nil, want an error")
	}
}
