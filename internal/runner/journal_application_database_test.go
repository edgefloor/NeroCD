package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func applicationConnectionJournalFixture(t *testing.T, engine string) []JournalHosting {
	t.Helper()
	capture := captureJournalFixture(t, engine)
	cp, origin := capture[0].Claim.Capture.Plan, capture[3].Ack.Origin
	old := cp.SourcePlan
	input := domain.HostingDatabaseNetworkPlanInput{Origin: origin, MembershipRevision: 1}
	database, err := domain.CompileHostingDatabasePlanV2(old.Snapshot, old.Release, cp.SourceHealthy, old, *old.Storage, *old.Database, input)
	if err != nil {
		t.Fatal(err)
	}
	healthy := cp.SourceHealthy
	healthy.OperationID, healthy.PlanHash, healthy.Snapshot = "database_upgraded", database.Hash, database.Snapshot
	s := domain.HostingSnapshot{ResourceID: "application", ResourceGeneration: 1, ImageReference: "nginx:1", BindingID: "application_binding", DestinationID: old.Snapshot.DestinationID, ServerID: old.Snapshot.ServerID, RunnerID: old.Snapshot.RunnerID, RuntimeNamespace: "ncd_" + strings.Repeat("e", 32), ContainerPort: 8080, HostPort: 22001, PolicyVersion: domain.HostingRuntimePolicyVersion, RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("f", 64)}}
	b := domain.HostingDatabaseBinding{Version: domain.HostingDatabaseBindingVersion, ID: domain.HostingDatabaseBindingID(s.BindingID, old.Snapshot.BindingID), Generation: 1, ApplicationResourceID: s.ResourceID, ApplicationResourceGeneration: 1, ApplicationBindingID: s.BindingID, DatabaseResourceID: old.Snapshot.ResourceID, DatabaseResourceGeneration: old.Snapshot.ResourceGeneration, DatabaseBindingID: old.Snapshot.BindingID, ProjectID: cp.ProjectID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, Database: old.Snapshot.Database, NetworkName: old.Database.NetworkName, Alias: domain.HostingDatabaseBindingAlias(old.Snapshot.BindingID), EnvironmentNames: domain.HostingDatabaseBindingEnvironmentNames(engine), RuntimeConfiguration: s.RuntimeConfiguration}
	for _, ref := range old.Database.Credentials {
		if ref.Role == "app" {
			b.Credential = ref
		}
	}
	b.Hash = b.DocumentHash()
	in := domain.HostingApplicationDatabasePlanInput{OperationID: "application_first", CreateNonce: strings.Repeat("a", 32), Binding: b, ApplicationProjectID: b.ProjectID, DatabaseProjectID: b.ProjectID, BindingApplicationSnapshot: s, BindingDatabasePlan: old, DatabasePlan: database, DatabaseHealthy: healthy, Origin: origin, MembershipRevision: 1, Members: []domain.HostingDatabaseNetworkMemberProof{}, Retired: []domain.HostingDatabaseNetworkRetirementProof{}}
	proof, err := domain.NewHostingApplicationDatabaseExecutionProof(in)
	if err != nil {
		t.Fatal("proof", b.ValidateTargets(b.ProjectID, s, b.ProjectID, old), err)
	}
	s.PolicyVersion = domain.HostingApplicationDatabasePolicyVersion
	s.ApplicationDatabase = domain.HostingApplicationDatabaseRef{BindingID: b.ID, BindingHash: b.Hash}
	r := old.Release
	r.CanonicalImage, r.Hash = "docker.io/library/nginx@sha256:"+strings.Repeat("1", 64), ""
	r.Hash = domain.HostingDocumentHash(r)
	p, err := domain.CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
	if err != nil {
		t.Fatal("application plan", domain.ValidateHostingSnapshotRelease(s, r), b.ValidateTargets(b.ProjectID, s, b.ProjectID, old), err)
	}
	a := capture[0].Attempt
	a.RunID = "run_" + in.OperationID
	authority := domain.HostingAuthority{OperationID: in.OperationID, RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence}
	base := JournalHosting{ApplicationDatabase: &proof, ApplicationSnapshot: &s, RuntimeEnvironment: true, RuntimeConfiguration: s.RuntimeConfiguration, Attempt: a, OperationID: in.OperationID, CreatedAt: a.CreatedAt}
	prepared := base
	prepared.ID, prepared.Phase = "prepared_application", "prepared"
	prepared.Prepared = &domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: prepared.ID, Release: r}
	intent := base
	intent.ID, intent.Phase, intent.Plan, intent.DeliveryID = "application_apply_intent", "apply_intent", &p, strings.Repeat("a", 32)
	c := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: in.OperationID, PlanHash: p.Hash, CreateNonce: in.CreateNonce, ContainerID: strings.Repeat("2", 64), ImageID: r.ImageID, DaemonID: r.DaemonID, DefaultNetworkID: strings.Repeat("3", 64)}
	c.Hash = c.DocumentHash()
	ep, err := domain.CompileHostingApplicationDatabaseAttachPlan(p, in, c)
	if err != nil {
		t.Fatal(err)
	}
	candidate := base
	candidate.ID, candidate.Phase, candidate.Plan, candidate.ApplicationCandidate = "actual_candidate", "application_candidate", &p, &c
	entries := []JournalHosting{prepared, intent, candidate}
	var receipt *domain.HostingDatabaseBindingEndpointReceipt
	for _, phase := range []string{"candidate_created", "attach_intent", "network_configured", "attached", "ready"} {
		if phase == "attach_intent" || phase == "attached" {
			r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "endpoint_" + phase, OperationID: ep.OperationID, PlanHash: ep.Hash, BindingHash: b.Hash, MembershipRevision: ep.MembershipRevision, Phase: phase}
			if phase == "attach_intent" {
				r.Intent = &domain.HostingDatabaseBindingEndpoint{Target: ep.Target}
			} else {
				r.Observation = &domain.HostingDatabaseBindingEndpointObservation{Present: true, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: ep.Target, EndpointID: strings.Repeat("4", 64)}}
			}
			r.Hash = r.DocumentHash()
			receipt = &r
		}
		stage := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, ReceiptID: "stage_" + phase, OperationID: in.OperationID, PlanHash: p.Hash, Phase: phase, Candidate: c, AttachPlan: ep, EndpointReceipt: receipt}
		stage.Hash = stage.DocumentHash()
		e := base
		e.ID, e.Phase, e.Plan, e.ApplicationStage = stage.ReceiptID, "application_stage", &p, &stage
		entries = append(entries, e)
	}
	result := base
	result.ID, result.Phase = "application_complete", "result"
	result.Result = &domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: result.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: c.ContainerID, DaemonID: r.DaemonID, ImageID: r.ImageID, PlanHash: p.Hash}}
	return append(entries, result)
}

