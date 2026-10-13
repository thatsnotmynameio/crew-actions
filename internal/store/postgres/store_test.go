package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// errStop is the error the tests' transactions return to roll back.
var errStop = errors.New("stop")

// insertInTx inserts a running build of repo in the context's transaction.
func insertInTx(ctx context.Context) error {
	if _, err := txFrom(ctx).Exec(ctx,
		`INSERT INTO builds (host, organization, repository, state) VALUES ($1, $2, $3, 'running')`,
		repo.host, repo.organization, repo.repository); err != nil {
		return fmt.Errorf("insert a build: %w", err)
	}
	return nil
}

// countBuilds returns how many builds s holds.
func countBuilds(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM builds").Scan(&n); err != nil {
		t.Fatalf("count the builds: %v", err)
	}
	return n
}

func TestInTxCommits(t *testing.T) {
	s := newStore(t)
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		if err := insertInTx(ctx); err != nil {
			return err
		}
		return insertInTx(ctx)
	})
	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}
	if got := countBuilds(t, s); got != 2 {
		t.Errorf("builds = %d, want 2", got)
	}
}

func TestInTxRollsBack(t *testing.T) {
	s := newStore(t)
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		if err := insertInTx(ctx); err != nil {
			return err
		}
		if err := insertInTx(ctx); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("InTx() error = %v, want %v", err, errStop)
	}
	if got := countBuilds(t, s); got != 0 {
		t.Errorf("builds = %d, want 0", got)
	}
}

func TestInTxJoinsTheContextsTransaction(t *testing.T) {
	s := newStore(t)
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		if err := s.InTx(ctx, insertInTx); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("InTx() error = %v, want %v", err, errStop)
	}
	if got := countBuilds(t, s); got != 0 {
		t.Errorf("builds = %d, want 0: the inner InTx committed on its own", got)
	}
}

func TestInTxReportsAFailedCommit(t *testing.T) {
	s := newStore(t)
	err := s.InTx(t.Context(), func(ctx context.Context) error {
		if err := insertInTx(ctx); err != nil {
			return err
		}
		// The failed statement aborts the transaction, and fn drops its error.
		_, _ = txFrom(ctx).Exec(ctx, "SELECT 1 / 0")
		return nil
	})
	if err == nil || !strings.HasPrefix(err.Error(), "commit a transaction: ") {
		t.Errorf("InTx() error = %v, want the commit's", err)
	}
	if got := countBuilds(t, s); got != 0 {
		t.Errorf("builds = %d, want 0", got)
	}
}

func TestClosedStoreFails(t *testing.T) {
	s := newStore(t)
	s.Close()
	err := s.InTx(t.Context(), func(context.Context) error { return nil })
	if err == nil || !strings.HasPrefix(err.Error(), "begin a transaction: ") {
		t.Errorf("InTx() error = %v, want the begin's", err)
	}
	err = s.Migrate(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "migrate the store: ") {
		t.Errorf("Migrate() error = %v, want the migration's", err)
	}
}

func TestOpenFails(t *testing.T) {
	urls := map[string]string{
		"unreachable": "postgres://crew@127.0.0.1:1/crew?sslmode=disable&connect_timeout=5",
		"malformed":   "postgres://crew@127.0.0.1:port/crew",
	}
	for name, u := range urls {
		t.Run(name, func(t *testing.T) {
			s, err := Open(t.Context(), u)
			if err == nil {
				s.Close()
				t.Fatal("Open() error = nil, want an error")
			}
			if s != nil {
				t.Errorf("Open() store = %v, want nil", s)
			}
			if !strings.HasPrefix(err.Error(), "open the store: ") {
				t.Errorf("Open() error = %q, want it to say what failed", err)
			}
		})
	}
}
