package app

import (
	"context"
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"testing"
	"time"
)

type networkCaptureServiceRepository struct {
	store.RunRepository
	runner, credential, id string
	calls                  int
}

func (r *networkCaptureServiceRepository) ClaimNextHostingDatabaseNetworkCapture(_ context.Context, runner, credential string, _ time.Duration) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	r.calls++
	r.runner, r.credential = runner, credential
	return domain.HostingDatabaseNetworkCaptureClaim{}, store.ErrNotFound
}

func (r *networkCaptureServiceRepository) RequestHostingDatabaseNetworkCapture(context.Context, string, string, domain.AuditEvent) (domain.HostingDatabaseNetworkCapture, error) {
	return domain.HostingDatabaseNetworkCapture{}, nil
}
func (r *networkCaptureServiceRepository) ClaimHostingDatabaseNetworkCapture(_ context.Context, runner, credential, id string, _ time.Duration) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	r.calls++
	r.runner, r.credential, r.id = runner, credential, id
	return domain.HostingDatabaseNetworkCaptureClaim{}, nil
}
func (r *networkCaptureServiceRepository) HostingDatabaseNetworkCapturePlan(_ context.Context, runner, credential string, _ domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error) {
	r.calls++
	r.runner, r.credential = runner, credential
	return domain.HostingDatabaseNetworkCapture{}, nil
}
func (r *networkCaptureServiceRepository) RecordHostingDatabaseNetworkOrigin(context.Context, string, string, domain.HostingDatabaseNetworkOriginRequest) (domain.HostingDatabaseNetworkOriginReceipt, error) {
	r.calls++
	return domain.HostingDatabaseNetworkOriginReceipt{}, nil
}

func TestDatabaseNetworkCaptureServiceRunnerIdentityAndUnavailable(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	for _, input := range []RunRequestInput{
		{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseNetworkCapture}},
		{ProjectID: "proj_platform", Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseNetworkCapture}}}}},
	} {
		if _, err := s.RequestRunWithSpec(admin, input); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("generic submission accepted internal capture")
		}
	}
	registered, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_capture", Name: "capture", Capabilities: []string{domain.HostingDatabaseNetworkCaptureCapability}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: registered.Runner.ID, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	if _, err = s.RunnerClaimHostingDatabaseNetworkCapture(ctx, registered.Token, "capture"); !errors.Is(err, store.ErrHostingDatabaseNetworkCaptureUnavailable) {
		t.Fatal("memory backend enabled capture", err)
	}
	repo := &networkCaptureServiceRepository{RunRepository: s.runs}
	s.runs = repo
	if _, err = s.RunnerClaimHostingDatabaseNetworkCapture(ctx, registered.Token, "capture"); err != nil || repo.calls != 1 || repo.runner != registered.Runner.ID || repo.credential != runnerTokenHash(registered.Token) || repo.credential == registered.Token {
		t.Fatal("current runner credential was not bound", err)
	}
	for _, badctx := range []context.Context{admin, t.Context(), auth.WithPrincipal(t.Context(), auth.Principal{ID: "other_runner", Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})} {
		if _, err = s.RunnerClaimHostingDatabaseNetworkCapture(badctx, registered.Token, "capture"); err == nil || repo.calls != 1 {
			t.Fatal("foreign actor reached capture repository")
		}
	}
	if _, err = s.RunnerClaimHostingDatabaseNetworkCapture(ctx, "wrong", "capture"); err == nil || repo.calls != 1 {
		t.Fatal("wrong bearer reached repository")
	}
	if _, err = s.RunnerRecordHostingDatabaseNetworkOrigin(ctx, registered.Token, domain.HostingDatabaseNetworkOriginRequest{}); !errors.Is(err, ErrInvalidInput) || repo.calls != 1 {
		t.Fatal("invalid origin reached repository")
	}
}

func TestDatabaseNetworkCaptureClaimNextServiceIdentity(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	registered, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_capture", Name: "capture", Capabilities: []string{domain.HostingDatabaseNetworkCaptureCapability}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: registered.Runner.ID, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	if _, err = s.RunnerClaimNextHostingDatabaseNetworkCapture(ctx, registered.Token); !errors.Is(err, store.ErrHostingDatabaseNetworkCaptureUnavailable) {
		t.Fatal("memory backend selected capture", err)
	}
	repo := &networkCaptureServiceRepository{RunRepository: s.runs}
	s.runs = repo
	if _, err = s.RunnerClaimNextHostingDatabaseNetworkCapture(ctx, registered.Token); !errors.Is(err, store.ErrNotFound) || repo.calls != 1 || repo.runner != registered.Runner.ID || repo.credential != runnerTokenHash(registered.Token) || repo.credential == registered.Token {
		t.Fatal("claim-next did not bind current credential or preserve no-work", err)
	}
	for _, invalid := range []context.Context{admin, t.Context(), auth.WithPrincipal(t.Context(), auth.Principal{ID: "foreign", Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})} {
		if _, err = s.RunnerClaimNextHostingDatabaseNetworkCapture(invalid, registered.Token); err == nil || repo.calls != 1 {
			t.Fatal("foreign actor selected work", err)
		}
	}
	if _, err = s.RunnerClaimNextHostingDatabaseNetworkCapture(ctx, "wrong"); err == nil || repo.calls != 1 {
		t.Fatal("wrong bearer selected work", err)
	}
}
