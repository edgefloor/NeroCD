package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

type imageCommandFunc func(context.Context, string, []string, string, []string) ([]byte, error)

func (f imageCommandFunc) RunEnvironment(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	return f(ctx, name, args, dir, env)
}
func imageTestRelease() domain.HostingRelease {
	r := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon-image-test"}
	r.Hash = domain.HostingDocumentHash(r)
	return r
}
func imageTestPlan(t *testing.T) domain.HostingApplyPlan {
	t.Helper()
	p, err := domain.CompileHostingPlan(domain.HostingSnapshot{ResourceID: "res_image", ResourceGeneration: 1, ImageReference: "nginx:1.27", BindingID: "binding_image", RuntimeNamespace: "ncd_" + strings.Repeat("a", 36), ContainerPort: 80, HostPort: 18080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 2}, PolicyVersion: domain.HostingPolicyVersion}, imageTestRelease(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func testImageEngine(t *testing.T) *imageEngine {
	t.Helper()
	e, err := newImageEngine("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return e
}

func TestImagePreparationPinsPullDigestAndIsolatesRegistryConfig(t *testing.T) {
	e := testImageEngine(t)
	pulls, inspections := 0, 0
	e.command = imageCommandFunc(func(_ context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if name != "docker" || len(args) < 5 || args[0] != "--host" || args[1] != defaultImageDockerEndpoint || args[2] != "--config" || args[3] != e.configDir || dir != e.configDir {
			t.Fatalf("unpinned invocation: %s %v dir=%s", name, args, dir)
		}
		config, err := os.ReadFile(filepath.Join(e.configDir, "config.json"))
		if err != nil || strings.TrimSpace(string(config)) != "{}" {
			t.Fatalf("credential config not empty: %v", err)
		}
		info, err := os.Stat(e.configDir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("unsafe config directory: %v", err)
		}
		if !strings.Contains(strings.Join(env, "\n"), "DOCKER_CONFIG="+e.configDir) {
			t.Fatal("registry isolation missing")
		}
		switch args[4] {
		case "info":
			return []byte(`"daemon-image-test"`), nil
		case "image":
			switch args[5] {
			case "pull":
				pulls++
				return []byte("Digest: sha256:" + strings.Repeat("a", 64) + "\nStatus: pulled\n"), nil
			case "inspect":
				inspections++
				if args[6] != imageTestRelease().CanonicalImage {
					t.Fatalf("moving tag inspected after pull: %v", args)
				}
				return json.Marshal(inspectedImage{ID: imageTestRelease().ImageID, RepoDigests: []string{imageTestRelease().CanonicalImage}, OS: "linux", Architecture: "amd64"})
			}
		}
		t.Fatalf("preparation attempted workload mutation: %v", args)
		return nil, nil
	})
	release, err := e.Prepare(t.Context(), imageTestPlan(t).Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if release != imageTestRelease() || pulls != 1 || inspections != 1 {
		t.Fatalf("unexpected evidence %+v pulls=%d inspect=%d", release, pulls, inspections)
	}
}

func TestImagePullFailureAndDaemonMismatchNeverMutateWorkloads(t *testing.T) {
	for _, mode := range []string{"pull-failure", "daemon-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			e := testImageEngine(t)
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if args[4] == "info" {
					if mode == "daemon-mismatch" {
						return []byte(`"other-daemon"`), nil
					}
					return []byte(`"daemon-image-test"`), nil
				}
				if args[4] == "image" && args[5] == "pull" {
					return nil, errors.New("pull rejected")
				}
				t.Fatalf("unexpected workload invocation: %v", args)
				return nil, nil
			})
			if mode == "pull-failure" {
				if _, err := e.Prepare(t.Context(), imageTestPlan(t).Snapshot); err == nil {
					t.Fatal("failed pull succeeded")
				}
			} else {
				result := e.Apply(t.Context(), "hop_image", imageTestPlan(t))
				if result.Outcome != "failed" || result.Ready {
					t.Fatalf("daemon mismatch succeeded: %+v", result)
				}
			}
		})
	}
}

func TestImageReadinessUsesOrdinaryOwnedHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/ready" || r.Header.Get("X-NeroCD-Revision") != "" || r.Header.Get("X-Deployment-Revision") != "" {
			t.Errorf("unexpected readiness contract: %s headers=%v", r.URL, r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	e := testImageEngine(t)
	plan := imageTestPlan(t)
	plan.Snapshot.HostPort = port
	id := strings.Repeat("c", 64)
	e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			return []byte(`"daemon-image-test"`), nil
		}
		if args[4] == "container" && args[5] == "inspect" {
			return json.Marshal(map[string]any{"Id": id, "Image": plan.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: plan.Snapshot.BindingID, imageOperationLabel: "hop_image", imageReleaseLabel: plan.Release.Hash, imagePlanLabel: plan.Hash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(port)}}}}})
		}
		t.Fatalf("readiness mutated Docker: %v", args)
		return nil, nil
	})
	if err = e.readiness(t.Context(), plan, "hop_image", id); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("ordinary HTTP requests=%d", requests)
	}
}
