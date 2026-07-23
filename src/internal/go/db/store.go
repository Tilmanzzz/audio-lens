package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds the shared pgx connection pool used by all query methods.
type Store struct {
	Pool *pgxpool.Pool
}

// NewStore creates a connection pool from connStr and verifies it with a ping
// before returning.
func NewStore(ctx context.Context, connStr string) (*Store, error) {
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db unreachable: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() {
	s.Pool.Close()
}
