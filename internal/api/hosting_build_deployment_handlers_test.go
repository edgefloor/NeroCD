package api

import (
	"nerocd/internal/domain"
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

func TestBuildDeploymentContinuityDecoder(t *testing.T) {
	valid := `{"build_id":"build","expected_generation":1,"expected_source_generation":1,"idempotency_key":"local","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5},"managed_stack":{"acknowledge_interruption":true},"storage_route_continuity":{"expected_loaded_generation":2,"expected_loaded_proxy_operation_id":"pop_loaded","routes":[{"route_id":"route_web","expected_version":1,"expected_selected_release_operation_id":"hop_prior"}]}}`
	for _, bad := range []bool{false, true} {
		body := valid
		if bad {
			body = strings.Replace(body, `"expected_loaded_generation":2`, `"untrusted_authority":2`, 1)
		}
		request := httptest.NewRequest("POST", "/", strings.NewReader(body))
		response := httptest.NewRecorder()
		var in domain.HostingBuildDeploymentInput
		ok := decodeBody(response, request, &in)
		if ok == bad {
			t.Fatalf("decoder accepted bad=%t: %s", bad, response.Body.String())
		}
		if ok && (in.StorageRouteContinuity == nil || domain.ValidateHostingBuildDeployment(in) != nil) {
			t.Fatal("decoder lost valid continuity")
		}
	}
}
