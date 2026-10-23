//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/store"
	"nerocd/web"
)

type switchBrowserFixture struct {
	server       *Server
	pg           *store.PostgresStore
	database     *pgxpool.Pool
	environment  domain.ProjectEnvironment
	resource     domain.Resource
	proxy        domain.HostingProxy
	a, b         domain.HostingOperation
	routes       []domain.HostingRoute
	runAgent     func() error
	agentCommand func() *exec.Cmd
	probe        func(string, string)
	state, work  string
}

// Browser mode shares only initial runtime setup with the controller gate. Every
// UI switch is dispatched by the registered API and real River consumer; this
// helper never calls PrepareProxy for browser-triggered work.
func runHostingRouteSwitchBrowser(t *testing.T, f switchBrowserFixture) {
	t.Helper()
	t.Logf("compiled opt-in agent arguments=%q", f.agentCommand().Args)
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	origin := os.Getenv("NEROCD_ROUTE_SWITCH_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil {
		t.Fatal("invalid switch browser origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1024 || port > 65535 || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || origin != "http://"+parsed.Host {
		t.Fatal("switch browser requires exact loopback HTTP origin")
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	owner, err := f.server.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "switch-owner@example.local", Name: "Switch Fixture Maintainer", Password: "switch-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: f.environment.ProjectID, Email: owner.Email, Role: "maintainer"}, {ProjectID: f.environment.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		if _, err = f.server.app.UpsertProjectMember(admin, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	hidden, err := f.server.app.CreateProject(admin, app.ProjectInput{Name: "hidden switch fixture"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := f.server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: hidden.ID, Name: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := f.server.app.CreateResource(admin, app.ResourceInput{ProjectID: hidden.ID, ProjectEnvironmentID: environment.ID, Name: "hidden switch", Kind: "application", Source: f.resource.Source})
	if err != nil {
		t.Fatal(err)
	}
	browserAPI := NewServerWithConfig(f.server.app, slog.Default(), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/route-switch/" + hex.EncodeToString(nonce)
	type stepRequest struct {
		name string
		done chan map[string]string
	}
	steps := make(chan stepRequest)
	done := make(chan struct{})
	var finish sync.Once
	controls := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := f.server.app.CurrentPrincipal(r.Context())
		if e != nil || principal.ID != owner.ID || r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/complete" {
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, prefix+"/step/")
		if !ok || !map[string]bool{"apply-b": true, "apply-a": true, "stop-b": true, "failed-b": true, "hold-b": true}[name] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		step := stepRequest{name: name, done: make(chan map[string]string, 1)}
		select {
		case steps <- step:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		select {
		case result := <-step.done:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			controls.ServeHTTP(w, r)
			return
		}
		browserAPI.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18081")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	metadata, err := json.Marshal(map[string]string{"origin": origin, "project_id": f.environment.ProjectID, "environment": f.environment.Name, "resource_id": f.resource.ID, "hidden_resource_id": resource.ID, "release_a": f.a.ID, "release_b": f.b.ID, "route_id": f.routes[0].ID, "unchanged_route_id": f.routes[1].ID, "step_path": prefix + "/step/", "complete_path": prefix + "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("ROUTE_SWITCH_BROWSER_READY %s\n", metadata)
	// These fixed stages can affect only the exact fixture resources above.
	expected := []string{"apply-b", "apply-a", "stop-b", "failed-b", "hold-b"}
	position := 0
	for {
		select {
		case <-ctx.Done():
			t.Fatal("switch browser fixture timed out")
		case <-done:
			if position != len(expected) {
				t.Fatal("browser ended before required switch stages")
			}
			return
		case step := <-steps:
			if position >= len(expected) || step.name != expected[position] {
				t.Fatal("unexpected switch fixture stage")
			}
			result := map[string]string{}
			if step.name == "stop-b" {
				switchFixtureDocker(t, "stop", f.b.Result.ContainerID)
			} else {
				operation := f.awaitDispatch(t, ctx)
				if step.name == "hold-b" {
					f.interruptPublished(t, operation)
				} else {
					runErr := f.runAgent()
					operation, err = f.pg.GetHostingProxyOperation(ctx, operation.ID)
					want := "applied"
					if step.name == "failed-b" {
						want = "rolled_back"
					}
					if err != nil || operation.Result == nil || operation.Result.Outcome != want || operation.MutationHold {
						t.Fatalf("browser agent stage=%s op=%+v err=%v agent=%v", step.name, operation, err, runErr)
					}
					if step.name == "apply-b" {
						f.probe("/", "B:/")
					} else {
						f.probe("/", "A:/")
					}
					f.probe("/unchanged", "A:/unchanged")
				}
				if step.name == "apply-b" {
					var count int
					if err = f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_operations WHERE proxy_id=$1 AND snapshot ? 'verified_switch'`, f.proxy.ID).Scan(&count); err != nil || count != 1 {
						t.Fatalf("lost response created %d switches: %v", count, err)
					}
				}
				result["operation_id"] = operation.ID
				result["run_id"] = operation.RunID
			}
			position++
			step.done <- result
		}
	}
}

func (f switchBrowserFixture) awaitDispatch(t *testing.T, ctx context.Context) domain.HostingProxyOperation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		proxy, err := f.pg.GetHostingProxy(ctx, f.proxy.ID)
		if err != nil {
			t.Fatal(err)
		}
		if proxy.ActiveOperationID != "" {
			op, e := f.pg.GetHostingProxyOperation(ctx, proxy.ActiveOperationID)
			if e != nil {
				t.Fatal(e)
			}
			if op.RunID != "" && op.State == "dispatched" {
				t.Logf("browser real River dispatch operation=%s run=%s generation=%d", op.ID, op.RunID, op.Generation)
				return op
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("real River dispatch not persisted")
	return domain.HostingProxyOperation{}
}

func (f switchBrowserFixture) interruptPublished(t *testing.T, op domain.HostingProxyOperation) {
	t.Helper()
	command := f.agentCommand()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	joined := false
	defer func() {
		if !joined {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned browser agent did not join")
			}
		}
	}()
	dynamicPath := filepath.Join(f.state, f.proxy.ID, "dynamic", "routes.yml")
	receiptPath := filepath.Join(f.state, f.proxy.ID, "receipt.json")
	deadline := time.Now().Add(15 * time.Second)
	for {
		dynamic, _ := os.ReadFile(dynamicPath)
		raw, _ := os.ReadFile(receiptPath)
		var receipt struct {
			Phase      string
			Generation int64
		}
		_ = json.Unmarshal(raw, &receipt)
		if receipt.Phase == "intent" && receipt.Generation == op.Generation && bytes.Contains(dynamic, []byte("20102")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("browser candidate publication handshake timed out")
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("browser agent ended before publication: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		joined = true
	case <-time.After(5 * time.Second):
		t.Fatal("browser SIGTERM did not join")
	}
	raw, err := os.ReadFile(filepath.Join(f.work, "journal", "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Version int                   `json:"version"`
		Proxy   []runner.JournalProxy `json:"proxy"`
	}
	if err = json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	uncertain := false
	for _, entry := range journal.Proxy {
		if entry.OperationID == op.ID && entry.Result != nil && entry.Result.Result.Outcome == "uncertain" {
			uncertain = true
		}
	}
	if journal.Version != 9 || !uncertain {
		t.Fatal("browser cancellation lacked durable uncertain result")
	}
	before, err := os.ReadFile(dynamicPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.runAgent()
	after, err := os.ReadFile(dynamicPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("browser replay mutated unresolved generation")
	}
	recorded, err := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
	if err != nil || !recorded.MutationHold || recorded.Result != nil && recorded.Result.Outcome == "applied" {
		t.Fatalf("browser uncertain hold lost: %+v %v", recorded, err)
	}
	t.Logf("browser genuine publication interruption retained hold operation=%s state=%s", op.ID, recorded.State)
}
