package runnerclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyIdentityRequiresAuthenticatedCapabilityAndExplicitConfig(t *testing.T) {
	for _, test := range []struct {
		name, body        string
		configured, valid bool
	}{
		{"configured", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}`, true, true},
		{"missing_capability", `{"runner_id":"runner_one","capabilities":["shell"]}`, true, false},
		{"legacy_configured", `{"runner_id":"runner_one"}`, true, false},
		{"legacy_default", `{"runner_id":"runner_one"}`, false, true},
		{"unconfigured_proxy", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}`, false, false},
		{"duplicate", `{"runner_id":"runner_one","capabilities":["proxy_apply","proxy_apply"]}`, true, false},
		{"concatenated", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}{}`, true, false},
		{"oversized", `{"runner_id":"runner_one","padding":"` + strings.Repeat("x", 64<<10) + `"}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer runner_credential" {
					t.Error("proxy identity performed a nonidentity or unauthenticated request")
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			id, err := New(server.URL, "runner_credential").RuntimeIdentity(t.Context(), test.configured)
			if (err == nil) != test.valid || test.valid && id != "runner_one" || requests != 1 {
				t.Fatalf("identity=%q err=%v requests=%d", id, err, requests)
			}
		})
	}
}
