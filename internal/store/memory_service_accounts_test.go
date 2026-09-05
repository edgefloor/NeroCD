package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryServiceAccountRotationRollbackAndRace(t *testing.T) {
	mem := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "admin", "viewer")
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	account := domain.ServiceAccount{ID: "svcacct_memory", ProjectID: "proj_platform", OwnerUserID: "usr_bootstrap", Name: "memory bot", Role: domain.RoleMaintainer, Status: domain.ServiceAccountActive, CreatedAt: now, UpdatedAt: now}
	accountID := account.ID
	original := domain.APIToken{ID: "pat_memory", TokenHash: "original-hash", ServiceAccountID: &accountID, ExpiresAt: &expires, CreatedAt: now}
	if _, err := mem.CreateServiceAccount(t.Context(), account, original, domain.AuditEvent{ID: "audit_memory_create", ActorID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CreateAuditEvent(t.Context(), domain.AuditEvent{ID: "audit_duplicate", ActorID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	replacement := domain.APIToken{ID: "pat_memory_failed", TokenHash: "replacement-hash", ExpiresAt: &expires, CreatedAt: now}
	if _, err := mem.RotateServiceAccountCredential(t.Context(), account.ID, replacement, now, domain.AuditEvent{ID: "audit_duplicate", ActorID: "usr_bootstrap", CreatedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate audit rotation error = %v", err)
	}
	if _, err := mem.GetManagedServiceAccountCredentialByHash(t.Context(), "original-hash", now); err != nil {
		t.Fatalf("failed rotation revoked original: %v", err)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	go func() {
		defer wait.Done()
		_, err := mem.RotateServiceAccountCredential(t.Context(), account.ID, domain.APIToken{ID: "pat_memory_race", TokenHash: "race-hash", ExpiresAt: &expires, CreatedAt: now}, now, domain.AuditEvent{ID: "audit_memory_rotate", ActorID: "usr_bootstrap", CreatedAt: now})
		results <- err
	}()
	go func() {
		defer wait.Done()
		_, err := mem.RevokeServiceAccount(t.Context(), account.ID, now, domain.AuditEvent{ID: "audit_memory_revoke", ActorID: "usr_bootstrap", CreatedAt: now})
		results <- err
	}()
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("race error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("race successes = %d, want 1", successes)
	}
	if _, _, err := mem.ServiceAccountProjectRole(t.Context(), account.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("account remained active after race: %v", err)
	}
}

func TestMemoryServiceAccountsAreProjectScopedAndPaged(t *testing.T) {
	mem := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "admin", "viewer")
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	for index, projectID := range []string{"proj_platform", "proj_platform", "proj_security"} {
		accountID := "svcacct_page_" + string(rune('a'+index))
		_, err := mem.CreateServiceAccount(t.Context(), domain.ServiceAccount{ID: accountID, ProjectID: projectID, OwnerUserID: "usr_bootstrap", Name: accountID, Role: domain.RoleViewer, CreatedAt: now.Add(time.Duration(index) * time.Second), UpdatedAt: now}, domain.APIToken{ID: "pat_page_" + accountID, TokenHash: "hash_" + accountID, ExpiresAt: &expires, CreatedAt: now}, domain.AuditEvent{ID: "audit_page_" + accountID, ActorID: "usr_bootstrap", CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := mem.ListServiceAccounts(t.Context(), "proj_platform", Page{Enabled: true, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Total != 2 || page.Items[0].ProjectID != "proj_platform" {
		t.Fatalf("page = %#v, %v", page, err)
	}
}
