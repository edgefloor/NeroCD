package app

import (
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"testing"
)

func TestHostingObservationServiceRequiresRunnerAndRealBackend(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	r, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_runtime_status", Name: "status", Capabilities: []string{domain.HostingObservationCapability}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateRunnerToken(t.Context(), r.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), p)
	in := domain.HostingObservationTargetsInput{Limit: 50}
	if _, err = s.RunnerHostingObservationTargets(ctx, r.Token, in); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.RunnerHostingObservationTargets(admin, r.Token, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.RunnerHostingObservationTargets(ctx, "wrong_token", in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = s.RunnerReportHostingObservation(ctx, r.Token, domain.HostingRuntimeObservation{BindingID: "binding", SampleID: "sample", ContainerState: "unknown", Readiness: "unknown", CollectionError: "inspect_failed"}); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.RunnerReportHostingObservation(ctx, r.Token, domain.HostingRuntimeObservation{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid input", err)
	}
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "status", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.HostingRuntimeStatus(admin, resource.ID); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.HostingRuntimeStatus(t.Context(), resource.ID); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
}
