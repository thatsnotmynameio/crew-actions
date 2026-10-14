package postgres

import (
	"regexp"
	"testing"
)

// uuidPattern matches a UUID as Postgres writes it.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// databaseID returns s's database id.
func databaseID(t *testing.T, s *Store) string {
	t.Helper()
	id, err := s.DatabaseID(t.Context())
	if err != nil {
		t.Fatalf("DatabaseID() error = %v", err)
	}
	return id
}

func TestDatabaseID(t *testing.T) {
	s := newStore(t)
	id := databaseID(t, s)
	if !uuidPattern.MatchString(id) {
		t.Errorf("DatabaseID() = %q, want a UUID", id)
	}
	if again := databaseID(t, s); again != id {
		t.Errorf("second DatabaseID() = %q, want %q", again, id)
	}
	other := open(t, emptyDatabase(t))
	if err := other.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if got := databaseID(t, other); got == id {
		t.Errorf("another database's DatabaseID() = %q, want another id than %q", got, id)
	}
}

func TestDatabaseIdentityHoldsOneRow(t *testing.T) {
	s := newStore(t)
	_, err := s.pool.Exec(t.Context(), "INSERT INTO database_identity DEFAULT VALUES")
	wantViolation(t, err, uniqueViolation, "database_identity_pkey")
	_, err = s.pool.Exec(t.Context(), "INSERT INTO database_identity (one) VALUES (false)")
	wantViolation(t, err, checkViolation, "database_identity_one_check")
}
