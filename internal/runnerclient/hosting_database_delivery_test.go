package runnerclient

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func databaseClientPlanParts(t *testing.T, engine, version string, platformIndex int) (domain.HostingSnapshot, domain.HostingRelease, domain.HostingStoragePlan, domain.HostingDatabasePlan) {
	t.Helper()
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal(err)
	}
	id, volume := domain.HostingStorageIdentity("binding_database")
	s := domain.HostingSnapshot{ResourceID: "resource_database", ResourceGeneration: 1, BindingID: "binding_database", DestinationID: "destination_database", ServerID: "server_database", RunnerID: "runner_database", RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion, Storage: domain.HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("c", 64)}, Database: domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}}
	credentials := slices.Clone(recipe.Credentials)
	for i := range credentials {
		credentials[i].SecretID = domain.HostingDatabaseSecretID(s.ResourceID, credentials[i].Role)
		credentials[i].Version = int64(i + 1)
	}
	hash, err := domain.HostingDatabaseConfigurationHash(s, credentials)
	if err != nil {
		t.Fatal(err)
	}
	s.Database.ConfigurationHash = hash
	p := recipe.Platforms[platformIndex]
	r := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + p.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: p.OS, Architecture: p.Architecture, Variant: p.Variant, DaemonID: "daemon_database"}
	r.Hash = domain.HostingDocumentHash(r)
	storage := domain.HostingStoragePlan{Reference: s.Storage, VolumeName: volume, MountPath: recipe.MountPath, CreationOperationID: "hop_database_origin", DaemonID: r.DaemonID}
	storage.CreationNonce = domain.HostingStorageCreationNonce(s.Storage, storage.CreationOperationID, r.DaemonID)
	db := domain.HostingDatabasePlan{Reference: s.Database, NetworkName: domain.HostingDatabaseNetworkName(s.BindingID), Credentials: credentials}
	return s, r, storage, db
}

func TestDatabaseCredentialClientExplicitTransport(t *testing.T) {
	s, r, storage, db := databaseClientPlanParts(t, "redis", domain.HostingDatabaseRedisRecipe, 0)
	plan, err := domain.CompileHostingDatabasePlan(s, r, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: plan.Hash, Mode: "deploy"}
	for _, server := range []string{"http://example.com", "http://localhost", "https://user:password@example.com", "https://example.com?token=private", "https://example.com#fragment"} {
		c := New(server, "token")
		if out, e := c.HostingDatabaseCredentials(t.Context(), "runner", in, plan, RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true}); e == nil {
			out.Close()
			t.Fatal("unsafe origin accepted")
		}
	}
	c := New("https://127.0.0.1:1", "token")
	c.HTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if out, e := c.HostingDatabaseCredentials(t.Context(), "runner", in, plan, RuntimeEnvironmentTransport{}); e == nil {
		out.Close()
		t.Fatal("insecure TLS transport accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		m := hostingdatabaseprotocol.Metadata{RunnerID: "runner", Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: plan.Database.Reference, RequestHash: hostingdatabaseprotocol.RequestHash("runner", in), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(20 * time.Second), References: plan.Database.Credentials}
		entries := make([]hostingdatabaseprotocol.Credential, 2)
		for i, ref := range m.References {
			entries[i] = hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(strings.Repeat("A", 43))}
		}
		out, e := hostingdatabaseprotocol.NewCredentials(m, entries)
		if e != nil {
			t.Error(e)
			return
		}
		defer out.Close()
		if hostingdatabaseprotocol.WriteCredentials(w, out) != nil {
			t.Error("write fixture")
		}
	}))
	defer server.Close()
	c = New(server.URL, "token")
	out, err := c.HostingDatabaseCredentials(t.Context(), "runner", in, plan, RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	out.Close()
}

