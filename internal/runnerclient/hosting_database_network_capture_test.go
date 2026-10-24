package runnerclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func captureClientFixture(t *testing.T, engine string) (domain.HostingDatabaseNetworkCaptureClaim, domain.HostingDatabaseNetworkOriginRequest) {
	t.Helper()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal(err)
	}
	id, volume := domain.HostingStorageIdentity("binding")
	now := time.Now().UTC()
	st := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: recipe.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner", CreatedAt: now}
	st.Hash = st.DocumentHash()
	s := domain.HostingSnapshot{ResourceID: st.ResourceID, BindingID: st.BindingID, DestinationID: st.DestinationID, ServerID: "server", RunnerID: "runner", ResourceGeneration: 1, RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion, Storage: st.Reference(), Database: domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}}
	refs := append([]domain.HostingDatabaseCredentialRef(nil), recipe.Credentials...)
	for i := range refs {
		refs[i].SecretID = domain.HostingDatabaseSecretID(s.ResourceID, refs[i].Role)
		refs[i].Version = 1
	}
	s.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(s, refs)
	if err != nil {
		t.Fatal(err)
	}
	platform := recipe.Platforms[0]
	r := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "daemon"}
	r.Hash = domain.HostingDocumentHash(r)
	sp := domain.HostingStoragePlan{Reference: s.Storage, VolumeName: volume, MountPath: st.MountPath, DaemonID: r.DaemonID, CreationOperationID: "source", CreationNonce: domain.HostingStorageCreationNonce(s.Storage, "source", r.DaemonID)}
	db, err := domain.CompileHostingDatabasePlan(s, r, nil, sp, domain.HostingDatabasePlan{Reference: s.Database, NetworkName: domain.HostingDatabaseNetworkName(s.BindingID), Credentials: refs})
	if err != nil {
		t.Fatal(err)
	}
	h := domain.HostingHealthy{Snapshot: s, Release: r, OperationID: "source", ContainerID: strings.Repeat("a", 64), PlanHash: db.Hash, Storage: &domain.HostingHealthyStorage{Plan: sp, CreationPlanHash: db.Hash}}
	receipt := domain.HostingReceipt{ID: "materialized", BodyHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: now}
	m := domain.HostingStorageMaterialization{StorageRef: s.Storage, State: "materialized", CreationOperationID: sp.CreationOperationID, CreationPlanHash: db.Hash, CreationNonce: sp.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &receipt}
	m.Labels = domain.ExpectedHostingStorageLabels(st, m)
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(h.OperationID), SourceOperationID: h.OperationID, ProjectID: st.ProjectID, ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, SourcePlan: db, SourceHealthy: h, Storage: st, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: volume, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil {
		t.Fatal("invalid capture client fixture")
	}
	c := domain.HostingDatabaseNetworkCaptureClaim{Capture: domain.HostingDatabaseNetworkCapture{ID: p.OperationID, RunID: "capture_" + p.OperationID, State: "running", CreatedBy: "owner", Plan: p, CreatedAt: now, UpdatedAt: now}, Lease: domain.RunLease{ID: "lease", RunID: "capture_" + p.OperationID, RunnerID: s.RunnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "private_fence_canary", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: domain.HostingAuthority{OperationID: c.Capture.ID, RunID: c.Lease.RunID, LeaseID: c.Lease.ID, Attempt: c.Lease.Attempt, Fence: c.Lease.Fence}, ReceiptID: "origin", PlanHash: p.Hash, ObservedAt: now, Origin: domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: r.DaemonID, Configuration: s.Database.ConfigurationHash, OriginOperation: m.CreationOperationID, OriginPlan: m.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: s.Storage, VolumeNonce: m.CreationNonce, VolumeCreatedAt: now.Format(time.RFC3339Nano), NetworkID: strings.Repeat("d", 64), InitReady: true}}
	return c, in
}

