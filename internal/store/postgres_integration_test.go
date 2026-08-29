package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/observability"
	"nerocd/internal/store"
	"nerocd/web"
)

func closePostgresStore(t *testing.T, pg *store.PostgresStore) {
	t.Helper()
	if err := pg.Close(); err != nil {
		t.Errorf("close postgres store: %v", err)
	}
}

func TestPostgresOperationalSnapshotAggregates(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "operational_snapshot")
	defer closePostgresStore(t, pg)
	baseline, err := pg.OperationalSnapshot(ctx)
	if err != nil {
		t.Fatalf("baseline snapshot: %v", err)
	}
	if baseline.BackupScheduleStatus != "disabled" || baseline.BackupScheduleFailures != 0 {
		t.Fatalf("baseline scheduler snapshot=%#v", baseline)
	}
	if _, err := database.Exec(ctx, `UPDATE backup_schedule SET enabled=true, next_run_at=clock_timestamp() + interval '90 seconds', consecutive_failures=2 WHERE singleton`); err != nil {
		t.Fatalf("configure schedule observation: %v", err)
	}

	now := time.Now().UTC()
	runnerID := "runner_observability"
	if _, err := pg.RegisterRunner(ctx, domain.Runner{
		ID: runnerID, Name: "Observability Runner", Tags: []string{}, Capabilities: []string{"shell"},
		Status: domain.RunnerActive, RegisteredAt: now.Add(-time.Minute), LastHeartbeatAt: now.Add(-12 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	create := func(id string, status domain.RunStatus, started time.Time, finished *time.Time) {
		t.Helper()
		if _, err := pg.CreateRun(ctx, domain.TaskRun{
			ID: id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell},
			RunnerTags: []string{}, Status: status, RequestedBy: "usr_bootstrap", StartedAt: started, FinishedAt: finished,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	activeStart := now.Add(-6 * time.Minute)
	expiredStart := now.Add(-5 * time.Minute)
	queuedStart := now.Add(-4 * time.Minute)
	create("run_observation_active", domain.RunQueued, activeStart, nil)
	create("run_observation_expired", domain.RunQueued, expiredStart, nil)
	create("run_observation_queued", domain.RunQueued, queuedStart, nil)
	for _, terminal := range []struct {
		id     string
		status domain.RunStatus
		start  time.Time
	}{
		{"run_observation_succeeded", domain.RunSucceeded, now.Add(-3 * time.Minute)},
		{"run_observation_failed", domain.RunFailed, now.Add(-2 * time.Minute)},
		{"run_observation_canceled", domain.RunCanceled, now.Add(-time.Minute)},
	} {
		finished := terminal.start.Add(30 * time.Second)
		create(terminal.id, terminal.status, terminal.start, &finished)
	}

	active, err := pg.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil {
		t.Fatalf("claim active: %v", err)
	}
	expired, err := pg.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil {
		t.Fatalf("claim expired: %v", err)
	}
	if _, err := database.Exec(ctx, `UPDATE run_leases SET status='expired' WHERE id=$1`, expired.Lease.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if err := pg.RecordRunnerOperationalObservation(ctx, runnerID, 7, 3, 2); err != nil {
		t.Fatalf("record runner observation: %v", err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO backup_operational_results (outcome, reason) VALUES ('success', 'none')`); err != nil {
		t.Fatalf("record backup: %v", err)
	}

	snapshot, err := pg.OperationalSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.QueueDepth != baseline.QueueDepth+1 || snapshot.ActiveLeases != baseline.ActiveLeases+1 || snapshot.ExpiredLeases != baseline.ExpiredLeases+1 {
		t.Fatalf("queue/lease snapshot=%#v", snapshot)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeded, domain.RunFailed, domain.RunCanceled} {
		aggregate := snapshot.TerminalRuns[status]
		before := baseline.TerminalRuns[string(status)]
		if aggregate.Count != before.Count+1 || aggregate.SumSeconds < before.SumSeconds+29 {
			t.Fatalf("terminal %s aggregate=%#v", status, aggregate)
		}
	}
	if snapshot.RunnerJournalDepth != 7 || snapshot.RunnerRetryCount != 3 || snapshot.RunnerRenewFailures != 2 || snapshot.BackupOutcome != observability.BackupSuccess || snapshot.BackupReason != "none" || snapshot.BackupScheduleStatus != "backoff" || snapshot.BackupScheduleFailures != 2 || snapshot.BackupScheduleNextSeconds < 80 || snapshot.BackupScheduleNextSeconds > 100 {
		t.Fatalf("telemetry/backup snapshot=%#v", snapshot)
	}
	if snapshot.QueueOldestAgeSeconds < 3*60 || snapshot.OldestRunnerHeartbeatSecond < 10 || snapshot.BackupAgeSeconds < 0 || snapshot.BackupAgeSeconds > 60 {
		t.Fatalf("database-clock ages snapshot=%#v", snapshot)
	}
	if active.Lease.ID == expired.Lease.ID {
		t.Fatal("distinct claims unexpectedly shared a lease")
	}
}

func TestPostgresRunLogRetentionAtomicReplayAndConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "run_log_retention")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	finished := now.Add(-48 * time.Hour)
	for _, id := range []string{"retention_run_a", "retention_run_b"} {
		if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{}, Status: domain.RunSucceeded, RequestedBy: "usr_bootstrap", StartedAt: finished.Add(-time.Minute), FinishedAt: &finished}); err != nil {
			t.Fatalf("create terminal run %s: %v", id, err)
		}
	}
	for _, log := range []domain.RunLog{{ID: "retention_log_a", RunID: "retention_run_a", Stream: domain.LogStdout, Message: "abc", CreatedAt: finished}, {ID: "retention_log_b", RunID: "retention_run_b", Stream: domain.LogStdout, Message: "wxyz", CreatedAt: finished}} {
		if err := pg.CreateRunLog(ctx, log); err != nil {
			t.Fatalf("create old log %s: %v", log.ID, err)
		}
	}
	policy, err := pg.UpdateRunLogRetentionPolicy(ctx, domain.RunLogRetentionPolicy{Enabled: true, KeepDays: 1, BatchSize: 1, UpdatedBy: "usr_bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	body := store.RunLogRetentionBodyHash(policy)
	const requestID = "retention-concurrent-request"
	results := make(chan domain.RunLogRetentionExecution, 8)
	errs := make(chan error, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			result, err := pg.ExecuteRunLogRetention(ctx, requestID, body, domain.AuditEvent{ID: fmt.Sprintf("retention_audit_%d", i), ActorID: "usr_bootstrap", Action: "run_log_retention.execute", TargetID: "run-log-retention", CreatedAt: now})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent execute: %v", err)
	}
	var first domain.RunLogRetentionExecution
	for result := range results {
		if first.RequestID == "" {
			first = result
		}
		if result != first {
			t.Fatalf("replay result=%#v want=%#v", result, first)
		}
	}
	if first.Deleted != 1 || first.DeletedBytes != 3 || first.Preview.EligibleLogs != 2 || first.Preview.EligibleBytes != 7 {
		t.Fatalf("execution=%#v", first)
	}
	var logs, receipts, audits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE id IN ('retention_log_a','retention_log_b')`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_log_retention_receipts WHERE request_id=$1`, requestID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='run_log_retention.execute'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if logs != 1 || receipts != 1 || audits != 1 {
		t.Fatalf("retention rows logs=%d receipts=%d audits=%d", logs, receipts, audits)
	}
	// An audit failure occurs after candidate selection/deletion in the SQL
	// transaction; it must roll the entire operation back.
	if err := pg.CreateAuditEvent(ctx, domain.AuditEvent{ID: "retention_duplicate_audit", ActorID: "usr_bootstrap", Action: "test", TargetID: "test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ExecuteRunLogRetention(ctx, "retention-rollback-request", body, domain.AuditEvent{ID: "retention_duplicate_audit", ActorID: "usr_bootstrap", Action: "run_log_retention.execute", TargetID: "run-log-retention", CreatedAt: now}); err == nil {
		t.Fatal("duplicate audit execution unexpectedly succeeded")
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE id='retention_log_b'`).Scan(&logs); err != nil || logs != 1 {
		t.Fatalf("failed execution deleted log count=%d err=%v", logs, err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_log_retention_receipts WHERE request_id='retention-rollback-request'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("failed execution receipt count=%d err=%v", receipts, err)
	}
}

func TestPostgresFencedDeploymentAttemptHTTPAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "deployment_fence")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	serviceID, environmentID := "svc_fenced_"+suffix, "env_fenced_"+suffix
	if _, err := pg.CreateService(ctx, domain.Service{ID: serviceID, ProjectID: "proj_platform", Name: "fenced-" + suffix, RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", Profiles: []string{}, OwnerID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateEnvironment(ctx, domain.Environment{ID: environmentID, ServiceID: serviceID, Name: "prod", RunnerSelector: []string{}, ComposeProject: "fenced-" + suffix, HealthPolicy: domain.HealthPolicy{}, TimeoutSeconds: 60, SecretBindings: []domain.SecretBinding{}, RollbackSafe: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	revisionA, revisionB := "rev_fenced_a_"+suffix, "rev_fenced_b_"+suffix
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: revisionA, ServiceID: serviceID, RequestedRef: "a", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: revisionB, ServiceID: serviceID, RequestedRef: "b", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var pendingRevisionCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM revisions WHERE service_id=$1 AND provenance_state='pending' AND content_identity=''`, serviceID).Scan(&pendingRevisionCount); err != nil || pendingRevisionCount != 2 {
		t.Fatalf("two pending revisions not independently durable count=%d err=%v", pendingRevisionCount, err)
	}

	runnerToken := "runner-deployment-" + suffix
	hash := sha256.Sum256([]byte(runnerToken))
	runnerID := "runner_deployment_" + suffix
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{}, Capabilities: []string{domain.RunTypeComposeDeploy}, TokenHash: fmt.Sprintf("%x", hash[:]), Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	create := func(id, runID, revision, key string) domain.Deployment {
		d, createErr := pg.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: environmentID, DesiredRevisionID: revision, IdempotencyKey: key, Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", FenceRequired: true, CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: runID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_" + id, ActorID: "usr_bootstrap", Action: "deployment.create", TargetID: id, Metadata: map[string]any{}, CreatedAt: now})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return d
	}
	depA := create("dep_fenced_a_"+suffix, "run_fenced_a_"+suffix, revisionA, "a")
	claimA, err := pg.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil || claimA.Run.ID != *depA.TaskRunID || claimA.Lease.Attempt != 1 {
		t.Fatalf("claim = %#v, %v", claimA, err)
	}

	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})

	if err != nil {

		t.Fatal(err)

	}
	server := api.NewServer(service, slog.Default(), web.Static())
	adminSession, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	genericComplete := func(lease domain.RunLease) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"lease_id":%q,"attempt":%d,"fence":%q,"completion_key":"generic-deployment","status":"succeeded"}`, lease.ID, lease.Attempt, lease.Fence)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if rec := genericComplete(claimA.Lease); rec.Code != http.StatusConflict {
		t.Fatalf("generic deployment completion before terminal = %d: %s", rec.Code, rec.Body.String())
	}
	postTransition := func(deployment domain.Deployment, claim domain.ClaimedRun, expected, target, key, fence string, health *bool) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"transition_key":%q,"expected_status":%q,"target_status":%q`, deployment.ID, claim.Run.ID, claim.Lease.ID, claim.Lease.Attempt, fence, key, expected, target)
		if health != nil {
			body += fmt.Sprintf(`,"health_passed":%t`, *health)
		}
		body += `,"metadata":{"integration":true}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/transition", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	postProvenance := func(deployment domain.Deployment, claim domain.ClaimedRun, resolutionID, commit, hash string, digests []string, fence string) *httptest.ResponseRecorder {
		digestJSON, marshalErr := json.Marshal(digests)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		body := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"resolution_id":%q,"git_commit":%q,"compose_hash":%q,"image_digests":%s,"content_identity":%q}`,
			deployment.ID, claim.Run.ID, claim.Lease.ID, claim.Lease.Attempt, fence, resolutionID, commit, hash, digestJSON, commit+":"+hash)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/provenance", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	transition := func(expected, target, key string, health *bool) *httptest.ResponseRecorder {
		return postTransition(depA, claimA, expected, target, key, claimA.Lease.Fence, health)
	}
	assertTerminalLifecycle := func(deployment domain.Deployment, claim domain.ClaimedRun, want string) {
		var attemptStatus, leaseStatus, runStatus string
		var runnerCleared, runFinished bool
		if queryErr := database.QueryRow(ctx, `
SELECT da.status, rl.status, tr.status, tr.runner_id IS NULL, tr.finished_at IS NOT NULL
FROM deployment_attempts da
JOIN run_leases rl ON rl.id=da.lease_id
JOIN task_runs tr ON tr.id=da.run_id
WHERE da.deployment_id=$1 AND da.attempt=$2`, deployment.ID, claim.Lease.Attempt).Scan(&attemptStatus, &leaseStatus, &runStatus, &runnerCleared, &runFinished); queryErr != nil {
			t.Fatal(queryErr)
		}
		if attemptStatus != want || leaseStatus != want || runStatus != want || !runnerCleared || !runFinished {
			t.Fatalf("terminal lifecycle deployment=%s attempt=%q lease=%q run=%q runner_cleared=%t finished=%t", deployment.ID, attemptStatus, leaseStatus, runStatus, runnerCleared, runFinished)
		}
	}
	// The runner can only start applying after it has fetched the constrained
	// plan and durably bound its observed provenance under this exact fence.
	planReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/runners/deployments/plan?deployment_id=%s&run_id=%s&lease_id=%s&attempt=%d&fence=%s", depA.ID, claimA.Run.ID, claimA.Lease.ID, claimA.Lease.Attempt, claimA.Lease.Fence), nil)
	planReq.Header.Set("Authorization", "Bearer "+runnerToken)
	planRec := httptest.NewRecorder()
	server.ServeHTTP(planRec, planReq)
	if planRec.Code != http.StatusOK {
		t.Fatalf("runner plan response %d: %s", planRec.Code, planRec.Body.String())
	}
	commitA, hashA, digestsA := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64), []string{"sha256:" + strings.Repeat("c", 64)}
	provenanceResponses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			provenanceResponses <- postProvenance(depA, claimA, "resolution-a", commitA, hashA, digestsA, claimA.Lease.Fence)
		}()
	}
	for range 2 {
		if rec := <-provenanceResponses; rec.Code != http.StatusOK {
			t.Fatalf("concurrent provenance response %d: %s", rec.Code, rec.Body.String())
		}
	}
	var provenanceAuditCount, provenanceReceiptCount int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='runner.deployment.provenance.resolve'`, depA.ID).Scan(&provenanceAuditCount); err != nil || provenanceAuditCount != 1 {
		t.Fatalf("provenance audit count=%d err=%v", provenanceAuditCount, err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM provenance_resolutions WHERE deployment_id=$1 AND resolution_id='resolution-a'`, depA.ID).Scan(&provenanceReceiptCount); err != nil || provenanceReceiptCount != 1 {
		t.Fatalf("provenance receipt count=%d err=%v", provenanceReceiptCount, err)
	}
	var resolvedA domain.Revision
	if err = json.Unmarshal(postProvenance(depA, claimA, "resolution-a", commitA, hashA, digestsA, claimA.Lease.Fence).Body.Bytes(), &resolvedA); err != nil || !resolvedA.ProvenanceResolved || resolvedA.ID != revisionA {
		t.Fatalf("provenance replay dto=%#v err=%v", resolvedA, err)
	}
	if rec := postProvenance(depA, claimA, "resolution-a", strings.Repeat("9", 40), hashA, digestsA, claimA.Lease.Fence); rec.Code != http.StatusConflict {
		t.Fatalf("altered provenance replay response %d: %s", rec.Code, rec.Body.String())
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "apply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "verify"}} {
		if rec := transition(step[0], step[1], step[2], nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	yes := true
	if rec := transition(domain.DeploymentVerifying, domain.DeploymentSucceeded, "succeed", &yes); rec.Code != http.StatusOK {
		t.Fatalf("success response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := transition(domain.DeploymentVerifying, domain.DeploymentSucceeded, "succeed", &yes); rec.Code != http.StatusOK {
		t.Fatalf("exact replay response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := transition(domain.DeploymentVerifying, domain.DeploymentFailed, "succeed", nil); rec.Code != http.StatusConflict {
		t.Fatalf("conflicting replay response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := genericComplete(claimA.Lease); rec.Code != http.StatusConflict {
		t.Fatalf("generic deployment completion after terminal = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err = pg.ActiveLeaseForRun(ctx, claimA.Run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal deployment retained active lease: %v", err)
	}
	runs, err := pg.ListRuns(ctx, "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == claimA.Run.ID && (run.Status != domain.RunSucceeded || run.RunnerID != nil || run.FinishedAt == nil) {
			t.Fatalf("terminal run lifecycle %#v", run)
		}
	}
	var attemptStatus, leaseStatus string
	if err = database.QueryRow(ctx, `SELECT da.status, rl.status FROM deployment_attempts da JOIN run_leases rl ON rl.id=da.lease_id WHERE da.deployment_id=$1 AND da.attempt=$2`, depA.ID, claimA.Lease.Attempt).Scan(&attemptStatus, &leaseStatus); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != domain.RunSucceeded || leaseStatus != domain.RunSucceeded {
		t.Fatalf("terminal lifecycle attempt=%q lease=%q", attemptStatus, leaseStatus)
	}
	assertTerminalLifecycle(depA, claimA, domain.RunSucceeded)
	if rec := postProvenance(depA, claimA, "resolution-a", commitA, hashA, digestsA, claimA.Lease.Fence); rec.Code != http.StatusOK {
		t.Fatalf("terminal exact provenance replay response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postProvenance(depA, claimA, "unseen-after-terminal", commitA, hashA, digestsA, claimA.Lease.Fence); rec.Code != http.StatusForbidden {
		t.Fatalf("terminal unseen provenance response %d: %s", rec.Code, rec.Body.String())
	}
	if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal deployment requeued/claimed: %v", err)
	}
	environments, err := pg.ListEnvironments(ctx, serviceID)
	if err != nil || environments[0].CurrentHealthyRevisionID == nil || *environments[0].CurrentHealthyRevisionID != revisionA {
		t.Fatalf("healthy pointer %#v, %v", environments, err)
	}

	depB := create("dep_fenced_b_"+suffix, "run_fenced_b_"+suffix, revisionB, "b")
	claimB, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	transitionB := func(expected, target, key string) {
		if rec := postTransition(depB, claimB, expected, target, key, claimB.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", key, rec.Code, rec.Body.String())
		}
	}
	transitionB(domain.DeploymentAssigned, domain.DeploymentPreparing, "bprepare")
	commitB, hashB, digestsB := strings.Repeat("d", 40), "sha256:"+strings.Repeat("e", 64), []string{"sha256:" + strings.Repeat("f", 64)}
	if rec := postProvenance(depB, claimB, "resolution-b", commitB, hashB, digestsB, claimB.Lease.Fence); rec.Code != http.StatusOK {
		t.Fatalf("provenance B response %d: %s", rec.Code, rec.Body.String())
	}
	transitionB(domain.DeploymentPreparing, domain.DeploymentApplying, "bapply")
	transitionB(domain.DeploymentApplying, domain.DeploymentCancelRequested, "bcancel-requested")
	if rec := postTransition(depB, claimB, domain.DeploymentCancelRequested, domain.DeploymentCanceled, "bcancelled", claimB.Lease.Fence, nil); rec.Code != http.StatusConflict {
		t.Fatalf("post-apply direct cancellation response %d: %s", rec.Code, rec.Body.String())
	}
	var postApplyStatus string
	if err = database.QueryRow(ctx, `SELECT status FROM deployments WHERE id=$1`, depB.ID).Scan(&postApplyStatus); err != nil {
		t.Fatal(err)
	}
	if postApplyStatus != domain.DeploymentCancelRequested {
		t.Fatalf("rejected post-apply cancel changed deployment status %q", postApplyStatus)
	}
	transitionB(domain.DeploymentCancelRequested, domain.DeploymentManualIntervention, "bmanual")
	assertTerminalLifecycle(depB, claimB, domain.RunFailed)
	environments, err = pg.ListEnvironments(ctx, serviceID)
	if err != nil || environments[0].CurrentHealthyRevisionID == nil || *environments[0].CurrentHealthyRevisionID != revisionA {
		t.Fatalf("rollback pointer %#v, %v", environments, err)
	}
	if rec := postTransition(depB, claimB, domain.DeploymentCancelRequested, domain.DeploymentManualIntervention, "stale", "stale", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("stale terminal fence response = %d: %s", rec.Code, rec.Body.String())
	}

	// The replacement attempt is created by the normal DB-clock reaper and
	// claim path; the expired authority cannot modify the still-assigned
	// deployment, while the new fence can.
	depC := create("dep_fenced_c_"+suffix, "run_fenced_c_"+suffix, revisionB, "c")
	if _, err = pg.HeartbeatRunner(ctx, runnerID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claimC1, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var expiredAttemptStatus string
	var expiredAttemptFinished bool
	if err = database.QueryRow(ctx, `SELECT status, finished_at IS NOT NULL FROM deployment_attempts WHERE deployment_id=$1 AND attempt=$2`, depC.ID, claimC1.Lease.Attempt).Scan(&expiredAttemptStatus, &expiredAttemptFinished); err != nil {
		t.Fatal(err)
	}
	if expiredAttemptStatus != domain.RunFailed || !expiredAttemptFinished {
		t.Fatalf("expired fence left nonterminal deployment attempt status=%q finished=%v", expiredAttemptStatus, expiredAttemptFinished)
	}
	if _, err = pg.HeartbeatRunner(ctx, runnerID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claimC2, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil || claimC2.Lease.Attempt <= claimC1.Lease.Attempt {
		t.Fatalf("replacement claim %#v after %#v: %v", claimC2.Lease, claimC1.Lease, err)
	}
	if rec := postTransition(depC, claimC1, domain.DeploymentAssigned, domain.DeploymentPreparing, "stale-expired", claimC1.Lease.Fence, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expired HTTP fence response = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err = pg.TransitionDeploymentAttempt(ctx, domain.DeploymentTransitionRequest{DeploymentID: depC.ID, RunID: claimC2.Run.ID, LeaseID: claimC2.Lease.ID, RunnerID: runnerID, Attempt: claimC2.Lease.Attempt, Fence: claimC2.Lease.Fence, TransitionKey: "current", ExpectedStatus: domain.DeploymentAssigned, TargetStatus: domain.DeploymentPreparing, Metadata: map[string]any{}}, domain.AuditEvent{ID: "audit_current" + suffix, ActorID: runnerID, Action: "runner.deployment.transition", TargetID: depC.ID, Metadata: map[string]any{}, CreatedAt: now}); err != nil {
		t.Fatalf("current fence: %v", err)
	}
	if rec := postTransition(depC, claimC2, domain.DeploymentPreparing, domain.DeploymentCanceled, "ccanceled", claimC2.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("pre-apply cancellation response = %d: %s", rec.Code, rec.Body.String())
	}
	assertTerminalLifecycle(depC, claimC2, domain.RunCanceled)

	// Two simultaneous terminal reports with the same key must produce one
	// durable transition and one replay acknowledgement, never a second
	// attempt, lease completion, run completion, or audit event.
	depD := create("dep_fenced_d_"+suffix, "run_fenced_d_"+suffix, revisionB, "d")
	claimD, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "dprepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "dapply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "dverify"}} {
		if rec := postTransition(depD, claimD, step[0], step[1], step[2], claimD.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			responses <- postTransition(depD, claimD, domain.DeploymentVerifying, domain.DeploymentSucceeded, "dsuccess", claimD.Lease.Fence, &yes)
		}()
	}
	for range 2 {
		if rec := <-responses; rec.Code != http.StatusOK {
			t.Fatalf("concurrent terminal response %d: %s", rec.Code, rec.Body.String())
		}
	}
	var auditCount int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='runner.deployment.transition'`, depD.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 { // prepare, apply, verify, and exactly one terminal transition.
		t.Fatalf("concurrent terminal audit count = %d", auditCount)
	}
	if _, err = pg.ActiveLeaseForRun(ctx, claimD.Run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("concurrent terminal deployment retained active lease: %v", err)
	}
	assertTerminalLifecycle(depD, claimD, domain.RunSucceeded)

	// A maintainer's generic cancel route is deliberately not an execution
	// authority for deployment-backed runs. The server path and direct store
	// path both reject it without stranding deployment state or its lease.
	depE := create("dep_fenced_cancel_"+suffix, "run_fenced_cancel_"+suffix, revisionA, "cancel")
	claimE, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postTransition(depE, claimE, domain.DeploymentAssigned, domain.DeploymentPreparing, "eprepare", claimE.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("prepare before generic cancellation response %d: %s", rec.Code, rec.Body.String())
	}
	var beforeLogs, beforeArtifacts int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1`, claimE.Run.ID).Scan(&beforeLogs); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_artifacts WHERE run_id=$1`, claimE.Run.ID).Scan(&beforeArtifacts); err != nil {
		t.Fatal(err)
	}
	if err = pg.CreateRunLog(ctx, domain.RunLog{ID: "log_generic_deployment_" + suffix, RunID: claimE.Run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "generic"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment log = %v", err)
	}
	if err = pg.CreateArtifact(ctx, domain.ArtifactRecord{ID: "art_generic_deployment_" + suffix, RunID: claimE.Run.ID, LeaseID: claimE.Lease.ID, Name: "generic", Path: "generic", Kind: domain.ArtifactFile}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment artifact = %v", err)
	}
	var afterGenericLogs, afterGenericArtifacts int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1`, claimE.Run.ID).Scan(&afterGenericLogs); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_artifacts WHERE run_id=$1`, claimE.Run.ID).Scan(&afterGenericArtifacts); err != nil {
		t.Fatal(err)
	}
	if afterGenericLogs != beforeLogs || afterGenericArtifacts != beforeArtifacts {
		t.Fatalf("generic deployment appends changed logs=%d/%d artifacts=%d/%d", beforeLogs, afterGenericLogs, beforeArtifacts, afterGenericArtifacts)
	}
	postRunnerEvents := func(fence, key string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"events":[{"event_key":%q,"sequence":1,"stream":"stdout","message":"deployment log"}]}`, claimE.Run.ID, claimE.Lease.ID, claimE.Lease.Attempt, fence, key)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/events/batch", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	postRunnerArtifact := func(fence, name string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"name":%q,"path":%q,"found":true,"required":true,"size":1,"kind":"file"}`, claimE.Run.ID, claimE.Lease.ID, claimE.Lease.Attempt, fence, name, name)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/artifacts", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if rec := postRunnerEvents(claimE.Lease.Fence, "deployment-event"); rec.Code != http.StatusOK {
		t.Fatalf("fenced runner batch response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postRunnerArtifact(claimE.Lease.Fence, "deployment-artifact"); rec.Code != http.StatusCreated {
		t.Fatalf("fenced runner artifact response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postRunnerEvents("stale", "stale-deployment-event"); rec.Code != http.StatusNotFound {
		t.Fatalf("stale runner batch response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postRunnerArtifact("stale", "stale-deployment-artifact"); rec.Code != http.StatusNotFound {
		t.Fatalf("stale runner artifact response %d: %s", rec.Code, rec.Body.String())
	}
	genericCancel := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/cancel", bytes.NewBufferString(fmt.Sprintf(`{"run_id":%q}`, claimE.Run.ID)))
		req.Header.Set("Authorization", "Bearer "+adminSession.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if rec := genericCancel(); rec.Code != http.StatusConflict {
		t.Fatalf("generic deployment cancel response %d: %s", rec.Code, rec.Body.String())
	}
	var deploymentStatus, pendingAttemptStatus, activeLeaseStatus, activeRunStatus string
	if err = database.QueryRow(ctx, `
SELECT d.status, da.status, rl.status, tr.status
FROM deployments d
JOIN deployment_attempts da ON da.deployment_id=d.id AND da.attempt=$2
JOIN run_leases rl ON rl.id=da.lease_id
JOIN task_runs tr ON tr.id=da.run_id
WHERE d.id=$1`, depE.ID, claimE.Lease.Attempt).Scan(&deploymentStatus, &pendingAttemptStatus, &activeLeaseStatus, &activeRunStatus); err != nil {
		t.Fatal(err)
	}
	if deploymentStatus != domain.DeploymentPreparing || pendingAttemptStatus != "active" || activeLeaseStatus != domain.LeaseActive || activeRunStatus != domain.RunRunning {
		t.Fatalf("generic cancel mutated deployment=%q attempt=%q lease=%q run=%q", deploymentStatus, pendingAttemptStatus, activeLeaseStatus, activeRunStatus)
	}
	if _, err = pg.CancelRunRequest(ctx, claimE.Run.ID, time.Now().UTC(), domain.RunLog{ID: "log_direct_cancel_" + suffix, RunID: claimE.Run.ID, Stream: domain.LogSystem}, domain.AuditEvent{ID: "audit_direct_cancel_" + suffix}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment cancel = %v", err)
	}
	if _, err = pg.UpdateRunStatus(ctx, claimE.Run.ID, domain.RunCanceled, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment status update = %v", err)
	}
	if _, err = pg.UpdateRunWorkflowState(ctx, claimE.Run.ID, domain.WorkflowState{}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment workflow update = %v", err)
	}
	if _, err = pg.CreateApproval(ctx, domain.Approval{ID: "approval_deployment_" + suffix, RunID: claimE.Run.ID, Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("direct generic deployment approval = %v", err)
	}
	if rec := postTransition(depE, claimE, domain.DeploymentPreparing, domain.DeploymentCanceled, "ecanceled", claimE.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("fenced cancellation response %d: %s", rec.Code, rec.Body.String())
	}
	assertTerminalLifecycle(depE, claimE, domain.RunCanceled)
	if rec := genericCancel(); rec.Code != http.StatusConflict {
		t.Fatalf("stale generic deployment cancel response %d: %s", rec.Code, rec.Body.String())
	}
	if _, err = pg.CancelRunRequest(ctx, claimE.Run.ID, time.Now().UTC(), domain.RunLog{ID: "log_stale_direct_cancel_" + suffix, RunID: claimE.Run.ID, Stream: domain.LogSystem}, domain.AuditEvent{ID: "audit_stale_direct_cancel_" + suffix}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale direct generic deployment cancel = %v", err)
	}
	if err = pg.CreateRunLog(ctx, domain.RunLog{ID: "log_terminal_generic_deployment_" + suffix, RunID: claimE.Run.ID, Sequence: 1, Stream: domain.LogSystem}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("terminal generic deployment log = %v", err)
	}
	if err = pg.CreateArtifact(ctx, domain.ArtifactRecord{ID: "art_terminal_generic_deployment_" + suffix, RunID: claimE.Run.ID, LeaseID: claimE.Lease.ID, Name: "terminal", Path: "terminal", Kind: domain.ArtifactFile}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("terminal generic deployment artifact = %v", err)
	}
	if rec := postRunnerEvents(claimE.Lease.Fence, "terminal-deployment-event"); rec.Code != http.StatusNotFound {
		t.Fatalf("terminal runner batch response %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postRunnerArtifact(claimE.Lease.Fence, "terminal-deployment-artifact"); rec.Code != http.StatusNotFound {
		t.Fatalf("terminal runner artifact response %d: %s", rec.Code, rec.Body.String())
	}
	var terminalLogs, terminalArtifacts int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id=$1`, claimE.Run.ID).Scan(&terminalLogs); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM run_artifacts WHERE run_id=$1`, claimE.Run.ID).Scan(&terminalArtifacts); err != nil {
		t.Fatal(err)
	}
	if terminalLogs != beforeLogs+1 || terminalArtifacts != beforeArtifacts+1 {
		t.Fatalf("terminal deployment append changed logs=%d artifacts=%d", terminalLogs, terminalArtifacts)
	}

	// A post-apply cancellation can escalate to manual intervention, which is
	// terminal and loud: it fails the exact run/lease/attempt but never moves
	// the healthy pointer.
	depF := create("dep_fenced_manual_"+suffix, "run_fenced_manual_"+suffix, revisionA, "manual")
	claimF, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "fprepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "fapply"}, {domain.DeploymentApplying, domain.DeploymentCancelRequested, "fcancel-requested"}, {domain.DeploymentCancelRequested, domain.DeploymentManualIntervention, "fmanual"}} {
		if rec := postTransition(depF, claimF, step[0], step[1], step[2], claimF.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	assertTerminalLifecycle(depF, claimF, domain.RunFailed)
	environments, err = pg.ListEnvironments(ctx, serviceID)
	if err != nil || environments[0].CurrentHealthyRevisionID == nil || *environments[0].CurrentHealthyRevisionID != revisionB {
		t.Fatalf("manual intervention changed healthy pointer %#v, %v", environments, err)
	}

	postPreAssignmentFailure := func(deploymentID, failureCode string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"id":%q,"failure_code":%q}`, deploymentID, failureCode)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/fail-preassignment", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+adminSession.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	preFailed := create("dep_fenced_pre_failed_"+suffix, "run_fenced_pre_failed_"+suffix, revisionA, "pre-failed")
	if rec := postPreAssignmentFailure(preFailed.ID, "validation_failed"); rec.Code != http.StatusOK {
		t.Fatalf("pre-assignment failure response %d: %s", rec.Code, rec.Body.String())
	}
	var preFailureAudits int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='deployment.preassignment_fail'`, preFailed.ID).Scan(&preFailureAudits); err != nil {
		t.Fatal(err)
	}
	if rec := postPreAssignmentFailure(preFailed.ID, "validation_failed"); rec.Code != http.StatusOK {
		t.Fatalf("idempotent pre-assignment failure response %d: %s", rec.Code, rec.Body.String())
	}
	var replayPreFailureAudits int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='deployment.preassignment_fail'`, preFailed.ID).Scan(&replayPreFailureAudits); err != nil {
		t.Fatal(err)
	}
	if replayPreFailureAudits != preFailureAudits {
		t.Fatalf("pre-assignment failure replay duplicated audit %d/%d", preFailureAudits, replayPreFailureAudits)
	}
	var preDeploymentStatus, preRunStatus string
	if err = database.QueryRow(ctx, `SELECT d.status, tr.status FROM deployments d JOIN task_runs tr ON tr.id=d.task_run_id WHERE d.id=$1`, preFailed.ID).Scan(&preDeploymentStatus, &preRunStatus); err != nil {
		t.Fatal(err)
	}
	if preDeploymentStatus != domain.DeploymentFailed || preRunStatus != domain.RunFailed {
		t.Fatalf("pre-assignment failure lifecycle deployment=%q run=%q", preDeploymentStatus, preRunStatus)
	}

	// Post-apply failure is one runner-authored atomic operation: its source
	// retains the environment lock while exactly one child rollback is queued.
	revisionC := "rev_fenced_rollback_" + suffix
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: revisionC, ServiceID: serviceID, RequestedRef: "rollback", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := create("dep_fenced_rollback_source_"+suffix, "run_fenced_rollback_source_"+suffix, revisionC, "rollback-source")
	claimSource, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postPreAssignmentFailure(source.ID, "too-late"); rec.Code != http.StatusConflict {
		t.Fatalf("assigned pre-assignment failure response %d: %s", rec.Code, rec.Body.String())
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "source-prepare"}} {
		if rec := postTransition(source, claimSource, step[0], step[1], step[2], claimSource.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	if rec := postProvenance(source, claimSource, "resolution-source", strings.Repeat("7", 40), "sha256:"+strings.Repeat("8", 64), []string{"sha256:" + strings.Repeat("9", 64)}, claimSource.Lease.Fence); rec.Code != http.StatusOK {
		t.Fatalf("source provenance %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postTransition(source, claimSource, domain.DeploymentPreparing, domain.DeploymentApplying, "source-apply", claimSource.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source apply %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postTransition(source, claimSource, domain.DeploymentApplying, domain.DeploymentRolledBack, "source-root-bypass", claimSource.Lease.Fence, nil); rec.Code != http.StatusConflict {
		t.Fatalf("root direct rollback response %d: %s", rec.Code, rec.Body.String())
	}
	var genericRunsBefore, genericDeploymentsBefore, genericAuditsBefore int
	if err = database.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_runs),(SELECT count(*) FROM deployments),(SELECT count(*) FROM audit_events)`).Scan(&genericRunsBefore, &genericDeploymentsBefore, &genericAuditsBefore); err != nil {
		t.Fatal(err)
	}
	genericChildBody := fmt.Sprintf(`{"environment_id":%q,"desired_revision_id":%q,"idempotency_key":"generic-rollback-child-%s","rollback_of_id":%q}`, environmentID, revisionB, suffix, source.ID)
	genericChildReq := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewBufferString(genericChildBody))
	genericChildReq.Header.Set("Authorization", "Bearer "+adminSession.Token)
	genericChildReq.Header.Set("Content-Type", "application/json")
	genericChildRec := httptest.NewRecorder()
	server.ServeHTTP(genericChildRec, genericChildReq)
	if genericChildRec.Code != http.StatusConflict {
		t.Fatalf("generic rollback child API response %d: %s", genericChildRec.Code, genericChildRec.Body.String())
	}
	var genericRunsAfter, genericDeploymentsAfter, genericAuditsAfter int
	if err = database.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_runs),(SELECT count(*) FROM deployments),(SELECT count(*) FROM audit_events)`).Scan(&genericRunsAfter, &genericDeploymentsAfter, &genericAuditsAfter); err != nil || genericRunsAfter != genericRunsBefore || genericDeploymentsAfter != genericDeploymentsBefore || genericAuditsAfter != genericAuditsBefore {
		t.Fatalf("generic rollback child mutated runs=%d/%d deployments=%d/%d audits=%d/%d err=%v", genericRunsBefore, genericRunsAfter, genericDeploymentsBefore, genericDeploymentsAfter, genericAuditsBefore, genericAuditsAfter, err)
	}
	postFailureWith := func(requestID, fence, failureCode string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"request_id":%q,"expected_status":"applying","failure_code":%q}`, source.ID, claimSource.Run.ID, claimSource.Lease.ID, claimSource.Lease.Attempt, fence, requestID, failureCode)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/fail-and-rollback", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	childID, childRunID := domain.RollbackObjectIDs(source.ID, "failure-source")
	postFailure := func(requestID, fence string) *httptest.ResponseRecorder {
		return postFailureWith(requestID, fence, "health_failed")
	}
	if rec := postFailure("failure-source", "stale"); rec.Code != http.StatusForbidden {
		t.Fatalf("stale failure fence %d: %s", rec.Code, rec.Body.String())
	}
	// Race two indistinguishable reports. One mutation and one replay ACK are
	// required even though both requests initially observe no child receipt.
	failureResponses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { failureResponses <- postFailure("failure-source", claimSource.Lease.Fence) }()
	}
	for range 2 {
		if rec := <-failureResponses; rec.Code != http.StatusOK {
			t.Fatalf("concurrent atomic failure %d: %s", rec.Code, rec.Body.String())
		}
	}
	var rootStatus, childStatus string
	var children int
	if err = database.QueryRow(ctx, `SELECT status FROM deployments WHERE id=$1`, source.ID).Scan(&rootStatus); err != nil || rootStatus != domain.DeploymentRollingBack {
		t.Fatalf("source lifecycle status=%q err=%v", rootStatus, err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*), min(status) FROM deployments WHERE rollback_of_id=$1`, source.ID).Scan(&children, &childStatus); err != nil || children != 1 || childStatus != domain.DeploymentQueued {
		t.Fatalf("rollback child count/status=%d/%q err=%v", children, childStatus, err)
	}
	assertTerminalLifecycle(source, claimSource, domain.RunFailed)
	var failureAudits, rollbackAudits, failureTransitions int
	if err = database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='runner.deployment.failed'), count(*) FILTER (WHERE action='runner.deployment.rollback_queued') FROM audit_events WHERE target_id IN ($1,$2)`, source.ID, childID).Scan(&failureAudits, &rollbackAudits); err != nil || failureAudits != 1 || rollbackAudits != 1 {
		t.Fatalf("concurrent failure audits failed=%d rollback=%d err=%v", failureAudits, rollbackAudits, err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM deployment_transitions WHERE deployment_id=$1 AND transition_key='failure:failure-source'`, source.ID).Scan(&failureTransitions); err != nil || failureTransitions != 1 {
		t.Fatalf("failure receipt transitions=%d err=%v", failureTransitions, err)
	}
	if rec := postFailureWith("failure-source", claimSource.Lease.Fence, "different_failure"); rec.Code != http.StatusConflict {
		t.Fatalf("changed replay body response %d: %s", rec.Code, rec.Body.String())
	}
	if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if rec := postFailure("failure-source", claimSource.Lease.Fence); rec.Code != http.StatusOK {
		t.Fatalf("post-terminal exact replay %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postFailure("unseen-after-terminal", claimSource.Lease.Fence); rec.Code != http.StatusForbidden {
		t.Fatalf("unseen terminal replay %d: %s", rec.Code, rec.Body.String())
	}

	// The linked child is the only new execution. Its terminal outcome settles
	// both records atomically and, on success, restores the previous healthy
	// pointer. The source's completed run is never reaped/reclaimed.
	claimChild, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil || claimChild.Run.ID != childRunID {
		t.Fatalf("claim rollback child %#v err=%v", claimChild, err)
	}
	child := domain.Deployment{ID: childID, TaskRunID: &childRunID}
	if rec := postTransition(child, claimChild, domain.DeploymentAssigned, domain.DeploymentFailed, "child-ordinary-fail", claimChild.Lease.Fence, nil); rec.Code != http.StatusConflict {
		t.Fatalf("child ordinary terminal response %d: %s", rec.Code, rec.Body.String())
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "child-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "child-apply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "child-verify"}} {
		if rec := postTransition(child, claimChild, step[0], step[1], step[2], claimChild.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	if rec := postTransition(child, claimChild, domain.DeploymentVerifying, domain.DeploymentRolledBack, "child-rolled-back", claimChild.Lease.Fence, &yes); rec.Code != http.StatusOK {
		t.Fatalf("child rolled back response %d: %s", rec.Code, rec.Body.String())
	}
	assertTerminalLifecycle(child, claimChild, domain.RunSucceeded)
	var sourceAfterSuccess, childAfterSuccess, pointerAfterSuccess string
	if err = database.QueryRow(ctx, `SELECT d.status,(SELECT status FROM deployments WHERE id=$2),e.current_healthy_revision_id FROM deployments d JOIN environments e ON e.id=d.environment_id WHERE d.id=$1`, source.ID, childID).Scan(&sourceAfterSuccess, &childAfterSuccess, &pointerAfterSuccess); err != nil || sourceAfterSuccess != domain.DeploymentRolledBack || childAfterSuccess != domain.DeploymentRolledBack || pointerAfterSuccess != revisionB {
		t.Fatalf("successful rollback settlement source=%q child=%q pointer=%q err=%v", sourceAfterSuccess, childAfterSuccess, pointerAfterSuccess, err)
	}
	if _, err = pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal source or child reaped/reclaimed: %v", err)
	}
	for _, terminalRunID := range []string{claimSource.Run.ID, claimChild.Run.ID} {
		if _, err = pg.UpdateRunStatus(ctx, terminalRunID, domain.RunFailed, nil); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("terminal deployment generic status update %s = %v", terminalRunID, err)
		}
	}
	if rec := postTransition(child, claimChild, domain.DeploymentRolledBack, domain.DeploymentRollbackFailed, "child-terminal-mutate", claimChild.Lease.Fence, nil); rec.Code != http.StatusConflict {
		t.Fatalf("terminal child mutation response %d: %s", rec.Code, rec.Body.String())
	}

	// Independently prove the loud rollback-failed settlement path. It must
	// release the root lock but leave the verified healthy pointer untouched.
	revisionD := "rev_fenced_rollback_failed_" + suffix
	if _, err = pg.CreateRevision(ctx, domain.Revision{ID: revisionD, ServiceID: serviceID, RequestedRef: "rollback-failed", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source2 := create("dep_fenced_rollback_failed_source_"+suffix, "run_fenced_rollback_failed_source_"+suffix, revisionD, "rollback-failed-source")
	claimSource2, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "source2-prepare"}} {
		if rec := postTransition(source2, claimSource2, step[0], step[1], step[2], claimSource2.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	if rec := postProvenance(source2, claimSource2, "resolution-source2", strings.Repeat("1", 40), "sha256:"+strings.Repeat("2", 64), []string{"sha256:" + strings.Repeat("3", 64)}, claimSource2.Lease.Fence); rec.Code != http.StatusOK {
		t.Fatalf("source2 provenance %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postTransition(source2, claimSource2, domain.DeploymentPreparing, domain.DeploymentApplying, "source2-apply", claimSource2.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source2 apply %d: %s", rec.Code, rec.Body.String())
	}
	child2ID, child2RunID := domain.RollbackObjectIDs(source2.ID, "failure-source2")
	body2 := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"request_id":"failure-source2","expected_status":"applying","failure_code":"apply_failed"}`, source2.ID, claimSource2.Run.ID, claimSource2.Lease.ID, claimSource2.Lease.Attempt, claimSource2.Lease.Fence)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/fail-and-rollback", bytes.NewBufferString(body2))
	req2.Header.Set("Authorization", "Bearer "+runnerToken)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("source2 atomic failure %d: %s", rec2.Code, rec2.Body.String())
	}
	claimChild2, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil || claimChild2.Run.ID != child2RunID {
		t.Fatalf("claim rollback child2 %#v err=%v", claimChild2, err)
	}
	child2 := domain.Deployment{ID: child2ID, TaskRunID: &child2RunID}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "child2-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "child2-apply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "child2-verify"}, {domain.DeploymentVerifying, domain.DeploymentRollbackFailed, "child2-rollback-failed"}} {
		if rec := postTransition(child2, claimChild2, step[0], step[1], step[2], claimChild2.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	assertTerminalLifecycle(child2, claimChild2, domain.RunFailed)
	var sourceAfterFailure, childAfterFailure, pointerAfterFailure string
	if err = database.QueryRow(ctx, `SELECT d.status,(SELECT status FROM deployments WHERE id=$2),e.current_healthy_revision_id FROM deployments d JOIN environments e ON e.id=d.environment_id WHERE d.id=$1`, source2.ID, child2ID).Scan(&sourceAfterFailure, &childAfterFailure, &pointerAfterFailure); err != nil || sourceAfterFailure != domain.DeploymentRollbackFailed || childAfterFailure != domain.DeploymentRollbackFailed || pointerAfterFailure != revisionB {
		t.Fatalf("failed rollback settlement source=%q child=%q pointer=%q err=%v", sourceAfterFailure, childAfterFailure, pointerAfterFailure, err)
	}

	// A stale healthy pointer invalidates rollback safety before *any* source,
	// run, lease, attempt, audit, or child mutation is committed.
	source3 := create("dep_fenced_rollback_mismatch_source_"+suffix, "run_fenced_rollback_mismatch_source_"+suffix, revisionA, "rollback-mismatch-source")
	claimSource3, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postTransition(source3, claimSource3, domain.DeploymentAssigned, domain.DeploymentPreparing, "source3-prepare", claimSource3.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source3 prepare %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postTransition(source3, claimSource3, domain.DeploymentPreparing, domain.DeploymentApplying, "source3-apply", claimSource3.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source3 apply %d: %s", rec.Code, rec.Body.String())
	}
	if _, err = database.Exec(ctx, `UPDATE environments SET current_healthy_revision_id=$2 WHERE id=$1`, environmentID, revisionA); err != nil {
		t.Fatal(err)
	}
	var beforeStatus, afterStatus string
	var beforeChild, afterChild, beforeLease, afterLease, beforeAttempt, afterAttempt, beforeAudit, afterAudit int
	if err = database.QueryRow(ctx, `SELECT d.status,(SELECT count(*) FROM deployments WHERE rollback_of_id=$1),(SELECT count(*) FROM run_leases WHERE run_id=$2),(SELECT count(*) FROM deployment_attempts WHERE deployment_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$1) FROM deployments d WHERE d.id=$1`, source3.ID, claimSource3.Run.ID).Scan(&beforeStatus, &beforeChild, &beforeLease, &beforeAttempt, &beforeAudit); err != nil {
		t.Fatal(err)
	}
	mismatchBody := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"request_id":"failure-mismatch","expected_status":"applying","failure_code":"health_failed"}`, source3.ID, claimSource3.Run.ID, claimSource3.Lease.ID, claimSource3.Lease.Attempt, claimSource3.Lease.Fence)
	mismatchReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/fail-and-rollback", bytes.NewBufferString(mismatchBody))
	mismatchReq.Header.Set("Authorization", "Bearer "+runnerToken)
	mismatchReq.Header.Set("Content-Type", "application/json")
	mismatchRec := httptest.NewRecorder()
	server.ServeHTTP(mismatchRec, mismatchReq)
	if mismatchRec.Code != http.StatusConflict {
		t.Fatalf("pointer mismatch response %d: %s", mismatchRec.Code, mismatchRec.Body.String())
	}
	if err = database.QueryRow(ctx, `SELECT d.status,(SELECT count(*) FROM deployments WHERE rollback_of_id=$1),(SELECT count(*) FROM run_leases WHERE run_id=$2),(SELECT count(*) FROM deployment_attempts WHERE deployment_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$1) FROM deployments d WHERE d.id=$1`, source3.ID, claimSource3.Run.ID).Scan(&afterStatus, &afterChild, &afterLease, &afterAttempt, &afterAudit); err != nil {
		t.Fatal(err)
	}
	if afterStatus != beforeStatus || afterChild != beforeChild || afterLease != beforeLease || afterAttempt != beforeAttempt || afterAudit != beforeAudit {
		t.Fatalf("pointer mismatch mutated source=%q/%q child=%d/%d lease=%d/%d attempt=%d/%d audit=%d/%d", beforeStatus, afterStatus, beforeChild, afterChild, beforeLease, afterLease, beforeAttempt, afterAttempt, beforeAudit, afterAudit)
	}
	if rec := postTransition(source3, claimSource3, domain.DeploymentApplying, domain.DeploymentCancelRequested, "source3-cleanup-request", claimSource3.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source3 cleanup cancellation request %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postTransition(source3, claimSource3, domain.DeploymentCancelRequested, domain.DeploymentManualIntervention, "source3-cleanup-manual", claimSource3.Lease.Fence, nil); rec.Code != http.StatusOK {
		t.Fatalf("source3 cleanup manual intervention %d: %s", rec.Code, rec.Body.String())
	}

	// AC12: the maintainer cancellation receipt and the runner-authenticated
	// rollback handoff are separate HTTP transactions.  The former is a stable
	// operator request; the latter is the only authority allowed to create the
	// server-derived child after an in-flight apply has been interrupted.
	if _, err = database.Exec(ctx, `UPDATE environments SET current_healthy_revision_id=$2 WHERE id=$1`, environmentID, revisionB); err != nil {
		t.Fatal(err)
	}
	cancelSource := create("dep_fenced_cancel_receipt_"+suffix, "run_fenced_cancel_receipt_"+suffix, revisionA, "cancel-receipt")
	claimCancel, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "cancel-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "cancel-apply"}} {
		if rec := postTransition(cancelSource, claimCancel, step[0], step[1], step[2], claimCancel.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	postMaintainerCancel := func(requestID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/cancel", bytes.NewBufferString(fmt.Sprintf(`{"deployment_id":%q,"request_id":%q}`, cancelSource.ID, requestID)))
		req.Header.Set("Authorization", "Bearer "+adminSession.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	cancelResponses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		go func() { cancelResponses <- postMaintainerCancel("ac12-cancel-receipt") }()
	}
	for range 8 {
		if rec := <-cancelResponses; rec.Code != http.StatusOK {
			t.Fatalf("concurrent maintainer cancel response %d: %s", rec.Code, rec.Body.String())
		}
	}
	if rec := postMaintainerCancel("ac12-cancel-receipt"); rec.Code != http.StatusOK {
		t.Fatalf("response-loss exact cancel replay %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postMaintainerCancel("ac12-cancel-changed"); rec.Code != http.StatusConflict {
		t.Fatalf("changed cancellation receipt %d: %s", rec.Code, rec.Body.String())
	}
	statusRequest := func(fence string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/runners/deployments/status?deployment_id=%s&run_id=%s&lease_id=%s&attempt=%d&fence=%s", cancelSource.ID, claimCancel.Run.ID, claimCancel.Lease.ID, claimCancel.Lease.Attempt, fence), nil)
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	cancelStatusRec := statusRequest(claimCancel.Lease.Fence)
	if cancelStatusRec.Code != http.StatusOK {
		t.Fatalf("runner cancellation status %d: %s", cancelStatusRec.Code, cancelStatusRec.Body.String())
	}
	var observed domain.DeploymentPlan
	if err := json.Unmarshal(cancelStatusRec.Body.Bytes(), &observed); err != nil || observed.Status != domain.DeploymentCancelRequested || observed.CancellationRequestID == nil || *observed.CancellationRequestID != "ac12-cancel-receipt" {
		t.Fatalf("cancellation watcher receipt %#v err=%v", observed, err)
	}
	if rec := statusRequest("stale"); rec.Code != http.StatusForbidden {
		t.Fatalf("stale fence observed cancellation receipt %d: %s", rec.Code, rec.Body.String())
	}
	postCancellationHandoff := func(fence, requestID string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"request_id":%q,"cancellation_request_id":%q,"expected_status":"cancel_requested","failure_code":"cancellation_requested"}`, cancelSource.ID, claimCancel.Run.ID, claimCancel.Lease.ID, claimCancel.Lease.Attempt, fence, requestID, requestID)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/fail-and-rollback", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if rec := postCancellationHandoff("stale", "ac12-cancel-receipt"); rec.Code != http.StatusForbidden {
		t.Fatalf("stale fence cancellation handoff %d: %s", rec.Code, rec.Body.String())
	}
	handoffResponses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { handoffResponses <- postCancellationHandoff(claimCancel.Lease.Fence, "ac12-cancel-receipt") }()
	}
	for range 2 {
		if rec := <-handoffResponses; rec.Code != http.StatusOK {
			t.Fatalf("concurrent cancellation handoff %d: %s", rec.Code, rec.Body.String())
		}
	}
	if rec := postCancellationHandoff(claimCancel.Lease.Fence, "ac12-cancel-receipt"); rec.Code != http.StatusOK {
		t.Fatalf("response-loss cancellation handoff replay %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postCancellationHandoff(claimCancel.Lease.Fence, "ac12-handoff-changed"); rec.Code != http.StatusConflict {
		t.Fatalf("changed cancellation handoff receipt %d: %s", rec.Code, rec.Body.String())
	}
	var cancellationRows, cancellationAudits, handoffAudits, queuedAudits, cancellationChildren, activeCancellationLeases int
	if err := database.QueryRow(ctx, `SELECT (SELECT count(*) FROM deployment_cancellations WHERE deployment_id=$1), (SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='deployment.cancel'), (SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='runner.deployment.cancellation_rollback'), (SELECT count(*) FROM audit_events WHERE action='runner.deployment.rollback_queued' AND metadata->>'source_deployment_id'=$1), (SELECT count(*) FROM deployments WHERE rollback_of_id=$1), (SELECT count(*) FROM run_leases WHERE run_id=$2 AND status='active')`, cancelSource.ID, claimCancel.Run.ID).Scan(&cancellationRows, &cancellationAudits, &handoffAudits, &queuedAudits, &cancellationChildren, &activeCancellationLeases); err != nil {
		t.Fatal(err)
	}
	if cancellationRows != 1 || cancellationAudits != 1 || handoffAudits != 1 || queuedAudits != 1 || cancellationChildren != 1 || activeCancellationLeases != 0 {
		t.Fatalf("AC12 atomic receipt/audit/child counts receipt=%d cancel_audit=%d handoff_audit=%d queued_audit=%d children=%d active=%d", cancellationRows, cancellationAudits, handoffAudits, queuedAudits, cancellationChildren, activeCancellationLeases)
	}
	assertTerminalLifecycle(cancelSource, claimCancel, domain.RunFailed)
	// Free the environment through the normal linked-child path before racing
	// another deployment.  This also proves that the public cancellation
	// receipt did not strand an invisible lock.
	cancelChildID, cancelChildRunID := domain.RollbackObjectIDs(cancelSource.ID, "ac12-cancel-receipt")
	claimCancelChild, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil || claimCancelChild.Run.ID != cancelChildRunID {
		t.Fatalf("claim cancellation child %#v err=%v", claimCancelChild, err)
	}
	cancelChild := domain.Deployment{ID: cancelChildID, TaskRunID: &cancelChildRunID}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "cancel-child-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "cancel-child-apply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "cancel-child-verify"}} {
		if rec := postTransition(cancelChild, claimCancelChild, step[0], step[1], step[2], claimCancelChild.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	if rec := postTransition(cancelChild, claimCancelChild, domain.DeploymentVerifying, domain.DeploymentRolledBack, "cancel-child-terminal", claimCancelChild.Lease.Fence, &yes); rec.Code != http.StatusOK {
		t.Fatalf("cancel child terminal response %d: %s", rec.Code, rec.Body.String())
	}

	// DB clock expiry races the same public cancellation boundary.  Whichever
	// transaction serializes first, stale authority must not observe a receipt
	// or perform a handoff, and the reaper cannot leave an active lease/attempt.
	expiryEnvironmentID := "env_fenced_cancel_expiry_" + suffix
	if _, err := pg.CreateEnvironment(ctx, domain.Environment{ID: expiryEnvironmentID, ServiceID: serviceID, Name: "expiry-" + suffix, RunnerSelector: []string{}, ComposeProject: "expiry-" + suffix, HealthPolicy: domain.HealthPolicy{}, TimeoutSeconds: 60, SecretBindings: []domain.SecretBinding{}, RollbackSafe: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	expiryRace, err := pg.CreateDeploymentRequest(ctx, domain.Deployment{ID: "dep_fenced_cancel_expiry_" + suffix, EnvironmentID: expiryEnvironmentID, DesiredRevisionID: revisionA, IdempotencyKey: "cancel-expiry", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", FenceRequired: true, CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_fenced_cancel_expiry_" + suffix, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_fenced_cancel_expiry_" + suffix, ActorID: "usr_bootstrap", Action: "deployment.create", TargetID: "dep_fenced_cancel_expiry_" + suffix, Metadata: map[string]any{}, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.HeartbeatRunner(ctx, runnerID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claimExpiry, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "expiry-race-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "expiry-race-apply"}} {
		if rec := postTransition(expiryRace, claimExpiry, step[0], step[1], step[2], claimExpiry.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	time.Sleep(25 * time.Millisecond)
	expiryStart := make(chan struct{})
	expiryCancel := make(chan *httptest.ResponseRecorder, 1)
	expiryReap := make(chan error, 1)
	go func() {
		<-expiryStart
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/cancel", bytes.NewBufferString(fmt.Sprintf(`{"deployment_id":%q,"request_id":"ac12-expiry-race"}`, expiryRace.ID)))
		req.Header.Set("Authorization", "Bearer "+adminSession.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		expiryCancel <- rec
	}()
	go func() { <-expiryStart; expiryReap <- pg.ExpireLeases(ctx, time.Now().UTC()) }()
	close(expiryStart)
	if err := <-expiryReap; err != nil {
		t.Fatalf("expiry race reaper: %v", err)
	}
	expiryCancelRec := <-expiryCancel
	if expiryCancelRec.Code != http.StatusOK && expiryCancelRec.Code != http.StatusConflict {
		t.Fatalf("expiry race public cancel=%d: %s", expiryCancelRec.Code, expiryCancelRec.Body.String())
	}
	var expiryActiveLeases, expiryActiveAttempts int
	if err := database.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1 AND status='active'), (SELECT count(*) FROM deployment_attempts WHERE deployment_id=$2 AND status='active')`, claimExpiry.Run.ID, expiryRace.ID).Scan(&expiryActiveLeases, &expiryActiveAttempts); err != nil {
		t.Fatal(err)
	}
	if expiryActiveLeases != 0 || expiryActiveAttempts != 0 {
		t.Fatalf("expiry/cancel left orphan authority leases=%d attempts=%d", expiryActiveLeases, expiryActiveAttempts)
	}
	staleExpiryStatus := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/runners/deployments/status?deployment_id=%s&run_id=%s&lease_id=%s&attempt=%d&fence=%s", expiryRace.ID, claimExpiry.Run.ID, claimExpiry.Lease.ID, claimExpiry.Lease.Attempt, claimExpiry.Lease.Fence), nil)
	staleExpiryStatus.Header.Set("Authorization", "Bearer "+runnerToken)
	staleExpiryRec := httptest.NewRecorder()
	server.ServeHTTP(staleExpiryRec, staleExpiryStatus)
	if staleExpiryRec.Code != http.StatusForbidden {
		t.Fatalf("expired fence observed status %d: %s", staleExpiryRec.Code, staleExpiryRec.Body.String())
	}
	// If cancellation won, the reaper requeues the run but preserves the
	// receipt. A fresh fence, never the expired one, owns the explicit manual
	// fallback because this isolated environment has no healthy target.
	if expiryCancelRec.Code == http.StatusOK {
		if _, err := pg.HeartbeatRunner(ctx, runnerID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		claimExpiry2, claimErr := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
		if claimErr != nil || claimExpiry2.Run.ID != claimExpiry.Run.ID || claimExpiry2.Lease.Attempt <= claimExpiry.Lease.Attempt {
			t.Fatalf("fresh expiry claim %#v err=%v", claimExpiry2.Lease, claimErr)
		}
		body := fmt.Sprintf(`{"deployment_id":%q,"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"request_id":"ac12-expiry-race","cancellation_request_id":"ac12-expiry-race","expected_status":"cancel_requested","failure_code":"cancellation_requested"}`, expiryRace.ID, claimExpiry2.Run.ID, claimExpiry2.Lease.ID, claimExpiry2.Lease.Attempt, claimExpiry2.Lease.Fence)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/deployments/fail-and-rollback", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+runnerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("fresh expiry cancellation handoff %d: %s", rec.Code, rec.Body.String())
		}
		var expiryStatus string
		if err := database.QueryRow(ctx, `SELECT status FROM deployments WHERE id=$1`, expiryRace.ID).Scan(&expiryStatus); err != nil || expiryStatus != domain.DeploymentManualIntervention {
			t.Fatalf("expiry cancellation fallback status=%q err=%v", expiryStatus, err)
		}
	}

	// A real public cancel races the only legal runner terminal transition.
	// Either serial winner is acceptable; the losing operation must leave no
	// second receipt/audit or active source authority behind.
	successRace := create("dep_fenced_cancel_success_"+suffix, "run_fenced_cancel_success_"+suffix, revisionA, "cancel-success")
	claimSuccess, err := pg.ClaimRun(ctx, runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][3]string{{domain.DeploymentAssigned, domain.DeploymentPreparing, "success-race-prepare"}, {domain.DeploymentPreparing, domain.DeploymentApplying, "success-race-apply"}, {domain.DeploymentApplying, domain.DeploymentVerifying, "success-race-verify"}} {
		if rec := postTransition(successRace, claimSuccess, step[0], step[1], step[2], claimSuccess.Lease.Fence, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s response %d: %s", step[2], rec.Code, rec.Body.String())
		}
	}
	startRace := make(chan struct{})
	cancelRaceResponse := make(chan *httptest.ResponseRecorder, 1)
	successRaceResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-startRace
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/cancel", bytes.NewBufferString(fmt.Sprintf(`{"deployment_id":%q,"request_id":"ac12-success-race"}`, successRace.ID)))
		req.Header.Set("Authorization", "Bearer "+adminSession.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		cancelRaceResponse <- rec
	}()
	go func() {
		<-startRace
		successRaceResponse <- postTransition(successRace, claimSuccess, domain.DeploymentVerifying, domain.DeploymentSucceeded, "success-race-terminal", claimSuccess.Lease.Fence, &yes)
	}()
	close(startRace)
	cancelRaceRec, successRaceRec := <-cancelRaceResponse, <-successRaceResponse
	var successRaceStatus string
	if err := database.QueryRow(ctx, `SELECT status FROM deployments WHERE id=$1`, successRace.ID).Scan(&successRaceStatus); err != nil {
		t.Fatal(err)
	}
	switch successRaceStatus {
	case domain.DeploymentSucceeded:
		if successRaceRec.Code != http.StatusOK || cancelRaceRec.Code != http.StatusConflict {
			t.Fatalf("success winner cancel=%d success=%d", cancelRaceRec.Code, successRaceRec.Code)
		}
		assertTerminalLifecycle(successRace, claimSuccess, domain.RunSucceeded)
	case domain.DeploymentCancelRequested:
		if cancelRaceRec.Code != http.StatusOK || successRaceRec.Code != http.StatusConflict {
			t.Fatalf("cancel winner cancel=%d success=%d", cancelRaceRec.Code, successRaceRec.Code)
		}
		var active int
		if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE run_id=$1 AND status='active'`, claimSuccess.Run.ID).Scan(&active); err != nil || active != 1 {
			t.Fatalf("cancel winner authority active=%d err=%v", active, err)
		}
	default:
		t.Fatalf("illegal cancel/success race status=%q", successRaceStatus)
	}
}

func TestPostgresClaimAndApprovalAuditRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "claim_approval_audit")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	runnerID, runID := "runner_audit_"+suffix, "run_audit_"+suffix
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"audit-" + suffix}, Capabilities: []string{domain.RunTypeShell}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: runID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"audit-" + suffix}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	duplicate := domain.AuditEvent{ID: "audit_claim_duplicate_" + suffix, ActorID: runnerID, Action: "runner.claim", TargetID: runID, Metadata: map[string]any{}, CreatedAt: now}
	if err := pg.CreateAuditEvent(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ClaimRun(ctx, runnerID, now, time.Minute, store.WithAudit(duplicate)); err == nil {
		t.Fatal("claim with a duplicate audit ID unexpectedly succeeded")
	}
	var status string
	var leases int
	if err := database.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, runID).Scan(&status); err != nil || status != domain.RunQueued {
		t.Fatalf("duplicate audit partially claimed run status=%q err=%v", status, err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE run_id=$1`, runID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("duplicate audit created leases=%d err=%v", leases, err)
	}
	claimAudit := domain.AuditEvent{ID: "audit_claim_ok_" + suffix, ActorID: runnerID, Action: "runner.claim", Metadata: map[string]any{}, CreatedAt: now}
	claim, err := pg.ClaimRun(ctx, runnerID, now, time.Minute, store.WithAudit(claimAudit))
	if err != nil {
		t.Fatal(err)
	}
	var claimAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1 AND target_id=$2 AND metadata->>'lease_id'=$3 AND metadata->>'fence'=$4`, claimAudit.ID, runID, claim.Lease.ID, claim.Lease.Fence).Scan(&claimAudits); err != nil || claimAudits != 1 {
		t.Fatalf("claim audit count=%d err=%v", claimAudits, err)
	}

	approvalRunID := "run_approval_audit_" + suffix
	if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: approvalRunID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{}, Status: domain.RunWaitingApproval, RequestedBy: "usr_bootstrap", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateApproval(ctx, domain.Approval{ID: "approval_audit_" + suffix, RunID: approvalRunID, Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	duplicateApprovalAudit := domain.AuditEvent{ID: "audit_approval_duplicate_" + suffix, ActorID: "usr_bootstrap", Action: "run.approve", TargetID: approvalRunID, Metadata: map[string]any{}, CreatedAt: now}
	if err := pg.CreateAuditEvent(ctx, duplicateApprovalAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ApproveRun(ctx, approvalRunID, "usr_bootstrap", now, store.WithAudit(duplicateApprovalAudit)); err == nil {
		t.Fatal("approval with a duplicate audit ID unexpectedly succeeded")
	}
	var approvalStatus, approvalRunStatus string
	if err := database.QueryRow(ctx, `SELECT status FROM approvals WHERE run_id=$1`, approvalRunID).Scan(&approvalStatus); err != nil || approvalStatus != domain.ApprovalPending {
		t.Fatalf("duplicate approval audit partially resolved approval status=%q err=%v", approvalStatus, err)
	}
	if err := database.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, approvalRunID).Scan(&approvalRunStatus); err != nil || approvalRunStatus != domain.RunWaitingApproval {
		t.Fatalf("duplicate approval audit partially queued run status=%q err=%v", approvalRunStatus, err)
	}
	approvalAudit := domain.AuditEvent{ID: "audit_approval_ok_" + suffix, ActorID: "usr_bootstrap", Action: "run.approve", Metadata: map[string]any{}, CreatedAt: now}
	approval, err := pg.ApproveRun(ctx, approvalRunID, "usr_bootstrap", now, store.WithAudit(approvalAudit))
	if err != nil {
		t.Fatal(err)
	}
	var approvalAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1 AND target_id=$2 AND metadata->>'approval_id'=$3`, approvalAudit.ID, approvalRunID, approval.ID).Scan(&approvalAudits); err != nil || approvalAudits != 1 {
		t.Fatalf("approval audit count=%d err=%v", approvalAudits, err)
	}
}

func TestPostgresDeploymentRequestSerializesOneActiveEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, _ := openPostgresIntegrationStore(t, ctx, "deployment_lock")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	suffix := strconv.FormatInt(now.UnixNano(), 36)
	serviceID, environmentID := "svc_lock_"+suffix, "env_lock_"+suffix
	if _, err := pg.CreateService(ctx, domain.Service{ID: serviceID, ProjectID: "proj_platform", Name: "lock-" + suffix, RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", Profiles: []string{}, OwnerID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateEnvironment(ctx, domain.Environment{ID: environmentID, ServiceID: serviceID, Name: "prod", RunnerSelector: []string{}, ComposeProject: "lock-" + suffix, HealthPolicy: domain.HealthPolicy{}, TimeoutSeconds: 60, SecretBindings: []domain.SecretBinding{}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	revisionID := "rev_lock_" + suffix
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: revisionID, ServiceID: serviceID, RequestedRef: "main", GitCommit: "commit-" + suffix, ComposeHash: "hash", ImageDigests: []string{}, ContentIdentity: "identity-" + suffix, CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var wins int
	var lock sync.Mutex
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			id := fmt.Sprintf("dep_lock_%s_%d", suffix, i)
			_, createErr := pg.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: environmentID, DesiredRevisionID: revisionID, IdempotencyKey: fmt.Sprintf("key-%d", i), Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", FenceRequired: true, CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_" + id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_" + id, ActorID: "usr_bootstrap", Action: "deployment.create", TargetID: id, Metadata: map[string]any{}, CreatedAt: now})
			if createErr == nil {
				lock.Lock()
				wins++
				lock.Unlock()
				return
			}
			if !errors.Is(createErr, store.ErrConflict) {
				t.Errorf("request %d: %v", i, createErr)
			}
		}(i)
	}
	group.Wait()
	if wins != 1 {
		t.Fatalf("active environment admitted %d deployments", wins)
	}
}

func TestPostgresIntegrationSQLCRoundTripsPaginationAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "sqlc")
	defer closePostgresStore(t, pg)

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
	defer closePostgresStore(t, pg)

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

	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})

	if err != nil {

		t.Fatal(err)

	}
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
	defer closePostgresStore(t, pg)
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
	defer closePostgresStore(t, pg)
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
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
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
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}

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
	defer closePostgresStore(t, pg)
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
	defer closePostgresStore(t, setupStore)
	completionStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, schemaURL, completionApplication))
	if err != nil {
		t.Fatal(err)
	}
	defer closePostgresStore(t, completionStore)
	claimStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, schemaURL, claimApplication))
	if err != nil {
		t.Fatal(err)
	}
	defer closePostgresStore(t, claimStore)

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
	defer func() {
		if err := barrier.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback barrier transaction: %v", err)
		}
	}()
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
	defer closePostgresStore(t, pg)

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
	defer closePostgresStore(t, pg)

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
	defer closePostgresStore(t, pg)
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
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
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
		closePostgresStore(t, preMigration)
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
	defer closePostgresStore(t, pg)
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

