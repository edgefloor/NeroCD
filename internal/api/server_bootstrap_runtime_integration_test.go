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
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
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
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	const origin = "https://127.0.0.1:19443"
	qualified := app.ServerBootstrapArtifact{ArtifactID: "fixture-arm64", Path: "/fixture/onboarding-agent", Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:]), Version: "fixture-v1", Architecture: "arm64", Profile: "prepared_debian12_v1"}
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
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
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
			t.Fatal("bootstrap response cacheable")
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
			_ = response.Body.Close()
			t.Fatal("committed response was not lost")
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
		if action == "review" {
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
		v = command(v, "probe", nil)
		return wait(v.ID, "real SSH probe", func(v app.ServerBootstrap) bool { return v.Probe != nil && v.Probe.Prerequisites.Qualifies("arm64") })
	}
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, "", 401)
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, viewer, 403)
	request("GET", "/api/v1/server-bootstrap-options?project_id=proj_platform", nil, credential, 200)
	fingerprint := os.Getenv("NEROCD_BOOTSTRAP_PROBE_FINGERPRINT")
	target := domain.ServerBootstrapTarget{IP: "127.0.0.1", Port: 22, User: "root", HostFingerprint: fingerprint}
	input := app.ServerBootstrapCreateInput{RequestID: uuid(), ProjectID: "proj_platform", Name: "actual SSH onboarding", Target: target, SSHSecretID: secret.ID, SSHSecretVersion: 1, ArtifactID: qualified.ArtifactID}
	unknown := input
	unknown.ArtifactID = "unknown"
	request("POST", "/api/v1/server-bootstraps", unknown, credential, 400)
	cross := input
	cross.ProjectID = "proj_security"
	request("POST", "/api/v1/server-bootstraps", cross, credential, 403)
	// Negative host trust remains a real SSH probe, with no replacement adapter.
	wrong := input
	wrong.RequestID = uuid()
	wrong.Target.HostFingerprint = "SHA256:" + strings.Repeat("A", 43)
	wrongView := decode(request("POST", "/api/v1/server-bootstraps", wrong, credential, 201))
	wrongView = command(wrongView, "probe", nil)
	wait(wrongView.ID, "wrong host rejection", func(v app.ServerBootstrap) bool {
		for _, p := range v.Progress {
			if p.Action == "probe" && p.Status == "completed" {
				return p.Outcome == "refused"
			}
		}
		return false
	})
	for _, grant := range []bool{true, false} {
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
	if bytes.Contains(logs.Bytes(), key) || strings.Contains(logs.String(), "nce_") {
		t.Fatal("credential in controller logs")
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
	if e = os.WriteFile("/etc/ssl/certs/ca-certificates.crt", encoded, 0644); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(encoded)
	// Assert real systemd remains PID 1 before changing this disposable trust file.
	if out, e := exec.Command("systemctl", "is-system-running").Output(); e != nil || strings.TrimSpace(string(out)) != "running" {
		t.Fatal("real systemd target required")
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, roots
}
