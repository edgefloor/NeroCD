package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

const supplementarySecret = "CANARY_PRIVATE"

type supplementaryAuditRepository struct {
	store.AuditRepository
	err   error
	calls int
}

func (r *supplementaryAuditRepository) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	return r.AuditRepository.CreateAuditEvent(ctx, event)
}

type supplementaryRunRepository struct {
	store.RunRepository
	err          error
	logs         int
	stateUpdates int
}

func (r *supplementaryRunRepository) CreateRunLog(ctx context.Context, event domain.RunLog) error {
	r.logs++
	if r.err != nil {
		return r.err
	}
	return r.RunRepository.CreateRunLog(ctx, event)
}

func (r *supplementaryRunRepository) UpdateRunWorkflowState(ctx context.Context, id string, state domain.WorkflowState) (domain.TaskRun, error) {
	r.stateUpdates++
	return r.RunRepository.UpdateRunWorkflowState(ctx, id, state)
}

type supplementaryRunnerRepository struct {
	store.RunnerRepository
	claims int
}

func (r *supplementaryRunnerRepository) ClaimRun(ctx context.Context, id string, now time.Time, ttl time.Duration, options ...store.MutationOption) (domain.ClaimedRun, error) {
	r.claims++
	return r.RunnerRepository.ClaimRun(ctx, id, now, ttl, options...)
}

// These tests are serial because slog's default logger is process-wide.
func captureSupplementaryWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func supplementaryWarnings(t *testing.T, output *bytes.Buffer, count int) []map[string]any {
	t.Helper()
	if strings.Contains(output.String(), supplementarySecret) {
		t.Fatal("supplementary warning exposed private data")
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var events []map[string]any
	for decoder.More() {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event["level"] != "WARN" {
			t.Fatalf("diagnostic level = %v", event["level"])
		}
		events = append(events, event)
	}
	if len(events) != count {
		t.Fatalf("warnings = %d, want %d: %s", len(events), count, output)
	}
	return events
}

func TestDeniedAuditWriteFailureWarnsWithoutChangingDenial(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			output := captureSupplementaryWarnings(t)
			service, mem := newOIDCAppService(t)
			audits := &supplementaryAuditRepository{AuditRepository: mem}
			if failed {
				audits.err = errors.New("password=" + supplementarySecret + "\nforged warning")
			}
			service.audit = audits
			ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Roles: []string{domain.RoleViewer}, Provider: domain.PrincipalLocal})
			_, err := service.RegisterRunner(ctx, RunnerInput{ID: supplementarySecret, Name: supplementarySecret})
			if !errors.Is(err, auth.ErrForbidden) || audits.calls != 1 {
				t.Fatalf("denial err=%v audit attempts=%d", err, audits.calls)
			}
			runners, err := mem.ListRunners(ctx)
			if err != nil || len(runners) != 0 {
				t.Fatalf("denied registration mutated runners: %v, %v", runners, err)
			}
			count := 0
			if failed {
				count = 1
			}
			events := supplementaryWarnings(t, output, count)
			if failed && (events[0]["action"] != "runner.register.denied" || events[0]["actor_id"] != "usr_viewer") {
				t.Fatalf("missing denial identifiers: %v", events)
			}
		})
	}
}

func TestDeniedAuditWarningBoundsActorIdentifier(t *testing.T) {
	for _, actorID := range []string{"", "viewer@example.invalid", "usr_\n" + supplementarySecret, strings.Repeat(supplementarySecret, 100)} {
		t.Run(actorID[:min(len(actorID), 20)], func(t *testing.T) {
			output := captureSupplementaryWarnings(t)
			audits := &supplementaryAuditRepository{err: errors.New(supplementarySecret)}
			service := &Service{audit: audits}
			service.writeDeniedAudit(t.Context(), auth.Principal{ID: actorID}, "runner.list.denied", "runners", nil)
			events := supplementaryWarnings(t, output, 1)
			if events[0]["actor_id"] != "unavailable" || len(events[0]) != 5 || audits.calls != 1 {
				t.Fatalf("unsafe denial diagnostic: %v, audit attempts=%d", events, audits.calls)
			}
		})
	}
}