func TestPostgresIntegrationProvenanceMigratesLegacyAndAllowsPendingSiblings(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_provenance_upgrade_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = applyEmbeddedMigrations(ctx, database, "", "0026_repository_policy_configuration_receipts.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = database.Exec(ctx, `INSERT INTO services(id,project_id,name,repository_id,compose_path,owner_id,created_at) VALUES ('svc_legacy','proj_platform','legacy','repo_platform_runbooks','compose.yml','usr_bootstrap',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO revisions(id,service_id,requested_ref,git_commit,compose_hash,image_digests,content_identity,created_by,created_at) VALUES ('rev_legacy','svc_legacy','main',$1,$2,ARRAY[]::text[],$3,'usr_bootstrap',$4)`, strings.Repeat("1", 40), "sha256:"+strings.Repeat("2", 64), strings.Repeat("1", 40)+":sha256:"+strings.Repeat("2", 64), now); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(ctx, database, "0026_repository_policy_configuration_receipts.sql", ""); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = database.QueryRow(ctx, `SELECT provenance_state FROM revisions WHERE id='rev_legacy'`).Scan(&state); err != nil || state != "legacy_unverified" {
		t.Fatalf("legacy migration state=%q err=%v", state, err)
	}
	if _, err = database.Exec(ctx, `UPDATE revisions SET provenance_state='pending' WHERE id='rev_legacy'`); err == nil {
		t.Fatal("legacy provenance was promotable")
	}
	if _, err = database.Exec(ctx, `INSERT INTO revisions(id,service_id,requested_ref,git_commit,compose_hash,image_digests,content_identity,created_by,created_at,provenance_state,provenance_resolved,resolved_at) VALUES ('rev_bad_digest','svc_legacy','bad',$1,$2,ARRAY[NULL::text],$3,'usr_bootstrap',$4,'resolved',true,$4)`, strings.Repeat("3", 40), "sha256:"+strings.Repeat("4", 64), strings.Repeat("3", 40)+":sha256:"+strings.Repeat("4", 64), now); err == nil {
		t.Fatal("resolved revision accepted a NULL digest")
	}
	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer closePostgresStore(t, pg)
	for _, id := range []string{"rev_pending_one", "rev_pending_two"} {
		if _, err = pg.CreateRevision(ctx, domain.Revision{ID: id, ServiceID: "svc_legacy", RequestedRef: id, CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	var pending int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM revisions WHERE service_id='svc_legacy' AND provenance_state='pending' AND content_identity=''`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("pending siblings=%d err=%v", pending, err)
	}
}

func TestPostgresLinkedRollbackLifecycleMigratesFrom0026(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := "nerocd_rollback_upgrade_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	databaseURL = databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = applyEmbeddedMigrations(ctx, database, "", "0026_repository_policy_configuration_receipts.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seedStatements := []string{
		`INSERT INTO services(id,project_id,name,repository_id,compose_path,owner_id,created_at) VALUES ('svc_upgrade_rollback','proj_platform','upgrade rollback','repo_platform_runbooks','compose.yml','usr_bootstrap',$1)`,
		`INSERT INTO revisions(id,service_id,requested_ref,git_commit,compose_hash,image_digests,content_identity,created_by,created_at,provenance_state) VALUES ('rev_upgrade_a','svc_upgrade_rollback','a','','',ARRAY[]::text[],'','usr_bootstrap',$1,'pending'),('rev_upgrade_b','svc_upgrade_rollback','b','','',ARRAY[]::text[],'','usr_bootstrap',$1,'pending')`,
		`INSERT INTO environments(id,service_id,name,runner_selector,compose_project,health_policy,timeout_seconds,secret_bindings,rollback_safe,current_healthy_revision_id,created_at) VALUES ('env_upgrade_rollback','svc_upgrade_rollback','prod',ARRAY[]::text[],'upgrade-rollback','{}'::jsonb,60,'[]'::jsonb,true,'rev_upgrade_a',$1)`,
		`INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at) VALUES ('run_upgrade_source','proj_platform','{}'::jsonb,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,ARRAY[]::text[],'running','usr_bootstrap',$1),('run_upgrade_child','proj_platform','{}'::jsonb,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,ARRAY[]::text[],'queued','usr_bootstrap',$1),('run_upgrade_child_2','proj_platform','{}'::jsonb,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,ARRAY[]::text[],'queued','usr_bootstrap',$1),('run_upgrade_bad','proj_platform','{}'::jsonb,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,ARRAY[]::text[],'failed','usr_bootstrap',$1),('run_upgrade_bad_child','proj_platform','{}'::jsonb,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,ARRAY[]::text[],'queued','usr_bootstrap',$1)`,
		`INSERT INTO runners(id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash) VALUES ('runner_upgrade','runner upgrade',ARRAY[]::text[],ARRAY['compose_deploy']::text[],'active',$1,$1,'upgrade-token')`,
		`INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES ('lease_upgrade','run_upgrade_source','runner_upgrade','active',clock_timestamp() + interval '1 hour',$1,1,'upgrade-fence')`,
		`INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,created_at,updated_at,fence_required) VALUES ('dep_upgrade_source','env_upgrade_rollback','rev_upgrade_b','rev_upgrade_a','run_upgrade_source','source','rolling_back','usr_bootstrap',$1,$1,true),('dep_upgrade_bad','env_upgrade_rollback','rev_upgrade_b','rev_upgrade_a','run_upgrade_bad','bad-source','failed','usr_bootstrap',$1,$1,true)`,
		`INSERT INTO deployment_attempts(deployment_id,run_id,lease_id,runner_id,attempt,fence) VALUES ('dep_upgrade_source','run_upgrade_source','lease_upgrade','runner_upgrade',1,'upgrade-fence')`,
		`INSERT INTO deployment_transitions(deployment_id,attempt,transition_key,expected_status,target_status) VALUES ('dep_upgrade_source',1,'apply','preparing','applying')`,
	}
	for _, statement := range seedStatements {
		args := []any(nil)
		if strings.Contains(statement, "$1") {
			args = append(args, now)
		}
		if _, err = database.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed representative 0026 data: %v", err)
		}
	}
	if err = applyEmbeddedMigrations(ctx, database, "0026_repository_policy_configuration_receipts.sql", ""); err != nil {
		t.Fatal(err)
	}
	var indexDef string
	if err = database.QueryRow(ctx, `SELECT pg_get_indexdef('deployments_one_active_root_environment'::regclass)`).Scan(&indexDef); err != nil || !strings.Contains(indexDef, "rollback_of_id IS NULL") || !strings.Contains(indexDef, "rolling_back") {
		t.Fatalf("root-only active index definition=%q err=%v", indexDef, err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,fence_required,rollback_of_id) VALUES ('dep_upgrade_child','env_upgrade_rollback','rev_upgrade_a','rev_upgrade_a','run_upgrade_child','child','queued','usr_bootstrap',true,'dep_upgrade_source')`); err != nil {
		t.Fatalf("root lock did not admit linked rollback child after upgrade: %v", err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,fence_required,rollback_of_id) VALUES ('dep_upgrade_child_2','env_upgrade_rollback','rev_upgrade_a','rev_upgrade_a','run_upgrade_child_2','child-2','queued','usr_bootstrap',true,'dep_upgrade_source')`); err == nil {
		t.Fatal("upgrade admitted a second rollback child")
	}
	if _, err = database.Exec(ctx, `INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,fence_required,rollback_of_id) VALUES ('dep_upgrade_bad_child','env_upgrade_rollback','rev_upgrade_a','rev_upgrade_a','run_upgrade_bad_child','bad-child','queued','usr_bootstrap',true,'dep_upgrade_bad')`); err == nil {
		t.Fatal("upgrade trigger accepted non-rolling-back source")
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
		closePostgresStore(t, candidate)
	}
	assertRejected := func(label string) {
		t.Helper()
		candidate, err := store.OpenPostgres(ctx, schemaURL)
		if err == nil {
			closePostgresStore(t, candidate)
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

func TestPostgresRepositoryPolicyReceiptSchemaGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "policy_schema_guard")
	defer closePostgresStore(t, pg)
	assertRejected := func(label string) {
		t.Helper()
		candidate, err := store.OpenPostgres(ctx, database.Config().ConnString())
		if err == nil {
			closePostgresStore(t, candidate)
			t.Fatalf("%s malformed receipt schema accepted", label)
		}
	}
	assertAccepted := func(label string) {
		t.Helper()
		candidate, err := store.OpenPostgres(ctx, database.Config().ConnString())
		if err != nil {
			t.Fatalf("%s repaired receipt schema rejected: %v", label, err)
		}
		closePostgresStore(t, candidate)
	}
	assertAccepted("current")
	mutations := []struct{ breakDB, repair string }{
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_pk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_pk PRIMARY KEY (configuration_id,repository_id)`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_pk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_pk PRIMARY KEY (repository_id,configuration_id)`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_audit_unique; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_audit_unique UNIQUE (actor_id)`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_audit_unique; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_audit_unique UNIQUE (audit_id)`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_repository_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_repository_fk FOREIGN KEY (repository_id) REFERENCES repositories(id)`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_repository_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_repository_fk FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_sha256_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_sha256_format CHECK (true)`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_sha256_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_sha256_format CHECK (policy_sha256 ~ '^[0-9a-f]{64}$')`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_repository_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_repository_fk FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE NOT VALID`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_repository_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_repository_fk FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_actor_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_actor_fk FOREIGN KEY (actor_id) REFERENCES users(id) NOT VALID`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_actor_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_actor_fk FOREIGN KEY (actor_id) REFERENCES users(id)`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_audit_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_audit_fk FOREIGN KEY (audit_id) REFERENCES audit_events(id) NOT VALID`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_audit_fk; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_audit_fk FOREIGN KEY (audit_id) REFERENCES audit_events(id)`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_configuration_id_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_configuration_id_format CHECK (configuration_id ~ '^cfg_[A-Za-z0-9_-]{8,128}$') NOT VALID`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_configuration_id_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_configuration_id_format CHECK (configuration_id ~ '^cfg_[A-Za-z0-9_-]{8,128}$')`},
		{`ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_sha256_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_sha256_format CHECK (policy_sha256 ~ '^[0-9a-f]{64}$') NOT VALID`, `ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_sha256_format; ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_sha256_format CHECK (policy_sha256 ~ '^[0-9a-f]{64}$')`},
	}
	for _, mutation := range mutations {
		if _, err := database.Exec(ctx, mutation.breakDB); err != nil {
			t.Fatal(err)
		}
		assertRejected(mutation.breakDB)
		if _, err := database.Exec(ctx, mutation.repair); err != nil {
			t.Fatal(err)
		}
		assertAccepted(mutation.repair)
	}
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
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
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

func TestPostgresRepositoryPolicyConfigurationReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "policy_receipts")
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})
	policy := domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https", "ssh"}, AllowedHosts: []string{"z.example.local", "a.example.local"}, RedirectHosts: []string{"redirect.example.local"}, SSHHostFingerprints: []string{"SHA256:fixture-host-fingerprint"}, CredentialReferenceID: "cred_sentinel_12345678"}
	input := app.RepositoryPolicyInput{ID: "repo_platform_runbooks", ProjectID: "proj_platform", ConfigurationID: "cfg_receipt_12345678", Policy: policy}
	session, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"project_id": input.ProjectID, "configuration_id": input.ConfigurationID, "policy": input.Policy})
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/repo_platform_runbooks/policy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+session.Token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "cred_sentinel") {
		t.Fatalf("real HTTP configure status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Equivalent sets must canonicalize to the same receipt hash and replay.
	input.Policy.AllowedSchemes = []string{"ssh", "https"}
	input.Policy.AllowedHosts = []string{"a.example.local", "z.example.local"}
	if _, err = service.ConfigureRepositoryPolicy(admin, input); err != nil {
		t.Fatalf("canonical retry: %v", err)
	}
	input.Policy.AllowedHosts = []string{"different.example.local"}
	if _, err = service.ConfigureRepositoryPolicy(admin, input); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("mismatched key = %v", err)
	}
	input.Policy.AllowedHosts = []string{"a.example.local", "z.example.local"}
	input.ConfigurationID = "cfg_second_12345678"
	if _, err = service.ConfigureRepositoryPolicy(admin, input); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second configuration = %v", err)
	}

	// An exact request remains an acknowledgement under contention, not a
	// second audit or receipt.
	input.ConfigurationID = "cfg_receipt_12345678"
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := service.ConfigureRepositoryPolicy(admin, input); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent exact replay: %v", e)
		}
	}
	var receiptCount, auditCount int
	var receiptAt, auditAt time.Time
	var auditMetadata string
	if err = database.QueryRow(ctx, `SELECT count(*), min(created_at) FROM repository_policy_configuration_receipts WHERE repository_id=$1`, "repo_platform_runbooks").Scan(&receiptCount, &receiptAt); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*), min(created_at), coalesce(min(metadata::text),'') FROM audit_events WHERE target_id=$1 AND action='repository.policy.configure'`, "repo_platform_runbooks").Scan(&auditCount, &auditAt, &auditMetadata); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || auditCount != 1 || receiptAt.IsZero() || auditAt.IsZero() {
		t.Fatalf("receipt/audit cardinality or DB clocks: %d %d %v %v", receiptCount, auditCount, receiptAt, auditAt)
	}
	if strings.Contains(auditMetadata, "cred_sentinel") {
		t.Fatalf("credential reference leaked to audit: %s", auditMetadata)
	}
	var receiptText string
	if err = database.QueryRow(ctx, `SELECT row_to_json(r)::text FROM repository_policy_configuration_receipts r WHERE repository_id=$1`, "repo_platform_runbooks").Scan(&receiptText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(receiptText, "cred_sentinel") {
		t.Fatalf("credential reference leaked to receipt: %s", receiptText)
	}

	// A forced audit error must abort the preceding policy update and receipt.
	legacyID := "repo_policy_rollback_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err = pg.CreateRepository(ctx, domain.Repository{ID: legacyID, ProjectID: "proj_platform", Name: legacyID, URL: "https://example.local/rollback.git", Provider: domain.ProviderGit, DefaultRef: "main", Policy: domain.RepositoryPolicy{Version: 1, State: "legacy_unverified"}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `CREATE FUNCTION reject_policy_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='repository.policy.configure' THEN RAISE EXCEPTION 'forced policy audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_policy_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err = service.ConfigureRepositoryPolicy(admin, app.RepositoryPolicyInput{ID: legacyID, ProjectID: "proj_platform", ConfigurationID: "cfg_rollback_12345678", Policy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.local"}}})
	if err == nil {
		t.Fatal("forced audit failure configured repository")
	}
	var state string
	var count int
	if err = database.QueryRow(ctx, `SELECT repository_policy->>'state' FROM repositories WHERE id=$1`, legacyID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM repository_policy_configuration_receipts WHERE repository_id=$1`, legacyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if state != "legacy_unverified" || count != 0 {
		t.Fatalf("audit rollback state=%q receipts=%d", state, count)
	}
	if _, err = database.Exec(ctx, `DROP TRIGGER reject_policy_audit ON audit_events; DROP FUNCTION reject_policy_audit(); CREATE FUNCTION reject_policy_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced policy receipt failure'; END $$; CREATE TRIGGER reject_policy_receipt BEFORE INSERT ON repository_policy_configuration_receipts FOR EACH ROW EXECUTE FUNCTION reject_policy_receipt()`); err != nil {
		t.Fatal(err)
	}
	receiptFailureID := "repo_policy_receipt_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err = pg.CreateRepository(ctx, domain.Repository{ID: receiptFailureID, ProjectID: "proj_platform", Name: receiptFailureID, URL: "https://example.local/receipt.git", Provider: domain.ProviderGit, DefaultRef: "main", Policy: domain.RepositoryPolicy{Version: 1, State: "legacy_unverified"}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, err = service.ConfigureRepositoryPolicy(admin, app.RepositoryPolicyInput{ID: receiptFailureID, ProjectID: "proj_platform", ConfigurationID: "cfg_receipt_failure_12345678", Policy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.local"}}})
	if err == nil {
		t.Fatal("forced receipt failure configured repository")
	}
	if err = database.QueryRow(ctx, `SELECT repository_policy->>'state' FROM repositories WHERE id=$1`, receiptFailureID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow(ctx, `SELECT count(*) FROM repository_policy_configuration_receipts WHERE repository_id=$1`, receiptFailureID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if state != "legacy_unverified" || count != 0 {
		t.Fatalf("receipt rollback state=%q receipts=%d", state, count)
	}
}

func TestPostgresBootstrapAdminIsAtomicAndAudited(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresEmptyIntegrationStore(t, ctx, "bootstrap")
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "owner@example.invalid", Name: "Owner", Password: "bootstrap-password"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("bootstrap successes=%d, want one", successes)
	}
	var users, successAudits, deniedAudits int
	if err := database.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='identity.bootstrap_admin'").Scan(&successAudits); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='identity.bootstrap_admin.denied'").Scan(&deniedAudits); err != nil {
		t.Fatal(err)
	}
	if users != 1 || successAudits != 1 || deniedAudits != 7 {
		t.Fatalf("users=%d success_audits=%d denied_audits=%d", users, successAudits, deniedAudits)
	}
}

