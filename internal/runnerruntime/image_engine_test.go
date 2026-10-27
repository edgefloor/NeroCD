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
	"sync/atomic"
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
					return json.Marshal(map[string]any{"Id": target.ContainerID, "Image": target.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: target.Snapshot.BindingID, imageOperationLabel: operation, imageReleaseLabel: target.Release.Hash, imagePlanLabel: target.PlanHash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"NetworkMode": "default", "PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(target.Snapshot.HostPort)}}}}})
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
			return json.Marshal(map[string]any{"Id": id, "Image": plan.Release.ImageID, "Config": map[string]any{"Labels": map[string]string{imageBindingLabel: plan.Snapshot.BindingID, imageOperationLabel: "hop_image", imageReleaseLabel: plan.Release.Hash, imagePlanLabel: plan.Hash}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]any{"NetworkMode": "default", "PortBindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(port)}}}}})
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

func unsafeImageHostChanges() map[string]func(*imageContainer) {
	return map[string]func(*imageContainer){
		"privileged":        func(c *imageContainer) { c.HostConfig.Privileged = true },
		"capability":        func(c *imageContainer) { c.HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"bind":              func(c *imageContainer) { c.HostConfig.Binds = []string{"/:/host"} },
		"host-network":      func(c *imageContainer) { c.HostConfig.NetworkMode = "host" },
		"container-network": func(c *imageContainer) { c.HostConfig.NetworkMode = "container:other" },
		"missing-network":   func(c *imageContainer) { c.HostConfig.NetworkMode = "" },
		"host-pid":          func(c *imageContainer) { c.HostConfig.PidMode = "host" },
		"host-ipc":          func(c *imageContainer) { c.HostConfig.IpcMode = "host" },
		"host-userns":       func(c *imageContainer) { c.HostConfig.UsernsMode = "host" },
		"device": func(c *imageContainer) {
			c.HostConfig.Devices = []json.RawMessage{json.RawMessage(`{"PathOnHost":"/dev/sda"}`)}
		},
		"device-request": func(c *imageContainer) {
			c.HostConfig.DeviceRequests = []json.RawMessage{json.RawMessage(`{"Count":-1}`)}
		},
		"security-option": func(c *imageContainer) { c.HostConfig.SecurityOpt = []string{"seccomp=unconfined"} },
		"volumes-from":    func(c *imageContainer) { c.HostConfig.VolumesFrom = []string{"other"} },
		"publish-all":     func(c *imageContainer) { c.HostConfig.PublishAllPorts = true },
		"extra-port":      func(c *imageContainer) { c.HostConfig.PortBindings["81/tcp"] = c.HostConfig.PortBindings["80/tcp"] },
		"extra-mount": func(c *imageContainer) {
			c.Mounts = append(c.Mounts, imageMount{Type: "bind", Destination: "/host", RW: true})
		},
	}
}

func TestImageHostPolicyGuardsOwnershipReadinessAndCleanup(t *testing.T) {
	for name, mutate := range unsafeImageHostChanges() {
		for _, path := range []string{"previous", "retained", "readiness", "cleanup"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				p := imageTestPlan(t)
				h := domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: "prior", PlanHash: p.Hash, ContainerID: strings.Repeat("c", 64)}
				c := mountedContainer(h, true)
				mutate(&c)
				e := testImageEngine(t)
				e.command = imageCommandFunc(func(_ context.Context, _ string, all []string, _ string, _ []string) ([]byte, error) {
					args := all[4:]
					switch {
					case args[0] == "info":
						return json.Marshal(p.Release.DaemonID)
					case args[0] == "image" && args[1] == "inspect":
						return json.Marshal(inspectedImage{ID: p.Release.ImageID, OS: p.Release.OS, Architecture: p.Release.Architecture})
					case args[0] == "container" && args[1] == "ls":
						return []byte(h.ContainerID), nil
					case args[0] == "container" && args[1] == "inspect":
						return json.Marshal(c)
					default:
						t.Fatalf("unsafe container reached mutation: %v", args)
						return nil, nil
					}
				})
				switch path {
				case "previous", "retained":
					p.Snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
					var err error
					if path == "previous" {
						p, err = domain.CompileRetainedHostingPlan(p.Snapshot, p.Release, &h, nil)
					} else {
						p, err = domain.CompileRetainedHostingPlan(p.Snapshot, p.Release, nil, []domain.HostingHealthy{h})
					}
					if err != nil {
						t.Fatal(err)
					}
					if result := e.Apply(t.Context(), "next", p); result.Ready || !result.Uncertain {
						t.Fatalf("unsafe predecessor accepted: %+v", result)
					}
				case "readiness":
					if err := e.readiness(t.Context(), p, h.OperationID, h.ContainerID); err == nil {
						t.Fatal("unsafe container became ready")
					}
				case "cleanup":
					cleanup, err := domain.CompileHostingCleanupPlan(h, "current")
					if err != nil {
						t.Fatal(err)
					}
					if result := e.Cleanup(t.Context(), cleanup); result.Outcome == "succeeded" || !result.Uncertain {
						t.Fatalf("unsafe cleanup accepted: %+v", result)
					}
				}
			})
		}
	}
}

func TestImageReadinessRechecksHostPolicyAfterHTTP(t *testing.T) {
	var probed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { probed.Store(true); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	p := imageTestPlan(t)
	u, _ := url.Parse(server.URL)
	p.Snapshot.HostPort, _ = strconv.Atoi(u.Port())
	h := domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: "current", PlanHash: p.Hash, ContainerID: strings.Repeat("c", 64)}
	e := testImageEngine(t)
	e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			return json.Marshal(p.Release.DaemonID)
		}
		if args[4] != "container" || args[5] != "inspect" {
			t.Fatalf("unexpected command: %v", args)
		}
		c := mountedContainer(h, true)
		c.HostConfig.Privileged = probed.Load()
		return json.Marshal(c)
	})
	if err := e.readiness(t.Context(), p, h.OperationID, h.ContainerID); err == nil || !probed.Load() {
		t.Fatalf("host-policy drift escaped readiness recheck: %v", err)
	}
}

