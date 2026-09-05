package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/store"
)

type fakeOIDCProvider struct {
	issuer, clientID string
	nonce            string
	identity         auth.OIDCIdentity
	authorizationErr error
	exchangeErr      error
}

func (f *fakeOIDCProvider) Issuer() string   { return f.issuer }
func (f *fakeOIDCProvider) ClientID() string { return f.clientID }
func (f *fakeOIDCProvider) AuthorizationURL(_ context.Context, _, nonce, _ string) (string, error) {
	f.nonce = nonce
	return "https://idp.example/authorize", f.authorizationErr
}
func (f *fakeOIDCProvider) Exchange(_ context.Context, _, _ string) (auth.OIDCIdentity, error) {
	identity := f.identity
	identity.Nonce = f.nonce
	return identity, f.exchangeErr
}

func newOIDCAppService(t *testing.T) (*Service, *store.MemoryStore) {
	t.Helper()
	mem := store.NewMemoryStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	return service, mem
}

func TestOIDCLoginCreatesOrdinaryRevocableSessionWithoutSensitiveAudit(t *testing.T) {
	service, mem := newOIDCAppService(t)
	provider := &fakeOIDCProvider{issuer: "https://idp.example", clientID: "client", identity: auth.OIDCIdentity{Issuer: "https://idp.example", Subject: "sensitive-subject"}}
	if err := service.ConfigureOIDC(provider); err != nil {
		t.Fatal(err)
	}
	user, err := service.ProvisionOIDCIdentity(t.Context(), OIDCProvisionInput{Issuer: provider.issuer, Subject: provider.identity.Subject, Email: "oidc@example.invalid", Name: "OIDC User"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSession(t.Context(), user.Email, "any-local-password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("OIDC-only user authenticated through local password flow: %v", err)
	}
	started, err := service.StartOIDCLogin(t.Context(), "/runs?q=deploy", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, bound, err := service.CompleteOIDCLogin(t.Context(), "sensitive-code", strings.Repeat("0", 64), "wrong-verifier", SessionCreateMetadata{}); err == nil || bound {
		t.Fatalf("mismatched browser result bound=%t err=%v", bound, err)
	}
	flows := mem
	_ = flows
	// The caller receives state from the provider redirect. The fake URL does
	// not expose it, so recover the hash-bound value by starting a fresh flow
	// with a provider that captures state as well.
	capture := &capturingOIDCProvider{fakeOIDCProvider: *provider}
	if err := service.ConfigureOIDC(capture); err != nil {
		t.Fatal(err)
	}
	started, err = service.StartOIDCLogin(t.Context(), "/runs?q=deploy", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	created, redirect, bound, err := service.CompleteOIDCLogin(t.Context(), "sensitive-code", capture.state, started.Verifier, SessionCreateMetadata{SourceIP: "127.0.0.2", UserAgent: "test-agent"})
	if err != nil || !bound {
		t.Fatalf("complete bound=%t err=%v", bound, err)
	}
	if redirect != "/runs?q=deploy" || created.Session.UserID != user.ID || created.Token == "" {
		t.Fatalf("created=%#v redirect=%q", created.Session, redirect)
	}
	principal, err := service.AuthenticateBrowserSessionToken(t.Context(), created.Token)
	if err != nil || principal.ID != user.ID || principal.Provider != "local" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if _, _, bound, err := service.CompleteOIDCLogin(t.Context(), "sensitive-code", capture.state, started.Verifier, SessionCreateMetadata{}); err == nil || bound {
		t.Fatalf("replay bound=%t err=%v", bound, err)
	}
	audits, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(audits)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-subject", "sensitive-code", capture.nonce, started.Verifier} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit contains sensitive OIDC value")
		}
	}
	if !strings.Contains(string(raw), `"provider":"oidc"`) {
		t.Fatalf("OIDC provider missing from audit: %s", raw)
	}
}

type capturingOIDCProvider struct {
	fakeOIDCProvider
	state string
}

func (f *capturingOIDCProvider) AuthorizationURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	f.state = state
	return f.fakeOIDCProvider.AuthorizationURL(ctx, state, nonce, verifier)
}

func TestOIDCProviderOutageLeavesLocalAuthenticationAvailable(t *testing.T) {
	service, _ := newOIDCAppService(t)
	provider := &fakeOIDCProvider{issuer: "https://idp.example", clientID: "client", authorizationErr: errors.New("provider unavailable")}
	if err := service.ConfigureOIDC(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartOIDCLogin(t.Context(), "/", "127.0.0.1"); !errors.Is(err, ErrOIDCFailed) {
		t.Fatalf("outage error=%v", err)
	}
	if _, err := service.BootstrapAdmin(t.Context(), BootstrapAdminInput{Email: "admin@example.invalid", Name: "Admin", Password: "local-recovery-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSession(t.Context(), "admin@example.invalid", "local-recovery-password"); err != nil {
		t.Fatalf("local authentication unavailable during IdP outage: %v", err)
	}
}

func TestOIDCProvisioningNeverLinksEmailOrMutatesExistingUser(t *testing.T) {
	service, _ := newOIDCAppService(t)
	existing, err := service.BootstrapAdmin(t.Context(), BootstrapAdminInput{Email: "existing@example.invalid", Name: "Existing", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProvisionOIDCIdentity(t.Context(), OIDCProvisionInput{Issuer: "https://idp.example", Subject: "subject", Email: existing.Email, Name: "Collision"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("email collision was linked: %v", err)
	}
	if _, err := service.ProvisionOIDCIdentity(t.Context(), OIDCProvisionInput{Issuer: "https://idp.example", Subject: "subject", UserID: existing.ID, Email: "mutated@example.invalid"}); err == nil {
		t.Fatal("existing-user bind accepted profile mutation")
	}
	if _, err := service.ProvisionOIDCIdentity(t.Context(), OIDCProvisionInput{Issuer: "https://idp.example", Subject: "subject", UserID: existing.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProvisionOIDCIdentity(t.Context(), OIDCProvisionInput{Issuer: "https://idp.example", Subject: " exact-subject ", Email: "other@example.invalid", Name: "Other"}); err == nil {
		t.Fatal("provisioning silently normalized opaque subject")
	}
}
