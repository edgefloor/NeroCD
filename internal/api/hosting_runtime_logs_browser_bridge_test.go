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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/web"
)

// This bridge exists only in the opted-in DinD test binary. Its fixed actions
// operate solely on the current fixture and return reference metadata.
func runHostingRuntimeLogsActualBrowser(ctx context.Context, t *testing.T, server *Server, database *pgxpool.Pool, environment domain.ProjectEnvironment, resource domain.Resource, initial domain.HostingOperation, action func(string)) bool {
	t.Helper()
	origin := os.Getenv("NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil {
		t.Fatal("invalid actual logs browser origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || origin != "http://"+parsed.Host {
		t.Fatal("browser fixture requires an exact loopback HTTP origin")
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	owner, err := server.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "actual-logs-owner@example.local", Name: "Actual Logs Fixture Owner", Password: "actual-logs-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: environment.ProjectID, Email: owner.Email, Role: "owner"}, {ProjectID: environment.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		if _, err = server.app.UpsertProjectMember(admin, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	browserAPI := NewServerWithConfig(server.app, slog.Default(), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	browserCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/actual-logs/" + hex.EncodeToString(nonce)
	type stepRequest struct {
		name string
		done chan map[string]string
	}
	steps := make(chan stepRequest)
	done := make(chan struct{})
	var finish sync.Once
	ownerOnly := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := server.app.CurrentPrincipal(r.Context())
		if err != nil || principal.ID != owner.ID || r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/complete" {
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, prefix+"/step/")
		if !ok || !slices.Contains([]string{"emit", "burst", "disconnect", "resume", "revoke"}, name) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		step := stepRequest{name: name, done: make(chan map[string]string, 1)}
		select {
		case steps <- step:
		case <-r.Context().Done():
			return
		case <-browserCtx.Done():
			return
		}
		select {
		case result := <-step.done:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-r.Context().Done():
		case <-browserCtx.Done():
		}
	}))
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			ownerOnly.ServeHTTP(w, r)
			return
		}
		browserAPI.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	defer func() { cancel(); exposed.Close() }()
	metadata, err := json.Marshal(map[string]string{"origin": origin, "project_id": environment.ProjectID, "environment": environment.Name, "resource_id": resource.ID, "initial_container_id": initial.Result.ContainerID, "initial_operation_id": initial.ID, "settled_at": initial.UpdatedAt.UTC().Format(time.RFC3339Nano), "complete_path": prefix + "/complete", "step_path": prefix + "/step/"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("ACTUAL_LOGS_BROWSER_READY %s\n", metadata)
	expected := []string{"emit", "burst", "disconnect", "resume", "revoke"}
	position := 0
	for {
		select {
		case <-browserCtx.Done():
			t.Fatal("actual logs browser fixture timed out")
		case <-done:
			if position != len(expected) {
				t.Log("browser ended before all stages; browser exit remains authoritative failure")
				return false
			}
			return true
		case step := <-steps:
			if position >= len(expected) || step.name != expected[position] {
				t.Fatal("actual logs browser requested an unexpected lifecycle stage")
			}
			if step.name == "revoke" {
				if _, err := database.Exec(browserCtx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=(SELECT id FROM users WHERE email='viewer@example.local')`, environment.ProjectID); err != nil {
					t.Fatal(err)
				}
			} else {
				action(step.name)
			}
			position++
			step.done <- map[string]string{"stage": step.name}
		}
	}
}
