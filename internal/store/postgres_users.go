package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListUsersPage implements bounded administrative identity listing.
func (s *PostgresStore) ListUsersPage(ctx context.Context, page Page) (PageResult[domain.User], error) {
	total64, err := s.queries.CountUsers(ctx)
	if err != nil {
		return PageResult[domain.User]{}, fmt.Errorf("count users query: %w", err)
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListUsersPage(ctx, sqlcgen.ListUsersPageParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return PageResult[domain.User]{}, fmt.Errorf("list users query: %w", err)
	}
	users := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		users = append(users, userFromSQLC(row))
	}
	return PageResult[domain.User]{Items: users, Limit: limit, Offset: offset, Total: total}, nil
}

// OffboardUser serializes last-administrator decisions and commits identity,
// credential, and audit effects as one transaction.
func (s *PostgresStore) OffboardUser(ctx context.Context, request UserOffboardingRequest) (domain.UserOffboardingResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("begin user offboarding transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if _, err = q.LockIdentityAdministration(ctx); err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("lock identity administration: %w", err)
	}
	row, err := q.GetUserForUpdate(ctx, request.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserOffboardingResult{}, ErrNotFound
	}
	if err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("get user for offboarding: %w", err)
	}
	user := userFromSQLC(row)
	if user.Status == domain.UserDisabled {
		return domain.UserOffboardingResult{User: user}, nil
	}
	if user.Status != domain.UserActive {
		return domain.UserOffboardingResult{}, ErrConflict
	}
	if user.GlobalRole == domain.RoleSystemAdmin {
		activeAdmins, countErr := q.CountActiveSystemAdministrators(ctx)
		if countErr != nil {
			return domain.UserOffboardingResult{}, fmt.Errorf("count active system administrators: %w", countErr)
		}
		if activeAdmins <= 1 {
			request.DeniedAudit.Metadata = auditMetadata(request.DeniedAudit.Metadata, map[string]any{"reason": "last_active_system_administrator"})
			if err = createAuditWithQueries(ctx, q, request.DeniedAudit); err != nil {
				return domain.UserOffboardingResult{}, fmt.Errorf("create denied user offboarding audit: %w", err)
			}
			if err = tx.Commit(ctx); err != nil {
				return domain.UserOffboardingResult{}, fmt.Errorf("commit denied user offboarding audit: %w", err)
			}
			return domain.UserOffboardingResult{}, ErrLastAdministrator
		}
	}
	row, err = q.DisableUser(ctx, sqlcgen.DisableUserParams{ID: user.ID, UpdatedAt: request.RevokedAt})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserOffboardingResult{}, ErrConflict
	}
	if err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("disable user: %w", err)
	}
	revokedAt := request.RevokedAt
	revokedSessions, err := q.RevokeSessionsForUser(ctx, sqlcgen.RevokeSessionsForUserParams{UserID: user.ID, RevokedAt: &revokedAt})
	if err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("revoke user sessions: %w", err)
	}
	revokedTokens, err := q.RevokeAPITokensCreatedByUser(ctx, sqlcgen.RevokeAPITokensCreatedByUserParams{CreatedBy: user.ID, RevokedAt: &revokedAt})
	if err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("revoke user api tokens: %w", err)
	}
	request.Audit.Metadata = auditMetadata(request.Audit.Metadata, map[string]any{"revoked_sessions": revokedSessions, "revoked_api_tokens": revokedTokens})
	if err = createAuditWithQueries(ctx, q, request.Audit); err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("create user offboarding audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.UserOffboardingResult{}, fmt.Errorf("commit user offboarding: %w", err)
	}
	return domain.UserOffboardingResult{User: userFromSQLC(row), RevokedSessions: int(revokedSessions), RevokedAPITokens: int(revokedTokens)}, nil
}
