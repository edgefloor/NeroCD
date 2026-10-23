package serverbootstrap_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

// TestInstallerControlFixture runs only inside the owned target. Real registered
// API handlers and PostgreSQL retain enrollment effects while the first successful
// consume response is deliberately lost. This is same-host fixture TLS only.
func TestInstallerControlFixture(t *testing.T) {
	if os.Getenv("NEROCD_BOOTSTRAP_INSTALL_CONTROL") != "owned-systemd-target" {
		t.Skip("owned installer qualifier only")
	}
	if os.Geteuid() != 0 {
		t.Fatal("control fixture must run inside owned root target")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	databaseURL := os.Getenv("NEROCD_BOOTSTRAP_DATABASE_URL")
	database, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := db.Files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(ctx, string(data)); err != nil {
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
	}
	seed, err := os.ReadFile("/fixture/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	}()
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	enrollment, err := service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_ssh_installer_fixture", RunnerName: "owned SSH installer fixture", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, "image_release_ports_v1", "hosting_readiness_v1"}, TTLSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/fixture/enrollment", []byte(enrollment.Token), 0600); err != nil {
		t.Fatal(err)
	}
	lossEnrollment, err := service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_ssh_loss_fixture", RunnerName: "owned SSH response loss fixture", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, "image_release_ports_v1", "hosting_readiness_v1"}, TTLSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/fixture/loss-enrollment", []byte(lossEnrollment.Token), 0600); err != nil {
		t.Fatal(err)
	}
	certificate := fixtureTLS(t)
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	var consumes atomic.Int32
	var identities atomic.Int32
	var lost atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/runners/identity" {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, r)
			var identity struct {
				RunnerID string `json:"runner_id"`
			}
			if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &identity) == nil && identity.RunnerID == "runner_ssh_installer_fixture" && identities.Add(1) >= 2 {
				if err := os.WriteFile("/fixture/restart.identity", []byte("authenticated\n"), 0600); err != nil {
					t.Error(err)
				}
			}
			if response.Code == http.StatusOK && identity.RunnerID == "runner_ssh_loss_fixture" {
				if err := os.WriteFile("/fixture/loss.identity", []byte("authenticated\n"), 0600); err != nil {
					t.Error(err)
				}
			}
			for key, values := range response.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runner-enrollments/consume" {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, r)
			if response.Code == http.StatusOK && r.Header.Get("Authorization") == "Bearer "+enrollment.Token {
				consumes.Add(1)
				if lost.CompareAndSwap(false, true) {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = connection.Close()
					}
					return
				}
			}
			for key, values := range response.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		server.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:18443")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- httpServer.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}))
	}()
	defer func() {
		_ = httpServer.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	if err := os.WriteFile("/fixture/control.ready", []byte("registered-api-pg-tls-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat("/fixture/control.finish"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("installer control fixture deadline")
		case <-ticker.C:
		}
	}
	var used, runners int
	if err := database.QueryRow(ctx, "SELECT count(*) FROM runner_enrollments WHERE runner_id=$1 AND used_at IS NOT NULL AND consume_request_id IS NOT NULL AND credential_hash IS NOT NULL", "runner_ssh_installer_fixture").Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, "SELECT count(*) FROM runners WHERE id=$1", "runner_ssh_installer_fixture").Scan(&runners); err != nil {
		t.Fatal(err)
	}
	if used != 1 || runners != 1 || consumes.Load() < 2 || !lost.Load() || identities.Load() < 2 {
		t.Fatalf("real enrollment replay missing: used=%d runners=%d successful-consumes=%d lost=%t", used, runners, consumes.Load(), lost.Load())
	}
	var lossUsed int
	if err := database.QueryRow(ctx, "SELECT count(*) FROM runner_enrollments WHERE runner_id='runner_ssh_loss_fixture' AND used_at IS NOT NULL").Scan(&lossUsed); err != nil || lossUsed != 1 {
		t.Fatalf("response-loss agent did not enroll: count=%d query-error=%t", lossUsed, err != nil)
	}
	t.Logf("real authorized consume committed once; successful exact replay responses=%d; first successful response deliberately lost; one enrolled runner", consumes.Load())
	if err := os.WriteFile("/fixture/control.done", []byte("pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureTLS(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "owned installer fixture CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "owned same-host fixture"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("/etc/ssl/certs", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/etc/ssl/certs/ca-certificates.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0644); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}
}