func TestApplicationConnectionJournalV15RestartAndAtomicAck(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			entries := applicationConnectionJournalFixture(t, engine)
			j, path := openBuildJournal(t)
			for _, e := range entries {
				if _, err := j.AppendHosting(e); err != nil {
					t.Fatalf("%s: %v", e.Phase, err)
				}
				before := j.Snapshot()
				if j.state.Version != 15 {
					t.Fatal("connection evidence lost v15 discrimination")
				}
				if _, err := j.AppendHosting(e); err != nil || !reflect.DeepEqual(before, j.Snapshot()) {
					t.Fatal("redelivery changed retained evidence", err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				j, err = OpenAttemptJournal(path)
				if err != nil || !reflect.DeepEqual(before, j.Snapshot()) {
					t.Fatal("restart lost full proof or stage", err)
				}
			}
			defer func() { _ = j.Close() }()
			if j.DiscardAttempt(entries[0].Attempt) == nil {
				t.Fatal("discarded unresolved create intent")
			}
			appendCapture(t, j, captureJournalFixture(t, engine))
			if err := j.AckHostingConnection(entries[len(entries)-1].Result.HostingAuthority); err != nil || len(j.Snapshot().Hosting) != 0 || j.state.Version != 13 {
				t.Fatal("atomic ACK failed or discarded older capture evidence", err)
			}
		})
	}
}