func captureClientReceipt(c domain.HostingDatabaseNetworkCaptureClaim, in domain.HostingDatabaseNetworkOriginRequest) domain.HostingDatabaseNetworkOriginReceipt {
	return domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: time.Now().UTC()}, OperationID: in.OperationID, SourceOperationID: c.Capture.Plan.SourceOperationID, PlanHash: in.PlanHash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: c.Lease.RunnerID}
}

func TestDatabaseNetworkCaptureClientExactExchanges(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			claim, in := captureClientFixture(t, engine)
			p := claim.Capture.Plan
			var bodies [][]byte
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private_bearer_canary" {
					t.Error("missing original bearer")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				bodies = append(bodies, raw)
				var out any
				switch r.URL.Path {
				case "/api/v1/runners/database-network-captures/claim":
					out = claim
				case "/api/v1/runners/database-network-captures/plan":
					out = claim.Capture
				case "/api/v1/runners/database-network-captures/origin":
					out = captureClientReceipt(claim, in)
				default:
					t.Error("unexpected route")
				}
				if err := json.NewEncoder(w).Encode(out); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(server.URL, "private_bearer_canary")
			client.HTTP = server.Client()
			if _, err := client.ClaimHostingDatabaseNetworkCapture(t.Context(), p.RunnerID, p.OperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := client.HostingDatabaseNetworkCapturePlan(t.Context(), p.RunnerID, in.HostingAuthority); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := client.RecordHostingDatabaseNetworkOrigin(t.Context(), p.RunnerID, in, p); err != nil {
					t.Fatal(err)
				}
			}
			if len(bodies) != 4 || string(bodies[2]) != string(bodies[3]) {
				t.Fatal("origin replay changed original request")
			}
			bad := in
			bad.PlanHash = "sha256:" + strings.Repeat("0", 64)
			if _, err := client.RecordHostingDatabaseNetworkOrigin(t.Context(), p.RunnerID, bad, p); err == nil || len(bodies) != 4 {
				t.Fatal("mismatched accepted plan reached HTTP")
			}
		})
	}
}

func TestDatabaseNetworkCaptureClientRejectsMalformedAndForeignResponses(t *testing.T) {
	claim, in := captureClientFixture(t, "postgresql")
	p := claim.Capture.Plan
	for _, phase := range []string{"claim", "plan", "origin"} {
		modes := []string{"unknown", "duplicate", "null", "oversize", "foreign_runner", "foreign_id", "wrong_hash", "wrong_time", "stale_lease"}
		if phase == "claim" {
			modes = append(modes, "future_lease", "long_lease")
		}
		for _, mode := range modes {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				var out any
				switch phase {
				case "claim":
					v := claim
					if mode == "foreign_runner" {
						v.Lease.RunnerID = "foreign"
					}
					if mode == "foreign_id" {
						v.Lease.RunID = "foreign"
					}
					if mode == "wrong_hash" {
						v.Capture.Plan.Hash = "sha256:" + strings.Repeat("0", 64)
					}
					if mode == "wrong_time" {
						v.Capture.CreatedAt = time.Time{}
					}
					if mode == "stale_lease" {
						v.Lease.ExpiresAt = time.Now().Add(-time.Hour)
					}
					if mode == "future_lease" {
						v.Lease.CreatedAt = time.Now().Add(time.Hour)
						v.Lease.ExpiresAt = v.Lease.CreatedAt.Add(time.Minute)
					}
					if mode == "long_lease" {
						v.Lease.ExpiresAt = v.Lease.CreatedAt.Add(11 * time.Minute)
					}
					out = v
				case "plan":
					v := claim.Capture
					if mode == "foreign_runner" {
						v.Plan.RunnerID = "foreign"
						v.Plan.Hash = v.Plan.DocumentHash()
					}
					if mode == "foreign_id" {
						v.ID = "foreign"
					}
					if mode == "wrong_hash" {
						v.Plan.Hash = "sha256:" + strings.Repeat("0", 64)
					}
					if mode == "wrong_time" {
						v.UpdatedAt = time.Time{}
					}
					if mode == "stale_lease" {
						v.State = "succeeded"
					}
					out = v
				case "origin":
					v := captureClientReceipt(claim, in)
					if mode == "foreign_runner" {
						v.RunnerID = "foreign"
					}
					if mode == "foreign_id" {
						v.ID = "foreign"
					}
					if mode == "wrong_hash" {
						v.BodyHash = "sha256:" + strings.Repeat("0", 64)
					}
					if mode == "wrong_time" {
						v.ObservedAt = v.ObservedAt.Add(time.Second)
					}
					if mode == "stale_lease" {
						v.OriginHash = "sha256:" + strings.Repeat("0", 64)
					}
					out = v
				}
				raw, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "unknown":
					raw = append(raw[:len(raw)-1], []byte(`,"private_canary":"value"}`)...)
				case "duplicate":
					raw = append(raw[:len(raw)-1], []byte(`,"private_canary":1,"private_canary":2}`)...)
				case "null":
					raw = []byte("null")
				case "oversize":
					raw = []byte(strings.Repeat(" ", domain.HostingStorageProtocolJSONLimit+1) + string(raw))
				}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if _, err := w.Write(raw); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				client := New(server.URL, "private_bearer_canary")
				client.HTTP = server.Client()
				switch phase {
				case "claim":
					_, err = client.ClaimHostingDatabaseNetworkCapture(t.Context(), p.RunnerID, p.OperationID)
				case "plan":
					_, err = client.HostingDatabaseNetworkCapturePlan(t.Context(), p.RunnerID, in.HostingAuthority)
				case "origin":
					_, err = client.RecordHostingDatabaseNetworkOrigin(t.Context(), p.RunnerID, in, p)
				}
				if err == nil || strings.Contains(err.Error(), "private_canary") || strings.Contains(err.Error(), in.Fence) {
					t.Fatal("foreign/private response accepted or leaked", err)
				}
			})
		}
	}
}

