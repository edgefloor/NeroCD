package store

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/store/sqlcgen"
)

// AutomaticPolicyReadTestStore supplies only a caller-owned isolated test pool.
// It exposes no store internals and is unavailable outside test builds.
func AutomaticPolicyReadTestStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, queries: sqlcgen.New(pool)}
}