func TestDatabaseCredentialClientTransport(t *testing.T) {
	s, r, storage, db := databaseClientPlanParts(t, "postgresql", domain.HostingDatabasePostgresRecipe, 0)
	plan, err := domain.CompileHostingDatabasePlan(s, r, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: plan.Hash, Mode: "deploy"}
	for _, mode := range []string{"valid", "authority", "mode", "runner", "plan", "configuration", "reference", "delivery", "request", "purpose", "expired", "future", "unknown", "duplicate", "cache", "redirect", "status", "value"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/runners/hosting/database-credentials" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong request authority")
				}
				got, e := hostingdatabaseprotocol.ReadRequest(r.Body)
				if e != nil || got != in {
					t.Error("request changed")
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/elsewhere", http.StatusFound)
					return
				}
				if mode == "status" {
					w.WriteHeader(503)
					return
				}
				m := hostingdatabaseprotocol.Metadata{RunnerID: "runner", Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: plan.Database.Reference, RequestHash: hostingdatabaseprotocol.RequestHash("runner", in), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(30 * time.Second), References: plan.Database.Credentials}
				entries := make([]hostingdatabaseprotocol.Credential, 2)
				for i, ref := range m.References {
					entries[i] = hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(strings.Repeat("A", 43))}
				}
				c, e := hostingdatabaseprotocol.NewCredentials(m, entries)
				if e != nil {
					t.Error(e)
					return
				}
				defer c.Close()
				var b bytes.Buffer
				if hostingdatabaseprotocol.WriteCredentials(&b, c) != nil {
					t.Error("encode")
					return
				}
				var body map[string]any
				if json.Unmarshal(b.Bytes(), &body) != nil {
					t.Error("decode fixture")
					return
				}
				switch mode {
				case "authority":
					body["authority"].(map[string]any)["fence"] = "other"
				case "mode":
					body["mode"] = "observe"
				case "runner":
					body["runner_id"] = "other"
				case "plan":
					body["plan_hash"] = "sha256:" + strings.Repeat("c", 64)
				case "configuration":
					body["database"].(map[string]any)["configuration_hash"] = "sha256:" + strings.Repeat("c", 64)
				case "reference":
					body["references"].([]any)[0].(map[string]any)["version"] = 2
					body["entries"].([]any)[0].(map[string]any)["reference"].(map[string]any)["version"] = 2
				case "delivery":
					body["delivery_id"] = strings.Repeat("c", 32)
				case "request":
					body["request_hash"] = "sha256:" + strings.Repeat("c", 64)
				case "purpose":
					body["purpose"] = "other"
				case "expired":
					body["use_until"] = time.Now().Add(-time.Second)
				case "future":
					body["use_until"] = time.Now().Add(2 * time.Minute)
				case "unknown":
					body["unknown"] = true
				case "value":
					body["entries"].([]any)[0].(map[string]any)["value"] = "private-diagnostic"
				}
				raw, e := json.Marshal(body)
				if e != nil {
					t.Error(e)
					return
				}
				if mode == "duplicate" {
					raw = bytes.Replace(raw, []byte(`"purpose":`), []byte(`"purpose":"other","purpose":`), 1)
				}
				if mode != "cache" {
					w.Header().Set("Cache-Control", "no-store")
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			c := New(server.URL, "token")
			c.HTTP = server.Client()
			out, e := c.HostingDatabaseCredentials(t.Context(), "runner", in, plan, RuntimeEnvironmentTransport{})
			if mode == "valid" {
				if e != nil {
					t.Fatal(e)
				}
				out.Close()
			} else {
				if out != nil {
					out.Close()
				}
				if e == nil || strings.Contains(fmt.Sprint(e), "private") {
					t.Fatal("invalid protected response accepted or leaked")
				}
			}
			if calls != 1 {
				t.Fatal("redirect followed or duplicate request")
			}
			c.Server = strings.Replace(server.URL, "https:", "http:", 1)
			if out, e = c.HostingDatabaseCredentials(t.Context(), "runner", in, plan, RuntimeEnvironmentTransport{}); e == nil {
				out.Close()
				t.Fatal("untrusted HTTP accepted")
			}
		})
	}
}
