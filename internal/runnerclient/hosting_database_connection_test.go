package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
)

func connectionClientOperation(t *testing.T, engine string, replacement bool) domain.HostingOperation {
	t.Helper()
	upgraded := upgradeClientOperation(t, engine)
	db := upgraded.DatabaseUpgrade.PreviousPlan
	s := domain.HostingSnapshot{ResourceID: "application", ResourceGeneration: 1, ImageReference: "nginx:1", BindingID: "application_binding", DestinationID: db.Snapshot.DestinationID, ServerID: db.Snapshot.ServerID, RunnerID: db.Snapshot.RunnerID, RuntimeNamespace: "ncd_" + strings.Repeat("e", 32), ContainerPort: 8080, HostPort: 22001, PolicyVersion: domain.HostingRuntimePolicyVersion, RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("f", 64)}}
	b := domain.HostingDatabaseBinding{Version: domain.HostingDatabaseBindingVersion, ID: domain.HostingDatabaseBindingID(s.BindingID, db.Snapshot.BindingID), Generation: 1, ApplicationResourceID: s.ResourceID, ApplicationResourceGeneration: 1, ApplicationBindingID: s.BindingID, DatabaseResourceID: db.Snapshot.ResourceID, DatabaseResourceGeneration: db.Snapshot.ResourceGeneration, DatabaseBindingID: db.Snapshot.BindingID, ProjectID: upgraded.ProjectID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, Database: db.Snapshot.Database, NetworkName: db.Database.NetworkName, Alias: domain.HostingDatabaseBindingAlias(db.Snapshot.BindingID), EnvironmentNames: domain.HostingDatabaseBindingEnvironmentNames(engine), RuntimeConfiguration: s.RuntimeConfiguration}
	for _, ref := range db.Database.Credentials {
		if ref.Role == "app" {
			b.Credential = ref
		}
	}
	b.Hash = b.DocumentHash()
	h := upgraded.DatabaseUpgrade.PreviousHealthy
	h.OperationID, h.PlanHash, h.Snapshot = upgraded.ID, upgraded.Plan.Hash, upgraded.Snapshot
	h.ContainerID = strings.Repeat("9", 64)
	in := domain.HostingApplicationDatabasePlanInput{OperationID: "application_first", CreateNonce: strings.Repeat("a", 32), Binding: b, ApplicationProjectID: b.ProjectID, DatabaseProjectID: b.ProjectID, BindingApplicationSnapshot: s, BindingDatabasePlan: db, DatabasePlan: *upgraded.Plan, DatabaseHealthy: h, Origin: upgraded.DatabaseUpgrade.OriginReceipt.Origin, MembershipRevision: 1, Members: []domain.HostingDatabaseNetworkMemberProof{}, Retired: []domain.HostingDatabaseNetworkRetirementProof{}}
	s.PolicyVersion = domain.HostingApplicationDatabasePolicyVersion
	s.ApplicationDatabase = domain.HostingApplicationDatabaseRef{BindingID: b.ID, BindingHash: b.Hash}
	r := upgraded.Plan.Release
	r.CanonicalImage, r.Hash = "docker.io/library/nginx@sha256:"+strings.Repeat("1", 64), ""
	r.Hash = domain.HostingDocumentHash(r)
	p, err := domain.CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := domain.NewHostingApplicationDatabaseExecutionProof(in)
	if err != nil {
		t.Fatal(err)
	}
	op := domain.HostingOperation{ID: in.OperationID, RunID: "run_" + in.OperationID, ProjectID: b.ProjectID, ResourceID: b.ApplicationResourceID, BindingID: b.ApplicationBindingID, Snapshot: s, Release: &r, Plan: &p, ApplicationDatabase: &proof, State: "applying", TaskMode: "deploy", MutationHold: true}
	if replacement {
		stages := connectionClientStages(t, op)
		ready := stages[len(stages)-1]
		previous := domain.HostingHealthy{Snapshot: s, Release: r, OperationID: op.ID, ContainerID: ready.Candidate.ContainerID, PlanHash: p.Hash}
		in.Members = []domain.HostingDatabaseNetworkMemberProof{{Binding: b, EndpointPlan: ready.AttachPlan, Receipt: *ready.EndpointReceipt, ApplicationHealthy: previous, DatabasePlan: db, ApplicationProjectID: b.ProjectID, DatabaseProjectID: b.ProjectID}}
		in.OperationID, in.CreateNonce, in.MembershipRevision = "application_replace", strings.Repeat("b", 32), ready.AttachPlan.MembershipRevision
		proof, err = domain.NewHostingApplicationDatabaseExecutionProof(in)
		if err != nil {
			t.Fatal(err)
		}
		s.HostPort++
		p, err = domain.CompileHostingApplicationDatabasePlan(s, r, &previous, nil, in)
		if err != nil {
			t.Fatal(err)
		}
		op.ID, op.RunID, op.Snapshot, op.Plan, op.ApplicationDatabase = in.OperationID, "run_"+in.OperationID, s, &p, &proof
	}
	if !connectionOperationValid(op) {
		t.Fatal("invalid connected operation fixture")
	}
	return op
}

