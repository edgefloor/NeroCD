package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresIntegrationSQLCRoundTripsPaginationAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "sqlc")
	defer pg.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	project := domain.Project{ID: "proj_sqlc_roundtrip", Name: "sqlc round trip", Description: "native pgx", CreatedAt: now}
	if _, err := pg.CreateProject(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	template := domain.TaskTemplate{
		ID: "tpl_sqlc_roundtrip", ProjectID: project.ID, Name: "round trip", Kind: "shell",
		RunSpec: domain.RunSpec{
			Type: "shell", Inputs: map[string]any{"command": "printf sqlc", "nested": map[string]any{"enabled": true}},
			Process:   &domain.ProcessSpec{Command: []string{"printf", "sqlc"}, Environment: map[string]string{"MODE": "test"}, TimeoutSeconds: 17},
			Artifacts: []domain.ArtifactSpec{{Name: "report", Path: "report.json", Required: true}},
		},
		Workflow:   domain.Workflow{Steps: []domain.WorkflowStep{{ID: "execute", Name: "Execute", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "printf sqlc"}}}}},
		RunnerTags: []string{"linux", "arm64", "tag,with,commas"}, RequiresAck: true,
	}
	createdTemplate, err := pg.CreateTemplate(ctx, template)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	loadedTemplate, err := pg.GetTemplate(ctx, template.ID)
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if !slices.Equal(createdTemplate.RunnerTags, template.RunnerTags) || !slices.Equal(loadedTemplate.RunnerTags, template.RunnerTags) {
		t.Fatalf("template tags round trip = %#v / %#v, want %#v", createdTemplate.RunnerTags, loadedTemplate.RunnerTags, template.RunnerTags)
	}
	if loadedTemplate.RunSpec.Process == nil || !slices.Equal(loadedTemplate.RunSpec.Process.Command, template.RunSpec.Process.Command) || loadedTemplate.RunSpec.Process.Environment["MODE"] != "test" || loadedTemplate.Workflow.Steps[0].ID != "execute" {
		t.Fatalf("template JSON round trip = %#v", loadedTemplate)
	}

	finishedAt := now.Add(5 * time.Minute)
	templateID := template.ID
	runs := []domain.TaskRun{
		{ID: "run_sqlc_old", ProjectID: project.ID, TemplateID: &templateID, RunSpec: template.RunSpec, Workflow: template.Workflow, WorkflowState: domain.WorkflowState{Steps: []domain.WorkflowStepState{{ID: "execute", Name: "Execute", Status: "pending"}}}, RunnerTags: template.RunnerTags, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-2 * time.Minute)},
		{ID: "run_sqlc_middle", ProjectID: project.ID, TemplateID: nil, RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"nullable": true}}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-time.Minute), FinishedAt: nil},
		{ID: "run_sqlc_new", ProjectID: project.ID, TemplateID: &templateID, RunSpec: template.RunSpec, Workflow: template.Workflow, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{"linux"}, Status: domain.RunSucceeded, RequestedBy: "usr_bootstrap", StartedAt: now, FinishedAt: &finishedAt},
	}
	for _, run := range runs {
		created, err := pg.CreateRun(ctx, run)
		if err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
		if run.ID == "run_sqlc_middle" && (created.TemplateID != nil || created.FinishedAt != nil) {
			t.Fatalf("nullable fields round trip = template %#v finished %#v", created.TemplateID, created.FinishedAt)
		}
		if !slices.Equal(created.RunnerTags, run.RunnerTags) || created.RunSpec.Type != run.RunSpec.Type {
			t.Fatalf("run JSON/TEXT[] round trip for %s = %#v", run.ID, created)
		}
	}
	page, err := pg.ListRunsPage(ctx, project.ID, store.Page{Enabled: true, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("list run page: %v", err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.Items[0].ID != "run_sqlc_middle" || page.Items[1].ID != "run_sqlc_old" {
		t.Fatalf("run page = %#v", page)
	}

	for _, id := range []string{"log_sqlc_one", "log_sqlc_two"} {
		if err := pg.CreateRunLog(ctx, domain.RunLog{ID: id, RunID: "run_sqlc_old", Sequence: 1, Stream: domain.LogSystem, Message: id, CreatedAt: now}); err != nil {
			t.Fatalf("create ordered log %s: %v", id, err)
		}
	}
	logPage, err := pg.ListRunLogsPage(ctx, "run_sqlc_old", store.Page{Enabled: true, Limit: 10})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if logPage.Total != 2 || len(logPage.Items) != 2 || logPage.Items[0].Sequence != 1 || logPage.Items[1].Sequence != 2 {
		t.Fatalf("duplicate requested log sequence was not allocated safely: %#v", logPage)
	}

	rollbackRun := domain.TaskRun{ID: "run_sqlc_rollback", ProjectID: project.ID, RunSpec: domain.RunSpec{Type: "shell"}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	_, err = pg.CreateRunRequest(ctx, rollbackRun,
		domain.RunLog{ID: "log_sqlc_rollback", RunID: rollbackRun.ID, Sequence: 1, Stream: "invalid", Message: "must roll back", CreatedAt: now}, nil,
		domain.AuditEvent{ID: "audit_sqlc_rollback", ActorID: "usr_bootstrap", Action: "run.create", TargetID: rollbackRun.ID, Metadata: map[string]any{"test": true}, CreatedAt: now})
	if err == nil {
		t.Fatal("CreateRunRequest with invalid later log unexpectedly succeeded")
	}
	for table, id := range map[string]string{"task_runs": rollbackRun.ID, "run_logs": "log_sqlc_rollback", "audit_events": "audit_sqlc_rollback"} {
		var count int
		if err := database.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id=$1`, id).Scan(&count); err != nil {
			t.Fatalf("count rolled-back %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s row %s persisted after composite rollback", table, id)
		}
	}
}

func TestPostgresIntegrationRunnerReplayIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "replay")
	defer pg.Close()

	now := time.Now().UTC()
	runnerID := "runner_replay"
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	secretBinding := domain.SecretBinding{Name: "database-password", Provider: domain.ProviderRunnerFile, Reference: "database-password", Target: "env:DATABASE_PASSWORD", Required: true, Version: "v1", Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	run := domain.TaskRun{ID: "run_replay", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{secretBinding}}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	if _, err := pg.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secretAccess := domain.SecretAccessRequest{
		AccessID: "secret_access_0123456789abcdef0123456789abcdef", RunnerID: runnerID, RunID: run.ID,
		LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence,
		Binding: "database-password", Provider: domain.ProviderRunnerFile, Version: "v1", RequestedAt: now,
	}
	firstAccess, err := pg.AuthorizeSecretAccess(ctx, secretAccess)
	if err != nil {
		t.Fatal(err)
	}
	grantJSON, _ := json.Marshal(firstAccess)
	if strings.Contains(string(grantJSON), "fingerprint") || strings.Contains(string(grantJSON), strings.Repeat("a", 64)) {
		t.Fatalf("secret access grant leaked configured fingerprint: %s", grantJSON)
	}
	replayedAccess, err := pg.AuthorizeSecretAccess(ctx, secretAccess)
	if err != nil || replayedAccess != firstAccess {
		t.Fatalf("secret access replay first=%#v replay=%#v err=%v", firstAccess, replayedAccess, err)
	}
	conflictingAccess := secretAccess
	conflictingAccess.Version = "v2"
	if _, err := pg.AuthorizeSecretAccess(ctx, conflictingAccess); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("secret access conflict error=%v", err)
	}
	unknownAccess := secretAccess
	unknownAccess.AccessID = "secret_access_1123456789abcdef0123456789abcdef"
	unknownAccess.Binding = "not-in-run-spec"
	if _, err := pg.AuthorizeSecretAccess(ctx, unknownAccess); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown binding error=%v", err)
	}
	var secretAuditCount int
	var secretAuditText string
	if err := database.QueryRow(ctx, `SELECT count(*), min(metadata::text) FROM audit_events WHERE id=$1`, secretAccess.AccessID).Scan(&secretAuditCount, &secretAuditText); err != nil {
		t.Fatal(err)
	}
	if secretAuditCount != 1 {
		t.Fatalf("secret access audit count=%d", secretAuditCount)
	}
	for _, forbidden := range []string{secretAccess.Fence, "fingerprint", strings.Repeat("a", 64), "logical-file-reference", "env:DATABASE_PASSWORD", "secret-value"} {
		if strings.Contains(secretAuditText, forbidden) {
			t.Fatalf("secret access audit leaked %q: %s", forbidden, secretAuditText)
		}
	}
	events := []domain.RunLog{
		{ID: "log_replay_one", RunID: run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, EventKey: "event_replay_one", Sequence: 4, RequestedSequence: 4, Stream: domain.LogStdout, Message: "one", CreatedAt: now},
		{ID: "log_replay_two", RunID: run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, EventKey: "event_replay_two", Sequence: 5, RequestedSequence: 5, Stream: domain.LogStdout, Message: "two", CreatedAt: now},
	}
	first, err := pg.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := pg.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(replayed) != 2 || first[0].ID != replayed[0].ID || first[1].ID != replayed[1].ID || first[0].Sequence >= first[1].Sequence {
		t.Fatalf("event replay results first=%#v replayed=%#v", first, replayed)
	}
	if _, err := pg.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, "wrong-fence", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exact event replay with wrong fence error = %v, want ErrNotFound", err)
	}
	conflict := append([]domain.RunLog(nil), events...)
	conflict[0].Message = "conflicting reuse"
	if _, err := pg.CreateRunLogsForLease(ctx, conflict, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("event key conflict error = %v", err)
	}
	var eventCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1 AND event_key IS NOT NULL`, run.ID).Scan(&eventCount); err != nil || eventCount != 2 {
		t.Fatalf("event count=%d err=%v", eventCount, err)
	}
	rollbackFinished := now.Add(time.Second)
	if _, err := pg.CompleteLeaseRequest(ctx, claim.Lease.ID, runnerID, domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, "completion_rollback", rollbackFinished, domain.RunSucceeded, &rollbackFinished, nil,
		[]domain.RunLog{{ID: "log_completion_rollback", RunID: run.ID, Sequence: 3, Stream: "invalid", Message: "rollback", CreatedAt: now}},
		domain.AuditEvent{ID: "audit_completion_rollback", ActorID: runnerID, Action: "runner.complete", TargetID: run.ID, Metadata: map[string]any{}, CreatedAt: now}); err == nil {
		t.Fatal("completion with invalid later log unexpectedly committed")
	}
	var rollbackLeaseStatus, rollbackRunStatus string
	var rollbackCompletionKey *string
	var rollbackAudit int
	if err := database.QueryRow(ctx, `SELECT status,completion_key FROM run_leases WHERE id=$1`, claim.Lease.ID).Scan(&rollbackLeaseStatus, &rollbackCompletionKey); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, run.ID).Scan(&rollbackRunStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id='audit_completion_rollback'`).Scan(&rollbackAudit); err != nil {
		t.Fatal(err)
	}
	if rollbackLeaseStatus != domain.LeaseActive || rollbackRunStatus != domain.RunRunning || rollbackCompletionKey != nil || rollbackAudit != 0 {
		t.Fatalf("completion rollback state lease=%s key=%v run=%s audit=%d", rollbackLeaseStatus, rollbackCompletionKey, rollbackRunStatus, rollbackAudit)
	}

	service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)
	runnerCtx := auth.WithPrincipal(ctx, auth.Principal{ID: runnerID, Provider: domain.PrincipalRunner})
	completed, err := service.CompleteLease(runnerCtx, claim.Lease.ID, domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, "completion_replay")
	if err != nil {
		t.Fatal(err)
	}
	var terminalLogs, terminalAudit int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1 AND message LIKE 'Runner completed lease%'`, run.ID).Scan(&terminalLogs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='runner.complete'`, run.ID).Scan(&terminalAudit); err != nil {
		t.Fatal(err)
	}
	retried, err := service.CompleteLease(runnerCtx, claim.Lease.ID, domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, "completion_replay")
	if err != nil {
		t.Fatal(err)
	}
	if completed.ID != retried.ID || completed.CompletedAt == nil || retried.CompletedAt == nil || !completed.CompletedAt.Equal(*retried.CompletedAt) {
		t.Fatalf("completion replay first=%#v retry=%#v", completed, retried)
	}
	var afterLogs, afterAudit int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1 AND message LIKE 'Runner completed lease%'`, run.ID).Scan(&afterLogs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='runner.complete'`, run.ID).Scan(&afterAudit); err != nil {
		t.Fatal(err)
	}
	if terminalLogs != 1 || terminalAudit != 1 || afterLogs != terminalLogs || afterAudit != terminalAudit {
		t.Fatalf("terminal mutation counts before=%d/%d after=%d/%d", terminalLogs, terminalAudit, afterLogs, afterAudit)
	}
	if _, err := service.CompleteLease(runnerCtx, claim.Lease.ID, domain.RunFailed, claim.Lease.Attempt, claim.Lease.Fence, "completion_conflict"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("completion key/status conflict error = %v", err)
	}
	if _, err := pg.AuthorizeSecretAccess(ctx, secretAccess); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal secret access replay error=%v", err)
	}

	// Exact committed events remain recognizable after authority is terminal,
	// while the same stale capability cannot authorize a first new mutation.
	if _, err := pg.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now); err != nil {
		t.Fatalf("exact stale event acknowledgement: %v", err)
	}
	newStale := []domain.RunLog{{ID: "log_stale_new", RunID: run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, EventKey: "event_stale_new", Sequence: 6, RequestedSequence: 6, Stream: domain.LogStdout, Message: "must not persist", CreatedAt: now}}
	if _, err := pg.CreateRunLogsForLease(ctx, newStale, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("first stale event error = %v", err)
	}
	if _, err := service.CompleteLease(runnerCtx, claim.Lease.ID, domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, "completion_stale_new"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("first stale completion error = %v", err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1 AND event_key='event_stale_new'`, run.ID).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("stale event count=%d err=%v", eventCount, err)
	}
}

func TestPostgresIntegrationSecretAccessUsesOnlyCurrentWorkflowStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "secret_workflow")
	defer pg.Close()
	now := time.Now().UTC()
	runnerID := "runner_secret_workflow"
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	makeBinding := func(name, version, fingerprint string) domain.SecretBinding {
		return domain.SecretBinding{Name: name, Provider: domain.ProviderRunnerFile, Reference: name, Target: "env:" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")), Required: true, Version: version, Fingerprint: "sha256:" + strings.Repeat(fingerprint, 64)}
	}
	top := makeBinding("top-secret", "top-v1", "a")
	first := makeBinding("first-secret", "first-v1", "b")
	second := makeBinding("second-secret", "second-v1", "c")
	run := domain.TaskRun{
		ID: "run_secret_workflow", ProjectID: "proj_platform", Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now, RunnerTags: []string{"local"},
		RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{top}},
		Workflow: domain.Workflow{Steps: []domain.WorkflowStep{
			{ID: "first", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{first}}},
			{ID: "second", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{second}}},
		}}, WorkflowState: domain.WorkflowState{CurrentStepID: "first"},
	}
	if _, err := pg.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := func(accessID string, binding domain.SecretBinding) domain.SecretAccessRequest {
		return domain.SecretAccessRequest{AccessID: accessID, RunnerID: runnerID, RunID: run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence, Binding: binding.Name, Provider: binding.Provider, Version: binding.Version, RequestedAt: now}
	}
	firstAccessID := "secret_access_50000000000000000000000000000000"
	if _, err := pg.AuthorizeSecretAccess(ctx, request(firstAccessID, first)); err != nil {
		t.Fatalf("current first step: %v", err)
	}
	for accessID, candidate := range map[string]domain.SecretBinding{
		"secret_access_60000000000000000000000000000000": top,
		"secret_access_70000000000000000000000000000000": second,
	} {
		if _, err := pg.AuthorizeSecretAccess(ctx, request(accessID, candidate)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("noncurrent binding %s error=%v", candidate.Name, err)
		}
	}
	run.WorkflowState.CurrentStepID = ""
	if _, err := pg.UpdateRunWorkflowState(ctx, run.ID, run.WorkflowState); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.AuthorizeSecretAccess(ctx, request("secret_access_75000000000000000000000000000000", top)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workflow without current step must fail closed, error=%v", err)
	}
	if _, err := pg.AuthorizeSecretAccess(ctx, request(firstAccessID, first)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exact prior audit replay without current step error=%v", err)
	}
	run.WorkflowState.CurrentStepID = "second"
	if _, err := pg.UpdateRunWorkflowState(ctx, run.ID, run.WorkflowState); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.AuthorizeSecretAccess(ctx, request("secret_access_80000000000000000000000000000000", first)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("prior step after transition error=%v", err)
	}
	if _, err := pg.AuthorizeSecretAccess(ctx, request(firstAccessID, first)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exact prior audit replay after transition error=%v", err)
	}
	secondGrant, err := pg.AuthorizeSecretAccess(ctx, request("secret_access_90000000000000000000000000000000", second))
	if err != nil {
		t.Fatalf("current second step: %v", err)
	}
	grantJSON, _ := json.Marshal(secondGrant)
	if strings.Contains(string(grantJSON), "fingerprint") || strings.Contains(string(grantJSON), strings.Repeat("c", 64)) {
		t.Fatalf("workflow grant leaked fingerprint: %s", grantJSON)
	}
	var unsafeAuditCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='secret.access' AND (metadata ? 'fingerprint' OR metadata::text LIKE $1 OR metadata::text LIKE $2)`, "%"+strings.Repeat("b", 64)+"%", "%"+strings.Repeat("c", 64)+"%").Scan(&unsafeAuditCount); err != nil {
		t.Fatal(err)
	}
	if unsafeAuditCount != 0 {
		t.Fatalf("workflow audit persisted fingerprint count=%d", unsafeAuditCount)
	}
	var firstAuditCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, firstAccessID).Scan(&firstAuditCount); err != nil {
		t.Fatal(err)
	}
	if firstAuditCount != 1 {
		t.Fatalf("first access audit count=%d, want 1", firstAuditCount)
	}
}

func TestPostgresIntegrationRunnerEnrollmentOneTimeReplayAndRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "runner_enrollment")
	defer pg.Close()
	now := time.Now().UTC()
	create := func(id, tokenHash, runnerID string, ttl time.Duration) domain.RunnerEnrollment {
		t.Helper()
		enrollment := domain.RunnerEnrollment{ID: id, TokenHash: tokenHash, RunnerID: runnerID, RunnerName: runnerID, Tags: []string{"linux"}, Capabilities: []string{"shell"}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(ttl)}
		created, err := pg.CreateRunnerEnrollment(ctx, enrollment, domain.AuditEvent{ID: "aud_" + id, ActorID: "usr_bootstrap", Action: "runner.enrollment.create", TargetID: id, Metadata: map[string]any{"enrollment_id": id, "runner_id": runnerID}, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	enrollment := create("enroll_pg_one", strings.Repeat("1", 64), "runner_pg_enrolled", time.Minute)
	consume := domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "enroll_consume_0123456789abcdef0123456789abcdef", CredentialHash: strings.Repeat("2", 64)}
	first, err := pg.ConsumeRunnerEnrollment(ctx, consume, domain.AuditEvent{ID: "aud_enroll_pg_consume", Action: "runner.enrollment.consume", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := pg.ConsumeRunnerEnrollment(ctx, consume, domain.AuditEvent{ID: "aud_enroll_pg_replay", Action: "runner.enrollment.consume", CreatedAt: now})
	if err != nil || replay.ID != first.ID || !replay.RegisteredAt.Equal(first.RegisteredAt) {
		t.Fatalf("exact replay=(%+v,%v), first=%+v", replay, err, first)
	}
	conflict := consume
	conflict.CredentialHash = strings.Repeat("3", 64)
	if _, err := pg.ConsumeRunnerEnrollment(ctx, conflict, domain.AuditEvent{ID: "aud_conflict"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflicting consume error=%v", err)
	}
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: first.ID, Name: "overwrite", Tags: []string{"other"}, Capabilities: []string{"other"}, TokenHash: strings.Repeat("f", 64), Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate registration error=%v", err)
	}
	var consumeAudits, unsafeAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='runner.enrollment.consume'), count(*) FILTER (WHERE metadata::text LIKE $1 OR metadata::text LIKE $2 OR metadata::text LIKE $3) FROM audit_events`, "%"+consume.TokenHash+"%", "%"+consume.CredentialHash+"%", "%"+consume.RequestID+"%").Scan(&consumeAudits, &unsafeAudits); err != nil {
		t.Fatal(err)
	}
	if consumeAudits != 1 || unsafeAudits != 0 {
		t.Fatalf("consume audits=%d unsafe=%d", consumeAudits, unsafeAudits)
	}

	revoked := create("enroll_pg_revoked", strings.Repeat("4", 64), "runner_pg_revoked", time.Minute)
	if _, err := pg.RevokeRunnerEnrollment(ctx, revoked.ID, domain.AuditEvent{ID: "aud_pg_revoke", ActorID: "usr_bootstrap", Action: "runner.enrollment.revoke", TargetID: revoked.ID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: revoked.TokenHash, RequestID: consume.RequestID, CredentialHash: strings.Repeat("5", 64)}, domain.AuditEvent{ID: "aud_revoked_consume"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked consume error=%v", err)
	}

	expired := create("enroll_pg_expired", strings.Repeat("6", 64), "runner_pg_expired", 20*time.Millisecond)
	if _, err := database.Exec(ctx, `SELECT pg_sleep(0.04)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: expired.TokenHash, RequestID: consume.RequestID, CredentialHash: strings.Repeat("7", 64)}, domain.AuditEvent{ID: "aud_expired_consume"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired consume error=%v", err)
	}

	racing := create("enroll_pg_race", strings.Repeat("8", 64), "runner_pg_race", time.Minute)
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			requestID := fmt.Sprintf("enroll_consume_%032x", index+10)
			credentialHash := strings.Repeat(strconv.Itoa(index+8), 64)
			_, consumeErr := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: racing.TokenHash, RequestID: requestID, CredentialHash: credentialHash}, domain.AuditEvent{ID: fmt.Sprintf("aud_race_%d", index), Action: "runner.enrollment.consume", CreatedAt: now})
			results <- consumeErr
		}()
	}
	winners, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Fatalf("race consume error=%v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("race winners=%d conflicts=%d", winners, conflicts)
	}

	rollback := create("enroll_pg_rollback", strings.Repeat("a", 64), "runner_pg_rollback", time.Minute)
	duplicateAudit := domain.AuditEvent{ID: "aud_enroll_pg_duplicate", ActorID: "usr_bootstrap", Action: "test.seed", TargetID: rollback.ID, Metadata: map[string]any{}, CreatedAt: now}
	if err := pg.CreateAuditEvent(ctx, duplicateAudit); err != nil {
		t.Fatal(err)
	}
	rollbackConsume := domain.RunnerEnrollmentConsume{TokenHash: rollback.TokenHash, RequestID: "enroll_consume_abcdefabcdefabcdefabcdefabcdefab", CredentialHash: strings.Repeat("b", 64)}
	if _, err := pg.ConsumeRunnerEnrollment(ctx, rollbackConsume, domain.AuditEvent{ID: duplicateAudit.ID, Action: "runner.enrollment.consume", CreatedAt: now}); err == nil {
		t.Fatal("consume with a later audit conflict unexpectedly committed")
	}
	var used bool
	var runnerCount int
	if err := database.QueryRow(ctx, `SELECT used_at IS NOT NULL, (SELECT count(*) FROM runners WHERE id=$2) FROM runner_enrollments WHERE id=$1`, rollback.ID, rollback.RunnerID).Scan(&used, &runnerCount); err != nil {
		t.Fatal(err)
	}
	if used || runnerCount != 0 {
		t.Fatalf("failed composite consume persisted used=%t runner_count=%d", used, runnerCount)
	}
}

func TestPostgresIntegrationControlPlanePrimitives(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	schema := "nerocd_test_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	migrationDB, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, migrationDB); err != nil {
		migrationDB.Close()
		t.Fatal(err)
	}
	migrationDB.Close()

	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)
	session, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	adminPrincipal, err := service.AuthenticateSessionToken(ctx, session.Token)
	if err != nil {
		t.Fatalf("authenticate admin session: %v", err)
	}
	if !hasRole(adminPrincipal, "system_admin") {
		t.Fatalf("admin principal roles = %#v, want system_admin", adminPrincipal.Roles)
	}

	adminCtx := auth.WithPrincipal(ctx, adminPrincipal)
	createdToken, err := service.CreateAPIToken(adminCtx, app.APITokenInput{Name: "Integration Bootstrap", Roles: []string{"system_admin"}})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	apiPrincipal, err := service.AuthenticateSessionToken(ctx, createdToken.Token)
	if err != nil {
		t.Fatalf("authenticate api token: %v", err)
	}
	if apiPrincipal.Provider != "api_token" || !hasRole(apiPrincipal, "system_admin") {
		t.Fatalf("api token principal = %#v", apiPrincipal)
	}

	apiCtx := auth.WithPrincipal(ctx, apiPrincipal)
	registered, err := service.RegisterRunner(apiCtx, app.RunnerInput{ID: "runner_pg", Name: "Postgres Runner", Tags: []string{"local"}, Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatalf("register runner with api token: %v", err)
	}
	runnerPrincipal, err := service.AuthenticateRunnerToken(ctx, registered.Token)
	if err != nil {
		t.Fatalf("authenticate runner token: %v", err)
	}

	run, err := pg.CreateRun(ctx, domain.TaskRun{
		ID:            "run_pg_json",
		ProjectID:     "proj_platform",
		RunSpec:       domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo pg"}, Process: &domain.ProcessSpec{Command: []string{"echo", "pg"}, TimeoutSeconds: 30}, Artifacts: []domain.ArtifactSpec{{Name: "stdout", Path: "stdout.txt", Required: true}}},
		Workflow:      domain.Workflow{Steps: []domain.WorkflowStep{{ID: "execute", Name: "Execute", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo pg"}}}}},
		WorkflowState: domain.WorkflowState{Steps: []domain.WorkflowStepState{{ID: "execute", Name: "Execute", Status: "pending"}}},
		RunnerTags:    []string{"local"},
		Status:        "queued",
		RequestedBy:   adminPrincipal.ID,
		StartedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create json run: %v", err)
	}
	if run.RunSpec.Process == nil || run.Workflow.Steps[0].ID != "execute" || run.WorkflowState.Steps[0].Status != "pending" {
		t.Fatalf("created run did not preserve JSON fields: %#v", run)
	}

	claimed, err := service.ClaimRun(auth.WithPrincipal(ctx, runnerPrincipal))
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if claimed.Run.ID != run.ID || claimed.Run.Status != "running" || claimed.PrimitivePlan.Process == nil {
		t.Fatalf("unexpected claimed run: %#v", claimed)
	}
	if claimed.Lease.Attempt != 1 || strings.TrimSpace(claimed.Lease.Fence) == "" {
		t.Fatalf("fresh claim authority = %#v, want attempt 1 and opaque fence", claimed.Lease)
	}
	if _, err := service.AppendRunLog(auth.WithPrincipal(ctx, runnerPrincipal), app.RunLogInput{RunID: run.ID, LeaseID: claimed.Lease.ID, Attempt: claimed.Lease.Attempt, Fence: claimed.Lease.Fence, EventKey: "event_pg_ok", Sequence: 1, Stream: "stdout", Message: "pg ok"}); err != nil {
		t.Fatalf("append run log: %v", err)
	}
	if _, err := service.CreateArtifact(auth.WithPrincipal(ctx, runnerPrincipal), app.ArtifactInput{RunID: run.ID, LeaseID: claimed.Lease.ID, Attempt: claimed.Lease.Attempt, Fence: claimed.Lease.Fence, Name: "stdout", Path: "stdout.txt", Found: true, Required: false, Size: 12, Kind: "file"}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, err := service.CompleteLease(auth.WithPrincipal(ctx, runnerPrincipal), claimed.Lease.ID, "succeeded", claimed.Lease.Attempt, claimed.Lease.Fence, "completion_pg_ok"); err != nil {
		t.Fatalf("complete lease: %v", err)
	}

	logs, err := pg.ListRunLogs(ctx, run.ID)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if !hasRunLogMessage(logs, "pg ok") {
		t.Fatalf("unexpected run logs: %#v", logs)
	}
	artifacts, err := pg.ListArtifacts(ctx, run.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != "stdout.txt" {
		t.Fatalf("unexpected artifacts: %#v", artifacts)
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	for _, action := range []string{"api_token.create", "runner.register", "runner.claim", "runner.complete"} {
		if !hasAuditAction(events, action) {
			t.Fatalf("missing audit action %s in %#v", action, events)
		}
	}

	revoked, err := service.RevokeAPIToken(adminCtx, app.RevokeAPITokenInput{TokenID: createdToken.APIToken.ID})
	if err != nil {
		t.Fatalf("revoke api token: %v", err)
	}
	if revoked.Status != "revoked" {
		t.Fatalf("revoked api token status = %q, want revoked", revoked.Status)
	}
	if _, err := service.AuthenticateSessionToken(ctx, createdToken.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked api token auth error = %v, want unauthenticated", err)
	}

	revokedRunner, err := service.RevokeRunnerToken(adminCtx, app.RunnerTokenInput{RunnerID: registered.Runner.ID})
	if err != nil {
		t.Fatalf("revoke runner token: %v", err)
	}
	if revokedRunner.Status != "revoked" {
		t.Fatalf("revoked runner status = %q, want revoked", revokedRunner.Status)
	}
	if _, err := service.AuthenticateRunnerToken(ctx, registered.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked runner token auth error = %v, want unauthenticated", err)
	}
}

func TestPostgresIntegrationTwoRunnerFencingAndBoundedClaim(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := "nerocd_fence_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	migrationDB, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, migrationDB); err != nil {
		migrationDB.Close()
		t.Fatal(err)
	}
	migrationDB.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)

	now := time.Now().UTC()
	for _, id := range []string{"runner_a", "runner_b"} {
		if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: id, Name: id, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	run := domain.TaskRun{ID: "run_fence", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell", Process: &domain.ProcessSpec{Command: []string{"echo", "fence"}}}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	if _, err := pg.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		claim domain.ClaimedRun
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, runnerID := range []string{"runner_a", "runner_b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			claim, err := pg.ClaimRun(ctx, id, time.Now().UTC(), 5*time.Second)
			results <- claimResult{claim: claim, err: err}
		}(runnerID)
	}
	close(start)
	wg.Wait()
	close(results)
	var first domain.ClaimedRun
	winners := 0
	for result := range results {
		if result.err == nil {
			winners++
			first = result.claim
			continue
		}
		if !errors.Is(result.err, store.ErrNotFound) {
			t.Fatalf("concurrent claim: %v", result.err)
		}
	}
	if winners != 1 || first.Lease.Attempt != 1 || first.Lease.Fence == "" {
		t.Fatalf("claim race winners=%d first=%#v", winners, first)
	}
	var activeCount int
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.run_leases WHERE run_id='run_fence' AND status='active'`).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("active lease count=%d err=%v", activeCount, err)
	}
	if _, err := adminDB.Exec(ctx, `UPDATE `+schema+`.run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.Lease.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ReapExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	secondRunner := "runner_a"
	if first.Lease.RunnerID == secondRunner {
		secondRunner = "runner_b"
	}
	second, err := pg.ClaimRun(ctx, secondRunner, time.Now().UTC(), 5*time.Second)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if second.Lease.Attempt != 2 || second.Lease.Fence == first.Lease.Fence {
		t.Fatalf("reassignment authority %#v after %#v", second.Lease, first.Lease)
	}
	runnerContext := func(runnerID string) context.Context {
		return auth.WithPrincipal(ctx, auth.Principal{ID: runnerID, Provider: domain.PrincipalRunner})
	}
	if _, err := pg.RenewLease(ctx, first.Lease.RunnerID, first.Lease.ID, first.Lease.Fence, first.Lease.Attempt, time.Now(), 5*time.Second); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale renew error=%v", err)
	}
	if _, err := pg.CreateRunLogForLease(ctx, domain.RunLog{ID: "log_stale", RunID: run.ID, Stream: domain.LogStdout, Message: "stale", CreatedAt: time.Now()}, first.Lease.RunnerID, first.Lease.ID, first.Lease.Attempt, first.Lease.Fence, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale log error=%v", err)
	}
	if err := pg.CreateArtifactForLease(ctx, domain.ArtifactRecord{ID: "art_stale", RunID: run.ID, LeaseID: first.Lease.ID, Name: "stale", Path: "stale", Kind: domain.ArtifactFile}, first.Lease.RunnerID, first.Lease.Attempt, first.Lease.Fence, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale artifact error=%v", err)
	}
	if _, err := service.CompleteLease(runnerContext(first.Lease.RunnerID), first.Lease.ID, domain.RunSucceeded, first.Lease.Attempt, first.Lease.Fence, "completion_stale_attempt"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale complete error=%v", err)
	}
	for name, capability := range map[string]struct {
		attempt int
		fence   string
	}{
		"wrong_attempt": {attempt: second.Lease.Attempt + 1, fence: second.Lease.Fence},
		"wrong_fence":   {attempt: second.Lease.Attempt, fence: second.Lease.Fence + "-wrong"},
	} {
		if _, err := service.CompleteLease(runnerContext(secondRunner), second.Lease.ID, domain.RunSucceeded, capability.attempt, capability.fence, "completion_wrong_"+name); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("%s completion error=%v", name, err)
		}
	}
	var activeStatus string
	var preCompletionLogs, preCompletionArtifacts, preCompletionAudit int
	if err := adminDB.QueryRow(ctx, `SELECT status FROM `+schema+`.run_leases WHERE id=$1`, second.Lease.ID).Scan(&activeStatus); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.run_logs WHERE run_id=$1`, run.ID).Scan(&preCompletionLogs); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.run_artifacts WHERE run_id=$1`, run.ID).Scan(&preCompletionArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.audit_events WHERE target_id=$1`, run.ID).Scan(&preCompletionAudit); err != nil {
		t.Fatal(err)
	}
	if activeStatus != domain.LeaseActive || preCompletionLogs != 0 || preCompletionArtifacts != 0 || preCompletionAudit != 0 {
		t.Fatalf("wrong capability mutated state: status=%s logs=%d artifacts=%d audit=%d", activeStatus, preCompletionLogs, preCompletionArtifacts, preCompletionAudit)
	}
	if _, err := pg.CreateRunLogForLease(ctx, domain.RunLog{ID: "log_second", RunID: run.ID, Stream: domain.LogStdout, Message: "second", CreatedAt: time.Now()}, secondRunner, second.Lease.ID, second.Lease.Attempt, second.Lease.Fence, time.Now()); err != nil {
		t.Fatalf("valid log: %v", err)
	}
	if err := pg.CreateArtifactForLease(ctx, domain.ArtifactRecord{ID: "art_second", RunID: run.ID, LeaseID: second.Lease.ID, Name: "second", Path: "second", Kind: domain.ArtifactFile}, secondRunner, second.Lease.Attempt, second.Lease.Fence, time.Now()); err != nil {
		t.Fatalf("valid artifact: %v", err)
	}
	if _, err := service.CompleteLease(runnerContext(secondRunner), second.Lease.ID, domain.RunSucceeded, second.Lease.Attempt, second.Lease.Fence, "completion_second_valid"); err != nil {
		t.Fatalf("valid complete: %v", err)
	}
	var firstStatus, secondStatus string
	if err := adminDB.QueryRow(ctx, `SELECT status FROM `+schema+`.run_leases WHERE id=$1`, first.Lease.ID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT status FROM `+schema+`.run_leases WHERE id=$1`, second.Lease.ID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != domain.LeaseExpired || secondStatus != domain.RunSucceeded {
		t.Fatalf("lease statuses first=%q second=%q", firstStatus, secondStatus)
	}
	var completedAudit int
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM `+schema+`.audit_events WHERE target_id=$1 AND action='runner.complete'`, run.ID).Scan(&completedAudit); err != nil || completedAudit != 1 {
		t.Fatalf("completion audit count=%d err=%v", completedAudit, err)
	}
	logs, err := pg.ListRunLogs(ctx, run.ID)
	if err != nil || len(logs) != 2 || logs[0].Sequence != 1 || logs[0].Message != "second" || logs[1].Sequence <= logs[0].Sequence || logs[1].Message != "Runner completed lease with status succeeded" || hasRunLogMessage(logs, "stale") {
		t.Fatalf("ordered logs=%#v err=%v", logs, err)
	}
	artifacts, err := pg.ListArtifacts(ctx, run.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].ID != "art_second" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}

	// Workflow/capability matching remains Go-owned. This documents the hard
	// bound: 8 batches of 32 incompatible runs do not cause unbounded locking.
	var boundedBase time.Time
	if err := adminDB.QueryRow(ctx, `SELECT claim_order_at FROM `+schema+`.task_runs WHERE id=$1`, run.ID).Scan(&boundedBase); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		id := fmt.Sprintf("run_incompatible_%03d", i)
		if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell"}, RunnerTags: []string{"other"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: boundedBase.Add(time.Duration(i+1) * time.Microsecond)}); err != nil {
			t.Fatalf("create incompatible %d: %v", i, err)
		}
	}
	if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: "run_after_bound", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell"}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: boundedBase.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	planRows, err := adminDB.Query(ctx, `EXPLAIN (COSTS OFF) SELECT id, claim_order_at FROM `+schema+`.task_runs
		WHERE status='queued' AND (claim_order_at, id) > ($1, $2)
		ORDER BY claim_order_at ASC, id ASC FOR UPDATE SKIP LOCKED LIMIT 32`, boundedBase, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cursorPlan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		cursorPlan.WriteString(line)
		cursorPlan.WriteByte('\n')
	}
	planRows.Close()
	plan := cursorPlan.String()
	if !strings.Contains(plan, "Index Scan using idx_task_runs_queued_claim_order_id") || !strings.Contains(plan, "Index Cond: (ROW(claim_order_at, id) >") || strings.Contains(plan, "Sort") || strings.Contains(plan, "Filter: (ROW(claim_order_at, id)") {
		t.Fatalf("cursor-present claim plan is not a direct ordered keyset index scan:\n%s", plan)
	}
	firstBounded, err := pg.ClaimRun(ctx, secondRunner, time.Now(), 5*time.Second)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("first bounded claim=%#v error=%v", firstBounded, err)
	}
	progressed, err := pg.ClaimRun(ctx, secondRunner, time.Now(), 5*time.Second)
	if err != nil {
		t.Fatalf("second bounded claim did not progress: %v", err)
	}
	if progressed.Run.ID != "run_after_bound" {
		t.Fatalf("bounded cursor claimed %q, want run_after_bound", progressed.Run.ID)
	}
	if _, err := service.CompleteLease(runnerContext(secondRunner), progressed.Lease.ID, domain.RunSucceeded, progressed.Lease.Attempt, progressed.Lease.Fence, "completion_progressed"); err != nil {
		t.Fatalf("complete post-bound run: %v", err)
	}
	if _, err := pg.ClaimRun(ctx, secondRunner, time.Now(), 5*time.Second); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("queue-end cursor reset error=%v", err)
	}
	if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: "run_before_cursor", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell"}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wrapped, err := pg.ClaimRun(ctx, secondRunner, time.Now(), 5*time.Second)
	if err != nil || wrapped.Run.ID != "run_before_cursor" {
		t.Fatalf("cursor did not wrap to earlier insert: run=%q err=%v", wrapped.Run.ID, err)
	}
}

func TestPostgresIntegrationSameRunnerClaimsSerializeCursor(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_same_runner_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	now := time.Now().UTC()
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: "runner_serial", Name: "serial", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := pg.CreateRun(ctx, raceRun(fmt.Sprintf("run_serial_%d", i), now.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan claimResultForTest, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claim, err := pg.ClaimRun(ctx, "runner_serial", time.Now().UTC(), 10*time.Second)
			results <- claimResultForTest{claim: claim, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claimedRuns := map[string]bool{}
	leaseIDs := map[string]bool{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("same-runner concurrent claim: %v", result.err)
		}
		if result.claim.Lease.Attempt != 1 || result.claim.Lease.Fence == "" {
			t.Fatalf("invalid claim authority: %#v", result.claim.Lease)
		}
		if claimedRuns[result.claim.Run.ID] || leaseIDs[result.claim.Lease.ID] {
			t.Fatalf("duplicate concurrent claim: %#v", result.claim)
		}
		claimedRuns[result.claim.Run.ID] = true
		leaseIDs[result.claim.Lease.ID] = true
	}
	if len(claimedRuns) != 2 || !claimedRuns["run_serial_1"] || !claimedRuns["run_serial_2"] {
		t.Fatalf("same-runner claimed runs = %v", claimedRuns)
	}
	var activeLeases int
	var cursorRunID *string
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE runner_id='runner_serial' AND status='active'`).Scan(&activeLeases); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT run_id FROM runner_claim_cursors WHERE runner_id='runner_serial'`).Scan(&cursorRunID); err != nil {
		t.Fatal(err)
	}
	if activeLeases != 2 || cursorRunID == nil || *cursorRunID != "run_serial_2" {
		t.Fatalf("serialized state active=%d cursor=%#v", activeLeases, cursorRunID)
	}
	if _, err := pg.ClaimRun(ctx, "runner_serial", time.Now().UTC(), 10*time.Second); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tail reset claim error=%v", err)
	}
	if err := database.QueryRow(ctx, `SELECT run_id FROM runner_claim_cursors WHERE runner_id='runner_serial'`).Scan(&cursorRunID); err != nil {
		t.Fatal(err)
	}
	if cursorRunID != nil {
		t.Fatalf("queue-tail cursor was not reset: %#v", cursorRunID)
	}
	if _, err := pg.CreateRun(ctx, raceRun("run_serial_wrapped", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	wrapped, err := pg.ClaimRun(ctx, "runner_serial", time.Now().UTC(), 10*time.Second)
	if err != nil || wrapped.Run.ID != "run_serial_wrapped" {
		t.Fatalf("wrapped claim run=%q err=%v", wrapped.Run.ID, err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE runner_id='runner_serial' AND status='active'`).Scan(&activeLeases); err != nil {
		t.Fatal(err)
	}
	if activeLeases != 3 {
		t.Fatalf("active lease count after wrap=%d, want 3", activeLeases)
	}
}

