package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// These narrow test-only bridges let external persistence fixtures exercise
// transaction composition without adding production API or arbitrary hooks.
func RequestHostingBuildDeploymentTxForTest(ctx context.Context, s *PostgresStore, tx pgx.Tx, id, resource, actor string, in domain.HostingBuildDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, bool, error) {
	return s.requestHostingBuildDeploymentTx(ctx, tx, id, resource, actor, in, audit)
}

func HostingRouteSwitchPlacementForTest(ctx context.Context, s *PostgresStore, resource string) (sqlcgen.ProxyResourceTargetRow, error) {
	return s.queries.ProxyResourceTarget(ctx, resource)
}

func RequestHostingRouteSwitchTxForTest(ctx context.Context, s *PostgresStore, tx pgx.Tx, placement sqlcgen.ProxyResourceTargetRow, resource, operation, actor string, in domain.HostingRouteSwitchInput, audit domain.AuditEvent) (domain.HostingProxyOperation, bool, error) {
	return s.requestHostingRouteSwitchTx(ctx, tx, placement, resource, operation, actor, in, audit)
}

// HostingOperationReaderForTest uses an independent traced pool in the exact
// existing fixture schema. No production hooks or transaction behavior change.
func HostingOperationReaderForTest(ctx context.Context, s *PostgresStore, tracer pgx.QueryTracer) (*PostgresStore, error) {
	config := s.pool.Config()
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool, queries: sqlcgen.New(pool)}, nil
}
