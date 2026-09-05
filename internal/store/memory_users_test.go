package store

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryOffboardUserRevokesCredentialsAndPreservesIdentityHistory(t *testing.T) {
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	mem := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "admin-hash", "viewer-hash")
	if err := mem.BindOIDCIdentity(t.Context(), domain.OIDCExternalIdentity{Issuer: "https://issuer.example", Subject: "viewer-subject", UserID: "usr_viewer", CreatedAt: now}, domain.AuditEvent{ID: "audit-bind", ActorID: "usr_viewer", Action: "oidc.identity.bind", TargetID: "usr_viewer", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CreateSession(t.Context(), domain.Session{ID: "session-viewer", UserID: "usr_viewer", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "session-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateAPIToken(t.Context(), domain.APIToken{ID: "token-viewer", Name: "viewer automation", Kind: domain.TokenKindServiceAccount, TokenHash: "token-hash", Roles: []string{domain.RoleViewer}, Status: domain.TokenActive, CreatedBy: "usr_viewer", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	result, err := mem.OffboardUser(t.Context(), UserOffboardingRequest{
		UserID: "usr_viewer", RevokedAt: now,
		Audit:       domain.AuditEvent{ID: "audit-offboard", ActorID: "usr_bootstrap", Action: "identity.user.offboard", TargetID: "usr_viewer", CreatedAt: now},
		DeniedAudit: domain.AuditEvent{ID: "audit-denied", ActorID: "usr_bootstrap", Action: "identity.user.offboard.denied", TargetID: "usr_viewer", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.User.Status != domain.UserDisabled || result.User.ArchivedAt == nil || result.RevokedSessions != 1 || result.RevokedAPITokens != 1 {
		t.Fatalf("offboarding result = %#v", result)
	}
	if _, err := mem.GetPrincipalBySessionTokenHash(t.Context(), "session-hash", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked session lookup error = %v, want ErrNotFound", err)
	}
	if _, err := mem.GetAPITokenByHash(t.Context(), "token-hash", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked API token lookup error = %v, want ErrNotFound", err)
	}
	if _, err := mem.CreateOIDCSession(t.Context(), "https://issuer.example", "viewer-subject", domain.Session{ID: "oidc-after-offboard", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "oidc-session-hash", domain.AuditEvent{ID: "audit-oidc", CreatedAt: now}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled OIDC login error = %v, want ErrNotFound", err)
	}
	users, err := mem.ListUsersPage(t.Context(), Page{})
	if err != nil {
		t.Fatal(err)
	}
	if users.Total != 2 {
		t.Fatalf("user history total = %d, want 2", users.Total)
	}
	events, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Action != "identity.user.offboard" || events[0].Metadata["revoked_sessions"] != 1 || events[0].Metadata["revoked_api_tokens"] != 1 {
		t.Fatalf("offboarding audit = %#v", events[0])
	}
}

func TestMemoryOffboardUserGuardsLastAdministratorAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	mem := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "admin-hash", "viewer-hash")
	request := UserOffboardingRequest{
		UserID: "usr_bootstrap", RevokedAt: now,
		Audit:       domain.AuditEvent{ID: "audit-offboard-admin", ActorID: "usr_bootstrap", Action: "identity.user.offboard", TargetID: "usr_bootstrap", CreatedAt: now},
		DeniedAudit: domain.AuditEvent{ID: "audit-offboard-admin-denied", ActorID: "usr_bootstrap", Action: "identity.user.offboard.denied", TargetID: "usr_bootstrap", CreatedAt: now},
	}
	if _, err := mem.OffboardUser(t.Context(), request); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last administrator error = %v", err)
	}
	admin, err := mem.GetUserByEmail(t.Context(), "admin@example.local")
	if err != nil || admin.Status != domain.UserActive {
		t.Fatalf("last administrator mutated = %#v, err=%v", admin, err)
	}
	events, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Action != "identity.user.offboard.denied" || events[0].Metadata["reason"] != "last_active_system_administrator" {
		t.Fatalf("denied audit = %#v", events[0])
	}

	result, err := mem.OffboardUser(t.Context(), UserOffboardingRequest{UserID: "usr_viewer", RevokedAt: now, Audit: domain.AuditEvent{ID: "audit-offboard-viewer", CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := mem.OffboardUser(t.Context(), UserOffboardingRequest{UserID: "usr_viewer", RevokedAt: now.Add(time.Hour), Audit: domain.AuditEvent{ID: "unused-replay-audit", CreatedAt: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if replay.User.Status != domain.UserDisabled || replay.User.ArchivedAt == nil || !replay.User.ArchivedAt.Equal(*result.User.ArchivedAt) || replay.RevokedSessions != 0 || replay.RevokedAPITokens != 0 {
		t.Fatalf("idempotent replay = %#v, original=%#v", replay, result)
	}
}
