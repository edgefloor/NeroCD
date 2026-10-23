//go:build linux && nerocd_proxy_fixture

package api

import (
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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type recoveryBrowserFixture struct {
	server   *Server
	pg       *store.PostgresStore
	database *pgxpool.Pool
	proxy    domain.HostingProxy
	original domain.HostingProxyOperation
	routes   []domain.HostingRoute
	initial  domain.HostingOperation
	loaded   int64
}

// This mode begins only after genuine compiled-agent rollback, revoked route
// metadata, accepted completion and durable lost-response journal evidence. The
// browser creates and applies the plan through registered authenticated APIs.
func runHostingProxyRecoveryBrowser(t *testing.T, f recoveryBrowserFixture) domain.HostingProxyRecoveryResolution {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	origin := os.Getenv("NEROCD_PROXY_RECOVERY_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil {
		t.Fatal("invalid recovery browser origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1024 || port > 65535 || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || origin != "http://"+parsed.Host {
		t.Fatal("recovery browser requires exact loopback HTTP origin")
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	owner, err := f.server.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "recovery-owner@example.local", Name: "Recovery Fixture Owner", Password: "recovery-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.app.UpsertProjectMember(admin, app.ProjectMemberInput{ProjectID: f.proxy.ControlProjectID, Email: owner.Email, Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.database.Exec(ctx, `UPDATE users SET global_role='runner_admin' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual system-administrator override without owner membership.
	if _, err = f.server.app.UpsertProjectMember(admin, app.ProjectMemberInput{ProjectID: f.proxy.ControlProjectID, Email: "admin@example.local", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	var beforeOperations int
	if err = f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_operations WHERE proxy_id=$1`, f.proxy.ID).Scan(&beforeOperations); err != nil {
		t.Fatal(err)
	}
	browserAPI := NewServerWithConfig(f.server.app, slog.Default(), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/proxy-recovery/" + hex.EncodeToString(nonce)
	type verification struct {
		done chan domain.HostingProxyRecoveryResolution
	}
	checks := make(chan verification)
	done := make(chan struct{})
	var finish sync.Once
	controls := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := f.server.app.CurrentPrincipal(r.Context())
		if e != nil || principal.ID != "usr_bootstrap" || r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/complete" {
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		if r.URL.Path != prefix+"/verify" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		check := verification{done: make(chan domain.HostingProxyRecoveryResolution, 1)}
		select {
		case checks <- check:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		select {
		case resolution := <-check.done:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resolution)
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
	listener, err := net.Listen("tcp", "0.0.0.0:18082")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	metadata, err := json.Marshal(map[string]string{"origin": origin, "server_id": f.proxy.ServerID, "proxy_id": f.proxy.ID, "project_id": f.proxy.ControlProjectID, "operation_id": f.original.ID, "receipt_id": f.original.CompleteReceipt.ID, "receipt_hash": f.original.CompleteReceipt.BodyHash, "verify_path": prefix + "/verify", "complete_path": prefix + "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PROXY_RECOVERY_BROWSER_READY %s\n", metadata)
	var resolution domain.HostingProxyRecoveryResolution
	for {
		select {
		case <-ctx.Done():
			t.Fatal("recovery browser timed out")
		case <-done:
			if resolution.ID == "" {
				t.Fatal("browser ended before required recovery verification")
			}
			return resolution
		case check := <-checks:
			status, e := f.server.app.HostingProxyRecovery(admin, f.original.ID)
			if e != nil || status.Resolution == nil || status.Eligible || status.Reason != "resolved" {
				t.Fatalf("browser resolution not committed %+v %v", status, e)
			}
			resolution = *status.Resolution
			original, e := f.pg.GetHostingProxyOperation(ctx, f.original.ID)
			if e != nil || original.MetadataConflict || original.MutationHold || !reflect.DeepEqual(original.Result, f.original.Result) || !reflect.DeepEqual(original.CompleteReceipt, f.original.CompleteReceipt) {
				t.Fatalf("browser altered original evidence %v", e)
			}
			proxy, e := f.pg.GetHostingProxy(ctx, f.proxy.ID)
			if e != nil || proxy.LoadedGeneration != f.loaded {
				t.Fatalf("metadata recovery changed loaded generation: %+v %v", proxy, e)
			}
			var operations, resolutions int
			if e = f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_operations WHERE proxy_id=$1`, f.proxy.ID).Scan(&operations); e != nil {
				t.Fatal(e)
			}
			if e = f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1`, f.original.ID).Scan(&resolutions); e != nil {
				t.Fatal(e)
			}
			if operations != beforeOperations+1 || resolutions != 1 {
				t.Fatalf("browser duplicated convergence/resolution operations=%d before=%d resolutions=%d", operations, beforeOperations, resolutions)
			}
			queued, e := f.pg.GetHostingProxyOperation(ctx, resolution.ConvergenceOperationID)
			if e != nil || len(queued.Snapshot.Routes) != 0 {
				t.Fatalf("browser queued revoked routes: %v", e)
			}
			for _, route := range f.routes {
				row, e := f.pg.GetHostingRoute(ctx, route.ID)
				if e != nil || row.Route.Enabled || row.Route.SelectedReleaseOperationID != f.initial.ID {
					t.Fatalf("browser changed route flags or wrong repair: %v", e)
				}
			}
			t.Logf("browser genuine metadata recovery original=%s receipt=%s resolution=%s convergence=%s loaded_unchanged=%d one_resolution=%d one_added_operation=%d", f.original.ID, f.original.CompleteReceipt.ID, resolution.ID, resolution.ConvergenceOperationID, proxy.LoadedGeneration, resolutions, operations-beforeOperations)
			check.done <- resolution
		}
	}
}
