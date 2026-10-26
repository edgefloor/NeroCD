package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeIdentityRemoteStorageBackupCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remote     bool
		capability bool
		task       bool
		duplicate  bool
		wantError  bool
	}{
		{name: "local archive"},
		{name: "remote archive", remote: true, capability: true, task: true},
		{name: "duplicate remote capabilities", remote: true, capability: true, task: true, duplicate: true},
		{name: "missing remote capability", remote: true, task: true, wantError: true},
		{name: "missing remote task", remote: true, capability: true, wantError: true},
		{name: "missing remote opt-in", capability: true, task: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := []string{domain.HostingOwnedStorageCapability, domain.HostingStorageArchiveCapability, domain.RunTypeStorageArchive}
			if tc.capability {
				caps = append(caps, domain.HostingStorageBackupRemoteCapability)
			}
			if tc.task {
				caps = append(caps, domain.RunTypeStorageBackupTransfer)
			}
			if tc.duplicate {
				caps = append(caps, caps...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("unexpected identity request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_storage_identity", "capabilities": caps}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			config := Config{Server: server.URL, Token: "fixture", RunnerID: "runner_storage_identity", StorageArchive: &StorageArchiveExecutionConfig{RemoteBackups: tc.remote, TrustedLoopbackHTTP: true}}
			if err := validateRuntimeIdentity(t.Context(), config); (err != nil) != tc.wantError {
				t.Fatalf("runtime identity error = %v, want error = %v", err, tc.wantError)
			}
		})
	}
}

func runtimeEnvironmentPlan(t *testing.T) domain.HostingApplyPlan {
	t.Helper()
	p := imageTestPlan(t)
	p.Snapshot.PolicyVersion = domain.HostingRuntimePolicyVersion
	p.Snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}
	p, err := domain.CompileRetainedHostingPlan(p.Snapshot, p.Release, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func runtimeEnvironmentMaterial(t *testing.T, p domain.HostingApplyPlan, until time.Time) *hostingruntimeprotocol.Environment {
	t.Helper()
	e, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: p.Hash, RuntimeConfiguration: p.Snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("a", 32), RequestHash: "sha256:" + strings.Repeat("b", 64), Purpose: hostingruntimeprotocol.Purpose, UseUntil: until}, []hostingruntimeprotocol.Entry{{Name: "APP_SECRET", Value: []byte("protected-fixture")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func TestRuntimeEnvironmentApplyRequiresMaterialAndOriginalWindow(t *testing.T) {
	for _, mode := range []string{"missing", "expired", "local-expired", "closed", "mismatched", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p := runtimeEnvironmentPlan(t)
			e := testImageEngine(t)
			mutations := 0
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if args[4] == "info" {
					return []byte(`"daemon-image-test"`), nil
				}
				if args[4] == "image" {
					return json.Marshal(inspectedImage{ID: p.Release.ImageID, OS: "linux", Architecture: "amd64"})
				}
				if args[4] == "container" && args[5] == "ls" {
					return nil, nil
				}
				mutations++
				return nil, errors.New("unexpected mutation")
			})
			until := time.Now().Add(time.Minute)
			local := until
			if mode == "expired" {
				until = time.Now().Add(-time.Second)
			}
			if mode == "local-expired" {
				local = time.Now().Add(-time.Second)
			}
			material := runtimeEnvironmentMaterial(t, p, until)
			if mode == "closed" {
				material.Close()
			}
			if mode == "mismatched" {
				p.Hash = "sha256:" + strings.Repeat("f", 64)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			var result domain.HostingResult
			if mode == "missing" {
				result = e.Apply(ctx, "hop", p)
			} else {
				result = e.ApplyWithRuntimeEnvironment(ctx, "hop", p, material, local)
			}
			if result.Outcome != "failed" || mutations != 0 {
				t.Fatal("invalid authority reached mutation")
			}
		})
	}
}
func TestRuntimeEnvironmentDeliveryRetriesRetainIdentityAndDeadline(t *testing.T) {
	for _, mode := range []string{"response-loss", "always-fails", "authority-rejected", "expired-local"} {
		t.Run(mode, func(t *testing.T) {
			p := runtimeEnvironmentPlan(t)
			request := domain.HostingRuntimeEnvironmentRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: p.Hash, RuntimeConfiguration: p.Snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("c", 32)}
			count := atomic.Int32{}
			originalUntil := time.Now().Add(20 * time.Second)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := count.Add(1)
				var in domain.HostingRuntimeEnvironmentRequest
				if json.NewDecoder(r.Body).Decode(&in) != nil || in != request {
					t.Error("retry identity changed")
				}
				if mode == "always-fails" {
					w.WriteHeader(503)
					return
				}
				if mode == "authority-rejected" {
					w.WriteHeader(409)
					return
				}
				if n == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				e, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: in.Authority, PlanHash: in.PlanHash, RuntimeConfiguration: in.RuntimeConfiguration, DeliveryID: in.DeliveryID, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash("runner", in), Purpose: hostingruntimeprotocol.Purpose, UseUntil: originalUntil}, []hostingruntimeprotocol.Entry{})
				if err != nil {
					t.Error(err)
					return
				}
				defer e.Close()
				w.Header().Set("Cache-Control", "no-store")
				if err = hostingruntimeprotocol.WriteEnvironment(w, e); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			lease := domain.RunLease{ExpiresAt: time.Now().Add(time.Minute)}
			s := newAttemptSupervisor(lease)
			defer s.Close()
			deadline := runtimeDeliveryDeadline(s)
			if mode == "expired-local" {
				deadline = time.Now().Add(-time.Second)
			}
			env, err := fetchHostingRuntimeEnvironment(s, runnerclient.New(server.URL, "token"), "runner", request, RuntimeEnvironmentConfig{TrustedLoopbackHTTP: true}, deadline)
			switch mode {
			case "response-loss":
				if err != nil {
					t.Fatal(err)
				}
				defer env.Close()
				m, _ := env.Metadata()
				if count.Load() != 2 || !m.UseUntil.Equal(originalUntil) {
					t.Fatal("retry changed identity/window")
				}
			case "always-fails":
				if err == nil || count.Load() != 3 {
					t.Fatal("retry budget changed")
				}
			case "authority-rejected":
				if err == nil || count.Load() != 1 {
					t.Fatal("authority rejection retried")
				}
			case "expired-local":
				if err == nil || count.Load() != 0 {
					t.Fatal("expired window sent request")
				}
			}
		})
	}
}
func TestRuntimeEnvironmentDeliveryIDs(t *testing.T) {
	a, err := newRuntimeDeliveryID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newRuntimeDeliveryID()
	if err != nil || a == b || !domain.ValidHostingRuntimeDeliveryID(a) || !domain.ValidHostingRuntimeDeliveryID(b) {
		t.Fatal("invalid delivery identities")
	}
}

