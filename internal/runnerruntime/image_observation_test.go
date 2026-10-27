package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"nerocd/internal/domain"
)

func observationContainerJSON(t *testing.T, target domain.HostingObservationTarget, mode string) []byte {
	t.Helper()
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(target.OperationID), "sha256:")[:16]
	values := map[string]any{"mounts": []imageMount{}, "host_policy": imageHostConfig{NetworkMode: "default"}, "id": target.ContainerID, "name": "/" + target.RuntimeNamespace + "_" + suffix, "image": target.ImageID, "status": "running", "running": true, "paused": false, "restarting": false, "dead": false, "exit_code": 0, "binding": target.BindingID, "operation": target.OperationID, "release": target.ReleaseHash, "plan": target.PlanHash, "ports": map[string]any{strconv.Itoa(target.ContainerPort) + "/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(target.HostPort)}}}}
	switch mode {
	case "exited":
		values["status"], values["running"], values["exit_code"] = "exited", false, 23
	case "created":
		values["status"], values["running"] = "created", false
	case "paused":
		values["paused"] = true
	case "restarting":
		values["status"], values["restarting"] = "restarting", true
	case "dead":
		values["status"], values["dead"], values["running"] = "dead", true, false
	case "binding":
		values["binding"] = "other"
	case "image":
		values["image"] = "sha256:" + strings.Repeat("f", 64)
	case "name":
		values["name"] = "/other"
	case "container":
		values["id"] = strings.Repeat("f", 64)
	case "ports":
		values["ports"] = map[string]any{"80/tcp": []map[string]string{{"HostIp": "0.0.0.0", "HostPort": strconv.Itoa(target.HostPort)}}}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestImageObservationClosedEvidenceAndIdentity(t *testing.T) {
	for _, mode := range []string{"healthy", "http503", "redirect", "exited", "paused", "restarting", "created", "dead", "missing", "inspect-error", "list-error", "daemon-mismatch", "binding", "image", "name", "container", "ports", "probe-stopped", "probe-replaced", "privileged", "capability", "bind", "host-network", "probe-privileged", "http-failure", "missing-exit-field", "missing-mount-field", "null-mount-field"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
			defer other.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "http503":
					w.WriteHeader(503)
				case "redirect":
					http.Redirect(w, r, other.URL, http.StatusFound)
				default:
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			target := observationTargetFixture()
			u, _ := url.Parse(server.URL)
			target.HostPort, _ = strconv.Atoi(u.Port())
			if mode == "http-failure" {
				server.Close()
			}
			e := testImageEngine(t)
			inspections, listings := 0, 0
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if !observationCommandAllowed(args[4:]) {
					t.Error("sampler used a forbidden Docker command")
				}
				if args[4] == "info" {
					if mode == "daemon-mismatch" {
						return []byte(`"other"`), nil
					}
					return []byte(`"daemon-image-test"`), nil
				}
				if args[5] == "ls" {
					listings++
					if mode == "list-error" {
						return nil, errors.New("private-diagnostic")
					}
					if mode == "missing" {
						return nil, nil
					}
					return []byte(target.ContainerID), nil
				}
				inspections++
				if mode == "missing" || mode == "inspect-error" || mode == "list-error" {
					return nil, errors.New("private-diagnostic")
				}
				inspectMode := mode
				if inspections > 1 && mode == "probe-stopped" {
					inspectMode = "exited"
				}
				if inspections > 1 && mode == "probe-replaced" {
					inspectMode = "container"
				}
				raw := observationContainerJSON(t, target, inspectMode)
				policy := imageHostConfig{NetworkMode: "default"}
				switch mode {
				case "privileged":
					policy.Privileged = true
				case "capability":
					policy.CapAdd = []string{"SYS_ADMIN"}
				case "bind":
					policy.Binds = []string{"/:/host"}
				case "host-network":
					policy.NetworkMode = "host"
				case "probe-privileged":
					policy.Privileged = inspections > 1
				}
				var projection map[string]any
				_ = json.Unmarshal(raw, &projection)
				projection["host_policy"] = policy
				raw, _ = json.Marshal(projection)
				if mode == "missing-exit-field" {
					var m map[string]any
					_ = json.Unmarshal(raw, &m)
					m["status"] = "exited"
					m["running"] = false
					delete(m, "exit_code")
					raw, _ = json.Marshal(m)
				}
				if mode == "missing-mount-field" || mode == "null-mount-field" {
					var m map[string]any
					_ = json.Unmarshal(raw, &m)
					if mode == "missing-mount-field" {
						delete(m, "mounts")
					} else {
						m["mounts"] = nil
					}
					raw, _ = json.Marshal(m)
				}
				return raw, nil
			})
			result := (&imageObservation{e}).Sample(t.Context(), target)
			if err := result.Validate(); err != nil {
				t.Fatalf("invalid closed evidence: %v", err)
			}
			if strings.Contains(fmt.Sprint(result), "private-diagnostic") {
				t.Fatal("raw diagnostics reached evidence")
			}
			switch mode {
			case "healthy", "redirect":
				if result.ContainerState != "running" || result.Readiness != "ready" || inspections != 2 {
					t.Fatal("exact running proof missing")
				}
			case "http-failure":
				if result.Readiness != "unknown" || result.CollectionError != "readiness_timeout" {
					t.Fatal("request failure asserted readiness")
				}
			case "http503":
				if result.Readiness != "not_ready" {
					t.Fatal("explicit HTTP status not recorded")
				}
			case "exited":
				if result.ContainerState != "exited" || result.ExitCode == nil || *result.ExitCode != 23 {
					t.Fatal("verified exit lost")
				}
			case "missing":
				if result.ContainerState != "missing" || listings != 1 {
					t.Fatal("missing requires bounded exact listing")
				}
			default:
				if result.ContainerState != "unknown" || result.Readiness != "unknown" || result.ExitCode != nil {
					t.Fatalf("unverified state asserted: %+v", result)
				}
			}
			if redirected.Load() != 0 {
				t.Fatal("readiness followed a redirect")
			}
		})
	}
}
func TestObservationCommandAllowlistAndProjection(t *testing.T) {
	for _, args := range [][]string{{"container", "start", strings.Repeat("a", 64)}, {"container", "inspect", strings.Repeat("a", 64)}, {"container", "ls", "--all"}, {"exec", "container", "env"}, {"logs", "container"}, {"stats", "container"}, {"pull", "image"}} {
		if observationCommandAllowed(args) {
			t.Fatal("mutating or overbroad command allowed")
		}
	}
	if strings.Contains(observationProjection, ".Config.Env") || strings.Contains(observationProjection, "json .Config") || strings.Contains(observationProjection, "json .State}}") {
		t.Fatal("projection exposes uncontrolled Docker metadata")
	}
}

