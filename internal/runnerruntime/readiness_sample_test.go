package runnerruntime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func readinessUnixFixture(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	root, err := os.MkdirTemp("", "ncd-rd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	closeServer := func() { _ = server.Close(); <-done }
	return "unix://" + socket, closeServer
}
func TestReadinessSamplerOnlyBoundedReadOnlyRequests(t *testing.T) {
	for _, mode := range []string{"valid", "alias", "unsupported", "changed", "oversized", "invalid", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			infos := 0
			endpoint, closeServer := readinessUnixFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				if r.Method != "GET" || r.URL.RawQuery != "" {
					t.Error("non-readonly request")
				}
				if mode == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat("x", readinessDockerBodyLimit+1)))
					return
				}
				if mode == "invalid" {
					_, _ = w.Write([]byte(`{"ApiVersion":12}`))
					return
				}
				if r.URL.Path == "/version" {
					maximum := "1.54"
					if mode == "unsupported" {
						maximum = "1.53"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": maximum, "MinAPIVersion": "1.44", "RawCanary": "never-in-report"})
					return
				}
				if r.URL.Path != "/v1.54/info" {
					t.Error("unselected path")
				}
				infos++
				id := "daemon_fixture"
				if mode == "changed" && infos == 2 {
					id = "replacement"
				}
				arch := runtime.GOARCH
				if mode == "alias" {
					if arch == "arm64" {
						arch = "aarch64"
					} else {
						arch = "x86_64"
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ID": id, "OSType": "linux", "Architecture": arch, "RawCanary": "never-in-report"})
			}))
			if mode == "unreachable" {
				closeServer()
			} else {
				defer closeServer()
			}
			sampler, err := newReadinessSampler(endpoint, t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer sampler.Close()
			result := sampler.Sample(t.Context())
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "never-in-report") {
				t.Fatal("raw output retained")
			}
			switch mode {
			case "valid", "alias":
				if result.Docker != "available" || result.DaemonArchitecture != readinessArchitecture(runtime.GOARCH) || result.DockerAPIVersion != "1.54" {
					t.Fatal(result)
				}
			case "unsupported":
				if result.Docker != "unsupported_api" {
					t.Fatal(result)
				}
			case "changed":
				if result.Error != "daemon_changed" || result.DaemonID != "" {
					t.Fatal(result)
				}
			case "invalid", "oversized":
				if result.Docker != "invalid_response" {
					t.Fatal(result)
				}
			case "unreachable":
				if result.Docker != "unreachable" {
					t.Fatal(result)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if mode == "valid" && (len(paths) != 3 || paths[0] != "/version" || paths[1] != "/v1.54/info" || paths[2] != "/v1.54/info") {
				t.Fatal(paths)
			}
		})
	}
	for _, endpoint := range []string{"tcp://127.0.0.1:2375", "unix://remote/a", "unix:///a?x=1", "unix:///a/../b"} {
		if s, err := newReadinessSampler(endpoint, "/work", "/journal"); err == nil {
			s.Close()
			t.Fatal(endpoint)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sampler, err := newReadinessSampler("unix:///missing", "/missing", "/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer sampler.Close()
	if report := sampler.Sample(ctx); report.Error != "timeout" {
		t.Fatal(report)
	}
}