func TestRuntimeEnvironmentMissingPreviousContainerRemainsUncertain(t *testing.T) {
	p := runtimeEnvironmentPlan(t)
	previous := &domain.HostingHealthy{OperationID: "old", ContainerID: strings.Repeat("e", 64), Snapshot: p.Snapshot, Release: p.Release, PlanHash: p.Hash}
	p.Snapshot.HostPort++
	var err error
	p, err = domain.CompileRetainedHostingPlan(p.Snapshot, p.Release, previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := testImageEngine(t)
	mutations := 0
	e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			return []byte(`"daemon-image-test"`), nil
		}
		if args[4] == "image" {
			return json.Marshal(inspectedImage{ID: p.Release.ImageID, OS: "linux", Architecture: "amd64"})
		}
		if args[4] == "container" && args[5] == "inspect" {
			return nil, errors.New("previous missing")
		}
		mutations++
		return nil, errors.New("unexpected mutation")
	})
	until := time.Now().Add(time.Minute)
	environment := runtimeEnvironmentMaterial(t, p, until)
	result := e.ApplyWithRuntimeEnvironment(t.Context(), "hop", p, environment, until)
	if result.Outcome != "failed" || !result.Uncertain || mutations != 0 {
		t.Fatal("missing previous container recreated or treated as known")
	}
}

func TestRuntimeEnvironmentDeadlineRecheckedAfterDaemonVerification(t *testing.T) {
	e := testImageEngine(t)
	mutations := 0
	deadline := time.Now().Add(20 * time.Millisecond)
	e.command = imageCommandFunc(func(ctx context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if args[4] == "info" {
			<-ctx.Done()
			return []byte(`"daemon-image-test"`), nil
		}
		mutations++
		return nil, nil
	})
	if _, err := e.mutateBeforeRuntimeDeadline(t.Context(), "daemon-image-test", deadline, "container", "stop", "previous"); err == nil || mutations != 0 {
		t.Fatal("daemon verification delay allowed expired mutation")
	}
}
