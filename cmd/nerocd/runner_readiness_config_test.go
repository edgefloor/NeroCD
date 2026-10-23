package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

func TestRunnerReadinessOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		enabled, execute, once, specified bool
		complete                          string
		caps, want                        []string
		valid                             bool
	}{
		{name: "implicit shell only removed", enabled: true, execute: true, caps: []string{"shell", "CUSTOM"}, want: []string{"CUSTOM", domain.ServerReadinessCapability}, valid: true},
		{name: "other features retained", enabled: true, execute: true, specified: true, caps: []string{domain.HostingObservationCapability, domain.RunTypeImageDeploy}, want: []string{domain.HostingObservationCapability, domain.RunTypeImageDeploy, domain.ServerReadinessCapability}, valid: true},
		{name: "no duplicate", enabled: true, execute: true, specified: true, caps: []string{domain.ServerReadinessCapability}, want: []string{domain.ServerReadinessCapability}, valid: true},
		{name: "explicit shell", enabled: true, execute: true, specified: true, caps: []string{"shell"}},
		{name: "execute required", enabled: true},
		{name: "once forbidden", enabled: true, execute: true, once: true},
		{name: "completion forbidden", enabled: true, execute: true, complete: "succeeded"},
		{name: "capability requires opt in", caps: []string{domain.ServerReadinessCapability}},
		{name: "disabled legacy unchanged", caps: []string{"shell"}, want: []string{"shell"}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runnerReadinessCapabilities(tc.enabled, tc.execute, tc.once, tc.complete, tc.specified, tc.caps)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid && !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, tc.want)
			}
		})
	}
}

func TestRunnerReadinessCLIRejectsBeforeCredentialAccess(t *testing.T) {
	for _, args := range [][]string{{"--hosting-readiness", "--execute=false"}, {"--hosting-readiness", "--once"}, {"--hosting-readiness", "--complete-status=succeeded"}, {"--hosting-readiness", "--capabilities=shell"}, {"--capabilities=hosting_readiness_v1"}, {"--hosting-readiness", "--server=http://127.0.0.1:1"}} {
		if err := runRunner(append(args, "--credential-file=/missing-readiness-credential")); err == nil {
			t.Fatal(args)
		}
	}
}

func TestRunnerReadinessBootstrapEnrollmentLossRetainsRequest(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/runner-enrollments/consume" || r.Header.Get("Authorization") != "Bearer one-time-fixture" {
			t.Error("bootstrap request changed authority")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(app.ConsumedRunnerEnrollment{Runner: domain.Runner{ID: "runner_bootstrap"}})
	}))
	defer server.Close()
	input := app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: strings.Repeat("b", 64)}
	var result app.ConsumedRunnerEnrollment
	if err := runnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runner-enrollments/consume", input, "one-time-fixture", &result, server.Client()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || result.Runner.ID != "runner_bootstrap" {
		t.Fatal("bootstrap retry refreshed fixed request")
	}
	var actual app.RunnerEnrollmentConsumeInput
	if err := json.Unmarshal(bodies[1], &actual); err != nil || actual != input {
		t.Fatal(actual, err)
	}
}

