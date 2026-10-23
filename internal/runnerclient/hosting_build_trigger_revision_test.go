package runnerclient

import (
	"encoding/json"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeIdentityBuildTriggerRevisionCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		caps         []string
		build, valid bool
	}{
		{"legacy build", []string{domain.HostingBuildTaskType}, true, true},
		{"new build", []string{domain.HostingBuildTaskType, domain.HostingBuildTriggerRevisionCapability}, true, true},
		{"unconfigured", []string{domain.HostingBuildTriggerRevisionCapability}, false, false},
		{"missing build capability", []string{domain.HostingBuildTriggerRevisionCapability}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": tc.caps}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Build: tc.build})
			if (err == nil) != tc.valid {
				t.Fatal("identity compatibility mismatch", err)
			}
		})
	}
}
