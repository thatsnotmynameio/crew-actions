package postgrestest

import (
	"errors"
	"testing"

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

// unreachable is the URL of a server that is not there.
const unreachable = "postgres://postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=5"

func TestMigrateReportsAnUnreachableDatabase(t *testing.T) {
	if err := migrate(t.Context(), unreachable); err == nil {
		t.Error("migrate() error = nil, want an error")
	}
}
