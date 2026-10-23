//go:build linux && nerocd_bootstrap_fixture

package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/serverbootstrap"
	"nerocd/web"
)

// This test runs only in the labelled disposable Linux systemd target. All
// onboarding operations enter registered HTTP handlers; River invokes the real
// configured processor. No bootstrap operation, receipt or enrollment is seeded.
func TestServerBootstrapRuntime(t *testing.T) {
	if os.Getenv("NEROCD_BOOTSTRAP_ONBOARDING") != "owned-systemd-target" {
		t.Skip("requires the owned SSH/systemd qualifier")
	}
	if os.Geteuid() != 0 {
		t.Fatal("owned target root required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	_, pg, database := newHostingBuildAPIFixture(t)
	key, err := os.ReadFile("/fixture/onboarding-client")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile("/fixture/onboarding-agent")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	ringBytes := make([]byte, 32)
	if _, err = rand.Read(ringBytes); err != nil {
		t.Fatal(err)
	}
	ring, err := secretcrypto.New("fixture", map[string][]byte{"fixture": ringBytes})
	if err != nil {
		t.Fatal(err)
	}
	var logs bootstrapRuntimeLogs
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "onboarding-platform@example.local", Name: "Owned platform bootstrap", Password: "onboarding-fixture-platform-bootstrap-password"}); err != nil {
		t.Fatal(err)
	}
	bootstrapStatus, err := service.PublicBootstrapStatus(ctx)
	if err != nil || bootstrapStatus.Status != "complete" {
		t.Fatalf("real platform bootstrap did not complete: status=%s error=%v", bootstrapStatus.Status, err)
	}
	const origin = "https://127.0.0.1:19443"
	qualified := app.ServerBootstrapArtifact{ArtifactID: "fixture-arm64", Path: "/fixture/onboarding-agent", Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:]), Version: "v0.0.0-fixture", Architecture: "arm64", Profile: "prepared_debian12_v1"}
	if err = service.ConfigureServerBootstrap(&app.ServerBootstrapConfig{ControllerOrigin: origin, Artifacts: []app.ServerBootstrapArtifact{qualified}}); err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureServerBootstrapProcessor(service.ProcessServerBootstrap); err != nil {
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
	executionCtx, stopExecution := context.WithCancel(ctx)
	stop, err := pg.StartHostingExecution(executionCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopExecution()
		end, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if e := stop(end); e != nil {
			t.Error(e)
		}
	}()
	server := NewServerWithConfig(service, logger, web.Static(), ServerConfig{PublicOrigin: origin})
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	secret, err := service.CreateManagedSecret(admin, app.ManagedSecretInput{ProjectID: "proj_platform", Name: "onboarding SSH fixture", Value: string(key)})
	if err != nil {
		t.Fatal(err)
	}
	credential := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	certificate, roots := bootstrapRuntimeTLS(t)
	var lostConsume atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-NeroCD-Fixture-Lose") == "committed" && r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/server-bootstraps") {
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, r)
			if rec.Code == http.StatusCreated || rec.Code == http.StatusAccepted {
				connection, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = connection.Close()
				}
				return
			}
			for k, values := range rec.Header() {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		// Execute the real consume transaction, then lose exactly its first
		// successful response. The independently running agent must replay.
		if r.URL.Path == "/api/v1/runner-enrollments/consume" && !lostConsume.Load() {
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, r)
			if rec.Code == 200 && lostConsume.CompareAndSwap(false, true) {
				connection, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = connection.Close()
				}
				return
			}
			for k, values := range rec.Header() {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		server.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:19443")
	if err != nil {
		t.Fatal(err)
	}
	https := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- https.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}))
	}()
	defer func() {
		_ = https.Close()
		if e := <-done; e != http.ErrServerClosed {
			t.Error(e)
		}
	}()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	request := func(method, path string, in any, token string, want int) []byte {
		t.Helper()
		var body []byte
		if in != nil {
			body, err = json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
		}
		r, e := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		out, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d closed response=%s", method, path, response.StatusCode, want, out)
		}
		if strings.Contains(path, "server-bootstrap") && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("bootstrap cache contract: %s %s status=%d Cache-Control=%q", method, path, response.StatusCode, response.Header.Get("Cache-Control"))
		}
		for _, forbidden := range []string{string(key), "/fixture/onboarding-agent", "enrollment_token", "credential_hash"} {
			if bytes.Contains(out, []byte(forbidden)) {
				t.Fatal("private bootstrap material escaped HTTP projection")
			}
		}
		return out
	}
	decode := func(raw []byte) app.ServerBootstrap {
		t.Helper()
		var v app.ServerBootstrap
		if e := json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	loseCommitted := func(path string, input any) {
		t.Helper()
		body, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		r, e := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-NeroCD-Fixture-Lose", "committed")
		response, e := client.Do(r)
		if e == nil {
			out, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			t.Fatalf("committed response was not lost: %s status=%d response=%s", path, response.StatusCode, out)
		}
	}
	get := func(id string) app.ServerBootstrap {
		return decode(request("GET", "/api/v1/server-bootstraps/"+id, nil, credential, 200))
	}
	seq := 0
	uuid := func() string { seq++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq) }
	command := func(v app.ServerBootstrap, action string, extra func(*app.ServerBootstrapCommandInput)) app.ServerBootstrap {
		t.Helper()
		input := app.ServerBootstrapCommandInput{CommandID: uuid(), ExpectedVersion: v.Version}
		if extra != nil {
			extra(&input)
		}
		code := 202
		if action == "review" || action == "cancel" {
			code = 200
		}
		return decode(request("POST", "/api/v1/server-bootstraps/"+v.ID+"/"+action, input, credential, code))
	}
	wait := func(id, description string, predicate func(app.ServerBootstrap) bool) app.ServerBootstrap {
		t.Helper()
		deadline := time.Now().Add(65 * time.Second)
		var v app.ServerBootstrap
		for time.Now().Before(deadline) {
			v = get(id)
			if predicate(v) {
				return v
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture deadline")
			case <-time.After(150 * time.Millisecond):
			}
		}
		t.Fatalf("%s timed out: state=%s progress=%+v", description, v.State, v.Progress)
		return v
	}
	probe := func(v app.ServerBootstrap) app.ServerBootstrap {
		t.Helper()
		previous := ""
		if v.Probe != nil {
			previous = v.Probe.ReceiptID
		}
		v = command(v, "probe", nil)
		return wait(v.ID, "real SSH probe", func(v app.ServerBootstrap) bool {
			return v.Probe != nil && v.Probe.ReceiptID != previous && v.Probe.Prerequisites.Qualifies("arm64")
		})
	}
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, "", 401)
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, viewer, 403)
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, credential, 200)
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, credential, 403)
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='system_admin' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	fingerprint := os.Getenv("NEROCD_BOOTSTRAP_PROBE_FINGERPRINT")
	target := domain.ServerBootstrapTarget{IP: "127.0.0.1", Port: 22, User: "root", HostFingerprint: fingerprint}
	input := app.ServerBootstrapCreateInput{RequestID: uuid(), ProjectID: "proj_platform", Name: "actual SSH onboarding", Target: target, SSHSecretID: secret.ID, SSHSecretVersion: 1, ArtifactID: qualified.ArtifactID}
	unknown := input
	unknown.ArtifactID = "unknown"
	request("POST", "/api/v1/server-bootstraps", unknown, credential, 400)
	cross := input
	cross.ProjectID = "proj_security"
	if _, err = database.Exec(ctx, `DELETE FROM project_members WHERE project_id='proj_security' AND user_id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='runner_admin' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/server-bootstraps", cross, credential, 403)
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='system_admin' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	// Negative host trust remains a real SSH probe, with no replacement adapter.
	wrong := input
	wrong.RequestID = uuid()
	wrong.Target.HostFingerprint = "SHA256:" + strings.Repeat("A", 43)
	wrongView := decode(request("POST", "/api/v1/server-bootstraps", wrong, credential, 201))
	wrongView = command(wrongView, "probe", nil)
	wrongView = wait(wrongView.ID, "wrong host rejection", func(v app.ServerBootstrap) bool {
		for _, p := range v.Progress {
			if p.Action == "probe" && p.Status == "completed" {
				return p.Outcome == "refused"
			}
		}
		return false
	})
	command(wrongView, "cancel", nil)
	for _, grant := range []bool{true, false} {
		if !grant {
			port, close := bootstrapRuntimeTCPResponseLoss(t, "127.0.0.1:22", 1<<62)
			defer close()
			input.Target.Port = uint16(port)
		}
		input.RequestID = uuid()
		input.Name = fmt.Sprintf("actual SSH grant %t", grant)
		loseCommitted("/api/v1/server-bootstraps", input)
		v := decode(request("POST", "/api/v1/server-bootstraps", input, credential, 201))
		exact := decode(request("POST", "/api/v1/server-bootstraps", input, credential, 201))
		if exact.ID != v.ID {
			t.Fatal("create replay changed operation")
		}
		op, e := pg.GetServerBootstrap(ctx, "usr_bootstrap", v.ID)
		if e != nil {
			t.Fatal(e)
		}
		var count int
		if e = database.QueryRow(ctx, `SELECT count(*) FROM server_bootstrap_operations WHERE id=$1`, v.ID).Scan(&count); e != nil || count != 1 {
			t.Fatalf("lost create response duplicated operation: count=%d error=%v", count, e)
		}
		probeInput := app.ServerBootstrapCommandInput{CommandID: uuid(), ExpectedVersion: v.Version}
		loseCommitted("/api/v1/server-bootstraps/"+v.ID+"/probe", probeInput)
		request("POST", "/api/v1/server-bootstraps/"+v.ID+"/probe", probeInput, credential, 202)
		v = wait(v.ID, "exact probe command replay", func(v app.ServerBootstrap) bool { return v.Probe != nil })
		count = 0
		for _, p := range v.Progress {
			if p.CommandID == probeInput.CommandID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("lost command response duplicated command: %d", count)
		}
		manifest := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: origin, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
		defer func() {
			cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
			defer done()
			out, e := serverbootstrap.CleanupOwned(cleanup, serverbootstrap.Target{Address: netip.MustParseAddr("127.0.0.1"), Port: 22, User: "root", HostKeyFingerprint: fingerprint}, key, serverbootstrap.Mutation{OperationID: strings.Repeat("c", 32), Ownership: serverbootstrap.Ownership{Manifest: manifest}})
			if e != nil || out.State != serverbootstrap.OwnedRemoved {
				t.Errorf("owned cleanup failed: state=%s error=%v", out.State, e)
			}
		}()
		v = probe(v)
		request("POST", "/api/v1/server-bootstraps/"+v.ID+"/review", app.ServerBootstrapCommandInput{CommandID: uuid(), ExpectedVersion: v.Version, ProbeReceiptID: "forged"}, credential, 400)
		v = command(v, "review", func(c *app.ServerBootstrapCommandInput) { c.ProbeReceiptID = v.Probe.ReceiptID })
		v = wait(v.ID, "review", func(v app.ServerBootstrap) bool { return v.Review != nil })
		if grant { // Real clock expiry, followed by a fresh real probe and review.
			remaining := time.Until(v.Review.ExpiresAt) + 150*time.Millisecond
			select {
			case <-ctx.Done():
				t.Fatal("deadline waiting for review expiry")
			case <-time.After(remaining):
			}
			request("POST", "/api/v1/server-bootstraps/"+v.ID+"/install", app.ServerBootstrapCommandInput{CommandID: uuid(), ExpectedVersion: v.Version, ManifestSHA256: v.Review.ManifestSHA256, AcknowledgeInstallAndStart: true, GrantProject: &grant}, credential, 409)
			v = probe(get(v.ID))
			v = command(v, "review", func(c *app.ServerBootstrapCommandInput) { c.ProbeReceiptID = v.Probe.ReceiptID })
			v = wait(v.ID, "refreshed review", func(v app.ServerBootstrap) bool { return v.Review != nil && v.Review.ExpiresAt.After(time.Now()) })
		}
		request("POST", "/api/v1/server-bootstraps/"+v.ID+"/install", app.ServerBootstrapCommandInput{CommandID: uuid(), ExpectedVersion: v.Version, ManifestSHA256: v.Review.ManifestSHA256, GrantProject: &grant}, credential, 400)
		v = command(v, "install", func(c *app.ServerBootstrapCommandInput) {
			c.ManifestSHA256 = v.Review.ManifestSHA256
			c.AcknowledgeInstallAndStart = true
			c.GrantProject = &grant
		})
		v = wait(v.ID, "independent agent enrollment and registration", func(v app.ServerBootstrap) bool {
			if grant {
				return v.State == "completed"
			}
			return v.State == "registered"
		})
		var consumed, bound int
		if e = database.QueryRow(ctx, `SELECT count(*) FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id AND r.token_hash=e.credential_hash WHERE e.id=$1 AND e.used_at IS NOT NULL AND e.consume_request_id IS NOT NULL`, op.IDs.EnrollmentID).Scan(&consumed); e != nil || consumed != 1 {
			t.Fatalf("independent enrollment binding count=%d error=%v", consumed, e)
		}
		if e = database.QueryRow(ctx, `SELECT count(*) FROM destination_project_grants WHERE destination_id=$1 AND project_id='proj_platform'`, v.DestinationID).Scan(&bound); e != nil {
			t.Fatal(e)
		}
		if (bound == 1) != grant {
			t.Fatalf("explicit grant choice=%t actual grants=%d", grant, bound)
		}
		if v.ReadinessState != "check_server_readiness" {
			t.Fatal("installation claimed readiness")
		}
		var readiness domain.ServerReadiness
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			if e = json.Unmarshal(request("GET", "/api/v1/servers/"+v.ServerID+"/readiness", nil, credential, 200), &readiness); e != nil {
				t.Fatal(e)
			}
			if readiness.Status == "ready" {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if readiness.Status != "ready" {
			t.Fatalf("actual agent readiness=%s reason=%s report=%+v", readiness.Status, readiness.Reason, readiness.Report)
		}
		if !grant {
			v = command(v, "grant", nil)
			v = wait(v.ID, "later explicit grant", func(v app.ServerBootstrap) bool { return v.State == "completed" })
		}
		t.Logf("real API/River/SSH/systemd/enrollment/register grant=%t operation=%s server=%s readiness=%s artifact_sha256=%s", grant, v.ID, v.ServerID, readiness.Status, qualified.SHA256)
	}
	if !lostConsume.Load() {
		t.Fatal("real enrollment response loss was not exercised")
	}
	if os.Getenv("NEROCD_BOOTSTRAP_BROWSER") == "1" {
		browserPort, closeBrowserSSH := bootstrapRuntimeTCPResponseLoss(t, "127.0.0.1:22", 1<<62)
		defer closeBrowserSSH()
		leaf, e := x509.ParseCertificate(certificate.Certificate[0])
		if e != nil {
			t.Fatal(e)
		}
		spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
		metadata := map[string]string{"origin": origin, "project_id": "proj_platform", "secret_id": secret.ID, "artifact_id": qualified.ArtifactID, "fingerprint": fingerprint, "ssh_port": fmt.Sprint(browserPort), "spki": base64.StdEncoding.EncodeToString(spki[:])}
		raw, e := json.Marshal(metadata)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Printf("SERVER_BOOTSTRAP_BROWSER_READY %s\n", raw)
		for {
			if _, e = os.Stat("/fixture/onboarding-browser.finish"); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("browser completion deadline")
			case <-time.After(250 * time.Millisecond):
			}
		}
		// Browser-created operations also require actual enrollment and owned cleanup.
		page, e := service.ListServerBootstraps(admin, "proj_platform", 100, 0)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, item := range page.Items {
			if item.Name != "browser actual SSH onboarding" {
				continue
			}
			found = true
			if item.State != "completed" {
				t.Fatalf("browser operation state=%s", item.State)
			}
			op, e := pg.GetServerBootstrap(ctx, "usr_bootstrap", item.ID)
			if e != nil {
				t.Fatal(e)
			}
			manifest := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: origin, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
			out, e := serverbootstrap.CleanupOwned(ctx, serverbootstrap.Target{Address: netip.MustParseAddr("127.0.0.1"), Port: 22, User: "root", HostKeyFingerprint: fingerprint}, key, serverbootstrap.Mutation{OperationID: strings.Repeat("d", 32), Ownership: serverbootstrap.Ownership{Manifest: manifest}})
			if e != nil || out.State != serverbootstrap.OwnedRemoved {
				t.Fatalf("browser owned cleanup failed: state=%s error=%v", out.State, e)
			}
		}
		if !found {
			t.Fatal("browser did not create an actual operation")
		}
	}
	// A transparent byte forwarder delays server chunks after a substantial
	// upload. Window adjustments still pass. The peer remains real OpenSSH.
	proxyPort, dropProxyConnections, closeProxy := bootstrapRuntimeTCPForwarder(t, "127.0.0.1:22", int64(len(artifact)))
	defer closeProxy()
	lossInput := input
	lossInput.RequestID = uuid()
	lossInput.Name = "controller execution restart"
	lossInput.Target.Port = uint16(proxyPort)
	v := decode(request("POST", "/api/v1/server-bootstraps", lossInput, credential, 201))
	op, e := pg.GetServerBootstrap(ctx, "usr_bootstrap", v.ID)
	if e != nil {
		t.Fatal(e)
	}
	lossManifest := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: origin, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
	direct := serverbootstrap.Target{Address: netip.MustParseAddr("127.0.0.1"), Port: 22, User: "root", HostKeyFingerprint: fingerprint}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		out, e := serverbootstrap.CleanupOwned(cleanup, direct, key, serverbootstrap.Mutation{OperationID: strings.Repeat("e", 32), Ownership: serverbootstrap.Ownership{Manifest: lossManifest}})
		if e != nil || out.State != serverbootstrap.OwnedRemoved {
			t.Errorf("restart fixture cleanup: state=%s error=%v", out.State, e)
		}
	}()
	v = probe(v)
	v = command(v, "review", func(c *app.ServerBootstrapCommandInput) { c.ProbeReceiptID = v.Probe.ReceiptID })
	v = wait(v.ID, "restart review", func(v app.ServerBootstrap) bool { return v.Review != nil })
	grant := false
	command(v, "install", func(c *app.ServerBootstrapCommandInput) {
		c.ManifestSHA256 = v.Review.ManifestSHA256
		c.AcknowledgeInstallAndStart = true
		c.GrantProject = &grant
	})
	var installed serverbootstrap.OwnedInspection
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		attempt, done := context.WithTimeout(ctx, 2*time.Second)
		installed, e = serverbootstrap.InspectOwned(attempt, direct, key, serverbootstrap.Ownership{Manifest: lossManifest})
		done()
		if e == nil && installed.State == serverbootstrap.OwnedInstalled && installed.ServiceActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if e != nil || installed.State != serverbootstrap.OwnedInstalled || !installed.ServiceActive {
		t.Fatalf("remote install not observed before controller interruption: state=%s error=%v", installed.State, e)
	}
	beforeCut := get(v.ID)
	for _, progress := range beforeCut.Progress {
		if progress.Action == "install" && progress.Status == "completed" {
			t.Fatal("response delay did not retain a pending install receipt before interruption")
		}
	}
	t.Logf("exact owned installation active before interruption; InvocationID=%s; controller install receipt still pending", installed.InvocationID)
	// Lose the real SSH response before gracefully draining River. Keep the
	// listener and target identity available for subsequent owned inspection.
	dropProxyConnections()
	stopExecution()
	end, finish := context.WithTimeout(context.Background(), 10*time.Second)
	if e = stop(end); e != nil {
		finish()
		t.Fatal(e)
	}
	finish()
	before := get(v.ID)
	uncertainReceipt := ""
	for _, progress := range before.Progress {
		if progress.Action == "install" && progress.Status == "completed" && progress.Outcome == "uncertain" {
			uncertainReceipt = progress.ReceiptID
		}
	}
	if !before.RemoteHold || before.State != "uncertain" || uncertainReceipt == "" {
		t.Fatalf("lost SSH response did not retain uncertain receipt/hold: state=%s hold=%t", before.State, before.RemoteHold)
	}
	restartCtx, restartCancel := context.WithCancel(ctx)
	defer restartCancel()
	stop, e = pg.StartHostingExecution(restartCtx)
	if e != nil {
		t.Fatal(e)
	}
	// Settled uncertain receipts require a fresh authorized operator inspection.
	// Automatic recovery covers missing receipts, a different failure boundary.
	command(get(v.ID), "inspect", nil)
	v = wait(v.ID, "operator inspection after execution controller restart", func(v app.ServerBootstrap) bool {
		for _, p := range v.Progress {
			if p.Action == "inspect" && p.Status == "completed" && p.Outcome == "observed" {
				return true
			}
		}
		return false
	})
	after, e := serverbootstrap.InspectOwned(ctx, direct, key, serverbootstrap.Ownership{Manifest: lossManifest})
	if e != nil {
		t.Fatal(e)
	}
	if after.InvocationID != installed.InvocationID || after.OperationID != installed.OperationID || after.ManifestSHA256 != installed.ManifestSHA256 || !v.RemoteHold {
		t.Fatal("restart recovery changed remote identity or released hold")
	}
	retainedUncertainty := false
	for _, progress := range v.Progress {
		if progress.Action == "install" && progress.ReceiptID == uncertainReceipt && progress.Outcome == "uncertain" {
			retainedUncertainty = true
		}
	}
	if !retainedUncertainty {
		t.Fatal("operator inspection lost the original uncertain install receipt")
	}
	t.Logf("execution lifecycle restarted against durable DB; retained hold and uncertain receipt; operator inspection preserved installation=%s operation=%s InvocationID=%s without reinstall", after.InstallationID, after.OperationID, after.InvocationID)
	if bytes.Contains(logs.snapshot(), key) || bytes.Contains(logs.snapshot(), []byte("nce_")) {
		t.Fatal("credential in controller logs")
	}
}

func bootstrapRuntimeSSHResponseLoss(t *testing.T, transferSize int64) (int, func()) {
	return bootstrapRuntimeTCPResponseLoss(t, "127.0.0.1:22", transferSize)
}

func bootstrapRuntimeTCPResponseLoss(t *testing.T, destination string, transferSize int64) (int, func()) {
	t.Helper()
	port, _, closeProxy := bootstrapRuntimeTCPForwarder(t, destination, transferSize)
	return port, closeProxy
}

func bootstrapRuntimeTCPForwarder(t *testing.T, destination string, transferSize int64) (int, func(), func()) {
	t.Helper()
	host, _, e := net.SplitHostPort(destination)
	if e != nil || host != "127.0.0.1" || transferSize < 1 {
		t.Fatal("fixed loopback destination and positive transfer bound required")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	var activeMu sync.Mutex
	active := make(map[net.Conn]chan struct{})
	slots := make(chan struct{}, 4)
	group.Add(1)
	go func() {
		defer group.Done()
		for {
			client, e := listener.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				_ = client.Close()
				continue
			}
			group.Add(1)
			joined := make(chan struct{})
			activeMu.Lock()
			active[client] = joined
			activeMu.Unlock()
			go func() {
				defer group.Done()
				defer func() {
					activeMu.Lock()
					delete(active, client)
					activeMu.Unlock()
					close(joined)
				}()
				defer func() { <-slots }()
				defer client.Close()
				peer, e := net.DialTimeout("tcp4", destination, 2*time.Second)
				if e != nil {
					return
				}
				defer peer.Close()
				connectionCtx, stopConnection := context.WithTimeout(ctx, 125*time.Second)
				defer stopConnection()
				_ = client.SetDeadline(time.Now().Add(125 * time.Second))
				_ = peer.SetDeadline(time.Now().Add(125 * time.Second))
				var transferred atomic.Int64
				done := make(chan struct{})
				watcherDone := make(chan struct{})
				go func() {
					defer close(watcherDone)
					select {
					case <-connectionCtx.Done():
						_ = client.Close()
						_ = peer.Close()
					case <-done:
					}
				}()
				pumps := make(chan struct{}, 2)
				go func() {
					defer func() { pumps <- struct{}{} }()
					buffer := make([]byte, 32768)
					for {
						n, e := client.Read(buffer)
						if n > 0 {
							transferred.Add(int64(n))
							if _, writeErr := peer.Write(buffer[:n]); writeErr != nil {
								return
							}
						}
						if e != nil {
							return
						}
					}
				}()
				go func() {
					defer func() { pumps <- struct{}{} }()
					buffer := make([]byte, 32768)
					for {
						n, e := peer.Read(buffer)
						if n > 0 {
							if transferred.Load() >= transferSize/2 {
								delay := time.NewTimer(500 * time.Millisecond)
								select {
								case <-connectionCtx.Done():
									delay.Stop()
									return
								case <-done:
									delay.Stop()
									return
								case <-delay.C:
								}
							}
							if _, writeErr := client.Write(buffer[:n]); writeErr != nil {
								return
							}
						}
						if e != nil {
							return
						}
					}
				}()
				<-pumps
				close(done)
				_ = client.Close()
				_ = peer.Close()
				<-pumps
				<-watcherDone
			}()
		}
	}()
	dropConnections := func() {
		activeMu.Lock()
		joined := make([]chan struct{}, 0, len(active))
		for connection, done := range active {
			_ = connection.Close()
			joined = append(joined, done)
		}
		activeMu.Unlock()
		if len(joined) == 0 {
			t.Fatal("response loss requires an active owned SSH transport")
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for _, done := range joined {
			select {
			case <-done:
			case <-deadline.C:
				t.Fatal("fixture SSH connections did not join after response loss")
			}
		}
	}
	var once sync.Once
	return listener.Addr().(*net.TCPAddr).Port, dropConnections, func() {
		once.Do(func() {
			cancel()
			_ = listener.Close()
			finished := make(chan struct{})
			go func() { group.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("fixture SSH forwarder did not join")
			}
		})
	}
}

type bootstrapRuntimeLogs struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *bootstrapRuntimeLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}
func (l *bootstrapRuntimeLogs) snapshot() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.buffer.Bytes())
}

func TestServerBootstrapRuntimeForwarderJoins(t *testing.T) {
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for range 2 {
			connection, e := listener.Accept()
			if e != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.Copy(connection, connection)
			_ = connection.Close()
		}
	}()
	port, dropConnections, closeProxy := bootstrapRuntimeTCPForwarder(t, listener.Addr().String(), 8)
	defer closeProxy()
	client, e := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = client.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	buffer := make([]byte, 3)
	if _, e = io.ReadFull(client, buffer); e != nil || string(buffer) != "abc" {
		t.Fatalf("transparent initial bytes: %q %v", buffer, e)
	}
	if _, e = client.Write([]byte("defgh")); e != nil {
		t.Fatal(e)
	}
	_ = client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, e = client.Read(buffer); e == nil {
		t.Fatal("post-transfer response was not delayed")
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	delayed := make([]byte, 5)
	if _, e = io.ReadFull(client, delayed); e != nil || string(delayed) != "defgh" {
		t.Fatalf("delayed response did not pass unchanged: %q %v", delayed, e)
	}
	if _, e = client.Write([]byte("ijklm")); e != nil {
		t.Fatal(e)
	}
	_ = client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, e = client.Read(buffer); e == nil {
		t.Fatal("second response was not delayed before cancellation")
	}
	dropConnections()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = client.Read(buffer); e == nil {
		t.Fatal("dropped SSH transport still delivered a response")
	}
	reconnected, e := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if e != nil {
		t.Fatal("response loss closed the recovery listener", e)
	}
	defer reconnected.Close()
	_ = reconnected.SetDeadline(time.Now().Add(time.Second))
	if _, e = reconnected.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if _, e = io.ReadFull(reconnected, buffer); e != nil || string(buffer) != "abc" {
		t.Fatalf("same-port recovery connection failed: %q %v", buffer, e)
	}
	closeProxy()
	select {
	case <-peerDone:
	case <-time.After(time.Second):
		t.Fatal("owned peer did not observe joined cancellation")
	}
	if connection, e := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second); e == nil {
		_ = connection.Close()
		t.Fatal("forwarder listener survived cleanup")
	}
}

func bootstrapRuntimeTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "owned onboarding fixture CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(102), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	// Assert real systemd remains PID 1 before changing this disposable trust file.
	if out, e := exec.Command("systemctl", "is-system-running").Output(); e != nil || strings.TrimSpace(string(out)) != "running" {
		t.Fatal("real systemd target required")
	}
	if e = os.WriteFile("/etc/ssl/certs/ca-certificates.crt", encoded, 0644); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(encoded)
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, roots
}