func TestPostgresIntegrationClaimAndWorkflowCompletionLockOrder(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_lock_order_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	const (
		completionApplication = "nerocd_lock_order_completion"
		claimApplication      = "nerocd_lock_order_claim"
		runBarrierKey         = int64(728190451923)
	)
	if _, err := database.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION hold_completion_at_run() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF current_setting('application_name', true) = '%s' THEN
				PERFORM pg_advisory_xact_lock(%d);
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER hold_completion_at_run BEFORE UPDATE ON task_runs
		FOR EACH ROW EXECUTE FUNCTION hold_completion_at_run()`, completionApplication, runBarrierKey)); err != nil {
		t.Fatal(err)
	}
	setupStore, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer setupStore.Close()
	completionStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, schemaURL, completionApplication))
	if err != nil {
		t.Fatal(err)
	}
	defer completionStore.Close()
	claimStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, schemaURL, claimApplication))
	if err != nil {
		t.Fatal(err)
	}
	defer claimStore.Close()

	now := time.Now().UTC()
	if _, err := setupStore.RegisterRunner(ctx, domain.Runner{ID: "runner_lock_order", Name: "lock-order", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	run := raceRun("run_lock_order", now)
	run.Workflow = domain.Workflow{Steps: []domain.WorkflowStep{
		{ID: "first", Name: "first", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}},
		{ID: "second", Name: "second", DependsOn: []string{"first"}, RunSpec: domain.RunSpec{Type: domain.RunTypeShell}},
	}}
	run.WorkflowState = domain.WorkflowState{CurrentStepID: "first", Steps: []domain.WorkflowStepState{
		{ID: "first", Name: "first", Status: domain.WorkflowRunning},
		{ID: "second", Name: "second", Status: domain.WorkflowPending},
	}}
	if _, err := setupStore.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	first, err := setupStore.ClaimRun(ctx, "runner_lock_order", now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, runBarrierKey); err != nil {
		t.Fatal(err)
	}
	completionResult := make(chan error, 1)
	go func() {
		workflowState := run.WorkflowState
		workflowState.Steps[0].Status = domain.RunSucceeded
		_, err := completionStore.CompleteLeaseRequest(ctx, first.Lease.ID, "runner_lock_order", domain.RunSucceeded, first.Lease.Attempt, first.Lease.Fence, "completion_lock_order", time.Now().UTC(), domain.RunQueued, nil, &workflowState, nil, domain.AuditEvent{
			ID: "audit_lock_order_completion", ActorID: "runner_lock_order", Action: "runner.complete", TargetID: run.ID, Metadata: map[string]any{}, CreatedAt: time.Now().UTC(),
		})
		completionResult <- err
	}()
	waitForPostgresLockWait(t, ctx, adminDB, completionApplication, "task_runs")
	if !postgresRowLocked(t, ctx, database, `SELECT id FROM task_runs WHERE id='run_lock_order' FOR UPDATE NOWAIT`) {
		t.Fatal("workflow completion reached its run barrier without holding the task run")
	}

	claimResult := make(chan claimResultForTest, 1)
	go func() {
		claim, err := claimStore.ClaimRun(ctx, "runner_lock_order", time.Now().UTC(), 10*time.Second)
		claimResult <- claimResultForTest{claim: claim, err: err}
	}()
	select {
	case result := <-claimResult:
		if !errors.Is(result.err, store.ErrNotFound) {
			t.Fatalf("claim while completion owns run error=%v claim=%#v", result.err, result.claim)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("claim waited on workflow completion; cursor/run ordering regressed")
	}
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-completionResult:
		if err != nil {
			t.Fatalf("workflow completion error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workflow completion did not finish after releasing run barrier")
	}
	second, err := claimStore.ClaimRun(ctx, "runner_lock_order", time.Now().UTC(), 10*time.Second)
	if err != nil {
		t.Fatalf("reclaim after cursor wrap: %v", err)
	}
	if second.Run.ID != run.ID || second.Lease.Attempt != first.Lease.Attempt+1 || second.Lease.Fence == first.Lease.Fence {
		t.Fatalf("reassignment=%#v after=%#v", second, first)
	}
	var active, succeeded int
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='active'), count(*) FILTER (WHERE status='succeeded') FROM run_leases WHERE run_id=$1`, run.ID).Scan(&active, &succeeded); err != nil {
		t.Fatal(err)
	}
	if active != 1 || succeeded != 1 {
		t.Fatalf("lease state active=%d succeeded=%d", active, succeeded)
	}
}

