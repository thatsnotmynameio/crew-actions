// Package postgres is the store: builds, their workflows, each workflow's
// attempts and each attempt's steps, in PostgreSQL.
//
// A transaction travels in the context. InTx runs a function with one in
// its context, and every primitive joins the context's transaction when
// there is one, and otherwise runs in its own, so each primitive is atomic
// alone and composes with others under InTx. No signature uses a pgx type:
// primitives take and return internal/model's types and errors.
//
// Only this package imports pgx, goose and the queries sqlc generates in
// queries/ (sqlc.yaml).
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/queries"
)

// Store is the Postgres store. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
	// q runs the generated queries on the connection or the transaction
	// each call names.
	q *queries.Queries
}

// Open connects to the database at url and returns the store on it. It
// returns no store when the database cannot be reached.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open the store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open the store: %w", err)
	}
	return &Store{pool: pool, q: queries.New()}, nil
}

// Close closes the store's connections, waiting for those in use.
func (s *Store) Close() {
	s.pool.Close()
}

// txKey is the context key of the transaction InTx runs its function in.
type txKey struct{}

// InTx runs fn with a transaction in its context, and commits it when fn
// returns nil. An error from fn rolls every write back, and InTx returns
// it as fn returned it. When ctx already carries a transaction, fn runs in
// it, and the outermost InTx commits or rolls back.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if txFrom(ctx) != nil {
		return fn(ctx)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin a transaction: %w", err)
	}
	// After a commit, the rollback does nothing.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit a transaction: %w", err)
	}
	return nil
}

// txFrom returns the transaction ctx carries, or nil. A primitive calls it
// inside InTx, which puts one there.
func txFrom(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value(txKey{}).(pgx.Tx)
	return tx
}
