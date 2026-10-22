package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/store"
)

type runtimeCredentialHTTPRepository struct {
	*store.MemoryStore
	document domain.HostingRuntimeDocument
	window   domain.HostingRuntimeEnvironmentWindow
	hash     string
	after    func()
}

func (r *runtimeCredentialHTTPRepository) AuthorizeHostingRuntimeEnvironment(_ context.Context, runner, hash string, in domain.HostingRuntimeEnvironmentRequest) (store.HostingRuntimeEnvironmentAuthorization, error) {
	r.hash = hash
	if r.after != nil {
		r.after()
	}
	return store.HostingRuntimeEnvironmentAuthorization{ProjectID: "project", Document: r.document, Window: r.window, Records: map[domain.HostingRuntimeSecretReference]store.ManagedSecretRecord{}}, nil
}

func TestRuntimeEnvironmentHTTPOriginalBearerBinding(t *testing.T) {
	mem := newSeededTestStore()
	old := "original-http-bearer"
	oldHash := fmtRuntimeCredentialHash(old)
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runtime_http", Name: "runtime_http", TokenHash: oldHash, Status: domain.RunnerActive, Capabilities: []string{domain.HostingRuntimeEnvironmentCapability}}); err != nil {
		t.Fatal(err)
	}
	value := "literal-value"
	doc, _, err := domain.CanonicalHostingRuntimeDocument([]domain.HostingRuntimeEntry{{Name: "APP", Kind: "literal", Value: &value}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingRuntimeEnvironmentRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: domain.HostingDocumentHash(doc)}}
	repo := &runtimeCredentialHTTPRepository{MemoryStore: mem, document: doc, window: domain.HostingRuntimeEnvironmentWindow{DeliveryID: in.DeliveryID, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash("runtime_http", in), UseUntil: time.Now().Add(time.Minute)}}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/api/v1/runners/hosting/runtime-environment", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache header missing")
		}
		return rec
	}
	for _, token := range []string{"", "incorrect-http-bearer"} {
		if rec := request(token); rec.Code != http.StatusUnauthorized {
			t.Fatal("invalid bearer accepted", rec.Code)
		}
	}
	publicBody := body
	body = []byte(strings.TrimSuffix(string(body), "}") + `,"credential_hash":"` + oldHash + `"}`)
	if rec := request(old); rec.Code != http.StatusBadRequest || repo.hash != "" {
		t.Fatal("caller supplied hash reached delivery", rec.Code)
	}
	body = publicBody
	for range 2 {
		rec := request(old)
		if rec.Code != http.StatusOK || repo.hash != oldHash {
			t.Fatal("original bearer was not privately bound", rec.Code)
		}
		responseBody := rec.Body.String()
		owned, e := hostingruntimeprotocol.ReadEnvironment(strings.NewReader(responseBody))
		if e != nil {
			t.Fatal(e)
		}
		metadata, e := owned.Metadata()
		owned.Close()
		if e != nil || !metadata.UseUntil.Equal(repo.window.UseUntil) {
			t.Fatal("same ID retry changed window", e)
		}
		if strings.Contains(responseBody, old) || strings.Contains(responseBody, oldHash) {
			t.Fatal("credential entered response")
		}
	}
	newToken := "replacement-http-bearer"
	repo.after = func() {
		if _, e := mem.UpdateRunnerToken(t.Context(), "runtime_http", fmtRuntimeCredentialHash(newToken), domain.RunnerActive, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	if rec := request(old); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "entries") {
		t.Fatal("rotated credential returned environment", rec.Code)
	}
	repo.after = nil
	if rec := request(old); rec.Code != http.StatusUnauthorized {
		t.Fatal("old fresh bearer accepted", rec.Code)
	}
	if rec := request(newToken); rec.Code != http.StatusOK || repo.hash != fmtRuntimeCredentialHash(newToken) {
		t.Fatal("replacement bearer failed", rec.Code)
	}
}

func fmtRuntimeCredentialHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestRuntimeEnvironmentNoStoreAcrossFailures(t *testing.T) {
	for _, test := range []struct {
		name, method, body, content string
		draining                    bool
	}{
		{"authentication", "POST", `{}`, "application/json", false},
		{"malformed", "POST", `{`, "application/json", false},
		{"content_type", "POST", `{}`, "text/plain", false},
		{"method", "GET", ``, "", false},
		{"draining", "POST", `{}`, "application/json", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newTestServer()
			s.SetDraining(test.draining)
			req := httptest.NewRequest(test.method, "/api/v1/runners/hosting/runtime-environment", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.content)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, req)
			if response.Code < http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%s", response.Code, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestRuntimeEnvironmentAuthenticatedStrictRequest(t *testing.T) {
	s, mem := newTestServer()
	token := "runtime-strict-token"
	sum := sha256.Sum256([]byte(token))
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runtime_strict", Name: "runtime_strict", Capabilities: []string{domain.HostingRuntimeEnvironmentCapability}, TokenHash: hex.EncodeToString(sum[:]), Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	valid := `{"authority":{"operation_id":"hop","run_id":"run_hop","lease_id":"lease","attempt":1,"fence":"fence"},"plan_hash":"sha256:` + strings.Repeat("a", 64) + `","runtime_configuration":{"generation":1,"hash":"sha256:` + strings.Repeat("b", 64) + `"},"delivery_id":"` + strings.Repeat("c", 32) + `"}`
	for _, body := range []string{strings.Replace(valid, `"attempt":1`, `"attempt":1,"attempt":2`, 1), strings.Replace(valid, `"authority":`, `"Authority":`, 1), strings.Replace(valid, `"fence":"fence"`, `"fence":"\ud800"`, 1), strings.Replace(valid, `"generation":1`, `"generation":1,"unknown":true`, 1)} {
		req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/api/v1/runners/hosting/runtime-environment", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "fence") {
			t.Fatal("strict request failure", rec.Code, rec.Body.String())
		}
	}
	// Well-formed intent reaches the durable-storage boundary and fails closed.
	req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/api/v1/runners/hosting/runtime-environment", strings.NewReader(valid))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("memory storage delivered", rec.Code)
	}
}

func TestRuntimeEnvironmentTLSTerminationTrust(t *testing.T) {
	for _, test := range []struct {
		name, peer, host    string
		proto               []string
		forwarded           string
		fixture, tls, allow bool
	}{
		{name: "direct TLS", peer: "unparsed", tls: true, allow: true},
		{name: "trusted proxy", peer: "192.0.2.1:9000", proto: []string{"https"}, allow: true},
		{name: "untrusted spoof", peer: "192.0.2.2:9000", proto: []string{"https"}},
		{name: "duplicate", peer: "192.0.2.1:9000", proto: []string{"https", "https"}},
		{name: "comma list", peer: "192.0.2.1:9000", proto: []string{"https,http"}},
		{name: "case", peer: "192.0.2.1:9000", proto: []string{"HTTPS"}},
		{name: "whitespace", peer: "192.0.2.1:9000", proto: []string{" https"}},
		{name: "missing proxy assertion", peer: "192.0.2.1:9000"},
		{name: "malformed peer", peer: "192.0.2.1", proto: []string{"https"}},
		{name: "default loopback denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080"},
		{name: "explicit loopback", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, allow: true},
		{name: "explicit IPv6 loopback", peer: "[::1]:9000", host: "[::1]:8080", fixture: true, allow: true},
		{name: "fixture DNS denied", peer: "127.0.0.1:9000", host: "localhost:8080", fixture: true},
		{name: "fixture remote Host denied", peer: "127.0.0.1:9000", host: "example.com", fixture: true},
		{name: "fixture remote peer denied", peer: "192.0.2.2:9000", host: "127.0.0.1:8080", fixture: true},
		{name: "fixture forwarded denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, forwarded: "for=127.0.0.1;proto=https"},
		{name: "fixture proxy header denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, proto: []string{"https"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newTestServerWithConfig(ServerConfig{TrustedProxyCIDRs: []string{"192.0.2.1/32"}, TrustedLoopbackRuntimeEnvironmentHTTP: test.fixture})
			r := httptest.NewRequest("POST", "http://example.com/api/v1/runners/hosting/runtime-environment", nil)
			r.RemoteAddr = test.peer
			r.Host = test.host
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for _, p := range test.proto {
				r.Header.Add("X-Forwarded-Proto", p)
			}
			if test.forwarded != "" {
				r.Header.Set("Forwarded", test.forwarded)
			}
			if got := s.runtimeEnvironmentTransport(r); got != test.allow {
				t.Fatalf("transport=%v expected=%v", got, test.allow)
			}
		})
	}
}