func TestApplicationConnectionJournalRejectsDowngradeAndMixedEvidence(t *testing.T) {
	entries := applicationConnectionJournalFixture(t, "postgresql")
	for _, e := range entries {
		for version := 1; version < 15; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{e}}) == nil {
				t.Fatalf("v%d admitted %s", version, e.Phase)
			}
		}
		bad := cloneJournalHosting(e)
		bad.ApplicationDatabase = nil
		if validateJournalHosting(bad) == nil {
			t.Fatal("missing complete proof admitted")
		}
	}
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], entries[1], entries[3]}) == nil {
		t.Fatal("stage admitted without actual candidate")
	}
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], entries[1], entries[2], entries[3], entries[5]}) == nil {
		t.Fatal("stage skip admitted")
	}
	bad := cloneJournalHosting(entries[1])
	bad.ApplicationSnapshot.HostPort++
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], bad}) == nil {
		t.Fatal("current frozen target changed between phases")
	}
	for _, inject := range []func([]byte) []byte{
		func(raw []byte) []byte { return bytes.Replace(raw, []byte(`"version":15`), []byte(`"version":14`), 1) },
		func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"application_database":{`), []byte(`"application_database":{"unknown":true,`), 1)
		},
		func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"phase":"apply_intent"`), []byte(`"phase":"apply_intent","phase":"result"`), 1)
		},
	} {
		t.Run("closed_decode", func(t *testing.T) {
			dir := t.TempDir()
			raw, err := json.Marshal(journalState{Version: 15, Hosting: entries[:2]})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "journal.json"), inject(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if j, err := OpenAttemptJournal(dir); err == nil {
				_ = j.Close()
				t.Fatal("invalid closed journal reopened")
			}
		})
	}
}

func TestApplicationConnectionJournalPreservesOriginalCreateIntent(t *testing.T) {
	entries := applicationConnectionJournalFixture(t, "postgresql")
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], entries[2]}) == nil {
		t.Fatal("candidate admitted without durable create intent")
	}
	changed := cloneJournalHosting(entries[1])
	changed.ID = "changed_delivery"
	changed.DeliveryID = strings.Repeat("b", 32)
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], entries[1], changed}) == nil {
		t.Fatal("original delivery window replaced")
	}
	stale := cloneJournalHosting(entries[1])
	stale.Attempt.Fence = "another_fence"
	if validateApplicationDatabaseJournalChain([]JournalHosting{entries[0], stale}) == nil {
		t.Fatal("another authority bypassed unresolved connection evidence")
	}
	result := cloneJournalHosting(entries[len(entries)-1])
	result.Result.Result.ContainerID = strings.Repeat("f", 64)
	bad := append([]JournalHosting(nil), entries[:len(entries)-1]...)
	if validateApplicationDatabaseJournalChain(append(bad, result)) == nil {
		t.Fatal("completion changed actual candidate")
	}
	j, _ := openBuildJournal(t)
	defer func() { _ = j.Close() }()
	for _, e := range entries {
		if _, err := j.AppendHosting(e); err != nil {
			t.Fatal(err)
		}
	}
	before := j.Snapshot()
	if j.AckHosting(entries[1].ID) == nil || !reflect.DeepEqual(before, j.Snapshot()) {
		t.Fatal("partial ACK removed durable create intent from retained chain")
	}
}

func TestApplicationConnectionJournalBoundsCompleteEnvelope(t *testing.T) {
	e := applicationConnectionJournalFixture(t, "redis")[1]
	s := *e.ApplicationSnapshot
	s.RuntimeNamespace = strings.Repeat("n", 40<<10)
	in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*e.ApplicationDatabase)
	if err != nil {
		t.Fatal(err)
	}
	p, err := domain.CompileHostingApplicationDatabasePlan(s, e.Plan.Release, nil, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	e.ApplicationSnapshot, e.Plan = &s, &p
	raw, err := json.Marshal(e)
	if err != nil || len(raw) <= journalMaxProvenanceBytes || len(raw) > domain.HostingApplicationDatabaseJournalJSONLimit || validateJournalHosting(e) != nil {
		t.Fatal("valid composed connection envelope incorrectly uses legacy 64KiB bound", len(raw), err)
	}
	oversized := applicationConnectionJournalFixture(t, "redis")[8]
	oversized.Result.Result = domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: strings.Repeat("x", domain.HostingApplicationDatabaseJournalJSONLimit)}
	raw, err = json.Marshal(oversized)
	if err != nil || len(raw) <= domain.HostingApplicationDatabaseJournalJSONLimit || validateJournalHosting(oversized) == nil {
		t.Fatal("oversized complete connection envelope admitted")
	}
}

func TestDatabaseMaintenanceJournalV15EveryPhase(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) { checkMaintenanceJournal(t, engine) })
	}
}

