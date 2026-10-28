package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func TestRunnerBuildCLIPropagatesComposeIdentityConfiguration(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
	for _, compose := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary_build", true: "compose_build"}[compose], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "build.json")
			raw := `{"version":1,"compose":` + map[bool]string{false: "false", true: "true"}[compose] + `,"target":{"runner_id":"runner_build"},"policy":{"git_compose":` + map[bool]string{false: "false", true: "true"}[compose] + `}}`
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var requests []string
			var capabilities []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.URL.Path)
				switch r.URL.Path {
				case "/api/v1/runners/register":
					var input runnerRegisterRequest
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.ID != "runner_build" {
						t.Error("configured CLI registration changed")
					}
					capabilities = input.Capabilities
					for _, needed := range []string{domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability} {
						found := false
						for _, capability := range capabilities {
							found = found || capability == needed
						}
						if found != compose {
							t.Error("CLI did not advertise exact configured Compose capability", needed)
						}
					}

					foundDockerfile := false
					for _, capability := range capabilities {
						foundDockerfile = foundDockerfile || capability == domain.HostingDockerfileArgsCapability
					}
					if !foundDockerfile {
						t.Error("configured CLI did not advertise Dockerfile arguments")
					}
					_ = json.NewEncoder(w).Encode(runnerRegisterResponse{Token: "dedicated-fixture"})
				case "/api/v1/runners/identity":
					if r.Header.Get("Authorization") != "Bearer dedicated-fixture" {
						t.Error("identity did not use issued runner credential")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_build", "capabilities": capabilities})
				default:
					t.Error("unexpected startup effect", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			// Only the engine prerequisite validation is bypassed. Strict config decoding,
			// real command flags, capability registration and HTTP identity are exercised.
			// Runtime validation must still reject this deliberately incomplete engine.
			loader := func(got string) (*runnerruntime.BuildConfig, error) {
				if got != path {
					t.Fatal("CLI lost explicit configuration file")
				}
				body, err := os.ReadFile(got)
				if err != nil {
					return nil, err
				}
				return decodeRunnerBuildConfig(body)
			}
			err := runRunnerWithBuildConfigLoader([]string{"--server", server.URL, "--token", "registration-fixture", "--build-config-file", path, "--work-dir", root, "--journal-dir", filepath.Join(root, "journal"), "--once"}, loader)
			if err == nil || !strings.Contains(err.Error(), "invalid explicit build configuration") {
				t.Fatalf("configured CLI did not pass initial identity before runtime prerequisite validation: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(requests, []string{"/api/v1/runners/register", "/api/v1/runners/identity"}) {
				t.Fatalf("unexpected CLI boundary requests: %v", requests)
			}
		})
	}
}
