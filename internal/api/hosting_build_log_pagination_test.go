package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostingBuildLogPaginationUsesRegisteredGETRoute(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	for _, query := range []string{"", "?limit=1", "?limit=100", "?limit=101", "?limit=200", "?after_sequence=0&limit=200"} {
		t.Run("typed"+query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/hosting-builds/build/logs"+query, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			// The memory backend is explicitly unavailable. Reaching that response proves
			// both the global boundary and typed query handler accepted the documented limit.
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("got%d body%s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, path := range []string{"/api/v1/hosting-builds/build/logs-more", "/api/v1/hosting-builds/build/logs/extra", "/api/v1/hosting-builds/build/logs/", "/api/v1/hosting-builds/build/other/logs", "/api/v1/hosting-builds-more/build/logs"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path+"?limit=101", nil)
			req.Pattern = "GET /api/v1/hosting-builds/{id}/logs"
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid pagination") {
				t.Fatalf("lookalike received typed limit: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/v1/hosting-builds/build/logs?limit=101", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("method%s received typed limit: %d", method, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosting-builds/build/logs?limit=200", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("typed pagination bypassed authentication: %d", rec.Code)
	}
}
func TestHostingBuildLogPaginationPreservesBoundsAndLegacyRoutes(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	for _, query := range []string{"?limit=201", "?limit=0", "?limit=-1", "?limit=", "?limit=no", "?limit=1&limit=2", "?offset=0", "?offset=-1", "?offset=100001", "?offset=", "?offset=1&offset=2", "?after_sequence=-1", "?after_sequence=2147483648", "?after_sequence=", "?after_sequence=0&after_sequence=1", "?unexpected=1"} {
		t.Run(query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/hosting-builds/build/logs"+query, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Fatalf("invalid typed query got%d body%s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, entry := range []struct {
		query string
		want  int
	}{{"", 200}, {"?limit=0", 200}, {"?limit=100", 200}, {"?limit=101", 400}, {"?limit=200", 400}, {"?limit=", 400}, {"?limit=1&limit=2", 400}, {"?offset=100000", 200}, {"?offset=100001", 400}} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects"+entry.query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != entry.want {
			t.Fatalf("legacy%s got%d want%d body%s", entry.query, rec.Code, entry.want, rec.Body.String())
		}
	}
}
