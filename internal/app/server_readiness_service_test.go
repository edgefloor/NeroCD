package app

import (
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func TestServerReadinessServiceAuthorization(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	r, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_readiness", Name: "readiness", Capabilities: []string{domain.ServerReadinessCapability}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateRunnerToken(t.Context(), r.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), p)
	if _, err = s.RunnerServerReadinessChallenge(ctx, r.Token); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.RunnerServerReadinessChallenge(admin, r.Token); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.RunnerServerReadinessChallenge(ctx, "wrong"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = s.RunnerReportServerReadiness(ctx, r.Token, domain.ServerReadinessReport{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err = s.ServerReadiness(t.Context(), "server"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	if _, err = s.ServerReadiness(viewer, "server"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.ServerReadiness(admin, "server"); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestServerReadinessServicePostgresCurrentEnrollment(t *testing.T) {
	s := newPostgresDestinationTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	token := "runner_readiness_enrolled"
	runner := enrollApplicationDestinationRunner(t, s, admin, token, []string{domain.RunTypeComposeDeploy, domain.ServerReadinessCapability})
	detail, err := s.CreateServer(admin, ServerInput{Name: "readiness", RunnerID: runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AuthenticateRunnerToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), principal)
	challenge, err := s.RunnerServerReadinessChallenge(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	body := domain.ServerReadinessReport{SampleID: challenge.SampleID, Profile: challenge.Profile, OS: "unknown", Architecture: "unknown", Execution: "disabled", Configuration: "mismatch", Docker: "unreachable", DaemonOS: "unknown", DaemonArchitecture: "unknown", CLI: "unknown", WorkRoot: "unknown", JournalRoot: "unknown", Error: "local_check_failed"}
	ack, err := s.RunnerReportServerReadiness(ctx, token, body)
	if err != nil || !ack.Accepted {
		t.Fatal(ack, err)
	}
	status, err := s.ServerReadiness(admin, detail.Server.ID)
	if err != nil || status.Status != "blocked" || status.Reason != "prerequisite_failed" {
		t.Fatal(status, err)
	}
	rotated, err := s.RotateRunnerToken(admin, RunnerTokenInput{RunnerID: runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	principal, err = s.AuthenticateRunnerToken(t.Context(), rotated.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunnerServerReadinessChallenge(auth.WithPrincipal(t.Context(), principal), rotated.Token); !errors.Is(err, store.ErrConflict) {
		t.Fatal("rotation without exact enrollment", err)
	}
	status, err = s.ServerReadiness(admin, detail.Server.ID)
	if err != nil || status.Status != "unknown" || status.Reason != "authority_unavailable" {
		t.Fatal(status, err)
	}
}
