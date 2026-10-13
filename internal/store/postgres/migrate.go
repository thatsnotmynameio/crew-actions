package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// migrations holds the schema's migrations, numbered 00001_… for goose. A
// merged migration is never edited: goose tracks versions, not contents.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies the migrations the database lacks. It holds goose's
// Postgres advisory lock while it does, so several engines can start at
// once and the schema is applied once.
func (s *Store) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(s.pool)
	defer func() { _ = db.Close() }()
	p, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate the store: %w", err)
	}
	return nil
}

// newProvider returns a goose provider of the embedded migrations on db,
// which migrates under goose's Postgres session lock.
func newProvider(db *sql.DB) (*goose.Provider, error) {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("make the migrations' lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, dir, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("prepare the migrations: %w", err)
	}
	return p, nil
}