func TestImageRestorationRechecksPreviousHostPolicyBeforeStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	p := imageTestPlan(t)
	u, _ := url.Parse(server.URL)
	p.Snapshot.HostPort, _ = strconv.Atoi(u.Port())
	p.Snapshot.Readiness.TimeoutSeconds = 1
	h := domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: "prior", PlanHash: p.Hash, ContainerID: strings.Repeat("c", 64)}
	p.PreviousHealthy = &h
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	prior := mountedContainer(h, true)
	candidate := mountedContainer(domain.HostingHealthy{Snapshot: p.Snapshot, Release: p.Release, OperationID: "current", PlanHash: p.Hash, ContainerID: strings.Repeat("d", 64)}, false)
	previousStarted := false
	e := testImageEngine(t)
	e.command = imageCommandFunc(func(_ context.Context, _ string, all []string, _ string, _ []string) ([]byte, error) {
		args := all[4:]
		if args[0] == "info" {
			return json.Marshal(p.Release.DaemonID)
		}
		if args[0] == "image" {
			return json.Marshal(inspectedImage{ID: p.Release.ImageID, OS: p.Release.OS, Architecture: p.Release.Architecture})
		}
		if args[0] != "container" {
			t.Fatalf("unexpected command: %v", args)
		}
		id := args[len(args)-1]
		switch args[1] {
		case "ls":
			return []byte(h.ContainerID), nil
		case "inspect":
			if args[2] == prior.ID {
				return json.Marshal(prior)
			}
			return json.Marshal(candidate)
		case "create":
			prior.HostConfig.Privileged = true
			return []byte(candidate.ID), nil
		case "stop":
			if id == prior.ID {
				prior.State.Running = false
			} else {
				candidate.State.Running = false
			}
		case "start":
			if id == prior.ID {
				previousStarted = true
			} else {
				candidate.State.Running = true
			}
		case "rm":
		default:
			t.Fatalf("unexpected command: %v", args)
		}
		return nil, nil
	})
	result := e.Apply(t.Context(), "current", p)
	if previousStarted || !result.Uncertain || result.Restored || result.Ready {
		t.Fatalf("unsafe previous container restarted: %+v started=%v", result, previousStarted)
	}
}
