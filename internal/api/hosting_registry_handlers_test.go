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

func TestRegistryCredentialClosedBodyBeforeTransport(t *testing.T) {
	s, _ := newTestServer()
	valid := `{"authority":{"operation_id":"hop","run_id":"run_hop","lease_id":"lease","attempt":1,"fence":"fence"},"delivery_id":"` + strings.Repeat("a", 32) + `"}`
	for _, test := range []struct {
		name, body string
		want       int
	}{
		{"unknown", `{"unknown_field":true}`, 400},
		{"duplicate", strings.Replace(valid, `"attempt":1`, `"attempt":1,"attempt":2`, 1), 400},
		{"null", `{"authority":null,"delivery_id":"` + strings.Repeat("a", 32) + `"}`, 400},
		{"over limit", strings.Repeat(" ", 4097), 400},
		{"valid untrusted transport", valid, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/runners/hosting/registry-credential", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer ignored")
			w := httptest.NewRecorder()
			s.runnerHostingRegistryCredential(w, r)
			if w.Code != test.want || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), `"credential"`) {
				t.Fatal("body/transport boundary changed", w.Code, w.Body.String())
			}
		})
	}
}
