package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryOIDCFlowBindingExpiryAndConcurrentConsume(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	flow := domain.OIDCLoginFlow{ID: "oidcf_1", StateHash: "state", NonceHash: "nonce", VerifierHash: "verifier", RedirectPath: "/runs", Issuer: "https://idp.example", ClientID: "client", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := mem.CreateOIDCLoginFlow(t.Context(), flow, now, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ConsumeOIDCLoginFlow(t.Context(), "state", "wrong", flow.Issuer, flow.ClientID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched browser consumed flow: %v", err)
	}
	var successes int
	var mu sync.Mutex
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := mem.ConsumeOIDCLoginFlow(t.Context(), "state", "verifier", flow.Issuer, flow.ClientID, now); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	if successes != 1 {
		t.Fatalf("concurrent consume successes=%d, want 1", successes)
	}
	expired := flow
	expired.ID, expired.StateHash, expired.ExpiresAt = "oidcf_expired", "expired", now
	if err := mem.CreateOIDCLoginFlow(t.Context(), expired, now.Add(-time.Minute), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ConsumeOIDCLoginFlow(t.Context(), "expired", "verifier", flow.Issuer, flow.ClientID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired flow consumed: %v", err)
	}
}

func TestMemoryOIDCFlowAdmissionIsBounded(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		flow := domain.OIDCLoginFlow{ID: fmt.Sprintf("flow_%d", i), StateHash: fmt.Sprintf("state_%d", i), ExpiresAt: now.Add(time.Minute)}
		if err := mem.CreateOIDCLoginFlow(t.Context(), flow, now, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.CreateOIDCLoginFlow(t.Context(), domain.OIDCLoginFlow{ID: "flow_3", StateHash: "state_3", ExpiresAt: now.Add(time.Minute)}, now, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("active flow limit not enforced: %v", err)
	}
}

func TestMemoryOIDCProvisionAndSessionAreAtomic(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_oidc", Email: "oidc@example.invalid", Name: "OIDC User", Status: domain.UserActive, GlobalRole: domain.RoleUser, CreatedAt: now}
	identity := domain.OIDCExternalIdentity{Issuer: "https://idp.example", Subject: "subject", UserID: user.ID, CreatedAt: now}
	audit := domain.AuditEvent{ID: "audit_provision", ActorID: "system", Action: "identity.oidc.provision", TargetID: user.ID, CreatedAt: now}
	if err := mem.ProvisionOIDCUser(t.Context(), user, identity, audit); err != nil {
		t.Fatal(err)
	}
	if err := mem.ProvisionOIDCUser(t.Context(), domain.User{ID: "usr_collision", Email: user.Email, Status: domain.UserActive, GlobalRole: domain.RoleUser}, domain.OIDCExternalIdentity{Issuer: identity.Issuer, Subject: "other", UserID: "usr_collision"}, domain.AuditEvent{ID: "audit_collision"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("email collision linked identity: %v", err)
	}
	mem.users[0].Status = "disabled"
	session := domain.Session{ID: "ses_oidc", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	matched, err := mem.CreateOIDCSession(t.Context(), identity.Issuer, identity.Subject, session, "token-hash", domain.AuditEvent{ID: "audit_session", Action: "session.create", CreatedAt: now})
	if !errors.Is(err, ErrNotFound) || matched.ID != user.ID || len(mem.sessions) != 0 {
		t.Fatalf("disabled user session result user=%#v err=%v sessions=%d", matched, err, len(mem.sessions))
	}
}
