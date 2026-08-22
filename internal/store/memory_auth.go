package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

func (s *MemoryStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.users {
		if user.Email == email {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) UpdatePasswordHash(_ context.Context, userID, previousHash, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID == userID && s.users[i].PasswordHash == previousHash {
			s.users[i].PasswordHash = passwordHash
			return nil
		}
	}
	return ErrConflict
}

func (s *MemoryStore) BootstrapAdmin(_ context.Context, user domain.User, audit domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) != 0 {
		s.auditEvents = append([]domain.AuditEvent{{ID: audit.ID + "-denied", ActorID: "system", Action: "identity.bootstrap_admin.denied", TargetID: "bootstrap-admin", Metadata: map[string]any{"reason": "already_completed"}, CreatedAt: audit.CreatedAt}}, s.auditEvents...)
		return ErrConflict
	}
	s.users = append(s.users, user)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return nil
}

// BootstrapComplete intentionally exposes only the one fixed lifecycle bit.
// It is safe to use before authentication and never reveals an administrator
// identity, address, timestamp, or migration detail.
func (s *MemoryStore) BootstrapComplete(_ context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) != 0, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, session domain.Session, tokenHash string, opts ...MutationOption) error {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, session)
	s.tokenHashBySessionID[session.ID] = tokenHash
	if audit != nil {
		s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
	}
	return nil
}

func (s *MemoryStore) GetPrincipalBySessionTokenHash(_ context.Context, tokenHash string, now time.Time) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] != tokenHash {
			continue
		}
		if session.RevokedAt != nil || !session.ExpiresAt.After(now) {
			return domain.User{}, ErrNotFound
		}
		threshold := now.Add(-SessionLastSeenUpdateInterval)
		if session.LastSeenAt == nil || !session.LastSeenAt.After(threshold) {
			seen := now
			session.LastSeenAt = &seen
			s.sessions[i] = session
		}
		for _, user := range s.users {
			if user.ID == session.UserID && user.Status == domain.UserActive {
				return user, nil
			}
		}
		return domain.User{}, ErrNotFound
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) RevokeSessionByTokenHash(_ context.Context, tokenHash string, revokedAt time.Time, opts ...MutationOption) error {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] == tokenHash && session.RevokedAt == nil {
			session.RevokedAt = &revokedAt
			s.sessions[i] = session
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) ListSessions(_ context.Context) ([]domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := append([]domain.Session(nil), s.sessions...)
	for i := range result {
		if result[i].LastSeenAt != nil {
			value := *result[i].LastSeenAt
			result[i].LastSeenAt = &value
		}
		if result[i].RevokedAt != nil {
			value := *result[i].RevokedAt
			result[i].RevokedAt = &value
		}
	}
	return result, nil
}

func (s *MemoryStore) RevokeSessionByID(_ context.Context, id string, revokedAt time.Time, opts ...MutationOption) (domain.Session, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if session.ID == id && session.RevokedAt == nil {
			session.RevokedAt = &revokedAt
			s.sessions[i] = session
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return session, nil
		}
	}
	return domain.Session{}, ErrNotFound
}

func (s *MemoryStore) CreateAPIToken(_ context.Context, token domain.APIToken, opts ...MutationOption) (domain.APIToken, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiTokens = append(s.apiTokens, token)
	if audit != nil {
		s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
	}
	return token, nil
}

func (s *MemoryStore) GetAPITokenByHash(_ context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.TokenHash != tokenHash || token.Status != domain.TokenActive || token.RevokedAt != nil {
			continue
		}
		if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
			return domain.APIToken{}, ErrNotFound
		}
		token.LastUsedAt = &now
		s.apiTokens[i] = token
		return token, nil
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) RevokeAPIToken(_ context.Context, tokenID string, revokedAt time.Time, opts ...MutationOption) (domain.APIToken, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.ID == tokenID && token.RevokedAt == nil {
			token.Status, token.RevokedAt = domain.TokenRevoked, &revokedAt
			s.apiTokens[i] = token
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return token, nil
		}
	}
	return domain.APIToken{}, ErrNotFound
}
