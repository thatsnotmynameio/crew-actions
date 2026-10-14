// Package postgrestest gives a test a migrated database of its own, on the
// server CREW_ACTIONS_TEST_DATABASE_URL names, so tests in several packages
// can use Postgres at once. Without the variable, the test skips.
//
// Each database is a copy of a template the store's migrations were
// applied to once, which testdb makes and copies.
package postgrestest

import (
	"context"
	"fmt"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/internal/testdb"
)

// New creates a migrated database, which the test's end drops, and returns
// its URL. The database has a database id of its own. It skips the test
// when CREW_ACTIONS_TEST_DATABASE_URL is unset.
func New(t *testing.T) string {
	t.Helper()
	migrations, err := postgres.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	return testdb.New(t, migrations, migrate)
}

// migrate applies the store's migrations to the database at u, and closes
// every connection to it before it returns.
func migrate(ctx context.Context, u string) error {
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