func TestRunnerReadinessBootstrapTransportLimitsAndSanitization(t *testing.T) {
	const secret = "bootstrap-credential-never-render"
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusUnauthorized, http.StatusConflict, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL+"/"+secret)
				w.WriteHeader(code)
				_, _ = w.Write([]byte(secret))
			}))
			defer server.Close()
			var out runnerRegisterResponse
			err := runnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runners/register", runnerRegisterRequest{ID: "runner"}, secret, &out, server.Client())
			if err == nil || !runnerclient.HTTPStatus(err, code) || calls.Load() != 1 || redirected.Load() != 0 || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), server.URL) {
				t.Fatal("unsafe registration transport", err, calls.Load(), redirected.Load())
			}
		})
	}
	for _, body := range []string{strings.Repeat("x", 8193), `{"runner":`, `{"runner":{},"raw":"` + secret + `"}`} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		var out app.ConsumedRunnerEnrollment
		err := runnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out, server.Client())
		server.Close()
		if err == nil || strings.Contains(err.Error(), secret) || runnerclient.ClassifyFailure(err) == runnerclient.FailureTransient {
			t.Fatal("unsafe response decoder", err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(app.ConsumedRunnerEnrollment{Runner: domain.Runner{ID: "runner"}})
	}))
	defer server.Close()
	var out app.ConsumedRunnerEnrollment
	if err := postRunnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out); err == nil || runnerclient.ClassifyFailure(err) == runnerclient.FailureTransient {
		t.Fatal("untrusted TLS accepted or retried", err)
	}
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	var proxied atomic.Int32
	transport.Proxy = func(*http.Request) (*url.URL, error) { proxied.Add(1); return nil, nil }
	client = &http.Client{Transport: transport}
	if err := runnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out, client); err != nil || proxied.Load() != 0 {
		t.Fatal("ambient proxy retained", err)
	}
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if err := runnerReadinessBootstrap(t.Context(), server.URL, "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out, &http.Client{Transport: transport}); err == nil {
		t.Fatal("insecure TLS enabled")
	}
	blocked := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer blocked.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := runnerReadinessBootstrap(ctx, blocked.URL, "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out, blocked.Client()); err == nil || time.Since(started) > time.Second || strings.Contains(err.Error(), secret) {
		t.Fatal("bootstrap cancellation lost", err)
	}
	if err := postRunnerReadinessBootstrap(t.Context(), "http://127.0.0.1:1", "/api/v1/runner-enrollments/consume", struct{}{}, secret, &out); err == nil {
		t.Fatal("cleartext bootstrap enabled")
	}
}

func TestRunnerReadinessBootstrapSignalChild(t *testing.T) {
	if os.Getenv("NEROCD_READINESS_BOOTSTRAP_CHILD") != "1" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(os.Getenv("NEROCD_READINESS_BOOTSTRAP_ARGS")), &args) != nil {
		os.Exit(3)
	}
	// Trust only this fixture CA in the isolated child; keep TLS verification enabled.
	ca, err := os.ReadFile(os.Getenv("NEROCD_READINESS_BOOTSTRAP_CA"))
	if err != nil {
		os.Exit(5)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		os.Exit(6)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer transport.CloseIdleConnections()
	if err := runRunner(args); err == nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestRunnerReadinessBootstrapSignalCancelsBeforeRetry(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runner-enrollments/consume" {
			t.Error("unexpected bootstrap traffic")
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	enrollment := filepath.Join(root, "enrollment")
	credential := filepath.Join(root, "credential")
	ca := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(enrollment, []byte("nce_"+strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal([]string{"--hosting-readiness", "--execute", "--server", server.URL, "--enrollment-file", enrollment, "--credential-file", credential, "--work-dir", root})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRunnerReadinessBootstrapSignalChild$", "-test.timeout=10s")
	command.Env = append(os.Environ(), "NEROCD_READINESS_BOOTSTRAP_CHILD=1", "NEROCD_READINESS_BOOTSTRAP_ARGS="+string(args), "NEROCD_MODE=development", "NEROCD_TOKEN=", "NEROCD_RUNNER_CREDENTIAL_FILE=", "NEROCD_READINESS_BOOTSTRAP_CA="+ca)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("bootstrap child did not join")
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("bootstrap child did not reach authenticated request")
	}
	signalAt := time.Now()
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		finished = true
		if err != nil {
			t.Fatal(err, output.String())
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("SIGTERM did not cancel bootstrap before request timeout")
	}
	if time.Since(signalAt) > 1500*time.Millisecond {
		t.Fatal("bootstrap exceeded cancellation bound")
	}
	t.Logf("verified task child pid=%d joined after SIGTERM during bootstrap", command.Process.Pid)
}