func connectionClientStages(t *testing.T, op domain.HostingOperation) []domain.HostingApplicationDatabaseStage {
	t.Helper()
	in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: op.ID, PlanHash: op.Plan.Hash, CreateNonce: in.CreateNonce, ContainerID: strings.Repeat("2", 64), ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, DefaultNetworkID: strings.Repeat("3", 64)}
	if len(in.Members) != 0 {
		c.ContainerID = strings.Repeat("5", 64)
	}
	c.Hash = c.DocumentHash()
	p, err := domain.CompileHostingApplicationDatabaseAttachPlan(*op.Plan, in, c)
	if err != nil {
		t.Fatal(err)
	}
	var stages []domain.HostingApplicationDatabaseStage
	var receipt *domain.HostingDatabaseBindingEndpointReceipt
	for _, phase := range []string{"candidate_created", "attach_intent", "network_configured", "attached", "ready"} {
		if phase == "attach_intent" || phase == "attached" {
			r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "receipt_" + phase, OperationID: op.ID, PlanHash: p.Hash, BindingHash: in.Binding.Hash, MembershipRevision: p.MembershipRevision, Phase: phase}
			if phase == "attach_intent" {
				r.Intent = &domain.HostingDatabaseBindingEndpoint{Target: p.Target}
			} else {
				r.Observation = &domain.HostingDatabaseBindingEndpointObservation{Present: true, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: strings.Repeat("4", 64)}}
			}
			r.Hash = r.DocumentHash()
			receipt = &r
		}
		s := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, ReceiptID: "stage_" + phase, OperationID: op.ID, PlanHash: op.Plan.Hash, Phase: phase, Candidate: c, AttachPlan: p, EndpointReceipt: receipt}
		s.Hash = s.DocumentHash()
		if s.Validate(*op.Plan, in) != nil {
			t.Fatal("invalid stage fixture", phase)
		}
		stages = append(stages, s)
	}
	return stages
}

func TestApplicationDatabaseClientAuthorizationEchoAndExpiry(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"valid", "id", "operation", "plan", "action", "stage", "lease", "attempt", "fence", "revision", "expired", "long_window", "lease_expiry", "unknown", "duplicate"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				op := connectionClientOperation(t, engine, false)
				a := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
				request := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("a", 32), Action: "create"}
				expires := time.Now().Add(25 * time.Second)
				value := domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: request.AuthorizationID, OperationID: op.ID, PlanHash: op.Plan.Hash, Action: request.Action, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, MembershipRevision: 1, UseUntil: time.Now().Add(20 * time.Second)}
				switch mode {
				case "id":
					value.AuthorizationID = strings.Repeat("b", 32)
				case "operation":
					value.OperationID = "other"
				case "plan":
					value.PlanHash = "sha256:" + strings.Repeat("f", 64)
				case "action":
					value.Action, value.StageHash = "start", value.PlanHash
				case "stage":
					value.StageHash = value.PlanHash
				case "lease":
					value.LeaseID = "other"
				case "attempt":
					value.Attempt++
				case "fence":
					value.Fence = "other"
				case "revision":
					value.MembershipRevision++
				case "expired":
					value.UseUntil = time.Now().Add(-time.Second)
				case "long_window":
					value.UseUntil, expires = time.Now().Add(time.Minute), time.Now().Add(2*time.Minute)
				case "lease_expiry":
					expires = time.Now().Add(10 * time.Second)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var got domain.HostingApplicationDatabaseEffectRequest
					if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/database-binding/authorize" || json.NewDecoder(r.Body).Decode(&got) != nil || got != request || r.Header.Get("Authorization") != "Bearer credential_canary" {
						t.Error("effect request wire changed")
					}
					raw, _ := json.Marshal(value)
					if mode == "unknown" {
						raw = bytes.Replace(raw, []byte("{"), []byte(`{"unknown":"credential_canary",`), 1)
					}
					if mode == "duplicate" {
						raw = bytes.Replace(raw, []byte("{"), []byte(`{"attempt":1,`), 1)
					}
					_, _ = w.Write(raw)
				}))
				defer server.Close()
				ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
				_, err := New(server.URL, "credential_canary").HostingApplicationDatabaseAuthorize(ctx, request, op, 1, expires)
				if (err == nil) != (mode == "valid") || err != nil && strings.Contains(err.Error(), "credential_canary") {
					t.Fatalf("unexpected authorization outcome: %v", err)
				}
			})
		}
	}
}

