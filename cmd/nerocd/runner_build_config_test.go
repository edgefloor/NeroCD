package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func TestRunnerBuildConfigurationStrictJSON(t *testing.T) {
	raw := `{"version":1,"source_root":"/owned/sources","engine":{"docker_binary":"/fixed/docker","socket":"/fixed/docker.sock","buildx_binary":"/fixed/buildx","buildx_sha256":"sha256:pin","planner_image":"sha256:planner","state_dir":"/owned/state","cgroup_root":"/sys/fs/cgroup/owned"},"target":{"runner_id":"runner_build","server_id":"server","destination_id":"destination"},"policy":{},"status_interval_ns":100000000,"request_timeout_ns":5000000000}`
	c, err := decodeRunnerBuildConfig([]byte(raw))
	if err != nil || c.StatusInterval != 100*time.Millisecond || c.RequestTimeout != 5*time.Second || c.Target.BindingID != "" {
		t.Fatalf("canonical config decode: %v", err)
	}
	for _, input := range []string{"null", raw + " {}", strings.Replace(raw, `"version":1`, `"version":2`, 1), strings.Replace(raw, `"policy":{}`, `"policy":null`, 1), strings.Replace(raw, `"policy":{}`, `"policy":{"identity":null}`, 1), strings.Replace(raw, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(raw, `"version":1`, `"version":1,"VERSION":1`, 1), strings.Replace(raw, `"version":1`, `"version":1,"\u0076ersion":1`, 1), strings.Replace(raw, `"socket":`, `"secret":"fixture-secret-must-not-appear","socket":`, 1), strings.Repeat(" ", 64<<10) + raw} {
		if _, err = decodeRunnerBuildConfig([]byte(input)); err == nil {
			t.Fatal("invalid configuration accepted")
		} else if strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("configuration error echoed input")
		}
	}
}

func TestRunnerBuildConfigurationRejectsUnicodeKeys(t *testing.T) {
	for name, raw := range map[string]string{
		"literal fold alias":  `{"version":1,"source_root":"/expected","ſource_root":"/different"}`,
		"escaped fold alias":  `{"version":1,"source_root":"/expected","\u017fource_root":"/different"}`,
		"nested fold alias":   `{"version":1,"engine":{"socket":"/expected","\u017focket":"/different"}}`,
		"unknown Unicode key": `{"version":1,"policy":{"未知":"value"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRunnerBuildConfig([]byte(raw)); err == nil {
				t.Fatal("non-ASCII configuration key accepted")
			}
		})
	}
}

func TestRunnerBuildConfigurationPreservesUnicodeValues(t *testing.T) {
	raw := `{"version":1,"\u0073ource_root":"/owned/日本語/ſource","engine":{"socket":"/owned/δοκιμή.sock"},"policy":{"identity":"配置"}}`
	c, err := decodeRunnerBuildConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ASCII schema keys with Unicode values rejected: %v", err)
	}
	if c.SourceRoot != "/owned/日本語/ſource" || c.Engine.Socket != "/owned/δοκιμή.sock" || c.Policy.Identity != "配置" {
		t.Fatal("Unicode configuration values changed")
	}
}

func TestRunnerBuildConfigDeniesBeforeEnrollmentOrRegistration(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, "build.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"target":{"runner_id":"runner_build"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--build-config-file", path}, {"--capabilities", domain.HostingBuildTaskType}, {"--capabilities", "shell," + domain.HostingBuildTaskType}} {
		args := append([]string{"--server", server.URL, "--token", "fixture-registration"}, extra...)
		if runRunner(args) == nil {
			t.Fatal("invalid build config admitted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid build configuration reached authentication effects")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRunnerBuildConfig(path); err == nil {
		t.Fatal("unsafe config mode accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRunnerBuildConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
	if _, err := loadRunnerBuildConfig(root); err == nil {
		t.Fatal("directory config accepted")
	}
}
func TestRunnerBuildCapabilitiesExplicitAndDisabledByDefault(t *testing.T) {
	c := &runnerruntime.BuildConfig{}
	for _, tc := range []struct {
		config           *runnerruntime.BuildConfig
		proxy, specified bool
		caps             []string
		want             []string
		fail             bool
	}{
		{nil, false, false, []string{"shell"}, []string{"shell"}, false},
		{nil, false, true, []string{domain.HostingPrivateGitCapability}, nil, true},
		{c, false, true, []string{domain.HostingBuildTaskType, domain.HostingPrivateGitCapability}, nil, true},
		{c, false, false, []string{domain.HostingPrivateGitCapability}, nil, true},
		{c, false, false, []string{"shell"}, []string{domain.HostingBuildTaskType}, false},
		{c, true, false, []string{"shell"}, []string{domain.HostingBuildTaskType, domain.RunTypeProxyApply}, false},
		{c, false, true, []string{"shell"}, nil, true},
		{nil, false, true, []string{domain.HostingBuildTaskType}, nil, true},
		{c, false, true, []string{"opaque"}, []string{"opaque", domain.HostingBuildTaskType}, false},
	} {
		got, err := runnerBuildCapabilities(tc.config, tc.proxy, tc.specified, tc.caps)
		if (err != nil) != tc.fail {
			t.Fatal("capability guard mismatch")
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(tc.want)
		if !tc.fail && string(a) != string(b) {
			t.Fatalf("capabilities=%s want%s", a, b)
		}
	}
}

func TestRunnerPrivateGitConfigurationAndCapabilities(t *testing.T) {
	c, err := decodeRunnerBuildConfig([]byte(`{"version":1,"private_git":{"trusted_loopback_http":true}}`))
	if err != nil || c.PrivateGit == nil || !c.PrivateGit.TrustedLoopbackHTTP {
		t.Fatal("private opt-in lost", err)
	}
	for _, raw := range []string{`{"version":1,"private_git":null}`, `{"version":1,"private_git":{"unknown":true}}`, `{"version":1,"private_git":{"trusted_loopback_http":"true"}}`} {
		if _, err := decodeRunnerBuildConfig([]byte(raw)); err == nil {
			t.Fatal("invalid private config accepted")
		}
	}
	for _, caps := range [][]string{nil, {domain.HostingBuildTaskType}, {domain.HostingBuildTaskType, domain.HostingPrivateGitCapability}} {
		got, err := runnerBuildCapabilities(c, false, len(caps) > 0, caps)
		if err != nil || !strings.Contains(strings.Join(got, ","), domain.HostingPrivateGitCapability) {
			t.Fatal("private capability missing", err)
		}
	}
	if _, err := runnerBuildCapabilities(c, false, true, []string{"shell"}); err == nil {
		t.Fatal("private shell enabled")
	}
}
