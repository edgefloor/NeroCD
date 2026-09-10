package app

import (
	"context"
	"errors"
	"strings"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// OffboardUserInput identifies the local user whose access must be disabled.
type OffboardUserInput struct {
	UserID string `json:"user_id"`
}

// ListUsersPage lists safe local identity metadata for system administrators.
func (s *Service) ListUsersPage(ctx context.Context, page store.Page) (store.PageResult[domain.User], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.User]{}, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "identity.user.list.denied", "users", nil)
		return store.PageResult[domain.User]{}, auth.ErrForbidden
	}
	return s.users.ListUsersPage(ctx, page)
}

// OffboardUser atomically disables a user and revokes every credential issued
// by that user while preserving identity mappings and historical references.
func (s *Service) OffboardUser(ctx context.Context, input OffboardUserInput) (domain.UserOffboardingResult, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.UserOffboardingResult{}, err
	}
	userID := strings.TrimSpace(input.UserID)
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "identity.user.offboard.denied", userID, map[string]any{"reason": "forbidden"})
		return domain.UserOffboardingResult{}, auth.ErrForbidden
	}
	if userID == "" {
		return domain.UserOffboardingResult{}, InvalidInput(errors.New("user_id is required"))
	}
	now := s.clock().UTC()
	audit, err := s.auditEvent(ctx, principal.ID, "identity.user.offboard", userID, map[string]any{"credential_policy": "revoke_sessions_and_issuer_tokens"})
	if err != nil {
		return domain.UserOffboardingResult{}, err
	}
	audit.CreatedAt = now
	deniedAudit, err := s.auditEvent(ctx, principal.ID, "identity.user.offboard.denied", userID, nil)
	if err != nil {
		return domain.UserOffboardingResult{}, err
	}
	deniedAudit.CreatedAt = now
	return s.users.OffboardUser(ctx, store.UserOffboardingRequest{UserID: userID, RevokedAt: now, Audit: audit, DeniedAudit: deniedAudit})
}
