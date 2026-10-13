package postgres

import (
	"context"
	"fmt"
)

// DatabaseID returns the database's id: a random UUID the second migration
// stores once, so a database made again gets another. The engine names its
// log directory after it.
func (s *Store) DatabaseID(ctx context.Context) (string, error) {
	var id string
	err := s.InTx(ctx, func(ctx context.Context) error {
		var err error
		id, err = s.q.DatabaseID(ctx, txFrom(ctx))
		if err != nil {
			return fmt.Errorf("read the database id: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
