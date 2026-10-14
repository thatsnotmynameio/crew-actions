// Package testdb gives a test a migrated database of its own, on the
// server CREW_ACTIONS_TEST_DATABASE_URL names, so tests in several packages
// can use Postgres at once. Without the variable, the test skips.
//
// Each database is a copy of a template the store's migrations were
// applied to once. The template is named after the migrations, so test
// binaries running side by side share it, and a changed migration gets a
// new one.
//
// It takes the migrations and the function that applies them rather than
// importing the store, so the store's own tests can use it: postgrestest
// wraps it for every other package.
package testdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// databaseURLEnv names the variable that holds the server's URL.
const databaseURLEnv = "CREW_ACTIONS_TEST_DATABASE_URL"

// templateLock is the key of the advisory lock held while the template is
// looked up and made.
const templateLock = 7_036_217_408_512_667_941

// Migrate applies migrations to the database at u, and closes every
// connection to it before it returns.
type Migrate func(ctx context.Context, u string) error

// Server returns the server's URL, and skips the test when
// CREW_ACTIONS_TEST_DATABASE_URL is unset.
func Server(t *testing.T) string {
	t.Helper()
	server := os.Getenv(databaseURLEnv)
	if server == "" {
		t.Skipf("%s is unset, so the tests that need Postgres skip", databaseURLEnv)
	}
	return server
}

// New creates a database migrated with migrations, which the test's end
// drops, and returns its URL. migrate applies them to the template. The
// database has a database id of its own. It skips the test when
// CREW_ACTIONS_TEST_DATABASE_URL is unset.
func New(t *testing.T, migrations fs.FS, migrate Migrate) string {
	t.Helper()
	server := Server(t)
	ctx := t.Context()
	template, err := templateName(migrations)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureTemplate(ctx, server, template, migrate); err != nil {
		t.Fatalf("make the template database: %v", err)
	}
	// A random name, so tests in other binaries never pick it.
	name := "crew_actions_test_" + strings.ToLower(rand.Text())
	if err := exec(ctx, server, "CREATE DATABASE "+name+" TEMPLATE "+template); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := exec(context.Background(), server, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	u, err := WithDatabase(server, name)
	if err != nil {
		t.Fatal(err)
	}
	// Every copy starts with the template's id.
	if err := exec(ctx, u, "UPDATE database_identity SET id = gen_random_uuid()"); err != nil {
		t.Fatalf("give database %s an id: %v", name, err)
	}
	return u
}

// ensureTemplate makes the template database name with migrate on server,
// unless a finished one is there. It holds an advisory lock until every
// connection to the template is closed, as copying a database needs.
//
// A template is finished once it is marked as one, which happens only
// after every migration ran. A database of that name left unmarked, by a
// test binary that died while making it, is dropped and made again.
func ensureTemplate(ctx context.Context, server, name string, migrate Migrate) error {
	conn, err := pgx.Connect(ctx, server)
	if err != nil {
		return fmt.Errorf("connect to Postgres: %w", err)
	}
	// Closing the connection releases the lock, after migrate has closed
	// its connections to the template.
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(templateLock)); err != nil {
		return fmt.Errorf("lock the template: %w", err)
	}
	var finished bool
	err = conn.QueryRow(ctx, "SELECT datistemplate FROM pg_database WHERE datname = $1", name).Scan(&finished)
	switch {
	case err == nil && finished:
		return nil
	case err == nil:
		if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			return fmt.Errorf("drop unfinished template %s: %w", name, err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("look the template up: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	if err := migrateTemplate(ctx, server, name, migrate); err != nil {
		if _, dropErr := conn.Exec(context.WithoutCancel(ctx), "DROP DATABASE "+name+" WITH (FORCE)"); dropErr != nil {
			return fmt.Errorf("%w, and drop database %s: %w", err, name, dropErr)
		}
		return err
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+name+" WITH IS_TEMPLATE true"); err != nil {
		return fmt.Errorf("mark database %s as a template: %w", name, err)
	}
	return nil
}

// migrateTemplate applies migrate to the database name on server.
func migrateTemplate(ctx context.Context, server, name string, migrate Migrate) error {
	u, err := WithDatabase(server, name)
	if err != nil {
		return err
	}
	if err := migrate(ctx, u); err != nil {
		return fmt.Errorf("migrate database %s: %w", name, err)
	}
	return nil
}

// templateName returns the template's name, from a hash of migrations'
// names and contents.
func templateName(migrations fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(migrations, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations, path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", path, err)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s", path, len(b), b)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash the migrations: %w", err)
	}
	return "crew_actions_template_" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// WithDatabase returns server's URL with its database set to name.
func WithDatabase(server, name string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", databaseURLEnv, err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// exec connects to the database at u, runs sql and disconnects.
func exec(ctx context.Context, u, sql string) error {
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		return fmt.Errorf("connect to Postgres: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("run %q: %w", sql, err)
	}
	return nil
}
