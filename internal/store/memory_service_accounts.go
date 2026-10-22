package store

import (
	"context"
	"sort"
	"time"

	"nerocd/internal/domain"
)

// ListServiceAccounts implements bounded project-scoped account listing.
func (s *MemoryStore) ListServiceAccounts(ctx context.Context, projectID string, page Page) (PageResult[domain.ServiceAccount], error) {
	if err := ctx.Err(); err != nil {
		return PageResult[domain.ServiceAccount]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make([]domain.ServiceAccount, 0)
	for _, account := range s.serviceAccounts {
		if account.ProjectID == projectID {
			accounts = append(accounts, cloneServiceAccount(account))
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].CreatedAt.Equal(accounts[j].CreatedAt) {
			return accounts[i].ID > accounts[j].ID
		}
		return accounts[i].CreatedAt.After(accounts[j].CreatedAt)
	})
	return paginateSlice(accounts, page), nil
}

// GetManagedServiceAccountCredentialByHash implements managed credential authentication.
func (s *MemoryStore) GetManagedServiceAccountCredentialByHash(ctx context.Context, tokenHash string, now time.Time) (domain.ManagedServiceAccountCredential, error) {
	if err := ctx.Err(); err != nil {
		return domain.ManagedServiceAccountCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for tokenIndex, token := range s.apiTokens {
		if token.TokenHash != tokenHash || token.ServiceAccountID == nil || token.Status != domain.TokenActive || token.RevokedAt != nil || token.ExpiresAt == nil || !token.ExpiresAt.After(now) {
			continue
		}
		for _, account := range s.serviceAccounts {
			if account.ID != *token.ServiceAccountID || account.Status != domain.ServiceAccountActive {
				continue
			}
			if !s.activeUserLocked(account.OwnerUserID) {
				return domain.ManagedServiceAccountCredential{}, ErrNotFound
			}
			usedAt := now
			token.LastUsedAt = &usedAt
			s.apiTokens[tokenIndex] = token
			return domain.ManagedServiceAccountCredential{ServiceAccount: cloneServiceAccount(account), APIToken: cloneAPIToken(token)}, nil
		}
		return domain.ManagedServiceAccountCredential{}, ErrNotFound
	}
	return domain.ManagedServiceAccountCredential{}, ErrNotFound
}

// ServiceAccountProjectRole returns current scope for an active account and owner.
func (s *MemoryStore) ServiceAccountProjectRole(ctx context.Context, accountID string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.serviceAccounts {
		if account.ID == accountID && account.Status == domain.ServiceAccountActive && s.activeUserLocked(account.OwnerUserID) {
			return account.ProjectID, account.Role, nil
		}
	}
	return "", "", ErrNotFound
}

// CreateServiceAccount atomically creates account, credential, and audit state.
func (s *MemoryStore) CreateServiceAccount(ctx context.Context, account domain.ServiceAccount, token domain.APIToken, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	if err := ctx.Err(); err != nil {
		return domain.ServiceAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeUserLocked(account.OwnerUserID) || !s.projectExistsLocked(account.ProjectID) {
		return domain.ServiceAccount{}, ErrNotFound
	}
	if !s.auditIDAvailableLocked(audit.ID) || s.serviceAccountOrTokenExistsLocked(account.ID, token.ID, token.TokenHash) {
		return domain.ServiceAccount{}, ErrConflict
	}
	account = cloneServiceAccount(account)
	account.Status = domain.ServiceAccountActive
	account.RevokedAt = nil
	accountID := account.ID
	token.Name = account.Name
	token.Kind = domain.TokenKindServiceAccount
	token.Roles = []string{}
	token.Status = domain.TokenActive
	token.CreatedBy = account.OwnerUserID
	token.ServiceAccountID = &accountID
	s.serviceAccounts = append(s.serviceAccounts, account)
	s.apiTokens = append(s.apiTokens, cloneAPIToken(token))
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return cloneServiceAccount(account), nil
}

// UpdateServiceAccount atomically updates mutable metadata and audit state.
func (s *MemoryStore) UpdateServiceAccount(ctx context.Context, accountID, name, role string, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	if err := ctx.Err(); err != nil {
		return domain.ServiceAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.ServiceAccount{}, ErrConflict
	}
	for i := range s.serviceAccounts {
		account := s.serviceAccounts[i]
		if account.ID != accountID || account.Status != domain.ServiceAccountActive || !s.activeUserLocked(account.OwnerUserID) {
			continue
		}
		if name != "" {
			account.Name = name
		}
		if role != "" {
			account.Role = role
		}
		account.UpdatedAt = audit.CreatedAt
		s.serviceAccounts[i] = account
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return cloneServiceAccount(account), nil
	}
	return domain.ServiceAccount{}, ErrNotFound
}

// RotateServiceAccountCredential atomically replaces all active credentials.
func (s *MemoryStore) RotateServiceAccountCredential(ctx context.Context, accountID string, token domain.APIToken, revokedAt time.Time, audit domain.AuditEvent) (domain.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return domain.APIToken{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.activeServiceAccountLocked(accountID)
	if !ok {
		return domain.APIToken{}, ErrNotFound
	}
	if !s.auditIDAvailableLocked(audit.ID) || s.serviceAccountOrTokenExistsLocked("", token.ID, token.TokenHash) {
		return domain.APIToken{}, ErrConflict
	}
	for i := range s.apiTokens {
		if s.apiTokens[i].ServiceAccountID != nil && *s.apiTokens[i].ServiceAccountID == accountID && s.apiTokens[i].Status == domain.TokenActive && s.apiTokens[i].RevokedAt == nil {
			s.apiTokens[i].Status = domain.TokenRevoked
			s.apiTokens[i].RevokedAt = timePointer(revokedAt)
		}
	}
	accountIDCopy := account.ID
	token.Name = account.Name
	token.Kind = domain.TokenKindServiceAccount
	token.Roles = []string{}
	token.Status = domain.TokenActive
	token.CreatedBy = account.OwnerUserID
	token.ServiceAccountID = &accountIDCopy
	s.apiTokens = append(s.apiTokens, cloneAPIToken(token))
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return cloneAPIToken(token), nil
}

// RevokeServiceAccount atomically disables the account and all credentials.
func (s *MemoryStore) RevokeServiceAccount(ctx context.Context, accountID string, revokedAt time.Time, audit domain.AuditEvent) (domain.ServiceAccount, error) {
	if err := ctx.Err(); err != nil {
		return domain.ServiceAccount{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.ServiceAccount{}, ErrConflict
	}
	for i := range s.serviceAccounts {
		account := s.serviceAccounts[i]
		if account.ID != accountID || account.Status != domain.ServiceAccountActive || !s.activeUserLocked(account.OwnerUserID) {
			continue
		}
		for tokenIndex := range s.apiTokens {
			if s.apiTokens[tokenIndex].ServiceAccountID != nil && *s.apiTokens[tokenIndex].ServiceAccountID == accountID && s.apiTokens[tokenIndex].Status == domain.TokenActive && s.apiTokens[tokenIndex].RevokedAt == nil {
				s.apiTokens[tokenIndex].Status = domain.TokenRevoked
				s.apiTokens[tokenIndex].RevokedAt = timePointer(revokedAt)
			}
		}
		account.Status = domain.ServiceAccountRevoked
		account.UpdatedAt = revokedAt
		account.RevokedAt = timePointer(revokedAt)
		s.serviceAccounts[i] = account
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return cloneServiceAccount(account), nil
	}
	return domain.ServiceAccount{}, ErrNotFound
}

func (s *MemoryStore) activeUserLocked(userID string) bool {
	for _, user := range s.users {
		if user.ID == userID && user.Status == domain.UserActive {
			return true
		}
	}
	return false
}

func (s *MemoryStore) projectExistsLocked(projectID string) bool {
	for _, project := range s.projects {
		if project.ID == projectID && project.ArchivedAt == nil {
			return true
		}
	}
	return false
}

func (s *MemoryStore) activeServiceAccountLocked(accountID string) (domain.ServiceAccount, bool) {
	for _, account := range s.serviceAccounts {
		if account.ID == accountID && account.Status == domain.ServiceAccountActive && s.activeUserLocked(account.OwnerUserID) {
			return account, true
		}
	}
	return domain.ServiceAccount{}, false
}

func (s *MemoryStore) serviceAccountOrTokenExistsLocked(accountID, tokenID, tokenHash string) bool {
	if accountID != "" && s.hostingPrincipalExistsLocked(accountID) {
		return true
	}
	for _, account := range s.serviceAccounts {
		if accountID != "" && account.ID == accountID {
			return true
		}
	}
	for _, token := range s.apiTokens {
		if token.ID == tokenID || token.TokenHash == tokenHash {
			return true
		}
	}
	return false
}

func cloneServiceAccount(account domain.ServiceAccount) domain.ServiceAccount {
	if account.RevokedAt != nil {
		account.RevokedAt = timePointer(*account.RevokedAt)
	}
	return account
}

func cloneAPIToken(token domain.APIToken) domain.APIToken {
	token.Roles = append([]string(nil), token.Roles...)
	if token.ServiceAccountID != nil {
		value := *token.ServiceAccountID
		token.ServiceAccountID = &value
	}
	if token.ExpiresAt != nil {
		token.ExpiresAt = timePointer(*token.ExpiresAt)
	}
	if token.LastUsedAt != nil {
		token.LastUsedAt = timePointer(*token.LastUsedAt)
	}
	if token.RevokedAt != nil {
		token.RevokedAt = timePointer(*token.RevokedAt)
	}
	return token
}

func timePointer(value time.Time) *time.Time { return &value }