func TestPostgresSessionMetadataSurvivesRestartAndAdminRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "session_metadata")
	defer closePostgresStore(t, pg)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	newService := func() *app.Service {
		service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
		if err != nil {
			t.Fatal(err)
		}
		service.SetClock(func() time.Time { return now })
		return service
	}
	service := newService()
	created, err := service.CreateSessionWithMetadata(ctx, "admin@example.local", "admin", app.SessionCreateMetadata{SourceIP: "203.0.113.9", UserAgent: "session-metadata-test"})
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(service, slog.Default(), web.Static())
	me := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	me.Header.Set("Authorization", "Bearer "+created.Token)
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, me)
	if meRec.Code != http.StatusOK {
		t.Fatalf("session authentication=%d %s", meRec.Code, meRec.Body.String())
	}
	var initialXmin string
	var initialSeen time.Time
	if err := database.QueryRow(ctx, `SELECT xmin::text, last_seen_at FROM sessions WHERE id=$1`, created.Session.ID).Scan(&initialXmin, &initialSeen); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := service.AuthenticateSessionToken(ctx, created.Token); err != nil {
			t.Fatal(err)
		}
	}
	var repeatedXmin string
	var repeatedSeen time.Time
	if err := database.QueryRow(ctx, `SELECT xmin::text, last_seen_at FROM sessions WHERE id=$1`, created.Session.ID).Scan(&repeatedXmin, &repeatedSeen); err != nil {
		t.Fatal(err)
	}
	if repeatedXmin != initialXmin || !repeatedSeen.Equal(initialSeen) {
		t.Fatalf("repeated auth wrote session xmin=%s/%s seen=%s/%s", initialXmin, repeatedXmin, initialSeen, repeatedSeen)
	}
	now = now.Add(store.SessionLastSeenUpdateInterval)
	var authWG sync.WaitGroup
	for range 8 {
		authWG.Add(1)
		go func() {
			defer authWG.Done()
			if _, err := service.AuthenticateSessionToken(ctx, created.Token); err != nil {
				t.Errorf("interval authentication: %v", err)
			}
		}()
	}
	authWG.Wait()
	var advancedXmin string
	var advancedSeen time.Time
	if err := database.QueryRow(ctx, `SELECT xmin::text, last_seen_at FROM sessions WHERE id=$1`, created.Session.ID).Scan(&advancedXmin, &advancedSeen); err != nil {
		t.Fatal(err)
	}
	if advancedXmin == initialXmin || !advancedSeen.Equal(now) {
		t.Fatalf("interval update xmin=%s/%s seen=%s want=%s", initialXmin, advancedXmin, advancedSeen, now)
	}
	var sessionAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_id=$1`, created.Session.ID).Scan(&sessionAudits); err != nil || sessionAudits != 1 {
		t.Fatalf("auth added session audits count=%d err=%v", sessionAudits, err)
	}
	// A new HTTP/service instance exercises persisted session metadata rather
	// than only the original in-memory object.
	restarted := api.NewServer(newService(), slog.Default(), web.Static())
	list := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	list.Header.Set("Authorization", "Bearer "+created.Token)
	listRec := httptest.NewRecorder()
	restarted.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), created.Session.ID) || !strings.Contains(listRec.Body.String(), "203.0.113.9") || !strings.Contains(listRec.Body.String(), "last_seen_at") {
		t.Fatalf("persisted session list=%d %s", listRec.Code, listRec.Body.String())
	}
	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/revoke", strings.NewReader(fmt.Sprintf(`{"session_id":%q}`, created.Session.ID)))
	revoke.Header.Set("Authorization", "Bearer "+created.Token)
	revoke.Header.Set("Content-Type", "application/json")
	revokeRec := httptest.NewRecorder()
	restarted.ServeHTTP(revokeRec, revoke)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("session revoke=%d %s", revokeRec.Code, revokeRec.Body.String())
	}
	var revokeAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='session.revoke' AND target_id=$1`, created.Session.ID).Scan(&revokeAudits); err != nil || revokeAudits != 1 {
		t.Fatalf("session revoke audit count=%d err=%v", revokeAudits, err)
	}
	after := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	after.Header.Set("Authorization", "Bearer "+created.Token)
	afterRec := httptest.NewRecorder()
	restarted.ServeHTTP(afterRec, after)
	if afterRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session authentication=%d %s", afterRec.Code, afterRec.Body.String())
	}
}

func TestPostgresAuditEventsAreAppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, database := openPostgresIntegrationStore(t, ctx, "audit_append_only")
	defer closePostgresStore(t, pg)
	event := domain.AuditEvent{ID: "aud_append_only", ActorID: "system", Action: "test.audit", TargetID: "target", Metadata: map[string]any{}, CreatedAt: time.Now().UTC()}
	if err := pg.CreateAuditEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `UPDATE audit_events SET action='mutated' WHERE id=$1`, event.ID); err == nil {
		t.Fatal("append-only audit trigger accepted UPDATE")
	}
	if _, err := database.Exec(ctx, `DELETE FROM audit_events WHERE id=$1`, event.ID); err == nil {
		t.Fatal("append-only audit trigger accepted DELETE")
	}
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

func openPostgresEmptyIntegrationStore(t *testing.T, ctx context.Context, label string) (*store.PostgresStore, *pgxpool.Pool) {
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
		t.Fatal(err)
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
	if err := applyEmbeddedMigrations(ctx, database, "", ""); err != nil {
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
	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			t.Errorf("rollback transaction: %v", err)
		}
	}()
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
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		return err
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply development test seed: %w", err)
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
