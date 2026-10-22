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

func TestImageCleanupPinsTargetAndRetainsAmbiguousEffects(t *testing.T) {
	for _, mode := range []string{"success", "foreign-label", "daemon-mismatch", "remove-response-lost", "already-absent"} {
		t.Run(mode, func(t *testing.T) {
			e := testImageEngine(t)
			base := imageTestPlan(t)
			target := domain.HostingHealthy{Snapshot: base.Snapshot, OperationID: "hop-retained", ContainerID: strings.Repeat("c", 64), Release: base.Release, PlanHash: base.Hash}
			plan, err := domain.CompileHostingCleanupPlan(target, "hop-current")
			if err != nil {
				t.Fatal(err)
			}
			removed := mode == "already-absent"
			stops, removals := 0, 0
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if args[4] == "info" {
					if mode == "daemon-mismatch" {
						return []byte(`"foreign-daemon"`), nil
					}
					return []byte(`"daemon-image-test"`), nil
				}
				if args[4] != "container" {
					t.Fatalf("unexpected command: %v", args)
				}
				switch args[5] {
				case "ls":
					if args[len(args)-1] != "id="+target.ContainerID {
						t.Fatalf("unbounded listing: %v", args)
					}
					if removed {
						return nil, nil
					}
					return []byte(target.ContainerID), nil
				case "inspect":
					if args[6] != target.ContainerID {
						t.Fatalf("wrong inspect target: %v", args)
					}
					operation := target.OperationID
					if mode == "foreign-label" {
						operation = "foreign-operation"
					}
					return json.Marshal(map[string]any{"Id": target.ContainerID, "Image": target.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: target.Snapshot.BindingID, imageOperationLabel: operation, imageReleaseLabel: target.Release.Hash, imagePlanLabel: target.PlanHash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(target.Snapshot.HostPort)}}}}})
				case "stop", "rm":
					if args[len(args)-1] != target.ContainerID {
						t.Fatalf("wrong mutation target: %v", args)
					}
					if args[5] == "stop" {
						stops++
						return nil, nil
					}
					removals++
					removed = true
					if mode == "remove-response-lost" {
						return nil, errors.New("response lost")
					}
					return nil, nil
				}
				t.Fatalf("unexpected command: %v", args)
				return nil, nil
			})
			result := e.Cleanup(t.Context(), plan)
			success := mode == "success" || mode == "already-absent"
			if (result.Outcome == "succeeded") != success || result.Uncertain == success {
				t.Fatalf("cleanup evidence: %+v", result)
			}
			if (mode == "foreign-label" || mode == "daemon-mismatch" || mode == "already-absent") && (stops != 0 || removals != 0) {
				t.Fatalf("unexpected mutations stop=%d rm=%d", stops, removals)
			}
			if mode == "success" && (stops != 1 || removals != 1) {
				t.Fatalf("missing exact cleanup stop=%d rm=%d", stops, removals)
			}
		})
	}
}

func TestImageRetentionRequiresVersionedProtocol(t *testing.T) {
	legacy := imageTestPlan(t)
	legacy.RetainPrevious = true
	legacy.Hash = ""
	legacy.Hash = domain.HostingDocumentHash(legacy)
	if verifyImagePlan(legacy) == nil {
		t.Fatal("legacy protocol ignored retention field")
	}
	snapshot := imageTestPlan(t).Snapshot
	snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
	modern, err := domain.CompileRetainedHostingPlan(snapshot, imageTestRelease(), nil, nil)
	if err != nil || verifyImagePlan(modern) != nil {
		t.Fatalf("modern protocol rejected: %v", err)
	}
	snapshot.HostPort = 0
	if _, err = domain.CompileRetainedHostingPlan(snapshot, imageTestRelease(), nil, nil); err == nil {
		t.Fatal("unresolved port reached execution")
	}
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
