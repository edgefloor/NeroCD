package runnerclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestHostingProtocolRequiresOneBoundedJSONDocument(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid_whitespace", " \n{\"id\":\"hop_actual\"}\t\n", true},
		{"trailing_garbage", `{"id":"hop_actual"} trailing`, false},
		{"concatenated_document", `{"id":"hop_actual"}{"id":"hop_forged"}`, false},
		{"over_limit", `{"id":"hop_actual","padding":"` + strings.Repeat("x", 1<<20) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/plan" || r.Header.Get("Authorization") != "Bearer runner_token" {
					t.Error("unexpected hosting protocol request")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			op, err := New(server.URL, "runner_token").HostingPlan(context.Background(), domain.HostingAuthority{OperationID: "hop_actual", RunID: "run_actual", LeaseID: "lease_actual", Attempt: 1, Fence: "fence_actual"})
			if (err == nil) != tc.valid || (tc.valid && op.ID != "hop_actual") {
				t.Fatalf("valid=%v op=%+v err=%v", tc.valid, op, err)
			}
		})
	}
}