func TestApplicationDatabaseClientStateKeepsAdmissionRevisionUntilComplete(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		op := connectionClientOperation(t, "postgresql", replacement)
		stages := connectionClientStages(t, op)
		a := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
		value := domain.HostingApplicationDatabaseExecutionState{OperationID: op.ID, PlanHash: op.Plan.Hash, MembershipRevision: op.ApplicationDatabase.MembershipRevision, HoldOperationID: op.ID}
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			_ = json.NewEncoder(w).Encode(value)
		}))
		t.Cleanup(server.Close)
		c := New(server.URL, "fixture")
		if _, err := c.HostingApplicationDatabaseState(t.Context(), a, op); err == nil || calls != 0 {
			t.Fatal("connection state used unprotected loopback HTTP")
		}
		ctx := WithDatabaseTransport(context.Background(), DatabaseTransport{TrustedLoopbackHTTP: true})
		for _, stage := range stages {
			value.Stage = &stage
			if _, err := c.HostingApplicationDatabaseState(ctx, a, op); err != nil {
				t.Fatal("in-flight N rejected", stage.Phase, err)
			}
			value.MembershipRevision++
			if _, err := c.HostingApplicationDatabaseState(ctx, a, op); err == nil {
				t.Fatal("stage advanced admission revision")
			}
			value.MembershipRevision--
		}
		op.State, op.MutationHold = "succeeded", false
		op.Result = &domain.HostingResult{Outcome: "succeeded", Ready: true}
		value.HoldOperationID = ""
		value.MembershipRevision++
		if replacement {
			value.MembershipRevision++
		}
		if _, err := c.HostingApplicationDatabaseState(ctx, a, op); err != nil {
			t.Fatal("atomic settled revision rejected", err)
		}
		before := calls
		if _, err := c.HostingApplicationDatabaseAuthorize(ctx, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("a", 32), Action: "create"}, op, op.ApplicationDatabase.MembershipRevision, time.Now().Add(time.Minute)); err == nil || calls != before {
			t.Fatal("settled historical evidence requested fresh effect permission")
		}
		server.Close()
	}
}

func TestApplicationDatabaseClientClosedOperationEnvelope(t *testing.T) {
	op := connectionClientOperation(t, "redis", false)
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, inject := range []func([]byte) []byte{
		func(raw []byte) []byte { return raw },
		func(raw []byte) []byte { return bytes.Replace(raw, []byte("{"), []byte(`{"unknown":true,`), 1) },
		func(raw []byte) []byte { return bytes.Replace(raw, []byte(`"id":`), []byte(`"ID":"other","id":`), 1) },
	} {
		body := inject(raw)
		var response hostingOperationResponse
		err := response.decodeResponse(bytes.NewReader(body))
		if (err == nil) != bytes.Equal(body, raw) {
			t.Fatal("connected operation envelope accepted ambiguous data", err)
		}
	}
}