func checkMaintenanceJournal(t *testing.T, engine string) {
	t.Helper()
	entries := databaseJournalFixture(t)
	capture := captureJournalFixture(t, engine)
	cp, receipt := capture[0].Claim.Capture.Plan, *capture[3].Ack
	source := cp.SourcePlan
	previous, err := domain.CompileHostingDatabasePlanV2(source.Snapshot, source.Release, cp.SourceHealthy, source, *source.Storage, *source.Database, domain.HostingDatabaseNetworkPlanInput{Origin: receipt.Origin, MembershipRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	healthy := cp.SourceHealthy
	healthy.OperationID, healthy.PlanHash, healthy.Snapshot = "database_upgraded", previous.Hash, previous.Snapshot
	proof, err := domain.NewHostingDatabaseMaintenanceProof(cp, receipt, previous, healthy, domain.HostingDatabaseNetworkPlanInput{Origin: receipt.Origin, MembershipRevision: previous.Database.Network.MembershipRevision})
	if err != nil {
		t.Fatal(err)
	}
	p, err := domain.CompileHostingDatabaseMaintenancePlan(previous.Snapshot, previous.Release, proof)
	if err != nil {
		t.Fatal(err)
	}
	j, path := openBuildJournal(t)
	for i := range entries {
		e := &entries[i]
		e.DatabaseUpgrade, e.DatabaseMaintenance = nil, &proof
		e.DatabaseRef, e.StorageRef = p.Snapshot.Database, p.Snapshot.Storage
		e.OperationID = "database_maintenance"
		a := domain.HostingAuthority{OperationID: e.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
		if e.Prepared != nil {
			e.Prepared.HostingAuthority, e.Prepared.Release = a, p.Release
		}
		if e.Plan != nil {
			e.Plan = &p
		}
		if e.StorageCreate != nil {
			e.StorageCreate = &domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: p.Hash, StorageRef: p.Snapshot.Storage, CreationNonce: p.Storage.CreationNonce}
		}
		if e.StorageMaterialized != nil {
			m := cp.Materialization
			e.StorageMetadata = &cp.Storage
			e.StorageMaterialized = &domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: e.ID, PlanHash: p.Hash, StorageRef: p.Snapshot.Storage, CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: cp.Storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels}
		}
		if e.Result != nil {
			e.Result.HostingAuthority = a
		}
		if _, err := j.AppendHosting(*e); err != nil {
			t.Fatalf("%s: %v", e.Phase, err)
		}
		if j.state.Version != 15 {
			t.Fatal("maintenance did not select v15")
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		j, err = OpenAttemptJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		for version := 1; version < 15; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{*e}}) == nil {
				t.Fatalf("maintenance admitted in old v%d", version)
			}
		}
	}
	defer func() { _ = j.Close() }()
	bad := cloneJournalHosting(entries[4])
	bad.DatabaseMaintenance = nil
	bad.ID = "missing_maintenance_proof"
	bad.Result.ReceiptID = bad.ID
	if _, err := j.AppendHosting(bad); err == nil {
		t.Fatal("maintenance sidecar dropped between phases")
	}
	if err := j.AckHostingConnection(entries[4].Result.HostingAuthority); err != nil || len(j.Snapshot().Hosting) != 0 || j.state.Version >= 15 {
		t.Fatal("settled maintenance did not retire atomically", err)
	}
}

