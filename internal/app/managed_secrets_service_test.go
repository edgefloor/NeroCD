package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func managedTestRing(t *testing.T, key string) *secretcrypto.Keyring {
	t.Helper()
	ring, err := secretcrypto.New(key, map[string][]byte{key: bytes.Repeat([]byte{5}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
func TestManagedSecretsServiceRolesAndLifecycle(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	s.secretKeyring = managedTestRing(t, "test")
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	value := "  secret UTF8 árvíz\n"
	item, err := s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "token", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListManagedSecrets(ctx, "", store.Page{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing project: %v", err)
	}
	if _, err = s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "missing", Name: "token", Value: value}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project: %v", err)
	}
	actor := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleOwner}})
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "managed_member", UserID: "usr_viewer", ProjectID: "proj_platform", Role: role}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.GetManagedSecret(actor, item.ID); err != nil {
			t.Fatalf("%s metadata: %v", role, err)
		}
		if _, err = s.ListManagedSecretVersions(actor, item.ID, store.Page{}); err != nil {
			t.Fatal(err)
		}
		_, err = s.RevealManagedSecret(actor, item.ID, ManagedSecretRevealInput{Version: 1})
		if role == domain.RoleOwner {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("%s reveal: %v", role, err)
		}
		_, err = s.RotateManagedSecret(actor, item.ID, ManagedSecretRotateInput{ExpectedVersion: item.CurrentVersion, Value: value})
		if role == domain.RoleViewer {
			if !errors.Is(err, auth.ErrForbidden) {
				t.Fatal("viewer rotate accepted")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			item.CurrentVersion++
		}
	}
	account, err := s.CreateServiceAccount(ctx, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "other bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AuthenticateSessionToken(t.Context(), account.Token)
	if err != nil {
		t.Fatal(err)
	}
	accountCtx := auth.WithPrincipal(t.Context(), principal)
	if _, err = s.GetManagedSecret(accountCtx, item.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("crossproject serviceaccount: %v", err)
	}
	reveal, err := s.RevealManagedSecret(ctx, item.ID, ManagedSecretRevealInput{Version: 1})
	if err != nil || reveal.Value != value {
		t.Fatalf("exact historical reveal: %v", err)
	}
	if _, err = s.RevealManagedSecret(ctx, item.ID, ManagedSecretRevealInput{Version: 99}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing version revealed")
	}
	for _, invalid := range []string{"", strings.Repeat("x", 65537), string([]byte{0xff})} {
		if _, err = s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "bad", Value: invalid}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad value accepted: %v", err)
		}
	}
	if _, err = s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "max", Value: strings.Repeat(" ", 65536)}); err != nil {
		t.Fatalf("exact whitespace bound: %v", err)
	}
	s.secretKeyring = nil
	if _, err = s.GetManagedSecret(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "disabled", Value: value}); !errors.Is(err, ErrManagedSecretsUnavailable) {
		t.Fatalf("disabled create: %v", err)
	}
	if _, err = s.RotateManagedSecret(ctx, item.ID, ManagedSecretRotateInput{ExpectedVersion: item.CurrentVersion, Value: value}); !errors.Is(err, ErrManagedSecretsUnavailable) {
		t.Fatalf("disabled rotate: %v", err)
	}
	if reveal, err = s.RevealManagedSecret(ctx, item.ID, ManagedSecretRevealInput{Version: 1}); !errors.Is(err, ErrManagedSecretsUnavailable) || reveal.Value != "" {
		t.Fatal("disabled reveal returned value")
	}
	revoked, err := s.RevokeManagedSecret(ctx, item.ID, ManagedSecretRevokeInput{ExpectedVersion: item.CurrentVersion})
	if err != nil || revoked.Status != "revoked" {
		t.Fatalf("keyless revoke: %v", err)
	}
	s.secretKeyring = managedTestRing(t, "test")
	if reveal, err = s.RevealManagedSecret(ctx, item.ID, ManagedSecretRevealInput{Version: 1}); !errors.Is(err, store.ErrConflict) || reveal.Value != "" {
		t.Fatal("revoked value revealed")
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{value, "ciphertext", "key_id", "envelope", "value"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("metadata contains %s", private)
		}
	}
}

type managedFailureStore struct {
	store.ManagedSecretRepository
	failAudit, revokeBeforeAudit, tamper bool
}

func (s managedFailureStore) AuthorizeManagedSecretReveal(ctx context.Context, id string, v int64, a domain.AuditEvent) error {
	if s.failAudit {
		return errors.New("audit commit unavailable")
	}
	if s.revokeBeforeAudit {
		event := a
		event.ID += "revoke"
		item, err := s.GetManagedSecret(ctx, id)
		if err != nil {
			return err
		}
		if _, err = s.RevokeManagedSecret(ctx, id, item.CurrentVersion, event); err != nil {
			return err
		}
	}
	return s.ManagedSecretRepository.AuthorizeManagedSecretReveal(ctx, id, v, a)
}
func (s managedFailureStore) GetManagedSecretRecord(ctx context.Context, id string, v int64) (store.ManagedSecretRecord, error) {
	record, err := s.ManagedSecretRepository.GetManagedSecretRecord(ctx, id, v)
	if err == nil && s.tamper {
		record.Envelope.Data[0] ^= 1
	}
	return record, err
}
func TestManagedSecretRevealFailsClosed(t *testing.T) {
	for _, mode := range []string{"tamper", "audit failure", "revoke race", "missing key"} {
		t.Run(mode, func(t *testing.T) {
			s, mem := newServiceAccountTestService(t)
			s.secretKeyring = managedTestRing(t, "old")
			ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
			item, err := s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "failure", Value: "private-value"})
			if err != nil {
				t.Fatal(err)
			}
			s.managedSecrets = managedFailureStore{ManagedSecretRepository: mem, failAudit: mode == "audit failure", revokeBeforeAudit: mode == "revoke race", tamper: mode == "tamper"}
			if mode == "missing key" {
				s.secretKeyring = managedTestRing(t, "new")
			}
			result, err := s.RevealManagedSecret(ctx, item.ID, ManagedSecretRevealInput{Version: 1})
			if err == nil || result.Value != "" || strings.Contains(err.Error(), "private-value") {
				t.Fatal("failed reveal leaked value")
			}
			events, err := mem.ListAuditEvents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Action == "managed_secret.revealed" {
					t.Fatal("failed reveal audit exists")
				}
			}
		})
	}
}
