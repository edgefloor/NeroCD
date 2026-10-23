package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/web"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is a registered API/browser qualification with a simulated trusted runner
// producer. It does not start Docker or qualify collector transport/coverage A29.
func TestHostingRuntimeLogsBrowser(t *testing.T) {
	if os.Getenv("NEROCD_RUNTIME_LOGS_BROWSER") != "1" {
		t.Skip("explicit browser fixture opt-in required")
	}
	s, pg, database := newHostingBuildAPIFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime logs browser"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "log fixture workload", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_log_browser", RunnerName: "simulated producer", Capabilities: []string{domain.RunTypeImageDeploy, domain.RunTypeComposeDeploy, domain.HostingRuntimeLogsCapability}})
	if err != nil {
		t.Fatal(err)
	}
	const runnerToken = "ncr_log_browser_simulated_credential"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(runnerToken)))
	used, err := s.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := s.app.CreateServer(admin, app.ServerInput{Name: "simulated log server", RunnerID: used.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.GrantDestinationProject(admin, destination.Destination.ID, app.DestinationProjectInput{ProjectID: resource.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: destination.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	for {
		tx, e := database.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		changed, e := hostingjobs.MigrateNextTx(ctx, tx)
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if e := stop(stopCtx); e != nil {
			t.Error(e)
		}
	})
	deploy := func(key, container string) domain.HostingOperation {
		t.Helper()
		op, e := s.app.RequestHostingDeployment(admin, resource.ID, domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: key, ContainerPort: 80, HostPort: 18090, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}})
		if e != nil {
			t.Fatal(e)
		}
		if e = pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		lease, e := pg.ClaimRun(ctx, used.Runner.ID, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		authority := domain.HostingAuthority{OperationID: op.ID, RunID: lease.Run.ID, LeaseID: lease.Lease.ID, Attempt: lease.Lease.Attempt, Fence: lease.Lease.Fence}
		if _, e = pg.HostingRunnerPlan(ctx, used.Runner.ID, authority); e != nil {
			t.Fatal(e)
		}
		release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "simulated_log_daemon", OS: "linux", Architecture: "amd64"}
		release.Hash = domain.HostingDocumentHash(release)
		if _, e = pg.HostingPrepared(ctx, used.Runner.ID, domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: key + "_prepared", Release: release}); e != nil {
			t.Fatal(e)
		}
		plan, e := pg.HostingApplyBegin(ctx, used.Runner.ID, authority)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pg.HostingComplete(ctx, used.Runner.ID, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: key + "_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: container, ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: plan.Plan.Hash}}); e != nil {
			t.Fatal(e)
		}
		return op
	}
	initial := deploy("browser_logs_initial", "simulated_initial_container")
	owner, err := s.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "logs-owner@example.local", Name: "Logs Fixture Owner", Password: "logs-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: resource.ProjectID, Email: owner.Email, Role: "owner"}, {ProjectID: resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		if _, err = s.app.UpsertProjectMember(admin, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	origin := os.Getenv("NEROCD_RUNTIME_LOGS_BROWSER_ORIGIN")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }() // Server shutdown may already close the listener.
	actualOrigin := "http://" + listener.Addr().String()
	if origin != "" && origin != actualOrigin {
		t.Fatal("browser fixture origin mismatch")
	}
	origin = actualOrigin
	browserAPI := NewServerWithConfig(s.app, slog.Default(), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	// The runner client uses a verified test-CA TLS transport, never implicit HTTP.
	producerServer := httptest.NewTLSServer(s)
	t.Cleanup(producerServer.Close)
	producer := runnerclient.New(producerServer.URL, runnerToken)
	producer.HTTP = producerServer.Client()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/runtime-logs/" + hex.EncodeToString(nonce[:])
	type step struct {
		name   string
		result chan map[string]any
	}
	steps := make(chan step)
	complete := make(chan struct{})
	var finish sync.Once
	ownerOnly := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := s.app.CurrentPrincipal(r.Context())
		if e != nil || principal.ID != owner.ID || r.Method != "POST" || r.Header.Get("Origin") != origin {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == prefix+"/complete" {
			w.WriteHeader(204)
			finish.Do(func() { close(complete) })
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, prefix+"/step/")
		if !ok {
			w.WriteHeader(404)
			return
		}
		request := step{name: name, result: make(chan map[string]any, 1)}
		select {
		case steps <- request:
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		}
		select {
		case result := <-request.result:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-ctx.Done():
		case <-r.Context().Done():
		}
	}))
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			ownerOnly.ServeHTTP(w, r)
			return
		}
		browserAPI.ServeHTTP(w, r)
	}))
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	metadata, err := json.Marshal(map[string]string{"origin": origin, "project_id": resource.ProjectID, "environment": env.Name, "resource_id": resource.ID, "initial_operation_id": initial.ID, "initial_container_id": "simulated_initial_container", "step_path": prefix + "/step/", "complete_path": prefix + "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RUNTIME_LOGS_BROWSER_READY %s\n", metadata)
	instance := strings.Repeat("d", 32)
	var batches int
	for {
		select {
		case <-ctx.Done():
			t.Fatal("runtime logs browser fixture timed out")
		case <-complete:
			t.Log("browser fixture released; browser assertions determine journey outcome; simulated TLS producer, no Docker collector or A29 qualification")
			return
		case request := <-steps:
			result := map[string]any{}
			switch request.name {
			case "publish", "bulk", "gap":
				count := 1
				if request.name == "bulk" {
					count = 17
				}
				for i := 0; i < count; i++ {
					claims, e := producer.ClaimRuntimeLogSessions(ctx, domain.HostingRuntimeLogClaimInput{InstanceID: instance, AfterSessionID: "", Limit: 4})
					if e != nil {
						t.Fatal(e)
					}
					var claim *domain.HostingRuntimeLogClaim
					for index := range claims.Targets {
						if claims.Targets[index].Target.ResourceID == resource.ID {
							claim = &claims.Targets[index]
							break
						}
					}
					if claim == nil {
						t.Fatal("no explicitly demanded session for simulated producer")
					}
					rows := 1
					if request.name == "bulk" {
						rows = 64
					}
					events := make([]domain.HostingRuntimeLogEvent, rows)
					stamp := time.Now().UTC()
					for index := range events {
						events[index] = domain.HostingRuntimeLogEvent{Component: "application", Stream: "stdout", Message: fmt.Sprintf("simulated output %d/%d <b>literal</b>\x1b[31m", batches, index), SourceTimestamp: &stamp, MessageTruncated: index == 63}
					}
					batches++
					batch := domain.HostingRuntimeLogBatch{SessionID: claim.SessionID, InstanceID: instance, CollectionEpoch: claim.CollectionEpoch, BatchID: fmt.Sprintf("%032x", batches), ExpectedSequence: claim.NextSequence - 1, BatchSequence: claim.NextBatchSequence, Events: events, State: "following"}
					if request.name == "gap" {
						bad := batch
						bad.ExpectedSequence++
						if _, e = producer.AppendRuntimeLogs(ctx, bad); !runnerclient.HTTPStatus(e, 409) {
							t.Fatal("simulated source gap rejection missing")
						}
					}
					ack, e := producer.AppendRuntimeLogs(ctx, batch)
					if e != nil {
						t.Fatal(e)
					}
					result["session_id"] = claim.SessionID
					result["last_sequence"] = ack.LastSequence
				}
			case "replace":
				replacement := deploy("browser_logs_replacement", "simulated_replacement_container")
				result["operation_id"] = replacement.ID
			case "revoke":
				if _, err = database.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=(SELECT id FROM users WHERE email='viewer@example.local')`, resource.ProjectID); err != nil {
					t.Fatal(err)
				}
			case "restore":
				if _, err = s.app.UpsertProjectMember(admin, app.ProjectMemberInput{ProjectID: resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("unknown fixed browser fixture stage")
			}
			request.result <- result
		}
	}
}
