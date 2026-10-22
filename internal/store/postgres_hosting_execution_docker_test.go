package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/store"
	"nerocd/web"
)

func TestPostgresHostingDockerAgentAndRiver(t *testing.T) {
	for _, interruption := range []string{"lease_loss", "cancel"} {
		t.Run(interruption, func(t *testing.T) { testHostingDockerAgentAndRiver(t, interruption) })
	}
}

func testHostingDockerAgentAndRiver(t *testing.T, interruption string) {
	tag, versionA, versionB := os.Getenv("NEROCD_IMAGE_TEST_TAG"), os.Getenv("NEROCD_IMAGE_TEST_A"), os.Getenv("NEROCD_IMAGE_TEST_B")
	if tag == "" || versionA == "" || versionB == "" {
		t.Skip("run acceptance/image-hosting/gate.sh with NEROCD_TEST_DATABASE_URL")
	}
	var stopConsumer func(context.Context) error
	pg, database, resource, target, _ := hostingExecutionFixture(t, &stopConsumer)
	ctx := t.Context()
	fixtureDocker := func(args ...string) {
		t.Helper()
		commandCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		if output, err := exec.CommandContext(commandCtx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("owned fixture command: %v %s", err, output)
		}
	}
	fixtureDocker("tag", versionA, tag)
	fixtureDocker("push", tag)
	resource.Source.Image.Reference = tag
	resource.UpdatedAt = time.Now().UTC()
	resource, err := pg.UpdateResource(ctx, resource, resource.Generation)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(service, slog.Default(), web.Static())
	session, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	var losePrepared atomic.Bool
	losePrepared.Store(true)
	var preparedCalls atomic.Int32
	var loseLeaseOnVerifying atomic.Bool
	var heartbeatCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/heartbeat" {
			heartbeatCalls.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/status" && loseLeaseOnVerifying.Load() {
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var status domain.HostingStatusRequest
			if err := json.Unmarshal(body, &status); err != nil {
				t.Error(err)
				return
			}
			if status.Phase == "verifying" && loseLeaseOnVerifying.CompareAndSwap(true, false) {
				// Docker has started the owned container. Lose lease authority
				// before verifying it, while retaining the independent hold.
				if interruption == "cancel" {
					admin := auth.WithPrincipal(r.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
					if _, err := service.CancelHostingOperation(admin, status.OperationID); err != nil {
						t.Error(err)
						return
					}
				} else {
					if _, err := database.Exec(r.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, status.LeaseID); err != nil {
						t.Error(err)
						return
					}
					if err := pg.ExpireLeases(r.Context(), time.Now().UTC()); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}
		if r.URL.Path == "/api/v1/runners/hosting/prepared" {
			preparedCalls.Add(1)
			recorded := httptest.NewRecorder()
			server.ServeHTTP(recorded, r)
			if recorded.Code == http.StatusOK && losePrepared.CompareAndSwap(true, false) {
				fixtureDocker("tag", versionB, tag)
				fixtureDocker("push", tag)
				// The receipt committed, but the agent cannot know that until replay.
				connection, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr != nil {
					t.Error(hijackErr)
					return
				}
				_ = connection.Close()
				return
			}
			for key, values := range recorded.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		server.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var ownedContainers []string
	t.Cleanup(func() {
		for _, id := range ownedContainers {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
			output, inspectErr := exec.CommandContext(cleanupCtx, "docker", "container", "inspect", "--format", `{{index .Config.Labels "io.nerocd.binding"}}`, id).Output()
			if inspectErr == nil && strings.TrimSpace(string(output)) == "binding_image_host" {
				if output, removeErr := exec.CommandContext(cleanupCtx, "docker", "container", "rm", "--force", id).CombinedOutput(); removeErr != nil {
					t.Errorf("owned fixture cleanup: %v %s", removeErr, output)
				}
			}
			stop()
		}
	})
	requestDeployment := func(key string) domain.HostingOperation {
		t.Helper()
		body, _ := json.Marshal(domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, IdempotencyKey: key, ContainerPort: 8080, HostPort: port, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}})
		request, requestErr := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/api/v1/resources/"+resource.ID+"/deployments", bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+session.Token)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer func() { _ = response.Body.Close() }()
		var op domain.HostingOperation
		if response.StatusCode != 202 {
			output, _ := io.ReadAll(response.Body)
			t.Fatalf("request deployment: %d %s", response.StatusCode, output)
		}
		if err := json.NewDecoder(response.Body).Decode(&op); err != nil {
			t.Fatal(err)
		}
		return awaitHostingDispatch(t, pg, op.ID)
	}
	workDir := t.TempDir()
	runAgent := func() {
		t.Helper()
		if err := runnerruntime.Run(runnerruntime.Config{Server: httpServer.URL, Token: "image-hosting-fixture-token", RunnerID: target.RunnerID, WorkDir: workDir, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	var lastHealthy domain.HostingOperation
	for _, version := range []string{"A", "B"} {
		op := requestDeployment("actual-agent-" + version)
		runAgent()
		completed, err := pg.GetHostingOperation(ctx, op.ID)
		if err != nil || completed.State != "succeeded" || completed.MutationHold || completed.Result == nil || completed.CompleteReceipt == nil {
			t.Fatalf("actual agent operation: %+v %v", completed, err)
		}
		ownedContainers = append(ownedContainers, completed.Result.ContainerID)
		lastHealthy = completed
		response, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != version {
			t.Fatalf("ordinary HTTP version %s got %q err=%v", version, body, err)
		}
		t.Logf("River/API/agent operation=%s state=%s release=%s ordinary HTTP=%s", completed.ID, completed.State, completed.Release.CanonicalImage, strings.TrimSpace(string(body)))
	}
	preCanceled := requestDeployment("actual-agent-cancel-before-admission")
	cancelRequest, err := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/api/v1/hosting-operations/"+preCanceled.ID+"/cancel", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest.Header.Set("Authorization", "Bearer "+session.Token)
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResponse, err := http.DefaultClient.Do(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	var canceled domain.HostingOperation
	err = json.NewDecoder(cancelResponse.Body).Decode(&canceled)
	_ = cancelResponse.Body.Close()
	if err != nil || cancelResponse.StatusCode != 200 || canceled.State != "canceled" || canceled.MutationHold {
		t.Fatalf("pre-admission cancellation: %+v %v", canceled, err)
	}
	runAgent()
	stillLive, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	stillBody, err := io.ReadAll(io.LimitReader(stillLive.Body, 1024))
	_ = stillLive.Body.Close()
	if err != nil || stillLive.StatusCode != 200 || strings.TrimSpace(string(stillBody)) != "B" {
		t.Fatalf("pre-admission cancel changed Docker workload: %q %v", stillBody, err)
	}
	t.Log("public pre-admission cancellation preserved ordinary HTTP B without acquiring mutation hold")
	if preparedCalls.Load() < 3 {
		t.Fatal("lost prepared response was not replayed")
	}
	// Stop both control-plane listeners and the consumer. Workload HTTP is
	// independent of that process, then a fresh pool/server reads the same result.
	if err := stopConsumer(ctx); err != nil {
		t.Fatal(err)
	}
	httpServer.Close()
	response, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != "B" {
		t.Fatalf("control-plane stop interrupted workload: %q %v", body, err)
	}
	reopened, err := store.OpenPostgres(ctx, database.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	reopenedStop, err := reopened.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reopenedStop(stopCtx); err != nil {
			t.Error(err)
		}
	}()
	reopenedService, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: reopened, Sessions: reopened, APITokens: reopened, ServiceAccounts: reopened, OIDC: reopened, Hosting: reopened, Destinations: reopened, ManagedSecrets: reopened, Projects: reopened, Members: reopened, Templates: reopened, Sources: reopened, Runs: reopened, Runners: reopened, Approvals: reopened, Audit: reopened, Deployments: reopened, Observability: reopened, ObservationWriter: reopened, ObservationReader: reopened, Retention: reopened})
	if err != nil {
		t.Fatal(err)
	}
	server = api.NewServer(reopenedService, slog.Default(), web.Static())
	httpServer = httptest.NewServer(handler)
	defer httpServer.Close()
	request, err := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/hosting-operations/"+lastHealthy.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var persisted domain.HostingOperation
	err = json.NewDecoder(response.Body).Decode(&persisted)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || persisted.State != "succeeded" || persisted.Result == nil || persisted.Result.ContainerID != lastHealthy.Result.ContainerID {
		t.Fatalf("server restart recreated or lost workload evidence: %+v %v", persisted, err)
	}
	t.Log("control-plane API/consumer stopped: ordinary HTTP B remained live; fresh DB pool/server returned the same healthy container and result")
	// Reopening the same workdir must replay/ack before an idle heartbeat/claim.
	runAgent()
	uncertain := requestDeployment("actual-agent-lease-loss")
	loseLeaseOnVerifying.Store(true)
	config := runnerruntime.Config{Server: httpServer.URL, Token: "image-hosting-fixture-token", RunnerID: target.RunnerID, WorkDir: workDir, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond}
	if err := runnerruntime.Run(config); err == nil {
		t.Fatal("lease loss after Docker start reported success")
	}
	uncertain, err = pg.GetHostingOperation(ctx, uncertain.ID)
	if err != nil || !uncertain.MutationHold || uncertain.State != "reconciliation_required" || uncertain.Result == nil || !uncertain.Result.Uncertain {
		t.Fatalf("lease loss discarded external uncertainty: %+v %v", uncertain, err)
	}
	if uncertain.Result.ContainerID != "" {
		ownedContainers = append(ownedContainers, uncertain.Result.ContainerID)
	}
	beforeRestart := heartbeatCalls.Load()
	if err := runnerruntime.Run(config); err == nil {
		t.Fatal("restart erased unacknowledged external effects")
	}
	if heartbeatCalls.Load() != beforeRestart {
		t.Fatal("restart heartbeat preceded journal reconciliation")
	}
	t.Logf("%s after Docker start: operation=%s independent hold retained; same-workdir restart blocked before heartbeat", interruption, uncertain.ID)
}
