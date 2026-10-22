package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildDeploymentAPIDecodesStrictlyAndRequiresGit(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	valid := `{"build_id":"build","expected_generation":1,"expected_source_generation":1,"idempotency_key":"local","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5}}`
	for _, tt := range []struct {
		name, body, token string
		want              int
	}{
		{"authentication", valid, "", 401},
		{"unknown authority", strings.TrimSuffix(valid, "}") + `,"artifact":{}}`, token, 400},
		{"nonexistent resource", valid, token, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/resources/absent/deployments/from-build", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			if tt.token != "" {
				r.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status%d want%d %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}
