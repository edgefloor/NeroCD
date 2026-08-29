package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// PostgresStore is a PostgreSQL-backed implementation of the store repositories.
type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

// rollbackTransaction performs best-effort failure-path cleanup. It ignores all
// rollback errors so deferred cleanup cannot mask the operation's primary result.
func rollbackTransaction(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}

var (
	_ ProjectRepository             = (*PostgresStore)(nil)
	_ ProjectMemberRepository       = (*PostgresStore)(nil)
	_ TemplateRepository            = (*PostgresStore)(nil)
	_ SourceRepository              = (*PostgresStore)(nil)
	_ RunRepository                 = (*PostgresStore)(nil)
	_ RunnerRepository              = (*PostgresStore)(nil)
	_ UserRepository                = (*PostgresStore)(nil)
	_ SessionRepository             = (*PostgresStore)(nil)
	_ APITokenRepository            = (*PostgresStore)(nil)
	_ ApprovalRepository            = (*PostgresStore)(nil)
	_ AuditRepository               = (*PostgresStore)(nil)
	_ DeploymentRepository          = (*PostgresStore)(nil)
	_ OperationalSnapshotRepository = (*PostgresStore)(nil)
	_ OperationalObservationWriter  = (*PostgresStore)(nil)
	_ RunLogRetentionRepository     = (*PostgresStore)(nil)
)

// A claim or periodic reaper transaction may lock at most this many expired
// task runs. Keeping maintenance bounded prevents a large expired fleet from
// turning one runner claim into a global lock/materialization operation.
const leaseExpiryBatchSize = 64

// OpenPostgres opens a PostgreSQL store after verifying schema compatibility.
func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 0
	config.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	queries := sqlcgen.New(pool)
	compatible, err := queries.SchedulerSchemaCompatible(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("check schema compatibility: %w", err)
	}
	if compatible == nil || !*compatible {
		pool.Close()
		return nil, fmt.Errorf("database schema is incompatible: apply fenced lease, claim cursor, runner replay, and runner enrollment migrations before starting NeroCD")
	}
	return &PostgresStore{pool: pool, queries: queries}, nil
}

// SchemaCompatible is intentionally called for every readiness probe so an
// old or partially-applied schema can never remain ready after startup.
// SchemaCompatible implements the corresponding repository operation.
func (s *PostgresStore) SchemaCompatible(ctx context.Context) (bool, error) {
	compatible, err := s.queries.SchedulerSchemaCompatible(ctx)
	if err != nil {
		return false, fmt.Errorf("check schema compatibility: %w", err)
	}
	return compatible != nil && *compatible, nil
}

func maxSnapshotAge(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

// Generic task-run mutations must never alter the run that a deployment owns.
// Deployment transitions are the sole authority for that lifecycle; callers
// that need to act on it must supply the exact runner/lease/attempt/fence.
func rejectGenericDeploymentRun(ctx context.Context, queries *sqlcgen.Queries, runID string) error {
	isDeployment, err := queries.IsDeploymentRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("is deployment run query: %w", err)
	}
	if isDeployment {
		return ErrConflict
	}
	return nil
}

// Close implements the corresponding repository operation.
func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func createAuditWithQueries(ctx context.Context, queries *sqlcgen.Queries, audit domain.AuditEvent) error {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	return queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func decodeWorkflowState(raw []byte, workflowState *domain.WorkflowState) error {
	if len(raw) == 0 {
		*workflowState = domain.WorkflowState{}
		return nil
	}
	return json.Unmarshal(raw, workflowState)
}

func decodeRunSpec(raw []byte, runSpec *domain.RunSpec) error {
	if len(raw) == 0 {
		*runSpec = domain.RunSpec{Inputs: map[string]any{}}
		return nil
	}
	if err := json.Unmarshal(raw, runSpec); err != nil {
		return fmt.Errorf("decode run spec: %w", err)
	}
	if runSpec.Inputs == nil {
		runSpec.Inputs = map[string]any{}
	}
	return nil
}

func decodeWorkflow(raw []byte, workflow *domain.Workflow) error {
	if len(raw) == 0 {
		*workflow = domain.Workflow{}
		return nil
	}
	return json.Unmarshal(raw, workflow)
}

func resolvePage(page Page, total int) (int, int) {
	limit := total
	offset := 0
	if page.Enabled {
		limit = page.Limit
		offset = page.Offset
	}
	if page.Enabled && limit == 0 {
		limit = total
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > MaxPageOffset {
		offset = MaxPageOffset
	}
	if offset > total {
		offset = total
	}
	return limit, offset
}
