package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestRunnerInvalidInputErrorsUseBadRequestEnvelope(t *testing.T) {
	server, mem := newTestServer()
	runnerToken := "runner-error-envelope-token"
	hash := sha256.Sum256([]byte(runnerToken))
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{
		ID:              "runner_error_envelope",
		Name:            "error envelope",
		Tags:            []string{},
		Capabilities:    []string{"shell", "compose_deploy"},
		TokenHash:       hex.EncodeToString(hash[:]),
		Status:          domain.RunnerActive,
		RegisteredAt:    time.Now().UTC(),
		LastHeartbeatAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
		error  string
	}{
		{name: "lease missing attempt", method: http.MethodGet, path: "/api/v1/runners/lease", error: "attempt is required"},
		{name: "lease malformed attempt", method: http.MethodGet, path: "/api/v1/runners/lease?attempt=invalid", error: "attempt is required"},
		{name: "deployment plan missing attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/plan", error: "attempt is required"},
		{name: "deployment plan malformed attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/plan?attempt=invalid", error: "attempt is required"},
		{name: "deployment plan nonpositive attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/plan?attempt=0", error: "attempt is required"},
		{name: "deployment status missing attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/status", error: "attempt is required"},
		{name: "deployment status malformed attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/status?attempt=invalid", error: "attempt is required"},
		{name: "deployment status nonpositive attempt", method: http.MethodGet, path: "/api/v1/runners/deployments/status?attempt=0", error: "attempt is required"},
		{name: "complete missing attempt", method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"fence":"fence","completion_key":"complete"}`, error: "attempt, fence, and completion_key are required"},
		{name: "complete nonpositive attempt", method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"attempt":0,"fence":"fence","completion_key":"complete"}`, error: "attempt, fence, and completion_key are required"},
		{name: "complete missing fence", method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"attempt":1,"completion_key":"complete"}`, error: "attempt, fence, and completion_key are required"},
		{name: "complete blank completion key", method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"attempt":1,"fence":"fence","completion_key":" \t "}`, error: "attempt, fence, and completion_key are required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := runnerEnvelopeRequest(t, server, runnerToken, test.method, test.path, test.body)
			assertBadRequestEnvelope(t, rec, test.error)
		})
	}
}

func TestRunnerInvalidInputErrorsRemainUnauthorizedWithoutRunnerCredentials(t *testing.T) {
	server, _ := newTestServer()

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "lease", method: http.MethodGet, path: "/api/v1/runners/lease"},
		{name: "deployment plan", method: http.MethodGet, path: "/api/v1/runners/deployments/plan"},
		{name: "deployment status", method: http.MethodGet, path: "/api/v1/runners/deployments/status"},
		{name: "complete", method: http.MethodPost, path: "/api/v1/runners/complete", body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func runnerEnvelopeRequest(t *testing.T, server *Server, token string, method string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func assertBadRequestEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantError string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if got, want := body, map[string]string{"code": "bad_request", "error": wantError}; !mapsEqual(got, want) {
		t.Errorf("body = %#v, want %#v", got, want)
	}
}

func mapsEqual(got map[string]string, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			return false
		}
	}
	return true
}
