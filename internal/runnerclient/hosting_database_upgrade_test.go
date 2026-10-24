package runnerclient

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
)

func upgradeClientOperation(t *testing.T, kind string) domain.HostingOperation {
	t.Helper()
	capture, origin := captureClientFixture(t, kind)
	p := capture.Capture.Plan
	proof := domain.HostingDatabaseUpgradeProof{Version: domain.HostingDatabaseUpgradeVersion, CapturePlan: p, OriginReceipt: captureClientReceipt(capture, origin), PreviousPlan: p.SourcePlan, PreviousHealthy: p.SourceHealthy}
	proof.Hash = proof.DocumentHash()
	in, err := domain.HostingDatabaseUpgradePlanInput(proof)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := domain.CompileHostingDatabasePlanV2(p.SourcePlan.Snapshot, p.SourcePlan.Release, p.SourceHealthy, p.SourcePlan, *p.SourcePlan.Storage, *p.SourcePlan.Database, in)
	if err != nil {
		t.Fatal(err)
	}
	return domain.HostingOperation{ID: "upgrade", RunID: "run_upgrade", ProjectID: p.ProjectID, ResourceID: p.ResourceID, BindingID: p.BindingID, Snapshot: plan.Snapshot, Release: &plan.Release, Plan: &plan, DatabaseUpgrade: &proof, State: "prepared", TaskMode: "deploy"}
}

func TestDatabaseUpgradeCredentialsKeepWireAndRejectV1Downgrade(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			op := upgradeClientOperation(t, kind)
			request := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: op.Plan.Hash, Mode: "deploy"}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var got domain.HostingDatabaseCredentialRequest
				if json.NewDecoder(r.Body).Decode(&got) != nil || got != request {
					t.Error("upgrade changed credential request wire")
				}
				m := hostingdatabaseprotocol.Metadata{RunnerID: "runner", Authority: request.Authority, DeliveryID: request.DeliveryID, Mode: request.Mode, PlanHash: request.PlanHash, Database: op.Plan.Database.Reference, RequestHash: hostingdatabaseprotocol.RequestHash("runner", request), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(20 * time.Second), References: op.Plan.Database.Credentials}
				values := make([]hostingdatabaseprotocol.Credential, len(m.References))
				for i, ref := range m.References {
					values[i] = hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(strings.Repeat("A", 43))}
				}
				credentials, err := hostingdatabaseprotocol.NewCredentials(m, values)
				if err != nil {
					t.Error(err)
					return
				}
				defer credentials.Close()
				w.Header().Set("Cache-Control", "no-store")
				if err = hostingdatabaseprotocol.WriteCredentials(w, credentials); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			c := New(server.URL, "fixture")
			options := RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true}
			out, err := c.HostingDatabaseUpgradeCredentials(t.Context(), "runner", request, op, options)
			if err != nil {
				t.Fatal(err)
			}
			out.Close()
			if out, err = c.HostingDatabaseCredentials(t.Context(), "runner", request, *op.Plan, options); err == nil {
				out.Close()
				t.Fatal("v1 delivery admitted v2 plan")
			}
			for _, mode := range []string{"missing_proof", "applying", "result", "changed_authority", "changed_refs"} {
				changed := upgradeClientOperation(t, kind)
				switch mode {
				case "missing_proof":
					changed.DatabaseUpgrade = nil
				case "applying":
					changed.State = "applying"
				case "result":
					changed.Result = &domain.HostingResult{Outcome: "failed", Uncertain: true}
				case "changed_authority":
					changed.RunID = "other"
				case "changed_refs":
					changed.Plan.Database.Credentials[0].Version++
				}
				if out, err = c.HostingDatabaseUpgradeCredentials(t.Context(), "runner", request, changed, options); err == nil {
					out.Close()
					t.Fatalf("invalid %s accepted", mode)
				}
			}
			if requests != 1 {
				t.Fatal("invalid plan or applying state reached credential endpoint")
			}
		})
	}
}

func TestDatabaseUpgradeOperationResponseRequiresExactProof(t *testing.T) {
	op := upgradeClientOperation(t, "redis")
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"valid":     raw,
		"missing":   func() []byte { copy := op; copy.DatabaseUpgrade = nil; b, _ := json.Marshal(copy); return b }(),
		"unknown":   bytes.Replace(raw, []byte(`"database_upgrade":{`), []byte(`"database_upgrade":{"foreign":true,`), 1),
		"duplicate": bytes.Replace(raw, []byte(`"database_upgrade":{`), []byte(`"database_upgrade":{"version":"foreign",`), 1),
		"changed_plan": func() []byte {
			copy := upgradeClientOperation(t, "redis")
			copy.Plan.Database.Network.Alias = "foreign"
			b, _ := json.Marshal(copy)
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			var response hostingOperationResponse
			if err := response.decodeResponse(bytes.NewReader(body)); (err == nil) != (name == "valid") {
				t.Fatal("incorrect proof validation result", err)
			}
		})
	}
}

func TestDatabaseUpgradeIdentityRequiresExplicitMatchingCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, advertised := range []bool{false, true} {
			caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
			if advertised {
				caps = append(caps, domain.HostingDatabaseUpgradeCapability)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps})
			}))
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{Database: true, DatabaseUpgrade: enabled, DatabaseTrustedLoopbackHTTP: true})
			server.Close()
			if (err == nil) != (enabled == advertised) {
				t.Fatal("upgrade capability/config mismatch admitted")
			}
		}
	}
}

func TestDatabaseUpgradeCapabilityInExactCaptureModeRequiresDeploymentPrerequisites(t *testing.T) {
	for _, missing := range []string{"", domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
		caps := []string{domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability}
		for _, cap := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
			if cap != missing {
				caps = append(caps, cap)
			}
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps})
		}))
		_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{DatabaseNetworkCapture: true, DatabaseTrustedLoopbackHTTP: true})
		if (err == nil) != (missing == "") {
			t.Fatal("capture admitted missing upgrade prerequisites", missing)
		}
		_, err = New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{DatabaseNetworkCapture: true, DatabaseUpgrade: true, DatabaseTrustedLoopbackHTTP: true})
		server.Close()
		if err == nil {
			t.Fatal("capture enabled upgrade execution")
		}
	}
}
