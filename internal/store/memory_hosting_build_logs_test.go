package store

import (
	"errors"
	"nerocd/internal/domain"
	"testing"
	"time"
)

func TestMemoryHostingBuildLogsUnavailableAndGenericDenial(t *testing.T) {
	s := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash")
	ctx := t.Context()
	before := len(s.logs)
	if _, err := s.AppendHostingBuildLogs(ctx, "runner", domain.HostingBuildLogBatch{}); !errors.Is(err, ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.ListHostingBuildLogs(ctx, "usr_bootstrap", "build", 0, 200); !errors.Is(err, ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	for _, spec := range []domain.RunSpec{{Type: domain.HostingBuildTaskType, HostingBuildID: "build"}, {Type: "shell", Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{RunSpec: domain.RunSpec{Type: "shell", HostingBuildID: "build"}}}}}} {
		s.runs = append(s.runs, domain.TaskRun{ID: "run_build_logs", RunSpec: spec})
		log := domain.RunLog{RunID: "run_build_logs", EventKey: "key", RequestedSequence: 1, LeaseID: "lease", Attempt: 1}
		if err := s.CreateRunLog(ctx, log); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if _, err := s.CreateRunLogsForLease(ctx, []domain.RunLog{log}, log.RunID, "runner", "lease", 1, "fence", time.Now()); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		log.EventKey = ""
		if _, err := s.CreateRunLogForLease(ctx, log, "runner", "lease", 1, "fence", time.Now()); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		s.runs = s.runs[:len(s.runs)-1]
	}
	if len(s.logs) != before {
		t.Fatal("unavailable/denied log wrote data")
	}
}
