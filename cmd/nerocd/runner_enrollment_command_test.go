//go:build unix

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This compiles the CLI and consumes the actual TypeScript helper output. It
// proves command admission only: no task or Docker operation is offered.
func TestRunnerWebEnrollmentCommandAdmission(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("Bun is required to execute the frontend command contract")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
	t.Setenv("NEROCD_RUNNER_SECRET_ROOT", "")
	for _, mode := range []string{"matching", "missing runtime flag", "missing observation flag", "missing logs flag", "logs HTTP", "logs shell", "logs unknown", "missing enrollment file", "persisted mismatch"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			enrollment, credential := filepath.Join(directory, "enrollment"), filepath.Join(directory, "credential")
			work, journal := filepath.Join(directory, "work"), filepath.Join(directory, "journal")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(enrollment, []byte("nce_"+strings.Repeat("a", 64)), 0600); err != nil {
				t.Fatal(err)
			}
			capabilities := []string{"compose-deploy", "image_deploy", "image_release_ports_v1", "hosting_runtime_env_v1", "hosting_observation_v1", "hosting_runtime_logs_v1"}
			if strings.HasPrefix(mode, "logs ") {
				capabilities = []string{"hosting_runtime_logs_v1"}
				if mode == "logs unknown" {
					capabilities = append(capabilities, "unknown")
				}
			}
			persisted := append([]string(nil), capabilities...)
			if mode == "persisted mismatch" {
				persisted = persisted[:len(persisted)-1]
			}
			var mu sync.Mutex
			var paths []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/runner-enrollments/consume":
					_ = json.NewEncoder(w).Encode(map[string]any{"runner": map[string]any{"id": "runner_command"}})
				case "/api/v1/runners/identity":
					_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_command", "capabilities": persisted})
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "runner_command", "status": "active"})
				case "/api/v1/runners/telemetry":
					w.WriteHeader(http.StatusNoContent)
				case "/api/v1/runners/claim":
					w.WriteHeader(http.StatusUnauthorized) // Deterministic loop termination.
				case "/api/v1/runners/hosting/observation-targets":
					w.WriteHeader(http.StatusNotFound)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			// Nonparallel test. Restore only after synchronous runRunner has returned,
			// which joins its observation and runtime log collectors before unwinding.
			previous := http.DefaultTransport
			transport := previous.(*http.Transport).Clone()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			transport.TLSClientConfig.RootCAs = roots
			http.DefaultTransport = transport
			t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = previous })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			script := `import {configuredImageHostingCapabilities as capabilities, runnerEnrollmentCommand} from "./web/app/src/lib/runner-enrollment-command.ts"; const selected = process.argv[2] ? JSON.parse(process.argv[2]) : capabilities; process.stdout.write(JSON.stringify({capabilities:selected,...runnerEnrollmentCommand(process.argv[1],selected)}));`
			generate := exec.CommandContext(ctx, bun, "-e", script, server.URL)
			if strings.HasPrefix(mode, "logs ") {
				selected, err := json.Marshal(capabilities)
				if err != nil {
					t.Fatal(err)
				}
				generate.Args = append(generate.Args, string(selected))
			}
			generate.Dir = root
			output, err := generate.Output()
			if err != nil {
				t.Fatalf("frontend helper: %v", err)
			}
			var generated struct {
				Command      string
				Capabilities []string
				Issues       []string
			}
			if err := json.Unmarshal(output, &generated); err != nil {
				t.Fatal(err)
			}
			if mode == "logs unknown" {
				if generated.Command != "" || !strings.Contains(strings.Join(generated.Issues, " "), "Custom capabilities") {
					t.Fatalf("unknown grant produced a command: %s", output)
				}
				return
			}
			if generated.Command == "" || len(generated.Issues) != 0 || strings.Join(generated.Capabilities, ",") != strings.Join(capabilities, ",") {
				t.Fatalf("unexpected frontend contract: %s", output)
			}
			// Let the POSIX shell parse the helper's actual quoting, replacing only the
			// executable with an argv printer. It never starts a runner subprocess.
			tokenize := exec.CommandContext(ctx, "/bin/sh", "-c", "nerocd() { printf '%s\\0' \"$@\"; }; "+generated.Command)
			tokenize.Dir = root
			output, err = tokenize.Output()
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
			if len(args) < 2 || args[0] != "runner" {
				t.Fatalf("bad argv: %q", args)
			}
			args = args[1:]
			replacements := map[string]string{"--enrollment-file": enrollment, "--credential-file": credential, "--work-dir": work, "--journal-dir": journal}
			for i := 0; i < len(args); i++ {
				if replacement, ok := replacements[args[i]]; ok {
					args[i+1] = replacement
					i++
				}
			}
			remove := ""
			if mode == "missing runtime flag" {
				remove = "--hosting-runtime-env"
			}
			if mode == "missing logs flag" {
				remove = "--hosting-runtime-logs"
			}
			if mode == "missing observation flag" {
				remove = "--hosting-observation"
			}
			if remove != "" {
				var filtered []string
				for _, arg := range args {
					if arg != remove {
						filtered = append(filtered, arg)
					}
				}
				args = filtered
			}
			if mode == "missing enrollment file" {
				for i, arg := range args {
					if arg == "--enrollment-file" {
						args[i+1] = filepath.Join(directory, "not-created")
					}
				}
			}
			if mode == "logs HTTP" || mode == "logs shell" {
				for i, arg := range args {
					if mode == "logs HTTP" && arg == "--server" {
						args[i+1] = strings.Replace(server.URL, "https://", "http://", 1)
					}
					if mode == "logs shell" && arg == "--capabilities" {
						args[i+1] += ",shell"
					}
				}
			}
			err = runRunner(args)
			if err == nil {
				t.Fatal("fixture must terminate with its intentional admission/claim error")
			}
			mu.Lock()
			observed := append([]string(nil), paths...)
			mu.Unlock()
			reached := strings.Join(observed, ",")
			if mode == "matching" {
				if !strings.Contains(reached, "/api/v1/runners/claim") {
					t.Fatalf("matching command did not reach claim admission: %v paths=%s", err, reached)
				}
				if !strings.Contains(err.Error(), "401") {
					t.Fatalf("expected terminal fixture claim rejection, got %v", err)
				}
				info, statErr := os.Stat(credential)
				if statErr != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("durable credential permissions: %v", statErr)
				}
				if _, statErr := os.Stat(enrollment); !os.IsNotExist(statErr) {
					t.Fatalf("consumed enrollment remains: %v", statErr)
				}
			} else if mode == "persisted mismatch" {
				if !strings.Contains(err.Error(), "runtime log capability") || strings.Contains(reached, "heartbeat") {
					t.Fatalf("mismatch did not fail before runtime: %v paths=%s", err, reached)
				}
			} else if mode == "logs HTTP" || mode == "logs shell" {
				if len(observed) != 0 {
					t.Fatalf("unsafe logs configuration reached API: %v paths=%s", err, reached)
				}
				if mode == "logs HTTP" && !strings.Contains(err.Error(), "invalid runtime log controller transport") {
					t.Fatalf("unexpected HTTP rejection: %v", err)
				}
				if mode == "logs shell" && !strings.Contains(err.Error(), "cannot share explicit shell") {
					t.Fatalf("unexpected shell rejection: %v", err)
				}
			} else if mode == "missing enrollment file" {
				if len(observed) != 0 || !strings.Contains(err.Error(), "enrollment") {
					t.Fatalf("missing file did not fail before enrollment: %v paths=%s", err, reached)
				}
			} else if len(observed) != 0 || !strings.Contains(err.Error(), "requires --hosting-") {
				t.Fatalf("missing flag did not fail before enrollment: %v paths=%s", err, reached)
			}
		})
	}
}