func TestApplicationDatabaseClientStageAndExactRetirement(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			op := connectionClientOperation(t, engine, true)
			stages := connectionClientStages(t, op)
			a := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
			in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
			if err != nil {
				t.Fatal(err)
			}
			plan := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: op.ID, BindingHash: in.Binding.Hash, MembershipRevision: in.MembershipRevision + 2, Action: "retire", Target: in.Members[0].EndpointPlan.Target, ExpectedEndpointID: in.Members[0].Receipt.Observation.Endpoint.EndpointID}
			plan.Hash = plan.DocumentHash()
			receipt := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "retire_intent", OperationID: op.ID, PlanHash: plan.Hash, BindingHash: in.Binding.Hash, MembershipRevision: plan.MembershipRevision, Phase: "retire_intent", Intent: &domain.HostingDatabaseBindingEndpoint{Target: plan.Target, EndpointID: plan.ExpectedEndpointID}}
			receipt.Hash = receipt.DocumentHash()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-binding/stage":
					var request domain.HostingApplicationDatabaseStageRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostingAuthority != a || request.Stage.Hash != stages[4].Hash {
						t.Error("stage body changed")
					}
					_ = json.NewEncoder(w).Encode(op)
				case "/api/v1/runners/hosting/database-binding/retirement-plan":
					_ = json.NewEncoder(w).Encode(plan)
				case "/api/v1/runners/hosting/database-binding/retirement":
					var request domain.HostingApplicationDatabaseRetirementRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostingAuthority != a || request.PlanHash != op.Plan.Hash || request.Receipt.Hash != receipt.Hash {
						t.Error("retirement body changed")
					}
					_ = json.NewEncoder(w).Encode(op)
				default:
					t.Error("unexpected route", r.URL.Path)
				}
			}))
			defer server.Close()
			c := New(server.URL, "fixture")
			ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
			if _, err := c.HostingApplicationDatabaseStage(ctx, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: a, Stage: stages[4]}, op); err != nil {
				t.Fatal(err)
			}
			if p, err := c.HostingApplicationDatabaseRetirementPlan(ctx, a, op); err != nil || p != plan {
				t.Fatal("exact retirement plan", err)
			}
			request := domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, Receipt: receipt}
			if _, err := c.HostingApplicationDatabaseRetirement(ctx, request, plan, op); err != nil {
				t.Fatal(err)
			}
			request.PlanHash = plan.Hash
			if _, err := c.HostingApplicationDatabaseRetirement(ctx, request, plan, op); err == nil || calls != 3 {
				t.Fatal("endpoint hash substituted for application operation plan")
			}
		})
	}
}

func TestDatabaseMaintenanceClientCredentialsPreparedOnly(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			upgraded := upgradeClientOperation(t, engine)
			h := upgraded.DatabaseUpgrade.PreviousHealthy
			h.OperationID, h.PlanHash, h.Snapshot = upgraded.ID, upgraded.Plan.Hash, upgraded.Snapshot
			proof, err := domain.NewHostingDatabaseMaintenanceProof(upgraded.DatabaseUpgrade.CapturePlan, upgraded.DatabaseUpgrade.OriginReceipt, *upgraded.Plan, h, domain.HostingDatabaseNetworkPlanInput{Origin: upgraded.DatabaseUpgrade.OriginReceipt.Origin, MembershipRevision: 1})
			if err != nil {
				t.Fatal(err)
			}
			p, err := domain.CompileHostingDatabaseMaintenancePlan(upgraded.Snapshot, *upgraded.Release, proof)
			if err != nil {
				t.Fatal(err)
			}
			op := upgraded
			op.ID, op.RunID, op.Plan, op.DatabaseUpgrade, op.DatabaseMaintenance = "maintenance", "run_maintenance", &p, nil, &proof
			a := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
			request := domain.HostingDatabaseCredentialRequest{Authority: a, DeliveryID: strings.Repeat("a", 32), PlanHash: p.Hash, Mode: "deploy"}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var got domain.HostingDatabaseCredentialRequest
				if json.NewDecoder(r.Body).Decode(&got) != nil || got != request {
					t.Error("maintenance credential wire changed")
				}
				m := hostingdatabaseprotocol.Metadata{RunnerID: op.Snapshot.RunnerID, Authority: a, DeliveryID: request.DeliveryID, Mode: request.Mode, PlanHash: p.Hash, Database: p.Database.Reference, RequestHash: hostingdatabaseprotocol.RequestHash(op.Snapshot.RunnerID, request), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(20 * time.Second), References: p.Database.Credentials}
				var values []hostingdatabaseprotocol.Credential
				for _, ref := range m.References {
					values = append(values, hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(strings.Repeat("A", 43))})
				}
				credentials, err := hostingdatabaseprotocol.NewCredentials(m, values)
				if err != nil {
					t.Error(err)
					return
				}
				defer credentials.Close()
				w.Header().Set("Cache-Control", "no-store")
				if err := hostingdatabaseprotocol.WriteCredentials(w, credentials); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			c := New(server.URL, "fixture")
			options := RuntimeEnvironmentTransport{TrustedLoopbackHTTP: true}
			credentials, err := c.HostingDatabaseMaintenanceCredentials(t.Context(), op.Snapshot.RunnerID, request, op, options)
			if err != nil {
				t.Fatal(err)
			}
			credentials.Close()
			op.State = "applying"
			if credentials, err := c.HostingDatabaseMaintenanceCredentials(t.Context(), op.Snapshot.RunnerID, request, op, options); err == nil {
				credentials.Close()
				t.Fatal("refreshed credentials after apply admission")
			}
			if calls != 1 {
				t.Fatal("invalid maintenance request reached protected endpoint")
			}
		})
	}
}
