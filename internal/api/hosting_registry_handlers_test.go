package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestRegistryDeploymentRealDecoderContract(t *testing.T) {
	prefix := `{"expected_generation":1,"idempotency_key":"private-pull","container_port":80,"readiness":{"path":"/","timeout_seconds":5}`
	cases := []struct {
		name, selection string
		valid           bool
	}{{"selected routed storage", `,"host_port":18080,"registry_credential":{"secret_id":"secret","version":1},"storage_route_continuity":{"expected_loaded_generation":2,"expected_loaded_proxy_operation_id":"proxy_loaded","routes":[{"route_id":"route","expected_version":1,"expected_selected_release_operation_id":"previous"}]}`, true}, {"anonymous", "", true}, {"selected", `,"registry_credential":{"secret_id":"secret","version":1}`, true}, {"empty", `,"registry_credential":{}`, false}, {"null", `,"registry_credential":null`, false}, {"zero version", `,"registry_credential":{"secret_id":"secret","version":0}`, false}, {"extra field", `,"registry_credential":{"secret_id":"secret","version":1,"registry":"other"}`, false}, {"duplicate version", `,"registry_credential":{"secret_id":"secret","version":1,"version":2}`, false}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/resources/resource/deployments", strings.NewReader(prefix+c.selection+"}"))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			var in domain.HostingDeploymentInput
			valid := decodeBody(w, r, &in)
			if valid != c.valid {
				t.Fatal("decoder result", w.Code)
			}
			if valid && domain.ValidateHostingDeployment(in) != nil {
				t.Fatal("valid request rejected")
			}
		})
	}
}
