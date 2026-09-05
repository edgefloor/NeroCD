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

var _ ServiceAccountRepository = (*PostgresStore)(nil)

// ListServiceAccounts implements bounded project-scoped account listing.
func (s *PostgresStore) ListServiceAccounts(ctx context.Context, projectID string, page Page) (PageResult[domain.ServiceAccount], error) {
	total64, err := s.queries.CountServiceAccounts(ctx, projectID)
	if err != nil {
		return PageResult[domain.ServiceAccount]{}, fmt.Errorf("count service accounts: %w", err)
	}
	limit, offset := resolvePage(page, int(total64))
	rows, err := s.queries.ListServiceAccountsPage(ctx, sqlcgen.ListServiceAccountsPageParams{ProjectID: projectID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return PageResult[domain.ServiceAccount]{}, fmt.Errorf("list service accounts: %w", err)
	}
	items := make([]domain.ServiceAccount, 0, len(rows))
	for _, row := range rows {
		items = append(items, serviceAccountFromSQLC(row))
	}
	return PageResult[domain.ServiceAccount]{Items: items, Limit: limit, Offset: offset, Total: int(total64)}, nil
}

// GetManagedServiceAccountCredentialByHash authenticates a managed credential
// only while its token, account, and human owner are active.
func (s *PostgresStore) GetManagedServiceAccountCredentialByHash(ctx context.Context, tokenHash string, now time.Time) (domain.ManagedServiceAccountCredential, error) {
	row, err := s.queries.GetManagedServiceAccountCredentialByHash(ctx, sqlcgen.GetManagedServiceAccountCredentialByHashParams{TokenHash: tokenHash, ExpiresAt: &now})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ManagedServiceAccountCredential{}, ErrNotFound
	}
	if err != nil {
		return domain.ManagedServiceAccountCredential{}, fmt.Errorf("get managed service account credential: %w", err)
	}
	account := domain.ServiceAccount{ID: row.AccountID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Name: row.AccountName, Role: row.AccountRole, Status: row.AccountStatus, CreatedAt: row.AccountCreatedAt, UpdatedAt: row.AccountUpdatedAt, RevokedAt: row.AccountRevokedAt}
	token := domain.APIToken{ID: row.ID, Name: row.Name, Kind: row.Kind, TokenHash: row.TokenHash, Roles: append([]string(nil), row.Roles...), Status: row.Status, CreatedBy: row.CreatedBy, ServiceAccountID: row.ServiceAccountID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt}
	return domain.ManagedServiceAccountCredential{ServiceAccount: account, APIToken: token}, nil
}

// ServiceAccountProjectRole returns the current scope only for an active
// account with an active human owner.
func (s *PostgresStore) ServiceAccountProjectRole(ctx context.Context, accountID string) (string, string, error) {
	row, err := s.queries.ServiceAccountProjectRole(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("get service account project role: %w", err)
	}
	return row.ProjectID, row.Role, nil
}

// CreateServiceAccount atomically creates the principal, first credential,
// and audit event after serializing against owner offboarding.
func (s *PostgresStore) CreateServiceAccount(ctx context.Context, account domain.ServiceAccount, token domain.APIToken, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("begin service account create: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if _, err = q.LockIdentityAdministration(ctx); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("lock identity administration: %w", err)
	}
	if _, err = q.LockActiveUser(ctx, account.OwnerUserID); errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceAccount{}, ErrNotFound
	} else if err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("lock service account owner: %w", err)
	}
	row, err := q.CreateServiceAccount(ctx, sqlcgen.CreateServiceAccountParams{ID: account.ID, ProjectID: account.ProjectID, OwnerUserID: account.OwnerUserID, Name: account.Name, Role: account.Role, CreatedAt: account.CreatedAt})
	if err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("create service account: %w", err)
	}
	accountID := account.ID
	if _, err = q.InsertManagedServiceAccountCredential(ctx, sqlcgen.InsertManagedServiceAccountCredentialParams{ID: token.ID, Name: account.Name, TokenHash: token.TokenHash, CreatedBy: account.OwnerUserID, ServiceAccountID: &accountID, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt}); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("create managed credential: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("create service account audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("commit service account create: %w", err)
	}
	return serviceAccountFromSQLC(row), nil
}