func TestApplicationConnectionJournalReplacementRequiresExactRetirement(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			initial := applicationConnectionJournalFixture(t, engine)
			old := initial[7].ApplicationStage
			in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*initial[0].ApplicationDatabase)
			if err != nil {
				t.Fatal(err)
			}
			healthy := domain.HostingHealthy{Snapshot: *initial[0].ApplicationSnapshot, Release: initial[1].Plan.Release, OperationID: in.OperationID, ContainerID: old.Candidate.ContainerID, PlanHash: old.PlanHash}
			in.Members = []domain.HostingDatabaseNetworkMemberProof{{Binding: in.Binding, EndpointPlan: old.AttachPlan, Receipt: *old.EndpointReceipt, ApplicationHealthy: healthy, DatabasePlan: in.BindingDatabasePlan, ApplicationProjectID: in.ApplicationProjectID, DatabaseProjectID: in.DatabaseProjectID}}
			in.OperationID, in.CreateNonce, in.MembershipRevision = "application_replace", strings.Repeat("b", 32), old.AttachPlan.MembershipRevision
			proof, err := domain.NewHostingApplicationDatabaseExecutionProof(in)
			if err != nil {
				t.Fatal(err)
			}
			s := healthy.Snapshot
			s.HostPort++
			p, err := domain.CompileHostingApplicationDatabasePlan(s, healthy.Release, &healthy, nil, in)
			if err != nil {
				t.Fatal(err)
			}
			candidate := old.Candidate
			candidate.OperationID, candidate.PlanHash, candidate.CreateNonce, candidate.ContainerID = in.OperationID, p.Hash, in.CreateNonce, strings.Repeat("5", 64)
			candidate.Hash = candidate.DocumentHash()
			attach, err := domain.CompileHostingApplicationDatabaseAttachPlan(p, in, candidate)
			if err != nil {
				t.Fatal(err)
			}
			entries := make([]JournalHosting, len(initial))
			for i, entry := range initial {
				e := cloneJournalHosting(entry)
				e.ID = "replacement_" + e.ID
				e.ApplicationDatabase, e.ApplicationSnapshot, e.OperationID = &proof, &s, in.OperationID
				e.Attempt.RunID = "run_" + in.OperationID
				a := domain.HostingAuthority{OperationID: in.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
				if e.Prepared != nil {
					e.Prepared.HostingAuthority, e.Prepared.ReceiptID = a, e.ID
				}
				if e.Plan != nil {
					e.Plan = &p
				}
				if e.ApplicationCandidate != nil {
					e.ApplicationCandidate = &candidate
				}
				if stage := e.ApplicationStage; stage != nil {
					stage.ReceiptID, stage.OperationID, stage.PlanHash, stage.Candidate, stage.AttachPlan = e.ID, in.OperationID, p.Hash, candidate, attach
					if r := stage.EndpointReceipt; r != nil {
						r.OperationID, r.PlanHash, r.MembershipRevision = in.OperationID, attach.Hash, attach.MembershipRevision
						if r.Intent != nil {
							r.Intent.Target = attach.Target
						}
						if r.Observation != nil {
							r.Observation.Endpoint.Target = attach.Target
							r.Observation.Endpoint.EndpointID = strings.Repeat("6", 64)
						}
						r.Hash = r.DocumentHash()
					}
					stage.Hash = stage.DocumentHash()
				}
				if e.Result != nil {
					e.Result.HostingAuthority, e.Result.ReceiptID = a, e.ID
					e.Result.Result.ContainerID, e.Result.Result.PreviousContainerID, e.Result.Result.PlanHash = candidate.ContainerID, healthy.ContainerID, p.Hash
				}
				entries[i] = e
			}
			if validateApplicationDatabaseJournalChain(entries) == nil {
				t.Fatal("ready replacement completed without predecessor retirement")
			}
			retire := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: in.OperationID, BindingHash: in.Binding.Hash, MembershipRevision: in.MembershipRevision + 2, Action: "retire", Target: old.AttachPlan.Target, ExpectedEndpointID: old.EndpointReceipt.Observation.Endpoint.EndpointID}
			retire.Hash = retire.DocumentHash()
			chain := append([]JournalHosting(nil), entries[:8]...)
			for _, phase := range []string{"retire_intent", "detached"} {
				r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "retirement_" + phase, OperationID: in.OperationID, PlanHash: retire.Hash, BindingHash: in.Binding.Hash, MembershipRevision: retire.MembershipRevision, Phase: phase}
				if phase == "retire_intent" {
					r.Intent = &domain.HostingDatabaseBindingEndpoint{Target: retire.Target, EndpointID: retire.ExpectedEndpointID}
				} else {
					r.Observation = &domain.HostingDatabaseBindingEndpointObservation{Present: false, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: retire.Target, EndpointID: retire.ExpectedEndpointID}}
				}
				r.Hash = r.DocumentHash()
				e := entries[7]
				e.ID, e.Phase, e.ApplicationRetirementPlan, e.ApplicationRetirement = r.ReceiptID, "application_retirement", &retire, &r
				chain = append(chain, e)
			}
			chain = append(chain, entries[8])
			if err := validateApplicationDatabaseJournalChain(chain); err != nil {
				t.Fatal("exact retirement chain rejected", err)
			}
			j, _ := openBuildJournal(t)
			defer func() { _ = j.Close() }()
			for _, e := range chain {
				if _, err := j.AppendHosting(e); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