func TestOIDCDenialAuditWriteFailureWarnsWithoutChangingDenial(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			output := captureSupplementaryWarnings(t)
			service, mem := newOIDCAppService(t)
			audits := &supplementaryAuditRepository{AuditRepository: mem}
			if failed {
				audits.err = errors.New("token=" + supplementarySecret)
			}
			service.audit = audits
			provider := &capturingOIDCProvider{fakeOIDCProvider: fakeOIDCProvider{issuer: "https://idp.example", clientID: "client", exchangeErr: errors.New(supplementarySecret)}}
			if err := service.ConfigureOIDC(provider); err != nil {
				t.Fatal(err)
			}
			started, err := service.StartOIDCLogin(t.Context(), "/runs", "source")
			if err != nil {
				t.Fatal(err)
			}
			created, redirect, bound, err := service.CompleteOIDCLogin(t.Context(), supplementarySecret, provider.state, started.Verifier, SessionCreateMetadata{SourceIP: supplementarySecret, UserAgent: supplementarySecret})
			if !errors.Is(err, ErrOIDCFailed) || !bound || redirect != "/runs" || created.Token != "" || created.Session.ID != "" || audits.calls != 1 {
				t.Fatalf("denial err=%v bound=%t redirect=%q audit attempts=%d", err, bound, redirect, audits.calls)
			}
			if _, _, bound, err := service.CompleteOIDCLogin(t.Context(), supplementarySecret, provider.state, started.Verifier, SessionCreateMetadata{}); !errors.Is(err, ErrOIDCFailed) || bound || audits.calls != 1 {
				t.Fatalf("denied flow was reusable: err=%v bound=%t audit attempts=%d", err, bound, audits.calls)
			}
			count := 0
			if failed {
				count = 1
			}
			events := supplementaryWarnings(t, output, count)
			if failed && (events[0]["action"] != "session.create.denied" || events[0]["reason"] != "provider_rejected" || events[0]["stage"] != "create_event") {
				t.Fatalf("missing OIDC diagnostic context: %v", events)
			}
		})
	}
}

func TestClaimSupplementaryLogFailurePreservesLeaseAndWorkflow(t *testing.T) {
	for _, workflow := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			name := map[bool]string{false: "task", true: "workflow"}[workflow] + "/" + map[bool]string{false: "success", true: "failure"}[failed]
			t.Run(name, func(t *testing.T) {
				output := captureSupplementaryWarnings(t)
				service, mem := newOIDCAppService(t)
				now := time.Now().UTC()
				if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_logs", Status: domain.RunnerActive, Capabilities: []string{domain.RunTypeShell}, LastHeartbeatAt: now}); err != nil {
					t.Fatal(err)
				}
				run := domain.TaskRun{ID: "run_logs", Status: domain.RunQueued, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}, StartedAt: now}
				if workflow {
					run.Workflow = domain.Workflow{Steps: []domain.WorkflowStep{{ID: supplementarySecret, RunSpec: run.RunSpec}}}
					run.WorkflowState = initialWorkflowState(run.Workflow)
				}
				if _, err := mem.CreateRun(t.Context(), run); err != nil {
					t.Fatal(err)
				}
				runs := &supplementaryRunRepository{RunRepository: mem}
				if failed {
					runs.err = errors.New("credential=" + supplementarySecret)
				}
				runners := &supplementaryRunnerRepository{RunnerRepository: mem}
				service.runs, service.runners = runs, runners
				ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner_logs", Roles: []string{domain.RoleRunner}, Provider: domain.PrincipalRunner})
				claim, err := service.ClaimRun(ctx)
				if err != nil || claim.Run.ID != run.ID || claim.Lease.ID == "" || claim.Lease.Attempt != 1 || claim.PrimitivePlan.RunID != run.ID || runners.claims != 1 {
					t.Fatalf("claim changed after supplementary write: %#v err=%v claims=%d", claim, err, runners.claims)
				}
				lease, err := mem.ActiveLeaseForRun(ctx, run.ID)
				if err != nil || lease.ID != claim.Lease.ID || lease.Fence != claim.Lease.Fence {
					t.Fatalf("authoritative lease changed: %#v, %v", lease, err)
				}
				stored, err := mem.GetRun(ctx, run.ID)
				if err != nil || stored.Status != domain.RunRunning {
					t.Fatalf("authoritative run changed: %#v, %v", stored, err)
				}
				logCount, updates := 1, 0
				if workflow {
					logCount, updates = 2, 1
					if stored.WorkflowState.CurrentStepID != supplementarySecret || stored.WorkflowState.Steps[0].Status != domain.WorkflowRunning {
						t.Fatalf("workflow transition lost: %#v", stored.WorkflowState)
					}
				}
				if runs.logs != logCount || runs.stateUpdates != updates {
					t.Fatalf("duplicate or missing work: logs=%d updates=%d", runs.logs, runs.stateUpdates)
				}
				audits, err := mem.ListAuditEvents(ctx)
				if err != nil || len(audits) != 1 || audits[0].Action != "runner.claim" {
					t.Fatalf("claim audit changed: %#v, %v", audits, err)
				}
				count := 0
				if failed {
					count = logCount
				}
				for _, event := range supplementaryWarnings(t, output, count) {
					if event["run_id"] != run.ID || len(event) > 7 {
						t.Fatalf("unbounded or missing run context: %v", event)
					}
				}
				if strings.Contains(output.String(), lease.Fence) {
					t.Fatal("warning exposed lease fence")
				}
			})
		}
	}
}