// UpdateServiceAccount updates mutable metadata with the same lock order used
// by rotation, revocation, and offboarding.
func (s *PostgresStore) UpdateServiceAccount(ctx context.Context, accountID, name, role string, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	tx, q, _, err := s.lockActiveServiceAccount(ctx, accountID, "update")
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	defer rollbackTransaction(ctx, tx)
	row, err := q.UpdateServiceAccount(ctx, sqlcgen.UpdateServiceAccountParams{ID: accountID, Name: name, Role: role, UpdatedAt: audit.CreatedAt})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("update service account: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("create service account update audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("commit service account update: %w", err)
	}
	return serviceAccountFromSQLC(row), nil
}

// RotateServiceAccountCredential atomically revokes every old active token and
// issues one replacement while preserving the stable account ID.
func (s *PostgresStore) RotateServiceAccountCredential(ctx context.Context, accountID string, token domain.APIToken, revokedAt time.Time, audit domain.AuditEvent) (domain.APIToken, error) {
	tx, q, account, err := s.lockActiveServiceAccount(ctx, accountID, "rotation")
	if err != nil {
		return domain.APIToken{}, err
	}
	defer rollbackTransaction(ctx, tx)
	accountIDCopy := account.ID
	if _, err = q.RevokeActiveServiceAccountCredentials(ctx, sqlcgen.RevokeActiveServiceAccountCredentialsParams{ServiceAccountID: &accountIDCopy, RevokedAt: &revokedAt}); err != nil {
		return domain.APIToken{}, fmt.Errorf("revoke prior managed credentials: %w", err)
	}
	row, err := q.InsertManagedServiceAccountCredential(ctx, sqlcgen.InsertManagedServiceAccountCredentialParams{ID: token.ID, Name: account.Name, TokenHash: token.TokenHash, CreatedBy: account.OwnerUserID, ServiceAccountID: &accountIDCopy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
	if err != nil {
		return domain.APIToken{}, fmt.Errorf("insert rotated managed credential: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.APIToken{}, fmt.Errorf("create service account rotation audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.APIToken{}, fmt.Errorf("commit service account rotation: %w", err)
	}
	return apiTokenFromSQLC(row), nil
}

// RevokeServiceAccount atomically disables the identity and credentials.
func (s *PostgresStore) RevokeServiceAccount(ctx context.Context, accountID string, revokedAt time.Time, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	tx, q, _, err := s.lockActiveServiceAccount(ctx, accountID, "revocation")
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	defer rollbackTransaction(ctx, tx)
	accountIDCopy := accountID
	if _, err = q.RevokeActiveServiceAccountCredentials(ctx, sqlcgen.RevokeActiveServiceAccountCredentialsParams{ServiceAccountID: &accountIDCopy, RevokedAt: &revokedAt}); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("revoke managed credentials: %w", err)
	}
	row, err := q.RevokeServiceAccount(ctx, sqlcgen.RevokeServiceAccountParams{ID: accountID, RevokedAt: &revokedAt})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("revoke service account: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("create service account revocation audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ServiceAccount{}, fmt.Errorf("commit service account revocation: %w", err)
	}
	return serviceAccountFromSQLC(row), nil
}

func (s *PostgresStore) lockActiveServiceAccount(ctx context.Context, accountID, operation string) (pgx.Tx, *sqlcgen.Queries, sqlcgen.ServiceAccount, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, sqlcgen.ServiceAccount{}, fmt.Errorf("begin service account %s: %w", operation, err)
	}
	q := s.queries.WithTx(tx)
	if _, err = q.LockIdentityAdministration(ctx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, sqlcgen.ServiceAccount{}, fmt.Errorf("lock identity administration: %w", err)
	}
	account, err := q.LockActiveServiceAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, nil, sqlcgen.ServiceAccount{}, ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, sqlcgen.ServiceAccount{}, fmt.Errorf("lock service account for %s: %w", operation, err)
	}
	return tx, q, account, nil
}