func TestDatabaseNetworkCaptureClientProtectsEveryRoute(t *testing.T) {
	claim, in := captureClientFixture(t, "redis")
	p := claim.Capture.Plan
	for _, phase := range []string{"claim", "plan", "origin"} {
		t.Run(phase, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1); w.WriteHeader(500) }))
			defer target.Close()
			redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()
			call := func(client Client) error {
				switch phase {
				case "claim":
					_, err := client.ClaimHostingDatabaseNetworkCapture(context.Background(), p.RunnerID, p.OperationID)
					return err
				case "plan":
					_, err := client.HostingDatabaseNetworkCapturePlan(context.Background(), p.RunnerID, in.HostingAuthority)
					return err
				default:
					_, err := client.RecordHostingDatabaseNetworkOrigin(context.Background(), p.RunnerID, in, p)
					return err
				}
			}
			client := New(redirect.URL, "private_bearer_canary")
			client.HTTP = redirect.Client()
			if err := call(client); err == nil || received.Load() != 0 {
				t.Fatal("redirect leaked authority")
			}
			if err := call(New(target.URL, "private_bearer_canary")); err == nil || received.Load() != 0 {
				t.Fatal("unverified HTTP reached controller")
			}
		})
	}
}

func TestDatabaseNetworkCaptureExplicitLoopbackCannotWeakenStrictParent(t *testing.T) {
	claim, _ := captureClientFixture(t, "redis")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if err := json.NewEncoder(w).Encode(claim); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := New(server.URL, "private_bearer_canary")
	fixture := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
	if _, err := client.ClaimHostingDatabaseNetworkCapture(fixture, claim.Lease.RunnerID, claim.Capture.ID); err != nil || calls.Load() != 1 {
		t.Fatal("explicit trusted loopback fixture failed", err)
	}
	strict := WithDatabaseTransport(t.Context(), DatabaseTransport{})
	child := WithDatabaseTransport(strict, DatabaseTransport{TrustedLoopbackHTTP: true})
	if _, err := client.ClaimHostingDatabaseNetworkCapture(child, claim.Lease.RunnerID, claim.Capture.ID); err == nil || calls.Load() != 1 {
		t.Fatal("child weakened strict parent transport")
	}
}
