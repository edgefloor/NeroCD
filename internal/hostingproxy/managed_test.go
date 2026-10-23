package hostingproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedPreparationPinsOnlyConfiguredUnixDaemonBeforeEffects(t *testing.T) {
	for _, mode := range []string{"valid", "strict", "strict-empty", "changed", "nonlinux", "platform", "failed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			directory, err := os.MkdirTemp("", "proxy-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(directory); err != nil {
					t.Error(err)
				}
			})
			socket := filepath.Join(directory, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/info" || r.Method != "GET" {
					t.Error("preparation attempted a Docker mutation")
				}
				info := map[string]string{"ID": "daemon", "OSType": "linux", "Architecture": "aarch64"}
				switch mode {
				case "nonlinux":
					info["OSType"] = "windows"
				case "platform":
					info["Architecture"] = "unsupported"
				case "failed":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if err := json.NewEncoder(w).Encode(info); err != nil {
					t.Error(err)
				}
			})}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				if err := server.Close(); err != nil {
					t.Error(err)
				}
				if err := <-done; !errors.Is(err, http.ErrServerClosed) {
					t.Error(err)
				}
			})
			settings := testSettings()
			settings.DockerSocket = socket
			engine := &Engine{settings: settings}
			identity := testIdentity()
			if mode == "valid" {
				identity.DaemonID = ""
			}
			if mode == "changed" {
				identity.DaemonID = "foreign"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			var plan Plan
			var daemon string
			if mode == "strict" || mode == "strict-empty" {
				ids := []string{"one"}
				if mode == "strict-empty" {
					ids = nil
				}
				plan, daemon, err = engine.PrepareManagedVerifiedSwitch(ctx, identity, 2, 1, []Route{{ID: "one", Hostname: "app.example.test", PathPrefix: "/", HostPort: 19001}}, ids)
			} else {
				plan, daemon, err = engine.PrepareManaged(ctx, identity, 1, 0, nil)
			}
			if mode == "valid" || mode == "strict" {
				if err != nil || daemon != "daemon" || len(plan.Hash()) != 64 || calls.Load() != 1 {
					t.Fatalf("plan=%s daemon=%s calls=%d err=%v", plan.Hash(), daemon, calls.Load(), err)
				}
			} else if err == nil || daemon != "" {
				t.Fatalf("invalid daemon accepted: %s %v", daemon, err)
			}
			if mode == "strict" && plan.verification == nil {
				t.Fatal("strict preparation downgraded to legacy")
			}
			if mode == "strict-empty" && calls.Load() != 0 {
				t.Fatal("invalid strict selection probed daemon")
			}
			if mode == "changed" && !strings.Contains(err.Error(), "identity changed") {
				t.Fatal(err)
			}
		})
	}
}
