package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeIdentityPreservesOrdinaryIdentityContract(t *testing.T) {
	for _, test := range []struct {
		name, id     string
		capabilities []string
	}{
		{"legacy punctuation", "runner.legacy", []string{"shell"}},
		{"custom capability", "runner_legacy", []string{"shell", "CUSTOM"}},
		{"unicode identity and capability", "runner_árvíz", []string{"Saját képesség"}},
		{"duplicate capabilities", "runner_legacy", []string{"shell", "shell"}},
		{"long identity", "runner_" + strings.Repeat("a", 200), []string{"shell"}},
		{"long capability", "runner_legacy", []string{strings.Repeat("C", 200)}},
		{"many capabilities", "runner_legacy", strings.Split(strings.Repeat("CUSTOM,", 199)+"CUSTOM", ",")},
		{"bounded larger legacy response", "runner_legacy", []string{strings.Repeat("C", 70<<10)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"runner_id": test.id, "capabilities": test.capabilities})
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer runner_credential" {
					t.Error("identity made a nonidentity or unauthenticated request")
				}
				if _, err := w.Write(body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(server.URL, "runner_credential")
			oldID, oldErr := client.Identity(t.Context())
			id, err := client.RuntimeIdentity(t.Context(), false)
			if oldErr != nil || err != nil || oldID != test.id || id != oldID || requests != 2 {
				t.Fatalf("old id=%q err=%v runtime id=%q err=%v requests=%d", oldID, oldErr, id, err, requests)
			}
		})
	}
}

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
		{"duplicate", `{"runner_id":"runner_one","capabilities":["proxy_apply","proxy_apply"]}`, true, true},
		{"duplicate_unconfigured", `{"runner_id":"runner_one","capabilities":["proxy_apply","proxy_apply"]}`, false, false},
		{"wrong_case_configured", `{"runner_id":"runner_one","capabilities":["PROXY_APPLY"]}`, true, false},
		{"wrong_case_default", `{"runner_id":"runner_one","capabilities":["PROXY_APPLY"]}`, false, true},
		{"untrimmed_capability_configured", `{"runner_id":"runner_one","capabilities":[" proxy_apply "]}`, true, false},
		{"untrimmed_capability_default", `{"runner_id":"runner_one","capabilities":[" proxy_apply "]}`, false, true},
		{"null_capabilities_default", `{"runner_id":"runner_one","capabilities":null}`, false, true},
		{"null_capabilities_configured", `{"runner_id":"runner_one","capabilities":null}`, true, false},
		{"empty_capabilities_default", `{"runner_id":"runner_one","capabilities":[]}`, false, true},
		{"empty_capabilities_configured", `{"runner_id":"runner_one","capabilities":[]}`, true, false},
		{"additional_capabilities", `{"runner_id":"runner_one","capabilities":["CUSTOM","proxy_apply","other capability"]}`, true, true},
		{"concatenated", `{"runner_id":"runner_one","capabilities":["proxy_apply"]}{}`, true, false},
		{"oversized", `{"runner_id":"runner_one","padding":"` + strings.Repeat("x", maxIdentityResponseBytes) + `"}`, false, false},
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

func TestRuntimeIdentityRejectsMalformedOrUnauthenticatedResponsesWithinLegacyBound(t *testing.T) {
	valid := `{"runner_id":"runner.legacy","capabilities":["CUSTOM"]}`
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"exact legacy size bound", valid + strings.Repeat(" ", maxIdentityResponseBytes-len(valid)), 200, true},
		{"over legacy size bound", valid + strings.Repeat(" ", maxIdentityResponseBytes-len(valid)+1), 200, false},
		{"unknown fields", `{"runner_id":"runner.legacy","capabilities":["CUSTOM"],"future_field":{"value":true}}`, 200, true},
		{"surrounding document whitespace", " \t\r\n" + valid + " \t\r\n", 200, true},
		{"missing ID", `{"capabilities":[]}`, 200, false},
		{"null ID", `{"runner_id":null}`, 200, false},
		{"empty ID", `{"runner_id":""}`, 200, false},
		{"untrimmed ID", `{"runner_id":" runner.legacy "}`, 200, false},
		{"wrong ID type", `{"runner_id":123}`, 200, false},
		{"wrong capability type", `{"runner_id":"runner.legacy","capabilities":"proxy_apply"}`, 200, false},
		{"wrong capability element", `{"runner_id":"runner.legacy","capabilities":[123]}`, 200, false},
		{"malformed JSON", `{"runner_id":`, 200, false},
		{"null document", `null`, 200, false},
		{"array document", `[]`, 200, false},
		{"concatenated documents", valid + `{}`, 200, false},
		{"trailing garbage", valid + ` identity-secret-sentinel`, 200, false},
		{"unauthenticated", valid, 401, false},
		{"wrong actor credential", valid, 403, false},
		{"unsupported identity endpoint", valid, 404, false},
		{"server failure", valid, 500, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer dedicated_credential" {
					t.Error("unexpected identity authority or request")
				}
				w.WriteHeader(test.status)
				if _, err := w.Write([]byte(test.body)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			id, err := New(server.URL, "dedicated_credential").RuntimeIdentity(t.Context(), false)
			if (err == nil) != test.valid || test.valid && id != "runner.legacy" || !test.valid && id != "" || requests != 1 {
				t.Fatalf("id=%q error=%v requests=%d", id, err, requests)
			}
			if err != nil && strings.Contains(err.Error(), "identity-secret-sentinel") {
				t.Fatal("identity error reflected response contents")
			}
		})
	}
}

func TestRuntimeIdentityDecodeStopsBeforePublishingInvalidDocument(t *testing.T) {
	valid := `{"runner_id":"runner.legacy","capabilities":["proxy_apply"]}`
	reader := &countedIdentityReader{Reader: strings.NewReader(valid + strings.Repeat(" ", 2*maxIdentityResponseBytes))}
	var response proxyIdentityResponse
	if response.decodeResponse(reader) == nil || reader.bytesRead != maxIdentityResponseBytes+1 || response.RunnerID != "" || response.Capabilities != nil {
		t.Fatalf("oversized decoder published identity or exceeded bounded read: bytes=%d response=%+v", reader.bytesRead, response)
	}
	if response.decodeResponse(strings.NewReader(valid+` {}`)) == nil || response.RunnerID != "" || response.Capabilities != nil {
		t.Fatal("incomplete document published identity before whole-response validation")
	}
}
