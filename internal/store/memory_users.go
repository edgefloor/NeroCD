package store

import (
	"context"
	"sort"

	"nerocd/internal/domain"
)

// ListUsersPage implements bounded administrative identity listing.
func (s *MemoryStore) ListUsersPage(ctx context.Context, page Page) (PageResult[domain.User], error) {
	if err := ctx.Err(); err != nil {
		return PageResult[domain.User]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := append([]domain.User(nil), s.users...)
	sort.Slice(users, func(i, j int) bool {
		if users[i].CreatedAt.Equal(users[j].CreatedAt) {
			return users[i].ID > users[j].ID
		}
		return users[i].CreatedAt.After(users[j].CreatedAt)
	})
	return paginateSlice(users, page), nil
}

// OffboardUser atomically disables one user, revokes credentials issued by
// that user, and appends the corresponding audit record.
func (s *MemoryStore) OffboardUser(ctx context.Context, request UserOffboardingRequest) (domain.UserOffboardingResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.UserOffboardingResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userIndex := -1
	for i := range s.users {
		if s.users[i].ID == request.UserID {
			userIndex = i
			break
		}
	}
	if userIndex < 0 {
		return domain.UserOffboardingResult{}, ErrNotFound
	}
	user := s.users[userIndex]
	if user.Status == domain.UserDisabled {
		return domain.UserOffboardingResult{User: user}, nil
	}
	if user.Status != domain.UserActive {
		return domain.UserOffboardingResult{}, ErrConflict
	}
	if user.GlobalRole == domain.RoleSystemAdmin {
		activeAdmins := 0
		for _, candidate := range s.users {
			if candidate.Status == domain.UserActive && candidate.GlobalRole == domain.RoleSystemAdmin {
				activeAdmins++
			}
		}
		if activeAdmins <= 1 {
			if !s.auditIDAvailableLocked(request.DeniedAudit.ID) {
				return domain.UserOffboardingResult{}, ErrConflict
			}
			request.DeniedAudit.Metadata = auditMetadata(request.DeniedAudit.Metadata, map[string]any{"reason": "last_active_system_administrator"})
			s.auditEvents = append([]domain.AuditEvent{request.DeniedAudit}, s.auditEvents...)
			return domain.UserOffboardingResult{}, ErrLastAdministrator
		}
	}
	if !s.auditIDAvailableLocked(request.Audit.ID) {
		return domain.UserOffboardingResult{}, ErrConflict
	}
	user.Status = domain.UserDisabled
	user.UpdatedAt = request.RevokedAt
	archivedAt := request.RevokedAt
	user.ArchivedAt = &archivedAt
	s.users[userIndex] = user
	revokedSessions := 0
	for i := range s.sessions {
		if s.sessions[i].UserID == user.ID && s.sessions[i].RevokedAt == nil {
			s.sessions[i].RevokedAt = &archivedAt
			revokedSessions++
		}
	}
	revokedTokens := 0
	for i := range s.apiTokens {
		if s.apiTokens[i].CreatedBy == user.ID && s.apiTokens[i].Status == domain.TokenActive && s.apiTokens[i].RevokedAt == nil {
			s.apiTokens[i].Status = domain.TokenRevoked
			s.apiTokens[i].RevokedAt = &archivedAt
			revokedTokens++
		}
	}
	revokedServiceAccounts := 0
	for i := range s.serviceAccounts {
		if s.serviceAccounts[i].OwnerUserID == user.ID && s.serviceAccounts[i].Status == domain.ServiceAccountActive {
			s.serviceAccounts[i].Status = domain.ServiceAccountRevoked
			s.serviceAccounts[i].UpdatedAt = archivedAt
			s.serviceAccounts[i].RevokedAt = &archivedAt
			revokedServiceAccounts++
		}
	}
	request.Audit.Metadata = auditMetadata(request.Audit.Metadata, map[string]any{"revoked_sessions": revokedSessions, "revoked_api_tokens": revokedTokens, "revoked_service_accounts": revokedServiceAccounts})
	s.auditEvents = append([]domain.AuditEvent{request.Audit}, s.auditEvents...)
	return domain.UserOffboardingResult{User: user, RevokedSessions: revokedSessions, RevokedAPITokens: revokedTokens, RevokedServiceAccounts: revokedServiceAccounts}, nil
}
