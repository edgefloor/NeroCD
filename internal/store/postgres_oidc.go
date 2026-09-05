package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// CreateOIDCLoginFlow serializes the bounded cleanup/count/insert admission
// decision so concurrent anonymous starts cannot exceed the durable limit.
func (s *PostgresStore) CreateOIDCLoginFlow(ctx context.Context, flow domain.OIDCLoginFlow, now time.Time, activeLimit int) error {
	if activeLimit <= 0 {
		return ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin oidc flow transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if err = q.LockOIDCLoginFlows(ctx); err != nil {
		return fmt.Errorf("lock oidc flows: %w", err)
	}
	if _, err = q.DeleteExpiredOIDCLoginFlows(ctx, sqlcgen.DeleteExpiredOIDCLoginFlowsParams{ExpiresAt: now, Limit: oidcExpiredCleanupBatch}); err != nil {
		return fmt.Errorf("clean expired oidc flows: %w", err)
	}
	count, err := q.CountActiveOIDCLoginFlows(ctx, now)
	if err != nil {
		return fmt.Errorf("count active oidc flows: %w", err)
	}
	if count >= int64(activeLimit) {
		return ErrConflict
	}
	err = q.CreateOIDCLoginFlow(ctx, sqlcgen.CreateOIDCLoginFlowParams{ID: flow.ID, StateHash: flow.StateHash, NonceHash: flow.NonceHash, VerifierHash: flow.VerifierHash, RedirectPath: flow.RedirectPath, Issuer: flow.Issuer, ClientID: flow.ClientID, ExpiresAt: flow.ExpiresAt, CreatedAt: flow.CreatedAt})
	if err != nil {
		if constraintConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("create oidc flow: %w", err)
	}
	return tx.Commit(ctx)
}

// ConsumeOIDCLoginFlow implements the corresponding repository operation.
func (s *PostgresStore) ConsumeOIDCLoginFlow(ctx context.Context, stateHash, verifierHash, issuer, clientID string, now time.Time) (domain.OIDCLoginFlow, error) {
	row, err := s.queries.ConsumeOIDCLoginFlow(ctx, sqlcgen.ConsumeOIDCLoginFlowParams{StateHash: stateHash, VerifierHash: verifierHash, Issuer: issuer, ClientID: clientID, ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OIDCLoginFlow{}, ErrNotFound
	}
	if err != nil {
		return domain.OIDCLoginFlow{}, fmt.Errorf("consume oidc flow: %w", err)
	}
	return domain.OIDCLoginFlow{ID: row.ID, StateHash: row.StateHash, NonceHash: row.NonceHash, VerifierHash: row.VerifierHash, RedirectPath: row.RedirectPath, Issuer: row.Issuer, ClientID: row.ClientID, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt}, nil
}

// ProvisionOIDCUser implements the corresponding repository operation.
func (s *PostgresStore) ProvisionOIDCUser(ctx context.Context, user domain.User, identity domain.OIDCExternalIdentity, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin oidc provision transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if err = q.CreateOIDCUser(ctx, sqlcgen.CreateOIDCUserParams{ID: user.ID, Email: user.Email, Name: user.Name, Status: user.Status, GlobalRole: user.GlobalRole, PasswordHash: user.PasswordHash, CreatedAt: user.CreatedAt}); err != nil {
		if constraintConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("create oidc user: %w", err)
	}
	if err = q.CreateOIDCIdentity(ctx, sqlcgen.CreateOIDCIdentityParams{Issuer: identity.Issuer, Subject: identity.Subject, UserID: identity.UserID, CreatedAt: identity.CreatedAt}); err != nil {
		if constraintConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("create oidc identity: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return fmt.Errorf("create oidc provision audit: %w", err)
	}
	return tx.Commit(ctx)
}

// BindOIDCIdentity implements the corresponding repository operation.
func (s *PostgresStore) BindOIDCIdentity(ctx context.Context, identity domain.OIDCExternalIdentity, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin oidc bind transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if err = q.CreateOIDCIdentity(ctx, sqlcgen.CreateOIDCIdentityParams{Issuer: identity.Issuer, Subject: identity.Subject, UserID: identity.UserID, CreatedAt: identity.CreatedAt}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrNotFound
		}
		if constraintConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("create oidc identity: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return fmt.Errorf("create oidc bind audit: %w", err)
	}
	return tx.Commit(ctx)
}

// CreateOIDCSession resolves the explicit binding, rechecks local user status,
// and commits the ordinary session and audit as one database transaction.
func (s *PostgresStore) CreateOIDCSession(ctx context.Context, issuer, subject string, session domain.Session, tokenHash string, audit domain.AuditEvent) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin oidc session transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	row, err := q.GetOIDCUser(ctx, sqlcgen.GetOIDCUserParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("resolve oidc identity: %w", err)
	}
	user := userFromSQLC(row)
	if user.Status != domain.UserActive {
		return user, ErrNotFound
	}
	session.UserID = user.ID
	audit.ActorID = user.ID
	if err = q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, SourceIp: session.SourceIP, UserAgent: session.UserAgent, LastSeenAt: session.LastSeenAt}); err != nil {
		if constraintConflict(err) {
			return domain.User{}, ErrConflict
		}
		return domain.User{}, fmt.Errorf("create oidc session: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.User{}, fmt.Errorf("create oidc session audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit oidc session: %w", err)
	}
	return user, nil
}

func constraintConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514")
}
