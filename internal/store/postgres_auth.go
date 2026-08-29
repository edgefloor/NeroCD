package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	user, err := s.queries.GetUserByEmail(ctx, email)
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user by email query: %w", err)
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) UpdatePasswordHash(ctx context.Context, userID, previousHash, passwordHash string) error {
	updated, err := s.queries.UpdatePasswordHash(ctx, sqlcgen.UpdatePasswordHashParams{ID: userID, PasswordHash: passwordHash, PasswordHash_2: previousHash})
	if err != nil {
		return fmt.Errorf("update password hash query: %w", err)
	}
	if updated != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) BootstrapAdmin(ctx context.Context, user domain.User, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	completedBy := user.ID
	completedAt := user.CreatedAt
	claimed, err := q.ClaimBootstrapAdmin(ctx, sqlcgen.ClaimBootstrapAdminParams{CompletedBy: &completedBy, CompletedAt: &completedAt})
	if err != nil {
		return fmt.Errorf("claim bootstrap admin query: %w", err)
	}
	if claimed != 1 {
		denied := domain.AuditEvent{ID: audit.ID + "-denied", ActorID: "system", Action: "identity.bootstrap_admin.denied", TargetID: "bootstrap-admin", Metadata: map[string]any{"reason": "already_completed"}, CreatedAt: user.CreatedAt}
		if err = createAuditWithQueries(ctx, q, denied); err != nil {
			return fmt.Errorf("create audit event: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
		return ErrConflict
	}
	if err = q.CreateBootstrapUser(ctx, sqlcgen.CreateBootstrapUserParams{ID: user.ID, Email: user.Email, Name: user.Name, Status: user.Status, GlobalRole: user.GlobalRole, PasswordHash: user.PasswordHash, CreatedAt: user.CreatedAt}); err != nil {
		return fmt.Errorf("create bootstrap user query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return fmt.Errorf("create audit event: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) BootstrapComplete(ctx context.Context) (bool, error) {
	completed, err := s.queries.BootstrapComplete(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return completed, err
}

func (s *PostgresStore) CreateSession(ctx context.Context, session domain.Session, tokenHash string, opts ...MutationOption) error {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		return s.queries.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, SourceIp: session.SourceIP, UserAgent: session.UserAgent, LastSeenAt: session.LastSeenAt})
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	if err = q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, SourceIp: session.SourceIP, UserAgent: session.UserAgent, LastSeenAt: session.LastSeenAt}); err != nil {
		return fmt.Errorf("create session query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return fmt.Errorf("create audit event: %w", err)
	}
	return tx.Commit(ctx)
}

func sessionFromFields(id, userID string, expiresAt, createdAt time.Time, sourceIP, userAgent string, lastSeenAt, revokedAt *time.Time) domain.Session {
	return domain.Session{ID: id, UserID: userID, ExpiresAt: expiresAt, CreatedAt: createdAt, SourceIP: sourceIP, UserAgent: userAgent, LastSeenAt: lastSeenAt, RevokedAt: revokedAt}
}

func (s *PostgresStore) ListSessions(ctx context.Context) ([]domain.Session, error) {
	rows, err := s.queries.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions query: %w", err)
	}
	result := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		result = append(result, sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt))
	}
	return result, nil
}

func (s *PostgresStore) RevokeSessionByID(ctx context.Context, id string, revokedAt time.Time, opts ...MutationOption) (domain.Session, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		row, err := s.queries.RevokeSessionByID(ctx, sqlcgen.RevokeSessionByIDParams{ID: id, RevokedAt: &revokedAt})
		if err == pgx.ErrNoRows {
			return domain.Session{}, ErrNotFound
		}
		if err != nil {
			return domain.Session{}, fmt.Errorf("revoke session by id query: %w", err)
		}
		return sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Session{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RevokeSessionByID(ctx, sqlcgen.RevokeSessionByIDParams{ID: id, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("revoke session by id query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.Session{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Session{}, fmt.Errorf("commit transaction: %w", err)
	}
	return sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt), nil
}

func (s *PostgresStore) GetPrincipalBySessionTokenHash(ctx context.Context, tokenHash string, now time.Time) (domain.User, error) {
	threshold := now.Add(-SessionLastSeenUpdateInterval)
	user, err := s.queries.GetPrincipalBySessionTokenHash(ctx, sqlcgen.GetPrincipalBySessionTokenHashParams{TokenHash: tokenHash, ExpiresAt: now, LastSeenAt: &threshold})
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get principal by session token hash query: %w", err)
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) RevokeSessionByTokenHash(ctx context.Context, tokenHash string, revokedAt time.Time, opts ...MutationOption) error {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		count, err := s.queries.RevokeSessionByTokenHash(ctx, sqlcgen.RevokeSessionByTokenHashParams{TokenHash: tokenHash, RevokedAt: &revokedAt})
		if err != nil {
			return fmt.Errorf("revoke session by token hash query: %w", err)
		}
		if count == 0 {
			return ErrNotFound
		}
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	rows, err := q.RevokeSessionByTokenHash(ctx, sqlcgen.RevokeSessionByTokenHashParams{TokenHash: tokenHash, RevokedAt: &revokedAt})
	if err != nil {
		return fmt.Errorf("revoke session by token hash query: %w", err)
	}
	if rows != 1 {
		return ErrNotFound
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return fmt.Errorf("create audit event: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateAPIToken(ctx context.Context, token domain.APIToken, opts ...MutationOption) (domain.APIToken, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		inserted, err := s.queries.CreateAPIToken(ctx, sqlcgen.CreateAPITokenParams{ID: token.ID, Name: token.Name, Kind: token.Kind, TokenHash: token.TokenHash, Roles: token.Roles, Status: token.Status, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
		if err != nil {
			return domain.APIToken{}, fmt.Errorf("create api token query: %w", err)
		}
		return apiTokenFromSQLC(inserted), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateAPIToken(ctx, sqlcgen.CreateAPITokenParams{ID: token.ID, Name: token.Name, Kind: token.Kind, TokenHash: token.TokenHash, Roles: token.Roles, Status: token.Status, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("create api token query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.APIToken{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.APIToken{}, fmt.Errorf("commit transaction: %w", err)
	}
	return apiTokenFromSQLC(row), nil
}

func (s *PostgresStore) GetAPITokenByHash(ctx context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	token, err := s.queries.GetAPITokenByHash(ctx, sqlcgen.GetAPITokenByHashParams{TokenHash: tokenHash, LastUsedAt: &now})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("get api token by hash query: %w", err)
	}
	return apiTokenFromSQLC(token), nil
}

func (s *PostgresStore) RevokeAPIToken(ctx context.Context, tokenID string, revokedAt time.Time, opts ...MutationOption) (domain.APIToken, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		token, err := s.queries.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{ID: tokenID, RevokedAt: &revokedAt})
		if err == pgx.ErrNoRows {
			return domain.APIToken{}, ErrNotFound
		}
		if err != nil {
			return domain.APIToken{}, fmt.Errorf("revoke api token query: %w", err)
		}
		return apiTokenFromSQLC(token), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{ID: tokenID, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("revoke api token query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.APIToken{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.APIToken{}, fmt.Errorf("commit transaction: %w", err)
	}
	return apiTokenFromSQLC(row), nil
}