func TestPostgresIntegrationClaimMaintenanceIsBounded(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := "nerocd_bounded_maintenance_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	const (
		expectedExpiryBatch = 64 // Must match the store's transaction bound.
		expiredRunCount     = expectedExpiryBatch*2 + 1
	)
	now := time.Now().UTC()
	runners := []domain.Runner{
		{ID: "runner_expiry_owner", Name: "expiry-owner", Tags: []string{"expired"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now},
		{ID: "runner_bounded_claim", Name: "bounded-claim", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now},
		{ID: "runner_unrelated_stale_a", Name: "unrelated-a", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-time.Hour)},
		{ID: "runner_unrelated_stale_b", Name: "unrelated-b", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-time.Hour)},
		{ID: "runner_requested_stale", Name: "requested-stale", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-time.Hour)},
	}
	for _, runner := range runners {
		if _, err := pg.RegisterRunner(ctx, runner); err != nil {
			t.Fatalf("register %s: %v", runner.ID, err)
		}
	}
	for i := 0; i < expiredRunCount; i++ {
		runID := fmt.Sprintf("run_expiry_batch_%03d", i)
		run := raceRun(runID, now.Add(time.Duration(i)*time.Microsecond))
		run.RunnerTags = []string{"expired"}
		if _, err := pg.CreateRun(ctx, run); err != nil {
			t.Fatalf("create expired run %d: %v", i, err)
		}
		if _, err := database.Exec(ctx, `UPDATE task_runs SET status='running', runner_id='runner_expiry_owner' WHERE id=$1`, runID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(ctx, `INSERT INTO run_leases (id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES ($1,$2,'runner_expiry_owner','active',clock_timestamp()-interval '1 second',clock_timestamp()-interval '1 minute',1,$3)`, "lease_expiry_batch_"+runID, runID, "fence_expiry_batch_"+runID); err != nil {
			t.Fatal(err)
		}
	}
	for i, runID := range []string{"run_bounded_target_1", "run_bounded_target_2"} {
		if _, err := pg.CreateRun(ctx, raceRun(runID, now.Add(time.Duration(i+1)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	first, err := pg.ClaimRun(ctx, "runner_bounded_claim", now, 10*time.Second)
	if err != nil {
		t.Fatalf("bounded claim: %v", err)
	}
	if first.Run.ID != "run_bounded_target_1" {
		t.Fatalf("bounded claim selected %q, want compatible target", first.Run.ID)
	}
	var expiredLeases, activeExpiredLeases, queuedExpiredRuns, runningExpiredRuns, wronglyAssigned int
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='expired'), count(*) FILTER (WHERE status='active') FROM run_leases WHERE run_id LIKE 'run_expiry_batch_%'`).Scan(&expiredLeases, &activeExpiredLeases); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='queued'), count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE runner_id='runner_bounded_claim') FROM task_runs WHERE id LIKE 'run_expiry_batch_%'`).Scan(&queuedExpiredRuns, &runningExpiredRuns, &wronglyAssigned); err != nil {
		t.Fatal(err)
	}
	if expiredLeases != expectedExpiryBatch || activeExpiredLeases != expiredRunCount-expectedExpiryBatch || queuedExpiredRuns != expectedExpiryBatch || runningExpiredRuns != expiredRunCount-expectedExpiryBatch || wronglyAssigned != 0 {
		t.Fatalf("one claim maintenance expired_leases=%d active_leases=%d queued=%d running=%d wrongly_assigned=%d", expiredLeases, activeExpiredLeases, queuedExpiredRuns, runningExpiredRuns, wronglyAssigned)
	}
	for _, runnerID := range []string{"runner_unrelated_stale_a", "runner_unrelated_stale_b"} {
		var status string
		if err := database.QueryRow(ctx, `SELECT status FROM runners WHERE id=$1`, runnerID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != domain.RunnerActive {
			t.Fatalf("claim mass-updated unrelated runner %s to %q", runnerID, status)
		}
	}
	if _, err := pg.ClaimRun(ctx, "runner_requested_stale", now, 10*time.Second); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale requested runner claim error=%v", err)
	}
	var staleStatus string
	if err := database.QueryRow(ctx, `SELECT status FROM runners WHERE id='runner_requested_stale'`).Scan(&staleStatus); err != nil {
		t.Fatal(err)
	}
	if staleStatus != domain.RunnerStale {
		t.Fatalf("requested stale runner status=%q", staleStatus)
	}

	for call := 0; call < 3; call++ {
		if err := pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
			t.Fatalf("bounded reaper call %d: %v", call, err)
		}
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='expired'), count(*) FILTER (WHERE status='active') FROM run_leases WHERE run_id LIKE 'run_expiry_batch_%'`).Scan(&expiredLeases, &activeExpiredLeases); err != nil {
		t.Fatal(err)
	}
	if expiredLeases != expiredRunCount || activeExpiredLeases != 0 {
		t.Fatalf("reaper did not drain bounded batches: expired=%d active=%d", expiredLeases, activeExpiredLeases)
	}
	second, err := pg.ClaimRun(ctx, "runner_bounded_claim", time.Now().UTC(), 10*time.Second)
	if err != nil {
		t.Fatalf("claim after bounded drain: %v", err)
	}
	if second.Run.ID != "run_bounded_target_2" {
		t.Fatalf("post-drain claim selected %q, want second compatible target", second.Run.ID)
	}
}

func TestPostgresIntegrationRequeuedRunAdvancesPastDurableCursor(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := "nerocd_requeue_order_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	now := time.Now().UTC()
	for _, runnerID := range []string{"runner_requeue_owner", "runner_requeue_seeker"} {
		if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	target := raceRun("run_requeue_target", now.Add(-2*time.Minute))
	if _, err := pg.CreateRun(ctx, target); err != nil {
		t.Fatal(err)
	}
	firstTarget, err := pg.ClaimRun(ctx, "runner_requeue_owner", now, 2*time.Minute)
	if err != nil || firstTarget.Run.ID != target.ID {
		t.Fatalf("initial target claim run=%q err=%v", firstTarget.Run.ID, err)
	}
	anchor := raceRun("run_requeue_anchor", now.Add(-time.Minute))
	if _, err := pg.CreateRun(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	anchorClaim, err := pg.ClaimRun(ctx, "runner_requeue_seeker", now, 2*time.Minute)
	if err != nil || anchorClaim.Run.ID != anchor.ID {
		t.Fatalf("anchor claim run=%q err=%v", anchorClaim.Run.ID, err)
	}
	for i := 0; i < 8; i++ {
		run := raceRun(fmt.Sprintf("run_requeue_ahead_%02d", i), now.Add(-30*time.Second+time.Duration(i)*time.Second))
		if _, err := pg.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	var originalStartedAt time.Time
	if err := database.QueryRow(ctx, `SELECT started_at FROM task_runs WHERE id=$1`, target.ID).Scan(&originalStartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, firstTarget.Lease.ID); err != nil {
		t.Fatal(err)
	}
	if err := pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var targetOrder, anchorOrder time.Time
	if err := database.QueryRow(ctx, `SELECT target.claim_order_at, anchor.claim_order_at FROM task_runs target JOIN task_runs anchor ON anchor.id=$2 WHERE target.id=$1`, target.ID, anchor.ID).Scan(&targetOrder, &anchorOrder); err != nil {
		t.Fatal(err)
	}
	if !targetOrder.After(anchorOrder) {
		t.Fatalf("requeued claim order %s did not advance past cursor order %s", targetOrder, anchorOrder)
	}

	foundTarget := false
	for i := 0; i < 12; i++ {
		// Keep adding newer compatible work before every claim. The queue never
		// reaches a short tail that could reset the durable cursor.
		newer := raceRun(fmt.Sprintf("run_requeue_newer_%02d", i), time.Now().UTC().Add(time.Duration(i+1)*time.Minute))
		if _, err := pg.CreateRun(ctx, newer); err != nil {
			t.Fatal(err)
		}
		claim, err := pg.ClaimRun(ctx, "runner_requeue_seeker", time.Now().UTC(), 2*time.Minute)
		if err != nil {
			t.Fatalf("bounded progressing claim %d: %v", i, err)
		}
		if claim.Run.ID == target.ID {
			if claim.Lease.Attempt != firstTarget.Lease.Attempt+1 || claim.Lease.Fence == firstTarget.Lease.Fence {
				t.Fatalf("requeued authority=%#v after=%#v", claim.Lease, firstTarget.Lease)
			}
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatal("requeued run starved behind durable cursor while newer queue remained non-empty")
	}
	var persistedStartedAt time.Time
	if err := database.QueryRow(ctx, `SELECT started_at FROM task_runs WHERE id=$1`, target.ID).Scan(&persistedStartedAt); err != nil {
		t.Fatal(err)
	}
	if !persistedStartedAt.Equal(originalStartedAt) {
		t.Fatalf("business started_at changed on requeue: before=%s after=%s", originalStartedAt, persistedStartedAt)
	}
}

func TestPostgresIntegrationApprovedRunAdvancesPastDurableCursor(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := "nerocd_approval_order_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	now := time.Now().UTC()
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: "runner_approval_order", Name: "approval-order", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	blocked := raceRun("run_approval_order_target", now.Add(-2*time.Minute))
	blocked.Status = domain.RunWaitingApproval
	if _, err := pg.CreateRun(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateApproval(ctx, domain.Approval{ID: "approval_order_target", RunID: blocked.ID, Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: blocked.StartedAt}); err != nil {
		t.Fatal(err)
	}
	anchor := raceRun("run_approval_order_anchor", now.Add(-time.Minute))
	if _, err := pg.CreateRun(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	anchorClaim, err := pg.ClaimRun(ctx, "runner_approval_order", now, 10*time.Minute)
	if err != nil || anchorClaim.Run.ID != anchor.ID {
		t.Fatalf("anchor claim run=%q err=%v", anchorClaim.Run.ID, err)
	}
	for i := 0; i < 5; i++ {
		run := raceRun(fmt.Sprintf("run_approval_order_ahead_%02d", i), now.Add(-30*time.Second+time.Duration(i)*time.Second))
		if _, err := pg.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	var originalStartedAt time.Time
	if err := database.QueryRow(ctx, `SELECT started_at FROM task_runs WHERE id=$1`, blocked.ID).Scan(&originalStartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ApproveRun(ctx, blocked.ID, "usr_bootstrap", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var approvedOrder, anchorOrder time.Time
	if err := database.QueryRow(ctx, `SELECT target.claim_order_at, anchor.claim_order_at FROM task_runs target JOIN task_runs anchor ON anchor.id=$2 WHERE target.id=$1`, blocked.ID, anchor.ID).Scan(&approvedOrder, &anchorOrder); err != nil {
		t.Fatal(err)
	}
	if !approvedOrder.After(anchorOrder) {
		t.Fatalf("approved order %s did not advance past cursor %s", approvedOrder, anchorOrder)
	}
	found := false
	for i := 0; i < 10; i++ {
		newer := raceRun(fmt.Sprintf("run_approval_order_newer_%02d", i), time.Now().UTC().Add(time.Duration(i+1)*time.Minute))
		if _, err := pg.CreateRun(ctx, newer); err != nil {
			t.Fatal(err)
		}
		claim, err := pg.ClaimRun(ctx, "runner_approval_order", time.Now().UTC(), 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claim.Run.ID == blocked.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("approved run starved behind durable cursor while newer queue remained non-empty")
	}
	var persistedStartedAt time.Time
	if err := database.QueryRow(ctx, `SELECT started_at FROM task_runs WHERE id=$1`, blocked.ID).Scan(&persistedStartedAt); err != nil {
		t.Fatal(err)
	}
	if !persistedStartedAt.Equal(originalStartedAt) {
		t.Fatalf("approval changed started_at: before=%s after=%s", originalStartedAt, persistedStartedAt)
	}
}

func TestPostgresIntegrationFencingMigrationCompatibility(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_upgrade_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := applyEmbeddedMigrations(ctx, database, "", "0010_service_account_tokens.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := db.Files.ReadFile("seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.Exec(ctx, `INSERT INTO runners (id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash) VALUES ('runner_upgrade','upgrade',ARRAY['local'],ARRAY['shell'],'active',$1,$1,'')`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO task_runs (id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at) VALUES ('run_upgrade','proj_platform','{"type":"shell"}','{"steps":[]}','{"steps":[]}',ARRAY['local'],'queued','usr_bootstrap',$1)`, now); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{domain.RunSucceeded, domain.RunFailed, domain.RunCanceled} {
		if _, err := database.Exec(ctx, `INSERT INTO run_leases (id,run_id,runner_id,status,expires_at,created_at,completed_at) VALUES ($1,'run_upgrade','runner_upgrade',$2,$3,$4,$4)`, fmt.Sprintf("lease_historical_%d", i), status, now.Add(-time.Hour), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if preMigration, err := store.OpenPostgres(ctx, schemaURL); err == nil {
		preMigration.Close()
		t.Fatal("current store accepted a pre-fencing schema")
	}
	if err := applyEmbeddedMigrations(ctx, database, "0010_service_account_tokens.sql", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(ctx, `SELECT attempt,fence FROM run_leases WHERE run_id='run_upgrade' ORDER BY attempt`)
	if err != nil {
		t.Fatal(err)
	}
	attempts := []int{}
	fences := map[string]bool{}
	for rows.Next() {
		var attempt int
		var fence string
		if err := rows.Scan(&attempt, &fence); err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, attempt)
		if fence == "" || fences[fence] {
			t.Fatalf("invalid or duplicate fence %q", fence)
		}
		fences[fence] = true
	}
	rows.Close()
	if fmt.Sprint(attempts) != "[1 2 3]" {
		t.Fatalf("historical attempts=%v", attempts)
	}
	var nextAttempt int
	if err := database.QueryRow(ctx, `SELECT next_attempt FROM task_runs WHERE id='run_upgrade'`).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	if nextAttempt != 4 {
		t.Fatalf("historical next_attempt=%d, want 4", nextAttempt)
	}
	planRows, err := database.Query(ctx, `EXPLAIN (FORMAT TEXT) UPDATE task_runs
		SET status='running', runner_id='runner_upgrade', next_attempt=next_attempt+1
		WHERE id='run_upgrade' AND status='queued'
		RETURNING next_attempt-1`)
	if err != nil {
		t.Fatal(err)
	}
	var attemptPlan strings.Builder
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		attemptPlan.WriteString(line)
		attemptPlan.WriteByte('\n')
	}
	planRows.Close()
	if strings.Contains(attemptPlan.String(), "run_leases") || !strings.Contains(attemptPlan.String(), "task_runs") {
		t.Fatalf("attempt counter plan unexpectedly scans lease history:\n%s", attemptPlan.String())
	}
	if _, err := database.Exec(ctx, `INSERT INTO run_leases (id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES ('lease_duplicate','run_upgrade','runner_upgrade','failed',$1,$1,1,'duplicate')`, now); err == nil {
		t.Fatal("duplicate (run_id,attempt) was accepted")
	}
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	claim, err := pg.ClaimRun(ctx, "runner_upgrade", time.Now().UTC(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.ID != "run_upgrade" || claim.Lease.Attempt != 4 || claim.Lease.Fence == "" {
		t.Fatalf("post-upgrade claim=%#v", claim)
	}
	if err := database.QueryRow(ctx, `SELECT next_attempt FROM task_runs WHERE id='run_upgrade'`).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	if nextAttempt != 5 {
		t.Fatalf("consumed next_attempt=%d, want 5", nextAttempt)
	}
}

func TestPostgresIntegrationRejectsPartialSchedulerSchema(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_partial_scheduler_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	assertAccepted := func(label string) {
		t.Helper()
		candidate, err := store.OpenPostgres(ctx, schemaURL)
		if err != nil {
			t.Fatalf("%s current schema rejected: %v", label, err)
		}
		candidate.Close()
	}
	assertRejected := func(label string) {
		t.Helper()
		candidate, err := store.OpenPostgres(ctx, schemaURL)
		if err == nil {
			candidate.Close()
			t.Fatalf("%s partial scheduler schema accepted", label)
		}
	}
	assertAccepted("fully migrated")

	mutations := []struct {
		label   string
		breakDB string
		repair  string
	}{
		{"missing next-attempt positive check", `ALTER TABLE task_runs DROP CONSTRAINT task_runs_next_attempt_positive`, `ALTER TABLE task_runs ADD CONSTRAINT task_runs_next_attempt_positive CHECK (next_attempt > 0)`},
		{"incorrect next-attempt positive check under expected name", `ALTER TABLE task_runs DROP CONSTRAINT task_runs_next_attempt_positive; ALTER TABLE task_runs ADD CONSTRAINT task_runs_next_attempt_positive CHECK (true)`, `ALTER TABLE task_runs DROP CONSTRAINT task_runs_next_attempt_positive; ALTER TABLE task_runs ADD CONSTRAINT task_runs_next_attempt_positive CHECK (next_attempt > 0)`},
		{"missing lease-attempt positive check", `ALTER TABLE run_leases DROP CONSTRAINT run_leases_attempt_positive`, `ALTER TABLE run_leases ADD CONSTRAINT run_leases_attempt_positive CHECK (attempt > 0)`},
		{"incorrect lease-attempt positive check under expected name", `ALTER TABLE run_leases DROP CONSTRAINT run_leases_attempt_positive; ALTER TABLE run_leases ADD CONSTRAINT run_leases_attempt_positive CHECK (true)`, `ALTER TABLE run_leases DROP CONSTRAINT run_leases_attempt_positive; ALTER TABLE run_leases ADD CONSTRAINT run_leases_attempt_positive CHECK (attempt > 0)`},
		{"missing fence nonempty check", `ALTER TABLE run_leases DROP CONSTRAINT run_leases_fence_nonempty`, `ALTER TABLE run_leases ADD CONSTRAINT run_leases_fence_nonempty CHECK (length(fence) > 0)`},
		{"incorrect fence nonempty check under expected name", `ALTER TABLE run_leases DROP CONSTRAINT run_leases_fence_nonempty; ALTER TABLE run_leases ADD CONSTRAINT run_leases_fence_nonempty CHECK (true)`, `ALTER TABLE run_leases DROP CONSTRAINT run_leases_fence_nonempty; ALTER TABLE run_leases ADD CONSTRAINT run_leases_fence_nonempty CHECK (length(fence) > 0)`},
		{"missing attempt default", `ALTER TABLE task_runs ALTER COLUMN next_attempt DROP DEFAULT`, `ALTER TABLE task_runs ALTER COLUMN next_attempt SET DEFAULT 1`},
		{"missing claim-order default", `ALTER TABLE task_runs ALTER COLUMN claim_order_at DROP DEFAULT`, `ALTER TABLE task_runs ALTER COLUMN claim_order_at SET DEFAULT clock_timestamp()`},
		{"missing queue index", `DROP INDEX idx_task_runs_queued_claim_order_id`, `CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"queue index is unique under expected name", `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE UNIQUE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`, `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"queue index has wrong columns under expected name", `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (id, claim_order_at) WHERE status='queued'`, `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"queue index has expression key under expected name", `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, ((id || ''))) WHERE status='queued'`, `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"queue index has included column under expected name", `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) INCLUDE (status) WHERE status='queued'`, `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"queue index has wrong predicate under expected name", `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status IN ('queued','running')`, `DROP INDEX idx_task_runs_queued_claim_order_id; CREATE INDEX idx_task_runs_queued_claim_order_id ON task_runs (claim_order_at, id) WHERE status='queued'`},
		{"missing cursor claim-order column", `ALTER TABLE runner_claim_cursors RENAME COLUMN claim_order_at TO incomplete_order_at`, `ALTER TABLE runner_claim_cursors RENAME COLUMN incomplete_order_at TO claim_order_at`},
		{"missing cursor run-id column", `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_tuple_complete; ALTER TABLE runner_claim_cursors DROP COLUMN run_id`, `ALTER TABLE runner_claim_cursors ADD COLUMN run_id TEXT; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_tuple_complete CHECK ((claim_order_at IS NULL AND run_id IS NULL) OR (claim_order_at IS NOT NULL AND run_id IS NOT NULL))`},
		{"incorrect cursor tuple check under expected name", `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_tuple_complete; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_tuple_complete CHECK (true)`, `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_tuple_complete; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_tuple_complete CHECK ((claim_order_at IS NULL AND run_id IS NULL) OR (claim_order_at IS NOT NULL AND run_id IS NOT NULL))`},
		{"cursor primary key has wrong column under expected name", `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_pkey; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_pkey PRIMARY KEY (updated_at)`, `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_pkey; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_pkey PRIMARY KEY (runner_id)`},
		{"cursor primary key has wrong shape under expected name", `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_pkey; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_pkey PRIMARY KEY (runner_id, updated_at)`, `ALTER TABLE runner_claim_cursors DROP CONSTRAINT runner_claim_cursors_pkey; ALTER TABLE runner_claim_cursors ADD CONSTRAINT runner_claim_cursors_pkey PRIMARY KEY (runner_id)`},
		{"lease attempt index has wrong columns under expected name", `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (id, attempt)`, `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`},
		{"lease attempt index has expression key under expected name", `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, ((attempt + 0)))`, `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`},
		{"lease attempt index has included column under expected name", `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt) INCLUDE (status)`, `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`},
		{"lease attempt index is nonunique under expected name", `DROP INDEX run_leases_run_id_attempt_unique; CREATE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`, `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`},
		{"lease attempt index is partial under expected name", `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt) WHERE status='active'`, `DROP INDEX run_leases_run_id_attempt_unique; CREATE UNIQUE INDEX run_leases_run_id_attempt_unique ON run_leases (run_id, attempt)`},
	}
	for _, mutation := range mutations {
		if _, err := database.Exec(ctx, mutation.breakDB); err != nil {
			t.Fatalf("%s break: %v", mutation.label, err)
		}
		assertRejected(mutation.label)
		if _, err := database.Exec(ctx, mutation.repair); err != nil {
			t.Fatalf("%s repair: %v", mutation.label, err)
		}
	}
	assertAccepted("repaired")
}

func TestPostgresIntegrationCancelRacesRemainCoherent(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := "nerocd_cancel_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	defer database.Close()
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)
	now := time.Now().UTC()
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: "runner_race", Name: "race", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	runnerCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "runner_race", Provider: domain.PrincipalRunner})
	operatorCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})

	for i := 0; i < 8; i++ {
		runID := fmt.Sprintf("run_cancel_claim_%02d", i)
		if _, err := pg.CreateRun(ctx, raceRun(runID, now.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		claimCh := make(chan claimResultForTest, 1)
		cancelCh := make(chan error, 1)
		go func() {
			<-start
			claim, err := pg.ClaimRun(ctx, "runner_race", time.Now(), 5*time.Second)
			claimCh <- claimResultForTest{claim: claim, err: err}
		}()
		go func() {
			<-start
			_, err := service.CancelRun(operatorCtx, runID)
			cancelCh <- err
		}()
		close(start)
		claimResult := <-claimCh
		if err := <-cancelCh; err != nil {
			t.Fatalf("cancel vs claim %d: %v", i, err)
		}
		if claimResult.err != nil && !errors.Is(claimResult.err, store.ErrNotFound) {
			t.Fatalf("claim race %d: %v", i, claimResult.err)
		}
		assertTerminalRaceState(t, ctx, database, runID, domain.RunCanceled)
		if claimResult.err == nil {
			if _, err := pg.CreateRunLogForLease(ctx, domain.RunLog{ID: fmt.Sprintf("stale_claim_%d", i), RunID: runID, Stream: domain.LogStdout, Message: "stale", CreatedAt: time.Now()}, "runner_race", claimResult.claim.Lease.ID, claimResult.claim.Lease.Attempt, claimResult.claim.Lease.Fence, time.Now()); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("post-cancel stale write %d: %v", i, err)
			}
		}
	}

	for i := 0; i < 8; i++ {
		runID := fmt.Sprintf("run_cancel_complete_%02d", i)
		if _, err := pg.CreateRun(ctx, raceRun(runID, now.Add(time.Duration(100+i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
		claim, err := pg.ClaimRun(ctx, "runner_race", time.Now(), 5*time.Second)
		if err != nil || claim.Run.ID != runID {
			t.Fatalf("claim completion race %d: run=%q err=%v", i, claim.Run.ID, err)
		}
		start := make(chan struct{})
		completeCh := make(chan error, 1)
		cancelCh := make(chan error, 1)
		go func() {
			<-start
			_, err := service.CompleteLease(runnerCtx, claim.Lease.ID, domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, fmt.Sprintf("completion_race_%d", i))
			completeCh <- err
		}()
		go func() {
			<-start
			_, err := service.CancelRun(operatorCtx, runID)
			cancelCh <- err
		}()
		close(start)
		completeErr, cancelErr := <-completeCh, <-cancelCh
		if completeErr != nil && !errors.Is(completeErr, store.ErrNotFound) {
			t.Fatalf("completion race %d: %v", i, completeErr)
		}
		if cancelErr != nil && !errors.Is(cancelErr, store.ErrNotFound) {
			t.Fatalf("cancel race %d: %v", i, cancelErr)
		}
		var status string
		if err := database.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, runID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != domain.RunSucceeded && status != domain.RunCanceled {
			t.Fatalf("race %d terminal status=%q", i, status)
		}
		assertTerminalRaceState(t, ctx, database, runID, status)
		if _, err := pg.CreateRunLogForLease(ctx, domain.RunLog{ID: fmt.Sprintf("stale_complete_%d", i), RunID: runID, Stream: domain.LogStdout, Message: "stale", CreatedAt: time.Now()}, "runner_race", claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, time.Now()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("terminal stale write %d: %v", i, err)
		}
	}
}

type claimResultForTest struct {
	claim domain.ClaimedRun
	err   error
}

func raceRun(id string, startedAt time.Time) domain.TaskRun {
	return domain.TaskRun{ID: id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell"}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: startedAt}
}

func assertTerminalRaceState(t *testing.T, ctx context.Context, database *pgxpool.Pool, runID, expectedStatus string) {
	t.Helper()
	var status string
	var activeLeases, terminalLogs, terminalAudits int
	if err := database.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE run_id=$1 AND status='active'`, runID).Scan(&activeLeases); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1 AND (message='Run canceled by user' OR message LIKE 'Runner completed lease with status %')`, runID).Scan(&terminalLogs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action IN ('run.cancel','runner.complete')`, runID).Scan(&terminalAudits); err != nil {
		t.Fatal(err)
	}
	if status != expectedStatus || activeLeases != 0 || terminalLogs != 1 || terminalAudits != 1 {
		t.Fatalf("run=%s status=%s want=%s active=%d terminal_logs=%d terminal_audits=%d", runID, status, expectedStatus, activeLeases, terminalLogs, terminalAudits)
	}
}

