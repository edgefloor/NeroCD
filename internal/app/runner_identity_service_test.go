package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type identityReadFailure struct {
	store.RunnerRepository
	err error
}

func (p identityReadFailure) GetRunnerByID(context.Context, string) (domain.Runner, error) {
	return domain.Runner{}, p.err
}

func TestCurrentRunnerIdentityRequiresCurrentPersistedAuthorityWithoutWrites(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	registered, err := service.RegisterRunner(admin, RunnerInput{ID: "runner_identity", Name: "Identity Runner", Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateRunnerToken(t.Context(), registered.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), principal)
	before, err := mem.GetRunnerByID(t.Context(), registered.Runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	auditBefore, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.CurrentRunnerIdentity(ctx)
	if err != nil || identity.RunnerID != before.ID || !reflect.DeepEqual(identity.Capabilities, before.Capabilities) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	identity.Capabilities[0] = "proxy_apply"
	after, err := mem.GetRunnerByID(t.Context(), before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("identity changed runner: before=%+v after=%+v err=%v", before, after, err)
	}
	auditAfter, err := mem.ListAuditEvents(t.Context())
	if err != nil || !reflect.DeepEqual(auditBefore, auditAfter) {
		t.Fatalf("identity changed audit: %v", err)
	}
	if _, err = service.CurrentRunnerIdentity(t.Context()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err = service.CurrentRunnerIdentity(admin); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("human admin: %v", err)
	}
	missing := auth.WithPrincipal(t.Context(), auth.Principal{ID: "missing", Provider: domain.PrincipalRunner})
	if _, err = service.CurrentRunnerIdentity(missing); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("missing persisted runner: %v", err)
	}
	for _, state := range []struct{ status, hash string }{{domain.RunnerRevoked, "hash"}, {domain.RunnerStale, "hash"}, {domain.RunnerActive, ""}} {
		if _, err = mem.UpdateRunnerToken(t.Context(), before.ID, state.hash, state.status, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err = service.CurrentRunnerIdentity(ctx); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("status=%s token_present=%t: %v", state.status, state.hash != "", err)
		}
	}
	unavailable := errors.New("identity lookup unavailable")
	service.runners = identityReadFailure{RunnerRepository: mem, err: unavailable}
	if _, err = service.CurrentRunnerIdentity(ctx); !errors.Is(err, unavailable) {
		t.Fatalf("lookup failure: %v", err)
	}
}
