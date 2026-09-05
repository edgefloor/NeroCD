package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

const oidcExpiredCleanupBatch = 256

// CreateOIDCLoginFlow implements the corresponding repository operation.
func (s *MemoryStore) CreateOIDCLoginFlow(ctx context.Context, flow domain.OIDCLoginFlow, now time.Time, activeLimit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.oidcLoginFlows[:0]
	removed := 0
	for _, existing := range s.oidcLoginFlows {
		if removed < oidcExpiredCleanupBatch && !existing.ExpiresAt.After(now) {
			removed++
			continue
		}
		kept = append(kept, existing)
	}
	s.oidcLoginFlows = kept
	active := 0
	for _, existing := range s.oidcLoginFlows {
		if existing.ID == flow.ID || existing.StateHash == flow.StateHash {
			return ErrConflict
		}
		if existing.ExpiresAt.After(now) {
			active++
		}
	}
	if activeLimit <= 0 || active >= activeLimit {
		return ErrConflict
	}
	s.oidcLoginFlows = append(s.oidcLoginFlows, flow)
	return nil
}

// ConsumeOIDCLoginFlow implements the corresponding repository operation.
func (s *MemoryStore) ConsumeOIDCLoginFlow(ctx context.Context, stateHash, verifierHash, issuer, clientID string, now time.Time) (domain.OIDCLoginFlow, error) {
	if err := ctx.Err(); err != nil {
		return domain.OIDCLoginFlow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, flow := range s.oidcLoginFlows {
		if flow.StateHash != stateHash || flow.VerifierHash != verifierHash || flow.Issuer != issuer || flow.ClientID != clientID || !flow.ExpiresAt.After(now) {
			continue
		}
		s.oidcLoginFlows = append(s.oidcLoginFlows[:i], s.oidcLoginFlows[i+1:]...)
		return flow, nil
	}
	return domain.OIDCLoginFlow{}, ErrNotFound
}

// ProvisionOIDCUser implements the corresponding repository operation.
func (s *MemoryStore) ProvisionOIDCUser(ctx context.Context, user domain.User, identity domain.OIDCExternalIdentity, audit domain.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return ErrConflict
	}
	for _, existing := range s.users {
		if existing.ID == user.ID || existing.Email == user.Email {
			return ErrConflict
		}
	}
	if s.oidcIdentityConflictLocked(identity) {
		return ErrConflict
	}
	s.users = append(s.users, user)
	s.oidcIdentities = append(s.oidcIdentities, identity)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return nil
}

// BindOIDCIdentity implements the corresponding repository operation.
func (s *MemoryStore) BindOIDCIdentity(ctx context.Context, identity domain.OIDCExternalIdentity, audit domain.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) || s.oidcIdentityConflictLocked(identity) {
		return ErrConflict
	}
	found := false
	for _, user := range s.users {
		if user.ID == identity.UserID {
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	s.oidcIdentities = append(s.oidcIdentities, identity)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return nil
}

func (s *MemoryStore) oidcIdentityConflictLocked(identity domain.OIDCExternalIdentity) bool {
	for _, existing := range s.oidcIdentities {
		if existing.Issuer == identity.Issuer && (existing.Subject == identity.Subject || existing.UserID == identity.UserID) {
			return true
		}
	}
	return false
}

// CreateOIDCSession implements the corresponding repository operation.
func (s *MemoryStore) CreateOIDCSession(ctx context.Context, issuer, subject string, session domain.Session, tokenHash string, audit domain.AuditEvent) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userID := ""
	for _, identity := range s.oidcIdentities {
		if identity.Issuer == issuer && identity.Subject == subject {
			userID = identity.UserID
			break
		}
	}
	if userID == "" {
		return domain.User{}, ErrNotFound
	}
	var matched domain.User
	for _, user := range s.users {
		if user.ID == userID {
			matched = user
			break
		}
	}
	if matched.ID == "" || matched.Status != domain.UserActive {
		return matched, ErrNotFound
	}
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.User{}, ErrConflict
	}
	for _, existing := range s.sessions {
		if existing.ID == session.ID || s.tokenHashBySessionID[existing.ID] == tokenHash {
			return domain.User{}, ErrConflict
		}
	}
	session.UserID = matched.ID
	audit.ActorID = matched.ID
	s.sessions = append(s.sessions, session)
	s.tokenHashBySessionID[session.ID] = tokenHash
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return matched, nil
}
