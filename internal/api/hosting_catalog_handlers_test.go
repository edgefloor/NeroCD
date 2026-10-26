package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestHostingCatalogAPIAuthenticationAndClosedInput(t *testing.T) {
	s, _ := newTestServer()
	token := createTestSession(t, s)
	check := func(method, path, body, credential string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("catalog request %s %s status %d body %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	check("GET", "/api/v1/hosting-catalog?project_id=proj_platform", "", "", 401)
	raw := check("GET", "/api/v1/hosting-catalog?project_id=proj_platform", "", token, 200)
	var items []domain.HostingCatalogTemplate
	if json.Unmarshal(raw, &items) != nil || len(items) != 1 || items[0].Version != domain.HostingCatalogLinkdingVersion {
		t.Fatal("catalog metadata missing")
	}
	for _, body := range []string{`{}`, `{"request_id":"a","compose":"services: {}"}`, `{"request_id":"a","request_id":"b"}`, `null`} {
		check("POST", "/api/v1/hosting-catalog/installations", body, token, 400)
	}
}
