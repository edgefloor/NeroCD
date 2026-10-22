package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestHostingBuildLogAPIActualAuthorityAndProjection(t *testing.T) {
	s, pg, db := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "log-api"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "logs", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/example/web", RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_log_api", RunnerName: "logs", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType}})
	if err != nil {
		t.Fatal(err)
	}
	token := "ncr_log_api_fixture"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	enrolled, err := s.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("b", 32), CredentialHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.app.CreateServer(admin, app.ServerInput{Name: "logs", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.GrantDestinationProject(admin, target.Destination.ID, app.DestinationProjectInput{ProjectID: resource.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: target.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	policy := domain.HostingBuildPolicy{Identity: d, ToolchainIdentity: d, Protocol: domain.HostingBuildProtocol, Platform: "linux/amd64", SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 10 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 10 << 20, UnpackedBytes: 10 << 20, UsageBytes: 20 << 20, ReserveBytes: 10 << 20, PollInterval: time.Second, DiskMode: "monitored"}}
	audit := func(id string) domain.AuditEvent {
		return domain.AuditEvent{ID: id, ActorID: "usr_bootstrap", CreatedAt: time.Now()}
	}
	// Metadata-only canonical setup, no source checkout or engine execution.
	b, err := pg.RequestHostingBuild(ctx, "api-logs", resource.ID, "usr_bootstrap", domain.HostingBuildInput{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: "api-logs"}, policy, func(context.Context, pgx.Tx, domain.HostingBuild) error { return nil }, audit("request_api_logs"))
	if err != nil {
		t.Fatal(err)
	}
	b, err = pg.DispatchHostingBuild(ctx, b.ID, func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) (string, error) {
		id := "build_run_" + b.ID
		spec, _ := json.Marshal(map[string]string{"type": domain.HostingBuildTaskType, "hosting_build_id": b.ID})
		_, e := tx.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at) VALUES($1,$2,$3,'{}','{}','{}','queued',$4,clock_timestamp(),clock_timestamp())`, id, b.ProjectID, spec, b.CreatedBy)
		return id, e
	}, audit("dispatch_api_logs"))
	if err != nil {
		t.Fatal(err)
	}
	runnerCtx := auth.WithPrincipal(ctx, auth.Principal{ID: enrolled.Runner.ID, Provider: domain.PrincipalRunner})
	claim, err := s.app.ClaimRun(runnerCtx)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	in := domain.HostingBuildLogBatch{Authority: a, Events: []domain.HostingBuildLogEvent{{EventKey: "api-log", Sequence: 1, Component: "build", Stream: "stdout", Message: "hello public build output"}}}
	body, _ := json.Marshal(in)
	request := func(method, path, credential, body string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	session := createTestSession(t, s)
	path := "/api/v1/hosting-builds/" + b.ID + "/logs"
	request("POST", "/api/v1/runners/builds/events", session, string(body), 401)
	raw := request("POST", "/api/v1/runners/builds/events", token, string(body), 200)
	var ack domain.HostingBuildLogAck
	if json.Unmarshal(raw, &ack) != nil || ack.Authority != a || len(ack.Receipts) != 1 {
		t.Fatal("ack mismatch", string(raw))
	}
	if strings.Contains(string(raw), in.Events[0].Message) {
		t.Fatal("ack echoed plaintext")
	}
	raw = request("GET", path, session, "", 200)
	if !strings.Contains(string(raw), in.Events[0].Message) {
		t.Fatal("missing output")
	}
	for _, internal := range []string{a.Fence, a.LeaseID, "event_key", "body_hash", "policy"} {
		if strings.Contains(string(raw), internal) {
			t.Fatal("projection leak", internal)
		}
	}
	viewer := createTestSessionFor(t, s, "viewer@example.local", "viewer")
	request("GET", path, viewer, "", 403)
	if _, err = pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "member_log_viewer", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	request("GET", path, viewer, "", 200)
	for _, query := range []string{"?after_sequence=-1", "?after_sequence=2147483648", "?limit=201", "?limit=0", "?limit=1&limit=2", "?policy=forged"} {
		request("GET", path+query, session, "", 400)
	}
	request("POST", "/api/v1/runners/builds/events", token, strings.TrimSuffix(string(body), "}")+`,"project_id":"forged"}`, 400)
	generic := fmt.Sprintf(`{"run_id":%q,"lease_id":%q,"attempt":%d,"fence":%q,"events":[{"event_key":"bypass","sequence":2,"stream":"stdout","message":"bypass"}]}`, a.RunID, a.LeaseID, a.Attempt, a.Fence)
	request("POST", "/api/v1/runners/events/batch", token, generic, 409)

	// A harmless older persisted generic plan with nested internal authority must
	// not evade the build-only endpoint requirement.
	nested := domain.TaskRun{ID: "run_nested_log_api", ProjectID: resource.ProjectID, RunSpec: domain.RunSpec{Type: "ansible"}, RunnerTags: []string{}, Status: domain.RunRunning, RequestedBy: "usr_bootstrap", StartedAt: time.Now()}
	if _, err = pg.CreateRun(ctx, nested); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE task_runs SET workflow='{"steps":[{"id":"nested","run_spec":{"type":"ansible","hosting_build_id":"forged"}}]}' WHERE id=$1`, nested.ID); err != nil {
		t.Fatal(err)
	}
	nestedBody := fmt.Sprintf(`{"run_id":%q,"lease_id":"lease_nested","attempt":1,"fence":"fence_nested","events":[{"event_key":"nested","sequence":1,"stream":"stdout","message":"metadata only"}]}`, nested.ID)
	request("POST", "/api/v1/runners/events/batch", token, nestedBody, 409)
	if _, err = db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second',status='expired' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/runners/builds/events", token, string(body), 200)
	in.Events = append(in.Events, domain.HostingBuildLogEvent{EventKey: "stale", Sequence: 2, Component: "build", Stream: "stdout", Message: "stale"})
	body, _ = json.Marshal(in)
	request("POST", "/api/v1/runners/builds/events", token, string(body), 409)
	if _, err = db.Exec(ctx, `UPDATE runners SET status='revoked' WHERE id=$1`, enrolled.Runner.ID); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/runners/builds/events", token, string(body), 401)
}
