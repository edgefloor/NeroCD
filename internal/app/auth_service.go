package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/observability"
	"nerocd/internal/store"
)

type BootstrapStatus struct {
	Status string `json:"status"`
}

// PublicBootstrapStatus is deliberately the smallest pre-authentication
// signal: it tells an operator whether they must use the CLI bootstrap flow,
// without exposing identities, timestamps, database state, or errors.
func (s *Service) PublicBootstrapStatus(ctx context.Context) (BootstrapStatus, error) {
	repository, ok := s.users.(store.BootstrapRepository)
	if !ok {
		return BootstrapStatus{}, errors.New("bootstrap status is unavailable")
	}
	complete, err := repository.BootstrapComplete(ctx)
	if err != nil {
		return BootstrapStatus{}, err
	}
	if complete {
		return BootstrapStatus{Status: "complete"}, nil
	}
	return BootstrapStatus{Status: "required"}, nil
}

type OperationsStatus struct {
	Readiness string                 `json:"readiness"`
	Snapshot  observability.Snapshot `json:"snapshot"`
}

// OperationsStatus returns an all-or-nothing, database-authoritative
// administrative summary. A failed readiness or snapshot does not publish a
// stale or partial view.
func (s *Service) OperationsStatus(ctx context.Context) (OperationsStatus, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return OperationsStatus{}, err
	}
	if !isSystemAdmin(principal) {
		return OperationsStatus{}, auth.ErrForbidden
	}
	if err := s.Ready(ctx); err != nil {
		return OperationsStatus{}, err
	}
	snapshot, err := s.OperationalSnapshot(ctx)
	if err != nil {
		return OperationsStatus{}, err
	}
	return OperationsStatus{Readiness: "ready", Snapshot: snapshot}, nil
}

func (s *Service) SetLeaseTTL(ttl time.Duration) error {
	if ttl < 5*time.Second || ttl > 10*time.Minute {
		return errors.New("lease TTL must be between 5s and 10m")
	}
	s.leaseTTL = ttl
	return nil
}

func (s *Service) CurrentPrincipal(ctx context.Context) (auth.Principal, error) {
	return s.auth.CurrentPrincipal(ctx)
}