func databaseURLWithSearchPath(t *testing.T, databaseURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func openPostgresIntegrationStore(t *testing.T, ctx context.Context, label string) (*store.PostgresStore, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	schema := fmt.Sprintf("nerocd_%s_%d", label, time.Now().UTC().UnixNano())
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		adminDB.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		adminDB.Close()
	})
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := applyEmbeddedSQL(ctx, database); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	return pg, database
}

func databaseURLWithApplicationName(t *testing.T, databaseURL, applicationName string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("application_name", applicationName)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func waitForPostgresLockWait(t *testing.T, ctx context.Context, database *pgxpool.Pool, applicationName, queryFragment string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := database.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE application_name=$1 AND state='active' AND wait_event_type='Lock' AND position($2 in query)>0
		)`, applicationName, queryFragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("application %q did not block in query containing %q", applicationName, queryFragment)
		case <-ticker.C:
		}
	}
}

func postgresRowLocked(t *testing.T, ctx context.Context, database *pgxpool.Pool, query string) bool {
	t.Helper()
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var value string
	err = tx.QueryRow(ctx, query).Scan(&value)
	if err == nil {
		return false
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "55P03" {
		return true
	}
	t.Fatalf("probe row lock: %v", err)
	return false
}

func applyEmbeddedSQL(ctx context.Context, database *pgxpool.Pool) error {
	if err := applyEmbeddedMigrations(ctx, database, "", ""); err != nil {
		return err
	}
	seed, err := db.Files.ReadFile("seeds/dev.sql")
	if err != nil {
		return err
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply seeds/dev.sql: %w", err)
	}
	return nil
}

func applyEmbeddedMigrations(ctx context.Context, database *pgxpool.Pool, after, through string) error {
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, "migrations/"+entry.Name())
	}
	sort.Strings(files)
	for _, file := range files {
		name := strings.TrimPrefix(file, "migrations/")
		if after != "" && name <= after {
			continue
		}
		if through != "" && name > through {
			continue
		}
		content, err := db.Files.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := database.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", file, err)
		}
	}
	return nil
}

func hasRole(principal auth.Principal, role string) bool {
	for _, candidate := range principal.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func hasAuditAction(events []domain.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func hasRunLogMessage(logs []domain.RunLog, message string) bool {
	for _, log := range logs {
		if log.Message == message {
			return true
		}
	}
	return false
}
