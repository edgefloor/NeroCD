package app

import (
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"strings"
	"testing"
	"time"
)

func TestHostingRuntimeLogServiceAuthorityAndUnavailableMemory(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime-logs"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "runtime-logs", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingRuntimeLogOpen{RequestID: strings.Repeat("a", 32), RequestExpiresAt: time.Now().Add(20 * time.Second)}
	if _, err = s.OpenRuntimeLogSession(admin, resource.ID, input); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.OpenRuntimeLogSession(t.Context(), resource.ID, input); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = s.RuntimeLogPage(admin, "rls_test", -1, 1); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err = s.CloseRuntimeLogSession(admin, "rls_test"); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	r, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_log_service", Name: "logs", Capabilities: []string{domain.HostingRuntimeLogsCapability}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateRunnerToken(t.Context(), r.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), p)
	claim := domain.HostingRuntimeLogClaimInput{InstanceID: strings.Repeat("b", 32), AfterSessionID: "", Limit: 4}
	if _, err = s.ClaimRuntimeLogSessions(ctx, r.Token, claim); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.ClaimRuntimeLogSessions(admin, r.Token, claim); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.ClaimRuntimeLogSessions(ctx, "wrong", claim); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = s.AppendRuntimeLogs(ctx, r.Token, domain.HostingRuntimeLogBatch{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}
