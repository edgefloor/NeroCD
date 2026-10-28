package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildRegistryCredentialClosedBodyBeforeTransport(t *testing.T) {
	s, _ := newTestServer()
	valid := `{"authority":{"build_id":"build-one","run_id":"build_run_build-one","lease_id":"lease","attempt":1,"fence":"fence"},"delivery_id":"` + strings.Repeat("a", 32) + `"}`
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
			r := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/runners/builds/registry-credential", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer ignored")
			w := httptest.NewRecorder()
			s.runnerBuildRegistryCredential(w, r)
			if w.Code != test.want || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), `"credential"`) {
				t.Fatal("body/transport boundary changed", w.Code, w.Body.String())
			}
		})
	}
}
