package runnerruntime

import (
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
	"time"

	"nerocd/internal/domain"
)

func observationContainerJSON(t *testing.T, target domain.HostingObservationTarget, mode string) []byte {
	t.Helper()
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(target.OperationID), "sha256:")[:16]
	values := map[string]any{"id": target.ContainerID, "name": "/" + target.RuntimeNamespace + "_" + suffix, "image": target.ImageID, "status": "running", "running": true, "paused": false, "restarting": false, "dead": false, "exit_code": 0, "binding": target.BindingID, "operation": target.OperationID, "release": target.ReleaseHash, "plan": target.PlanHash, "ports": map[string]any{strconv.Itoa(target.ContainerPort) + "/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(target.HostPort)}}}}
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
	for _, mode := range []string{"healthy", "http503", "redirect", "exited", "paused", "restarting", "created", "dead", "missing", "inspect-error", "list-error", "daemon-mismatch", "binding", "image", "name", "container", "ports", "probe-stopped", "probe-replaced", "http-failure", "missing-exit-field"} {
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
				if mode == "missing-exit-field" {
					var m map[string]any
					_ = json.Unmarshal(raw, &m)
					m["status"] = "exited"
					m["running"] = false
					delete(m, "exit_code")
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
