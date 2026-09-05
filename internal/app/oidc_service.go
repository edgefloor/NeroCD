package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

const (
	oidcFlowLifetime    = 5 * time.Minute
	oidcActiveFlowLimit = 10_000
	maxOIDCValueLength  = 2_048
)

var (
	// ErrOIDCDisabled reports that enterprise sign-in is not configured.
	ErrOIDCDisabled = errors.New("oidc is disabled")
	// ErrOIDCFailed is the intentionally generic browser-facing failure.
	ErrOIDCFailed = errors.New("oidc authentication failed")
	// ErrOIDCRateLimited reports bounded anonymous initiation admission.
	ErrOIDCRateLimited = errors.New("oidc initiation rate limited")
)

// OIDCStatus is the complete public configuration surface.
type OIDCStatus struct {
	Enabled bool `json:"enabled"`
}

// OIDCLoginStart contains the redirect and cookie-held verifier for one flow.
type OIDCLoginStart struct {
	AuthorizationURL string
	Verifier         string
	ExpiresAt        time.Time
}

// ConfigureOIDC enables enterprise browser sign-in with fixed configuration.
func (s *Service) ConfigureOIDC(provider auth.OIDCProvider) error {
	if provider == nil || s.oidcStore == nil {
		return errors.New("oidc dependencies are unavailable")
	}
	s.oidc = provider
	return nil
}

// OIDCStatus reports only whether OIDC is enabled.
func (s *Service) OIDCStatus() OIDCStatus {
	return OIDCStatus{Enabled: s.oidc != nil && s.oidcStore != nil}
}

// StartOIDCLogin creates one durable, independently bound OIDC transaction.
func (s *Service) StartOIDCLogin(ctx context.Context, redirectPath, source string) (OIDCLoginStart, error) {
	if !s.OIDCStatus().Enabled {
		return OIDCLoginStart{}, ErrOIDCDisabled
	}
	limiterKey := strings.TrimSpace(source)
	if allowed, _ := s.oidcLimiter.Allow(limiterKey, limiterKey); !allowed {
		return OIDCLoginStart{}, ErrOIDCRateLimited
	}
	s.oidcLimiter.Failed(limiterKey, limiterKey)
	state, err := randomHex(32)
	if err != nil {
		return OIDCLoginStart{}, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return OIDCLoginStart{}, err
	}
	verifier := oauth2.GenerateVerifier()
	if verifier == "" {
		return OIDCLoginStart{}, errors.New("generate oidc verifier")
	}
	authorizationURL, err := s.oidc.AuthorizationURL(ctx, state, nonce, verifier)
	if err != nil {
		return OIDCLoginStart{}, ErrOIDCFailed
	}
	now := s.clock().UTC()
	id, err := prefixedID("oidcf")
	if err != nil {
		return OIDCLoginStart{}, err
	}
	flow := domain.OIDCLoginFlow{
		ID: id, StateHash: oidcHash(state), NonceHash: oidcHash(nonce), VerifierHash: oidcHash(verifier),
		RedirectPath: redirectPath, Issuer: s.oidc.Issuer(), ClientID: s.oidc.ClientID(),
		CreatedAt: now, ExpiresAt: now.Add(oidcFlowLifetime),
	}
	if err := s.oidcStore.CreateOIDCLoginFlow(ctx, flow, now, oidcActiveFlowLimit); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return OIDCLoginStart{}, ErrOIDCRateLimited
		}
		return OIDCLoginStart{}, err
	}
	return OIDCLoginStart{AuthorizationURL: authorizationURL, Verifier: verifier, ExpiresAt: flow.ExpiresAt}, nil
}

// CompleteOIDCLogin atomically consumes the browser binding before any code
// exchange, verifies the provider assertion, then creates an ordinary session.
func (s *Service) CompleteOIDCLogin(ctx context.Context, code, state, verifier string, metadata SessionCreateMetadata) (CreatedSession, string, bool, error) {
	if !s.OIDCStatus().Enabled {
		return CreatedSession{}, "", false, ErrOIDCDisabled
	}
	if !boundedOIDCValue(code) || !boundedOIDCValue(state) || !boundedOIDCValue(verifier) {
		return CreatedSession{}, "", false, ErrOIDCFailed
	}
	now := s.clock().UTC()
	flow, err := s.oidcStore.ConsumeOIDCLoginFlow(ctx, oidcHash(state), oidcHash(verifier), s.oidc.Issuer(), s.oidc.ClientID(), now)
	if err != nil {
		return CreatedSession{}, "", false, ErrOIDCFailed
	}
	identity, err := s.oidc.Exchange(ctx, code, verifier)
	if err != nil || identity.Issuer != flow.Issuer || strings.TrimSpace(identity.Subject) == "" {
		s.auditOIDCDenial(ctx, "provider_rejected", "")
		return CreatedSession{}, flow.RedirectPath, true, ErrOIDCFailed
	}
	if subtle.ConstantTimeCompare([]byte(oidcHash(identity.Nonce)), []byte(flow.NonceHash)) != 1 {
		s.auditOIDCDenial(ctx, "nonce_invalid", "")
		return CreatedSession{}, flow.RedirectPath, true, ErrOIDCFailed
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return CreatedSession{}, flow.RedirectPath, true, err
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return CreatedSession{}, flow.RedirectPath, true, err
	}
	metadata.SourceIP = strings.TrimSpace(metadata.SourceIP)
	metadata.UserAgent = strings.TrimSpace(metadata.UserAgent)
	if len(metadata.UserAgent) > 512 {
		metadata.UserAgent = metadata.UserAgent[:512]
	}
	session := domain.Session{ID: "ses_" + sessionID, ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now, LastSeenAt: &now, SourceIP: metadata.SourceIP, UserAgent: metadata.UserAgent}
	auditID, err := prefixedID("audit")
	if err != nil {
		return CreatedSession{}, flow.RedirectPath, true, err
	}
	audit := domain.AuditEvent{ID: auditID, ActorID: "system", Action: "session.create", TargetID: session.ID, Metadata: map[string]any{"provider": "oidc", "source_ip": metadata.SourceIP}, CreatedAt: now}
	user, err := s.oidcStore.CreateOIDCSession(ctx, identity.Issuer, identity.Subject, session, tokenHash, audit)
	if err != nil {
		reason, userID := "persistence_error", ""
		if errors.Is(err, store.ErrNotFound) && user.ID == "" {
			reason = "identity_unknown"
		} else if errors.Is(err, store.ErrNotFound) && user.ID != "" {
			reason, userID = "user_disabled", user.ID
		}
		s.auditOIDCDenial(ctx, reason, userID)
		return CreatedSession{}, flow.RedirectPath, true, ErrOIDCFailed
	}
	session.UserID = user.ID
	return CreatedSession{Session: session, Token: token}, flow.RedirectPath, true, nil
}

