package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostingRuntimeLogRegisteredHTTPStrictAuthAndCookieRevocation(t *testing.T) {
	s, mem := newTestServer()
	token := createTestSession(t, s)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "log-http"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "log-http", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	runnerToken := "runtime_log_runner"
	_, err = mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_logs_http", Name: "logs", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(runnerToken))), Status: domain.RunnerActive, Capabilities: []string{domain.HostingRuntimeLogsCapability}, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	open := domain.HostingRuntimeLogOpen{RequestID: strings.Repeat("a", 32), RequestExpiresAt: time.Now().Add(20 * time.Second)}
	raw, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	openPath := "/api/v1/resources/" + resource.ID + "/runtime-log-sessions"
	if w := call("POST", openPath, token, string(raw), 503); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache policy missing")
	}
	for _, body := range []string{`{"request_id":"a","request_id":"b"}`, strings.Replace(string(raw), `"request_id":`, `"Request_ID":`, 1), strings.TrimSuffix(string(raw), "}") + `,"container_id":"foreign"}`, `null`, string(raw) + ` {}`} {
		call("POST", openPath, token, body, 400)
	}
	call("POST", openPath, token, strings.Repeat(" ", 8193)+string(raw), 413)
	claim := `{"instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after_session_id":"","limit":4}`
	events := `{"session_id":"rls_test","instance_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","collection_epoch":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","batch_id":"cccccccccccccccccccccccccccccccc","expected_sequence":0,"batch_sequence":1,"events":[],"state":"following"}`
	for _, endpoint := range []struct{ path, body string }{{"/api/v1/runners/runtime-log-sessions/claim", claim}, {"/api/v1/runners/runtime-log-sessions/events", events}} {
		call("POST", endpoint.path, token, endpoint.body, 401)
		call("POST", endpoint.path, "", endpoint.body, 401)
		call("POST", endpoint.path, runnerToken, endpoint.body, 503)
		call("POST", endpoint.path, runnerToken, `{"unknown":true}`, 400)
	}
	for _, value := range []string{"missing", "null", "0", "-1", "2147483648", "1.5", `"1"`} {
		body := strings.Replace(events, `"batch_sequence":1`, `"batch_sequence":`+value, 1)
		if value == "missing" {
			body = strings.Replace(events, `"batch_sequence":1,`, "", 1)
		}
		call("POST", "/api/v1/runners/runtime-log-sessions/events", runnerToken, body, 400)
	}
	call("POST", "/api/v1/runtime-log-sessions/rls_test/renew", token, `{}`, 503)
	call("DELETE", "/api/v1/runtime-log-sessions/rls_test", token, "", 503)
	call("GET", "/api/v1/runtime-log-sessions/rls_test/logs", token, "", 503)
	call("GET", "/api/v1/runtime-log-sessions/rls_test/logs?limit=201", token, "", 400)
	call("GET", "/api/v1/runtime-log-sessions/rls_test/logs?container_id=foreign", token, "", 400)
	cookie := createBrowserSession(t, s)
	request := func(method, path, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("X-NeroCD-CSRF", "1")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	request("POST", openPath, string(raw), 503)
	request("DELETE", "/api/v1/browser-sessions", "", 204)
	request("POST", openPath, string(raw), 401)
	w := httptest.NewRecorder()
	writeRuntimeLogError(w, store.ErrRuntimeLogsExpired)
	if w.Code != http.StatusGone {
		t.Fatal(w.Code)
	}
}

func TestHostingRuntimeLogPostgresRegisteredRoundTrip(t *testing.T) {
	s, pg, database := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime-log-api"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "runtime-log-api", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_runtime_log_api", RunnerName: "logs", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingRuntimeLogsCapability}})
	if err != nil {
		t.Fatal(err)
	}
	runnerToken := "ncr_runtime_log_api_fixture"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(runnerToken)))
	used, err := s.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.app.CreateServer(admin, app.ServerInput{Name: "runtime-log-api", RunnerID: used.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.GrantDestinationProject(admin, detail.Destination.ID, app.DestinationProjectInput{ProjectID: resource.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: detail.Destination.ID}); err != nil {
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := stop(stopCtx); e != nil {
			t.Error(e)
		}
	})
	op, err := s.app.RequestHostingDeployment(admin, resource.ID, domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "runtime-log-api", ContainerPort: 80, HostPort: 18090, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := pg.ClaimRun(ctx, used.Runner.ID, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: lease.Run.ID, LeaseID: lease.Lease.ID, Attempt: lease.Lease.Attempt, Fence: lease.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, used.Runner.ID, a); err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "runtime_log_api_daemon", OS: "linux", Architecture: "amd64"}
	release.Hash = domain.HostingDocumentHash(release)
	if _, err = pg.HostingPrepared(ctx, used.Runner.ID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "log_api_prepared", Release: release}); err != nil {
		t.Fatal(err)
	}
	admitted, err := pg.HostingApplyBegin(ctx, used.Runner.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, used.Runner.ID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "log_api_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "log_api_container", ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: admitted.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, s)
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s status%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive response cached")
		}
		return w
	}
	input := domain.HostingRuntimeLogOpen{RequestID: strings.Repeat("c", 32), RequestExpiresAt: time.Now().UTC().Add(20 * time.Second)}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := call("POST", "/api/v1/resources/"+resource.ID+"/runtime-log-sessions", string(raw), 200)
	var session domain.HostingRuntimeLogSession
	if err = json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	client := runnerclient.New(httpServer.URL, runnerToken)
	claims, err := client.ClaimRuntimeLogSessions(ctx, domain.HostingRuntimeLogClaimInput{InstanceID: strings.Repeat("d", 32), AfterSessionID: "", Limit: 4})
	if err != nil || len(claims.Targets) != 1 {
		t.Fatal(claims, err)
	}
	claim := claims.Targets[0]
	if claim.SessionID != session.ID || claim.Target.ContainerID != "log_api_container" {
		t.Fatal(claim)
	}
	batch := domain.HostingRuntimeLogBatch{BatchSequence: 1, SessionID: session.ID, InstanceID: claim.InstanceID, CollectionEpoch: claim.CollectionEpoch, BatchID: strings.Repeat("e", 32), ExpectedSequence: 0, State: "following", Events: []domain.HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "sensitive " + runnerToken}}}
	if _, err = client.AppendRuntimeLogs(ctx, batch); err != nil {
		t.Fatal(err)
	}
	pageResponse := call("GET", "/api/v1/runtime-log-sessions/"+session.ID+"/logs?limit=1", "", 200)
	var page domain.HostingRuntimeLogPage
	if err = json.Unmarshal(pageResponse.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || !page.Sensitive || strings.Contains(page.Items[0].Message, runnerToken) {
		t.Fatal(page, err)
	}
	call("POST", "/api/v1/runtime-log-sessions/"+session.ID+"/renew", `{}`, 200)
	call("DELETE", "/api/v1/runtime-log-sessions/"+session.ID, "", 200)
	if _, err = client.AppendRuntimeLogs(ctx, batch); err == nil {
		t.Fatal("closed publication accepted")
	}
}
