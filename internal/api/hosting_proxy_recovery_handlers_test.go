package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostingProxyRecoveryAPIBoundaries(t *testing.T) {
	s, _ := newTestServer()
	for _, path := range []string{"/api/v1/proxy-operations/pop_test/recovery", "/api/v1/proxy-operations/pop_test/recovery-plans", "/api/v1/proxy-operations/pop_test/recovery-plans/prp_test/apply"} {
		method := http.MethodPost
		if strings.HasSuffix(path, "/recovery") {
			method = http.MethodGet
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		s.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status=%d", path, recorder.Code)
		}
	}
	for _, body := range []string{`{"idempotency_key":"apply","plan_hash":"sha256:abc","unlock":true}`, `{"idempotency_key":"apply","routes":[]}`, `{} {}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		s.hostingProxyRecoveryApply(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("arbitrary recovery input %s status=%d", body, recorder.Code)
		}
	}
}
