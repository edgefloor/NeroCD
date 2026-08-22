package app

import (
	"sync"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestBootstrapAdminIsOneTimeAndAuditedUnderConcurrency(t *testing.T) {
	mem := store.NewMemoryStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.BootstrapAdmin(t.Context(), BootstrapAdminInput{Email: "owner@example.invalid", Name: "Owner", Password: "safe-password"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("bootstrap successes=%d, want 1", successes)
	}
	audits, err := mem.ListAuditEvents(t.Context())
	if err != nil || len(audits) != 8 {
		t.Fatalf("audits=%d err=%v, want one success and seven denials", len(audits), err)
	}
	if audits[0].Action != "identity.bootstrap_admin.denied" {
		t.Fatalf("newest audit=%q", audits[0].Action)
	}
}

func TestLegacyPasswordIsUpgradedOnlyInDevelopmentPolicy(t *testing.T) {
	newService := func() (*Service, *store.MemoryStore) {
		mem := store.NewMemoryStore()
		now := time.Now().UTC()
		_ = mem.BootstrapAdmin(t.Context(), domain.User{ID: "legacy-user", Email: "legacy@example.invalid", Name: "Legacy", Status: domain.UserActive, GlobalRole: domain.RoleSystemAdmin, PasswordHash: "sha256:8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918", CreatedAt: now}, domain.AuditEvent{ID: "legacy-audit", ActorID: "legacy-user", Action: "test.bootstrap", TargetID: "legacy-user", CreatedAt: now})
		service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
		if err != nil {
			t.Fatal(err)
		}
		return service, mem
	}
	development, mem := newService()
	if _, err := development.CreateSession(t.Context(), "legacy@example.invalid", "admin"); err != nil {
		t.Fatalf("development legacy login: %v", err)
	}
	user, err := mem.GetUserByEmail(t.Context(), "legacy@example.invalid")
	if err != nil || user.PasswordHash[:9] != "$argon2id" {
		t.Fatalf("upgraded user=%#v err=%v", user, err)
	}
	production, _ := newService()
	production.SetAllowLegacyPasswordVerification(false)
	if _, err := production.CreateSession(t.Context(), "legacy@example.invalid", "admin"); err == nil || err.Error() != "invalid credentials" {
		t.Fatalf("production legacy result=%v", err)
	}
}
