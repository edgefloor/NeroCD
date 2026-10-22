package api

import (
	"crypto/sha256"
	"fmt"
	"nerocd/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostingObservationRegisteredRoutesAndStrictJSON(t *testing.T) {
	s, mem := newTestServer()
	user := createTestSession(t, s)
	token := "observation_runner_token"
	_, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_observer", Name: "observer", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), Status: domain.RunnerActive, Capabilities: []string{domain.HostingObservationCapability}, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, credential, body string, want int) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status %d want %d %s", path, rec.Code, want, rec.Body.String())
		}
	}
	for _, endpoint := range []struct{ path, body string }{
		{"/api/v1/runners/hosting-observations/targets", `{"limit":50}`},
		{"/api/v1/runners/hosting-observations/report", `{"binding_id":"binding","sample_id":"sample","sample_duration_ms":0,"container_state":"unknown","readiness":"unknown","collection_error":"inspect_failed"}`},
	} {
		call(endpoint.path, "", endpoint.body, 401)
		call(endpoint.path, user, endpoint.body, 401)
		call(endpoint.path, token, endpoint.body, 503)
		for _, body := range []string{`{"socket":"/var/run/docker.sock"}`, `{"limit":1,"limit":2}`, `{"limit":1,"Limit":2}`, `null`, endpoint.body + ` {}`} {
			call(endpoint.path, token, body, 400)
		}
		call(endpoint.path, token, strings.Repeat(" ", 8193)+endpoint.body, 413)
	}
	call("/api/v1/runners/hosting-observations/targets", token, `{"limit":51}`, 400)
	req := httptest.NewRequest("GET", "/api/v1/resources/missing/runtime-status", nil)
	req.Header.Set("Authorization", "Bearer "+user)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
