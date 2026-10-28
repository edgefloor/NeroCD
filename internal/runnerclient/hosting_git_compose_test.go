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
		{"configured", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, true},
		{"not configured", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability}, RuntimeExecutionOptions{Build: true}, false},
		{"args missing", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
		{"build profile missing", []string{domain.HostingBuildTaskType, domain.HostingGitComposeArgsCapability}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
		{"args not configured", []string{domain.HostingBuildTaskType, domain.HostingGitComposeArgsCapability}, RuntimeExecutionOptions{Build: true}, false},
		{"capability missing", []string{domain.HostingBuildTaskType}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
		{"build missing", []string{domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability}, RuntimeExecutionOptions{ComposeBuild: true}, false},
		{"shell forbidden", []string{domain.HostingBuildTaskType, domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability, "shell"}, RuntimeExecutionOptions{Build: true, ComposeBuild: true}, false},
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

func TestDockerfileArgumentsIdentityPreservesLegacyBuilds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		caps  []string
		build bool
		ok    bool
	}{
		{"legacy build", []string{domain.HostingBuildTaskType}, true, true},
		{"arguments", []string{domain.HostingBuildTaskType, domain.HostingDockerfileArgsCapability}, true, true},
		{"no configuration", []string{domain.HostingBuildTaskType, domain.HostingDockerfileArgsCapability}, false, false},
		{"no build type", []string{domain.HostingDockerfileArgsCapability}, true, false},
		{"shell", []string{domain.HostingBuildTaskType, domain.HostingDockerfileArgsCapability, "shell"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: tc.caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Build: tc.build})
			if (err == nil) != tc.ok {
				t.Fatal("identity acceptance changed", err)
			}
		})
	}
}

func TestBuildRegistryIdentityPreservesLegacyBuilds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		caps  []string
		build bool
		ok    bool
	}{
		{"legacy build", []string{domain.HostingBuildTaskType}, true, true},
		{"arguments", []string{domain.HostingBuildTaskType, domain.HostingBuildRegistryCapability}, true, true},
		{"no configuration", []string{domain.HostingBuildTaskType, domain.HostingBuildRegistryCapability}, false, false},
		{"no build type", []string{domain.HostingBuildRegistryCapability}, true, false},
		{"shell", []string{domain.HostingBuildTaskType, domain.HostingBuildRegistryCapability, "shell"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: tc.caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Build: tc.build})
			if (err == nil) != tc.ok {
				t.Fatal("identity acceptance changed", err)
			}
		})
	}
}
