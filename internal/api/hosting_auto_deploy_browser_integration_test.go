package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/web"
)

// This control bridge runs only in its owned disposable Linux daemon. Commands
// are fixed and authenticated; all assertions run on the test goroutine.
func TestHostingAutomaticDeploymentBrowser(t *testing.T) {
	if os.Getenv("NEROCD_AUTO_DEPLOY_BROWSER") != "1" {
		t.Skip("requires actual browser fixture")
	}
	origin := os.Getenv("NEROCD_AUTO_DEPLOY_BROWSER_ORIGIN")
	request, err := http.NewRequest(http.MethodGet, origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(request.URL.Port())
	if err != nil || port < 1 || port > 65535 || origin != "http://127.0.0.1:"+strconv.Itoa(port) {
		t.Fatal("exact loopback browser origin required")
	}
	fixture := newAutomaticRuntimeFixture(t)
	f, ctx := fixture.Control, fixture.ctx
	preparePushBrowserIdentity(t, ctx, f)
	var initial domain.GitHubPushPolicy
	f.request(http.MethodGet, "/api/v1/resources/"+f.resource.ID+"/github-push-policy", f.owner, nil, http.StatusOK, &initial)
	if initial.OwnerAutomaticDeploymentOptions == nil || !initial.OwnerAutomaticDeploymentOptions.Available {
		reason := "missing"
		if initial.OwnerAutomaticDeploymentOptions != nil {
			reason = initial.OwnerAutomaticDeploymentOptions.Reason
		}
		t.Fatal("ordinary-owner identity preparation must retain eligible manual A/HTTPS baseline", reason)
	}
	f.api = NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: origin, AllowInsecureCookies: true})
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	stepPath := "/api/v1/fixture/automatic-deployment/" + hex.EncodeToString(nonce) + "/"
	type step struct {
		command string
		result  chan int
	}
	commands := make(chan step)
	handler := f.api.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := f.service.CurrentPrincipal(r.Context())
		if e != nil || principal.ID != "usr_bootstrap" || r.Method != http.MethodPost || r.Header.Get("Origin") != origin || r.Header.Get("X-NeroCD-CSRF") != "1" || r.URL.RawQuery != "" {
			http.Error(w, "denied", 403)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 1))
		if e != nil || len(raw) != 0 {
			http.Error(w, "empty body required", 400)
			return
		}
		command := strings.TrimPrefix(r.URL.Path, stepPath)
		switch command {
		case "push-b", "duplicate-b", "delayed-a", "probe-b", "push-c", "probe-failed-c", "complete":
		default:
			http.Error(w, "unknown command", 400)
			return
		}
		item := step{command, make(chan int, 1)}
		select {
		case commands <- item:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		select {
		case status := <-item.result:
			w.WriteHeader(status)
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	listener, err := net.Listen("tcp4", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, stepPath) {
			handler.ServeHTTP(w, r)
			return
		}
		f.api.ServeHTTP(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(c); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	metadata := map[string]string{"base_url": origin, "project_id": f.resource.ProjectID, "environment_name": "push-fixture", "resource_id": f.resource.ID, "expected_commit_a": fixture.Commits[0], "expected_commit_b": fixture.Commits[1], "expected_commit_c": fixture.Commits[2], "step_path": stepPath}
	raw, _ := json.Marshal(metadata)
	fmt.Printf("AUTO_DEPLOY_BROWSER_READY %s\n", raw)
	stage := 0
	var statusB domain.HostingAutoDeployStatus
	for {
		select {
		case <-ctx.Done():
			t.Fatal("actual browser did not complete bounded fixture")
		case item := <-commands:
			expected := map[string]int{"push-b": 0, "duplicate-b": 1, "delayed-a": 2, "probe-b": 3, "push-c": 4, "probe-failed-c": 5, "complete": 6}
			if stage != expected[item.command] {
				item.result <- 409
				continue
			}
			var policy domain.GitHubPushPolicy
			f.request(http.MethodGet, "/api/v1/resources/"+f.resource.ID+"/github-push-policy", f.owner, nil, http.StatusOK, &policy)
			switch item.command {
			case "push-b":
				if !policy.Enabled || policy.AutomaticDeployment == nil || policy.OwnerConfiguration.AutomaticDeployment == nil || policy.DesiredCommit != fixture.Commits[0] {
					t.Fatal("UI did not configure, verify, and enable automatic deployment")
				}
				fixture.SignedPush(310, 1)
			case "duplicate-b":
				f.push(policy, 310, fixture.Commits[1])
			case "delayed-a":
				f.push(policy, 312, fixture.Commits[0])
			case "probe-b":
				build := fixture.AwaitBuild(fixture.Commits[1], "succeeded")
				statusB = fixture.AwaitDemand("succeeded")
				if statusB.BuildID != build.ID || statusB.DesiredCommit != fixture.Commits[1] || statusB.Reason != "https_verified" || statusB.DeploymentOperationID == "" || statusB.SwitchOperationID == "" {
					t.Fatal("B automatic deployment identity mismatch")
				}
				fixture.ProbeHTTPS("push fixture B\n")
			case "push-c":
				fixture.SignedPush(313, 2)
			case "probe-failed-c":
				build := fixture.AwaitBuild(fixture.Commits[2], "failed")
				failed := fixture.AwaitDemand("failed")
				if build.Artifact != nil || failed.DesiredCommit != fixture.Commits[2] || failed.Reason != "build_failed" {
					t.Fatal("C failure did not preserve automatic outcome")
				}
				var count int
				if err := f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_auto_deploy_origins WHERE build_id=$1`, build.ID).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed C admitted automatic operation", err)
				}
				fixture.ProbeHTTPS("push fixture B\n")
			case "complete":
				fixture.ProbeHTTPS("push fixture B\n")
				t.Logf("real owner browser configured automatic mode; signed B/dedup/delayed A reached trusted fixture-CA HTTPS B with deployment=%s switch=%s; source-owned failed C preserves B and unrelated HTTPS", statusB.DeploymentOperationID, statusB.SwitchOperationID)
				item.result <- 204
				return
			}
			stage++
			item.result <- 204
		}
	}
}
