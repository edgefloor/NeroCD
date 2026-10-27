package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nerocd/internal/domain"
)

func TestGitComposeIdentityRequiresExactExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caps    []string
		options RuntimeExecutionOptions
		ok      bool
	}{
		{"configured", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, true},
		{"not configured", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability}, RuntimeExecutionOptions{Build: true}, false},
		{"capability missing", []string{domain.HostingBuildTaskType}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
		{"build missing", []string{domain.HostingGitComposeBuildCapability}, RuntimeExecutionOptions{ComposeBuild: true}, false},
		{"shell forbidden", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability, "shell"}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: tc.caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), tc.options)
			if (err == nil) != tc.ok {
				t.Fatalf("identity acceptance %v", err)
			}
		})
	}
}
