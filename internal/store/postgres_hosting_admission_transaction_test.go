package store

import (
	"context"

	"github.com/jackc/pgx/v5"
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