func (s *Service) Ready(ctx context.Context) error {
	if compatible, ok := s.projects.(interface {
		SchemaCompatible(context.Context) (bool, error)
	}); ok {
		ok, err := compatible.SchemaCompatible(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("database schema is incompatible")
		}
	}
	if _, err := s.projects.ListProjects(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) AuthenticateSessionToken(ctx context.Context, token string) (auth.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	user, err := s.sessions.GetPrincipalBySessionTokenHash(ctx, sessionTokenHash(token), s.clock().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return s.authenticateAPIToken(ctx, token)
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       user.ID,
		Email:    user.Email,
		Name:     user.Name,
		Roles:    globalPrincipalRoles(user.GlobalRole),
		Provider: domain.PrincipalLocal,
	}, nil
}

// AuthenticateBrowserSessionToken authenticates only local session credentials.
// Unlike bearer authentication, it must never fall back to a platform API token.
func (s *Service) AuthenticateBrowserSessionToken(ctx context.Context, token string) (auth.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	user, err := s.sessions.GetPrincipalBySessionTokenHash(ctx, sessionTokenHash(token), s.clock().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       user.ID,
		Email:    user.Email,
		Name:     user.Name,
		Roles:    globalPrincipalRoles(user.GlobalRole),
		Provider: domain.PrincipalLocal,
	}, nil
}

func (s *Service) authenticateAPIToken(ctx context.Context, token string) (auth.Principal, error) {
	apiToken, err := s.apiTokens.GetAPITokenByHash(ctx, apiTokenHash(token), time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       apiToken.ID,
		Email:    "",
		Name:     apiToken.Name,
		Roles:    apiToken.Roles,
		Provider: domain.PrincipalAPIToken,
	}, nil
}

type CreatedAPIToken struct {
	APIToken domain.APIToken `json:"api_token"`
	Token    string          `json:"token"`
}

type BootstrapAdminInput struct {
	Email    string
	Name     string
	Password string
}

func (s *Service) BootstrapAdmin(ctx context.Context, input BootstrapAdminInput) (domain.User, error) {
	repository, ok := s.users.(store.BootstrapRepository)
	if !ok {
		return domain.User{}, errors.New("bootstrap is unavailable")
	}
	email := strings.TrimSpace(strings.ToLower(input.Email))
	name := strings.TrimSpace(input.Name)
	if email == "" || name == "" || strings.TrimSpace(input.Password) == "" {
		return domain.User{}, errors.New("bootstrap email, name, and password are required")
	}
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return domain.User{}, err
	}
	id, err := prefixedID("usr")
	if err != nil {
		return domain.User{}, err
	}
	auditID, err := prefixedID("audit")
	if err != nil {
		return domain.User{}, err
	}
	now := time.Now().UTC()
	user := domain.User{ID: id, Email: email, Name: name, Status: domain.UserActive, GlobalRole: domain.RoleSystemAdmin, PasswordHash: hash, CreatedAt: now}
	audit := domain.AuditEvent{ID: auditID, ActorID: id, Action: "identity.bootstrap_admin", TargetID: id, Metadata: map[string]any{"email": email}, CreatedAt: now}
	if err = repository.BootstrapAdmin(ctx, user, audit); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

type APITokenInput struct {
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Roles     []string   `json:"roles"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type RevokeAPITokenInput struct {
	TokenID string `json:"token_id"`
}

func (s *Service) CreateAPIToken(ctx context.Context, input APITokenInput) (CreatedAPIToken, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return CreatedAPIToken{}, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "api_token.create.denied", "api_tokens", map[string]any{"name": input.Name, "roles": input.Roles})
		return CreatedAPIToken{}, auth.ErrForbidden
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreatedAPIToken{}, errors.New("name is required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = domain.TokenKindServiceAccount
	}
	if kind != domain.TokenKindServiceAccount && kind != domain.TokenKindBootstrap {
		return CreatedAPIToken{}, errors.New("api token kind is invalid")
	}
	roles := normalizeAPITokenRoles(input.Roles)
	if len(roles) == 0 {
		return CreatedAPIToken{}, errors.New("roles are required")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		return CreatedAPIToken{}, errors.New("expires_at must be in the future")
	}
	token, tokenHash, err := newAPITokenSecret()
	if err != nil {
		return CreatedAPIToken{}, err
	}
	id, err := prefixedID("pat")
	if err != nil {
		return CreatedAPIToken{}, err
	}
	now := time.Now().UTC()
	apiToken := domain.APIToken{ID: id, Name: name, Kind: kind, TokenHash: tokenHash, Roles: roles, Status: domain.TokenActive, CreatedBy: principal.ID, CreatedAt: now, ExpiresAt: input.ExpiresAt}
	audit, err := s.auditEvent(ctx, principal.ID, "api_token.create", apiToken.ID, map[string]any{"name": apiToken.Name, "kind": apiToken.Kind, "roles": apiToken.Roles, "expires_at": apiToken.ExpiresAt})
	if err != nil {
		return CreatedAPIToken{}, err
	}
	apiToken, err = s.apiTokens.CreateAPIToken(ctx, apiToken, store.WithAudit(audit))
	if err != nil {
		return CreatedAPIToken{}, err
	}
	return CreatedAPIToken{APIToken: apiToken, Token: token}, nil
}

func (s *Service) RevokeAPIToken(ctx context.Context, input RevokeAPITokenInput) (domain.APIToken, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.APIToken{}, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "api_token.revoke.denied", strings.TrimSpace(input.TokenID), nil)
		return domain.APIToken{}, auth.ErrForbidden
	}
	tokenID := strings.TrimSpace(input.TokenID)
	if tokenID == "" {
		return domain.APIToken{}, errors.New("token_id is required")
	}
	// The token name is loaded by the atomic repository operation; audit only
	// needs the stable target ID to preserve all-or-nothing revocation.
	audit, err := s.auditEvent(ctx, principal.ID, "api_token.revoke", tokenID, nil)
	if err != nil {
		return domain.APIToken{}, err
	}
	return s.apiTokens.RevokeAPIToken(ctx, tokenID, time.Now().UTC(), store.WithAudit(audit))
}

type CreatedSession struct {
	Session domain.Session `json:"session"`
	Token   string         `json:"token"`
}

type SessionCreateMetadata struct {
	SourceIP  string
	UserAgent string
}

func (s *Service) CreateSession(ctx context.Context, email string, password string) (CreatedSession, error) {
	return s.CreateSessionWithMetadata(ctx, email, password, SessionCreateMetadata{})
}

func (s *Service) CreateSessionWithMetadata(ctx context.Context, email string, password string, metadata SessionCreateMetadata) (CreatedSession, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	metadata.SourceIP = strings.TrimSpace(metadata.SourceIP)
	metadata.UserAgent = strings.TrimSpace(metadata.UserAgent)
	if len(metadata.UserAgent) > 512 {
		metadata.UserAgent = metadata.UserAgent[:512]
	}
	if allowed, _ := s.loginLimiter.Allow(email, metadata.SourceIP); !allowed {
		return CreatedSession{}, auth.ErrRateLimited
	}

	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.VerifyDummyPassword(password)
			s.loginLimiter.Failed(email, metadata.SourceIP)
			return CreatedSession{}, auth.ErrInvalidCredentials
		}
		return CreatedSession{}, err
	}
	if user.Status != domain.UserActive {
		auth.VerifyDummyPassword(password)
		s.loginLimiter.Failed(email, metadata.SourceIP)
		return CreatedSession{}, auth.ErrInvalidCredentials
	}
	valid, legacy, verifyErr := auth.VerifyPassword(password, user.PasswordHash)
	if verifyErr != nil || !valid || (legacy && !s.allowLegacyPasswords) {
		s.loginLimiter.Failed(email, metadata.SourceIP)
		return CreatedSession{}, auth.ErrInvalidCredentials
	}
	if legacy {
		replacement, hashErr := auth.HashPassword(password)
		if hashErr != nil {
			return CreatedSession{}, hashErr
		}
		if err := s.users.UpdatePasswordHash(ctx, user.ID, user.PasswordHash, replacement); err != nil {
			return CreatedSession{}, err
		}
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return CreatedSession{}, err
	}
	now := s.clock().UTC()
	sessionID, err := randomHex(16)
	if err != nil {
		return CreatedSession{}, err
	}
	session := domain.Session{
		ID:         "ses_" + sessionID,
		UserID:     user.ID,
		ExpiresAt:  now.Add(12 * time.Hour),
		CreatedAt:  now,
		LastSeenAt: &now,
		SourceIP:   metadata.SourceIP,
		UserAgent:  metadata.UserAgent,
	}
	audit, err := s.auditEvent(ctx, user.ID, "session.create", session.ID, map[string]any{"provider": domain.PrincipalLocal, "source_ip": metadata.SourceIP})
	if err != nil {
		return CreatedSession{}, err
	}
	if err := s.sessions.CreateSession(ctx, session, tokenHash, store.WithAudit(audit)); err != nil {
		return CreatedSession{}, err
	}
	s.loginLimiter.Succeeded(email)
	return CreatedSession{Session: session, Token: token}, nil
}

func (s *Service) RevokeSessionToken(ctx context.Context, token string) error {
	principal, err := s.AuthenticateSessionToken(ctx, token)
	if err != nil {
		return err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "session.revoke", principal.ID, nil)
	if err != nil {
		return err
	}
	return s.sessions.RevokeSessionByTokenHash(ctx, sessionTokenHash(token), s.clock().UTC(), store.WithAudit(audit))
}

func (s *Service) ListSessions(ctx context.Context) ([]domain.Session, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "session.list.denied", "sessions", nil)
		return nil, auth.ErrForbidden
	}
	return s.sessions.ListSessions(ctx)
}

func (s *Service) RevokeSession(ctx context.Context, sessionID string) (domain.Session, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return domain.Session{}, errors.New("session_id is required")
	}
	if !isSystemAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "session.revoke.denied", sessionID, nil)
		return domain.Session{}, auth.ErrForbidden
	}
	audit, err := s.auditEvent(ctx, principal.ID, "session.revoke", sessionID, map[string]any{"admin": true})
	if err != nil {
		return domain.Session{}, err
	}
	return s.sessions.RevokeSessionByID(ctx, sessionID, s.clock().UTC(), store.WithAudit(audit))
}
