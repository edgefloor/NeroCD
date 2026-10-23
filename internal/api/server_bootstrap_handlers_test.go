package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerBootstrapHTTPRequiresAuthenticationAndRejectsProof(t *testing.T) {
	s, _ := newTestServer()
	for _, route := range PublicRoutes() {
		if !strings.Contains(route.Path, "server-bootstrap") {
			continue
		}
		request := httptest.NewRequest(route.Method, strings.ReplaceAll(route.Path, "{id}", "sbo_test"), strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, request)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s missing no-store on authentication denial", route.Method, route.Path)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d", route.Method, route.Path, rec.Code)
		}
	}
	for _, test := range []struct{ action, body string }{
		{"review", `{"command_id":"00000000-0000-4000-8000-000000000000","expected_version":1,"probe_receipt_id":"sbc_test","prerequisites":{"os_id":"debian"}}`},
		{"review", `{"command_id":"a","expected_version":1,"manifest":"fabricated"}`},
		{"install", `{"command_id":"a","expected_version":1,"controller_url":"https://attacker.invalid"}`},
		{"probe", `{"command_id":"a","expected_version":1,"actor_id":"admin"}`},
		{"inspect", `{"command_id":"a","expected_version":1,"lease_id":"invented"}`},
		{"grant", `{"command_id":"a","expected_version":1,"deadline":"2099-01-01T00:00:00Z"}`},
	} {
		rec := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/server-bootstraps/sbo_test/"+test.action, strings.NewReader(test.body))
		s.serverBootstrapCommand(rec, request)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", test.action, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
	}
	for _, body := range []string{`{"request_id":"a","artifact_path":"/bin/sh"}`, `{"request_id":"a","controller_url":"https://attacker.invalid"}`, `{"request_id":"a","ssh_private_key":"secret"}`} {
		rec := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/server-bootstraps", strings.NewReader(body))
		s.serverBootstraps(rec, request)
		if rec.Code != 400 {
			t.Fatalf("create input accepted %s: %d", body, rec.Code)
		}
	}
}

func TestServerBootstrapNoStoreBoundaries(t *testing.T) {
	s, _ := newTestServer()
	for _, path := range []string{
		"/api/v1/server-bootstrap-options?limit=invalid",
		"/api/v1/server-bootstraps?limit=invalid",
		"/api/v1/server-bootstraps/sbo_test/unknown",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d missing no-store", path, rec.Code)
		}
	}
	for _, path := range []string{
		"/api/v1/server-bootstrap-options-extra",
		"/api/v1/server-bootstrap-options/extra",
		"/api/v1/server-bootstraps-extra",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got == "no-store" {
			t.Fatalf("%s unexpectedly received bootstrap no-store", path)
		}
	}
}

func TestServerBootstrapNoStoreOnAuthorizationDenial(t *testing.T) {
	s, _ := newTestServer()
	viewer := createTestSessionFor(t, s, "viewer@example.local", "viewer")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/server-bootstrap-options?project_id=proj_platform", nil)
	request.Header.Set("Authorization", "Bearer "+viewer)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
}