func TestObservationCommandCancellationAndOutputBounds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process group fixture")
	}
	for _, mode := range []string{"cancelled", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf private-command-diagnostic >&2\nwhile :; do sleep 1; done\n"
			if mode == "overflow" {
				script = "#!/bin/sh\nhead -c 9000 /dev/zero\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			started := time.Now()
			output, err := (observationCommand{}).RunEnvironment(ctx, "docker", []string{"--host", "unix:///var/run/docker.sock", "--config", dir, "info", "--format", "{{json .ID}}"}, dir, nil)
			if err == nil || len(output) != 0 || strings.Contains(fmt.Sprint(err), "private-command-diagnostic") || time.Since(started) > 500*time.Millisecond {
				t.Fatal("command exceeded bounds or exposed diagnostics")
			}
		})
	}
}

// Render the real projection so an omitted Docker field cannot be hidden by a
// fake response that already contains that field.
func projectedObservationJSON(t *testing.T, target domain.HostingObservationTarget, c imageContainer) []byte {
	t.Helper()
	input := map[string]any{
		"Id": c.ID, "Name": "/" + target.RuntimeNamespace + "_" + strings.TrimPrefix(domain.HostingDocumentHash(target.OperationID), "sha256:")[:16],
		"Image": c.Image, "Config": c.Config, "HostConfig": c.HostConfig, "Mounts": c.Mounts,
		"State": map[string]any{"Status": "running", "Running": true, "Paused": false, "Restarting": false, "Dead": false, "ExitCode": 0},
	}
	// Docker templates receive JSON maps, and omit HostConfig.Mounts when empty.
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if mounts, ok := input["HostConfig"].(map[string]any)["Mounts"].([]any); ok {
		for _, mount := range mounts {
			configured := mount.(map[string]any)
			if configured["ReadOnly"] == false {
				delete(configured, "ReadOnly")
			}
		}
	}
	if mounts, ok := input["Mounts"].([]any); ok {
		for _, mount := range mounts {
			actual := mount.(map[string]any)
			if actual["Type"] == "bind" {
				delete(actual, "Name")
				delete(actual, "Driver")
			}
		}
	}
	if len(c.HostConfig.Mounts) == 0 {
		delete(input["HostConfig"].(map[string]any), "Mounts")
	}
	projection, err := template.New("inspect").Funcs(template.FuncMap{"json": func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }}).Parse(observationProjection)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := projection.Execute(&output, input); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestImageObservationEnforcesMountAndNamespacePolicy(t *testing.T) {
	for _, mode := range []string{"no-storage", "stateless-bind", "stateless-foreign-volume", "managed-volume", "managed-actual-only", "private-default", "mount-bind", "foreign-volume", "unauthorized-managed-volume", "configured-bind", "configured-foreign-volume", "actual-bind", "actual-foreign-volume", "missing-mount", "extra-mount", "read-only", "wrong-path", "host-uts", "host-cgroupns"} {
		for _, phase := range []string{"before-probe", "after-probe"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				var probes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { probes.Add(1); w.WriteHeader(http.StatusNoContent) }))
				defer server.Close()
				target := observationTargetFixture()
				u, _ := url.Parse(server.URL)
				target.HostPort, _ = strconv.Atoi(u.Port())
				storage := domain.HostingObservationStorage{VolumeName: "ncdv_" + strings.Repeat("a", 32), MountPath: "/data"}
				if mode != "no-storage" && mode != "stateless-bind" && mode != "stateless-foreign-volume" && mode != "unauthorized-managed-volume" {
					target.Storage = &storage
				}
				h := domain.HostingHealthy{ContainerID: target.ContainerID, OperationID: target.OperationID, PlanHash: target.PlanHash,
					Release:  domain.HostingRelease{ImageID: target.ImageID, Hash: target.ReleaseHash},
					Snapshot: domain.HostingSnapshot{BindingID: target.BindingID, ContainerPort: target.ContainerPort, HostPort: target.HostPort}}
				safe := mountedContainer(h, true)
				if target.Storage != nil {
					safe.Mounts = []imageMount{{Type: "volume", Name: storage.VolumeName, Driver: "local", Destination: storage.MountPath, RW: true}}
					safe.HostConfig.Mounts = []imageConfiguredMount{{Type: "volume", Source: storage.VolumeName, Target: storage.MountPath}}
				}
				e := testImageEngine(t)
				inspections := 0
				e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
					if !observationCommandAllowed(args[4:]) {
						t.Fatalf("forbidden command: %v", args)
					}
					if args[4] == "info" {
						return json.Marshal(target.DaemonID)
					}
					inspections++
					c := safe
					c.Mounts = append([]imageMount(nil), safe.Mounts...)
					c.HostConfig.Mounts = append([]imageConfiguredMount(nil), safe.HostConfig.Mounts...)
					if phase == "before-probe" || inspections > 1 {
						switch mode {
						case "stateless-bind":
							c.HostConfig.Mounts = []imageConfiguredMount{{Type: "bind", Source: "/", Target: "/host"}}
							c.Mounts = []imageMount{{Type: "bind", Destination: "/host", RW: true}}
						case "stateless-foreign-volume":
							c.HostConfig.Mounts = []imageConfiguredMount{{Type: "volume", Source: "foreign", Target: "/data"}}
							c.Mounts = []imageMount{{Type: "volume", Name: "foreign", Driver: "local", Destination: "/data", RW: true}}
						case "managed-actual-only":
							c.HostConfig.Mounts = nil
						case "private-default":
							c.HostConfig.CgroupnsMode = "private"
						case "mount-bind":
							c.HostConfig.Mounts[0] = imageConfiguredMount{Type: "bind", Source: "/", Target: "/host"}
							c.Mounts[0] = imageMount{Type: "bind", Destination: "/host", RW: true}
						case "foreign-volume":
							c.HostConfig.Mounts[0].Source = "foreign"
							c.Mounts[0].Name = "foreign"
						case "unauthorized-managed-volume":
							c.HostConfig.Mounts = []imageConfiguredMount{{Type: "volume", Source: storage.VolumeName, Target: storage.MountPath}}
							c.Mounts = []imageMount{{Type: "volume", Name: storage.VolumeName, Driver: "local", Destination: storage.MountPath, RW: true}}
						case "configured-bind":
							c.HostConfig.Mounts[0] = imageConfiguredMount{Type: "bind", Source: "/", Target: storage.MountPath}
						case "configured-foreign-volume":
							c.HostConfig.Mounts[0].Source = "foreign"
						case "actual-bind":
							c.Mounts[0].Type = "bind"
						case "actual-foreign-volume":
							c.Mounts[0].Name = "foreign"
						case "missing-mount":
							c.Mounts = nil
						case "extra-mount":
							c.Mounts = append(c.Mounts, imageMount{Type: "volume", Name: "foreign", Destination: "/extra", RW: true})
						case "read-only":
							c.Mounts[0].RW = false
						case "wrong-path":
							c.Mounts[0].Destination = "/wrong"
						case "host-uts":
							c.HostConfig.UTSMode = "host"
						case "host-cgroupns":
							c.HostConfig.CgroupnsMode = "host"
						}
					}
					return projectedObservationJSON(t, target, c), nil
				})
				got := (&imageObservation{e}).Sample(t.Context(), target)
				valid := mode == "no-storage" || mode == "managed-volume" || mode == "managed-actual-only" || mode == "private-default"
				if valid {
					if got.Readiness != "ready" || got.ContainerState != "running" || inspections != 2 || probes.Load() != 1 {
						t.Fatalf("legitimate workload rejected: %+v, inspections=%d probes=%d", got, inspections, probes.Load())
					}
				} else {
					if got.CollectionError != "target_mismatch" || got.ContainerState != "unknown" || got.Readiness != "unknown" {
						t.Fatalf("unsafe workload accepted: %+v", got)
					}
					if phase == "before-probe" && probes.Load() != 0 {
						t.Fatal("unsafe workload reached readiness probe")
					}
					if phase == "after-probe" && probes.Load() != 1 {
						t.Fatal("fixture did not reach readiness recheck")
					}
				}
			})
		}
	}
}