// RejectOIDCLogin consumes a correctly browser-bound provider denial without
// reflecting provider-controlled error text to the browser or audit trail.
func (s *Service) RejectOIDCLogin(ctx context.Context, state, verifier string) (bool, error) {
	if !s.OIDCStatus().Enabled {
		return false, ErrOIDCDisabled
	}
	if !boundedOIDCValue(state) || !boundedOIDCValue(verifier) {
		return false, ErrOIDCFailed
	}
	_, err := s.oidcStore.ConsumeOIDCLoginFlow(ctx, oidcHash(state), oidcHash(verifier), s.oidc.Issuer(), s.oidc.ClientID(), s.clock().UTC())
	if err != nil {
		return false, ErrOIDCFailed
	}
	s.auditOIDCDenial(ctx, "provider_denied", "")
	return true, ErrOIDCFailed
}

// OIDCCallbackIssuerValid validates an optional RFC 9207 response issuer.
func (s *Service) OIDCCallbackIssuerValid(value string) bool {
	return s.OIDCStatus().Enabled && subtle.ConstantTimeCompare([]byte(value), []byte(s.oidc.Issuer())) == 1
}

// OIDCProvisionInput describes the two explicit CLI-only provisioning modes.
type OIDCProvisionInput struct {
	Issuer  string
	Subject string
	Email   string
	Name    string
	UserID  string
}

// ProvisionOIDCIdentity creates a nonprivileged user or binds one existing user.
func (s *Service) ProvisionOIDCIdentity(ctx context.Context, input OIDCProvisionInput) (domain.User, error) {
	if s.oidcStore == nil {
		return domain.User{}, errors.New("oidc persistence is unavailable")
	}
	input.Issuer = strings.TrimSpace(input.Issuer)
	if strings.TrimSpace(input.Subject) != input.Subject {
		return domain.User{}, errors.New("subject must not contain surrounding whitespace")
	}
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.Name = strings.TrimSpace(input.Name)
	input.UserID = strings.TrimSpace(input.UserID)
	if !boundedOIDCValue(input.Issuer) || !boundedOIDCValue(input.Subject) {
		return domain.User{}, errors.New("issuer and subject are required")
	}
	now := s.clock().UTC()
	auditID, err := prefixedID("audit")
	if err != nil {
		return domain.User{}, err
	}
	if input.UserID != "" {
		if input.Email != "" || input.Name != "" {
			return domain.User{}, errors.New("existing-user binding rejects email and name")
		}
		identity := domain.OIDCExternalIdentity{Issuer: input.Issuer, Subject: input.Subject, UserID: input.UserID, CreatedAt: now}
		audit := domain.AuditEvent{ID: auditID, ActorID: "system", Action: "identity.oidc.bind", TargetID: input.UserID, Metadata: map[string]any{"provider": "oidc"}, CreatedAt: now}
		if err := s.oidcStore.BindOIDCIdentity(ctx, identity, audit); err != nil {
			return domain.User{}, err
		}
		return domain.User{ID: input.UserID}, nil
	}
	if input.Email == "" || input.Name == "" {
		return domain.User{}, errors.New("new oidc user requires email and name")
	}
	userID, err := prefixedID("usr")
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{ID: userID, Email: input.Email, Name: input.Name, Status: domain.UserActive, GlobalRole: domain.RoleUser, CreatedAt: now}
	identity := domain.OIDCExternalIdentity{Issuer: input.Issuer, Subject: input.Subject, UserID: user.ID, CreatedAt: now}
	audit := domain.AuditEvent{ID: auditID, ActorID: "system", Action: "identity.oidc.provision", TargetID: user.ID, Metadata: map[string]any{"provider": "oidc"}, CreatedAt: now}
	if err := s.oidcStore.ProvisionOIDCUser(ctx, user, identity, audit); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Service) auditOIDCDenial(ctx context.Context, reason, userID string) {
	auditID, err := prefixedID("audit")
	if err != nil {
		return
	}
	actor, target := "system", "oidc-session"
	if userID != "" {
		actor, target = userID, userID
	}
	_ = s.audit.CreateAuditEvent(ctx, domain.AuditEvent{ID: auditID, ActorID: actor, Action: "session.create.denied", TargetID: target, Metadata: map[string]any{"provider": "oidc", "reason": reason}, CreatedAt: s.clock().UTC()})
}

func oidcHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func boundedOIDCValue(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxOIDCValueLength
}
