package app

import (
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"testing"
)

func TestHostingBuildLogServiceAuthValidationAndUnavailable(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, testPrincipalContext(t))
	in := domain.HostingBuildLogBatch{Authority: domain.HostingBuildAuthority{BuildID: "a", RunID: "build_run_a", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Events: []domain.HostingBuildLogEvent{{EventKey: "key", Sequence: 1, Component: "build", Stream: "stdout", Message: "hello"}}}
	if _, err := s.AppendHostingBuildLogs(admin, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("human ingestion", err)
	}
	runner, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_build_logs", Name: "logs", Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AuthenticateRunnerToken(ctx, runner.Token)
	if err != nil {
		t.Fatal(err)
	}
	runnerCtx := auth.WithPrincipal(ctx, principal)
	before, _ := mem.ListAuditEvents(ctx)
	if _, err = s.AppendHostingBuildLogs(runnerCtx, in); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	if _, err = s.HostingBuildLogs(admin, "build", 0, 200); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	for _, pair := range [][2]int{{-1, 200}, {0, 0}, {0, 201}} {
		if _, err = s.HostingBuildLogs(admin, "build", pair[0], pair[1]); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	after, _ := mem.ListAuditEvents(ctx)
	if len(before) != len(after) {
		t.Fatal("unavailable logs wrote audit")
	}
	forged := auth.WithPrincipal(ctx, auth.Principal{ID: "missing", Provider: domain.PrincipalRunner})
	if _, err = s.AppendHostingBuildLogs(forged, in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("unpersisted runner", err)
	}
}
