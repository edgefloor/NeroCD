package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func applicationExecutionFixture(t *testing.T, engine string) (HostingOperation, HostingDatabaseUpgradeProof) {
	t.Helper()
	capture, upgrade := databaseUpgradeFixture(t, engine, false)
	upgraded := prepareDatabaseUpgrade(t, upgrade)
	s, r, old := applicationDatabaseFixture(t, engine, false)
	b := old.Binding
	db := capture.PreviousPlan
	b.Database = db.Snapshot.Database
	b.DatabaseResourceID = db.Snapshot.ResourceID
	b.DatabaseResourceGeneration = db.Snapshot.ResourceGeneration
	b.DatabaseBindingID = db.Snapshot.BindingID
	b.DestinationID = db.Snapshot.DestinationID
	b.ServerID = db.Snapshot.ServerID
	b.RunnerID = db.Snapshot.RunnerID
	b.Hash = b.DocumentHash()
	s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: b.ID, BindingHash: b.Hash}
	original := s
	original.ApplicationDatabase = HostingApplicationDatabaseRef{}
	original.PolicyVersion = HostingRuntimePolicyVersion
	healthy := capture.PreviousHealthy
	healthy.OperationID = "database_upgraded"
	healthy.PlanHash = upgraded.Plan.Hash
	healthy.Snapshot = upgraded.Plan.Snapshot
	in := HostingApplicationDatabasePlanInput{OperationID: "application_first", CreateNonce: strings.Repeat("a", 32), Binding: b, ApplicationProjectID: b.ProjectID, DatabaseProjectID: b.ProjectID, BindingApplicationSnapshot: original, BindingDatabasePlan: db, DatabasePlan: *upgraded.Plan, DatabaseHealthy: healthy, Origin: capture.OriginReceipt.Origin, MembershipRevision: 1, Members: []HostingDatabaseNetworkMemberProof{}, Retired: []HostingDatabaseNetworkRetirementProof{}}
	proof, err := NewHostingApplicationDatabaseExecutionProof(in)
	if err != nil {
		t.Fatal("execution proof fixture", err)
	}
	p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
	if err != nil {
		t.Fatal("application fixture", err)
	}
	op := HostingOperation{ID: in.OperationID, ProjectID: b.ProjectID, ResourceID: b.ApplicationResourceID, BindingID: b.ApplicationBindingID, Snapshot: s, Release: &r, Plan: &p, ApplicationDatabase: &proof}
	if ValidateHostingApplicationDatabaseOperation(op) != nil {
		t.Fatal("invalid execution operation fixture")
	}
	return op, capture
}

func executionCandidate(t *testing.T, op HostingOperation) (HostingApplicationDatabasePlanInput, HostingApplicationDatabaseCandidate, HostingDatabaseBindingEndpointPlan) {
	t.Helper()
	in, err := HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	if err != nil {
		t.Fatal(err)
	}
	c := HostingApplicationDatabaseCandidate{Version: HostingApplicationDatabaseCandidateVersion, OperationID: op.ID, PlanHash: op.Plan.Hash, CreateNonce: in.CreateNonce, ContainerID: strings.Repeat("3", 64), ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, DefaultNetworkID: strings.Repeat("4", 64)}
	if len(in.Members) > 0 {
		c.ContainerID = strings.Repeat("5", 64)
	}
	c.Hash = c.DocumentHash()
	ep, err := CompileHostingApplicationDatabaseAttachPlan(*op.Plan, in, c)
	if err != nil {
		t.Fatal(err)
	}
	return in, c, ep
}

func TestApplicationDatabaseExecutionInitialAndBoundReplacement(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			op, _ := applicationExecutionFixture(t, engine)
			in, c, ep := executionCandidate(t, op)
			if op.Plan.PreviousHealthy != nil || len(op.Plan.ApplicationDatabase.PreviousMembers) != 0 || ep.Target.ContainerID != c.ContainerID {
				t.Fatal("initial deployment acquired healthy candidate")
			}
			h := HostingHealthy{Snapshot: op.Snapshot, Release: *op.Release, OperationID: op.ID, ContainerID: c.ContainerID, PlanHash: op.Plan.Hash}
			attached := databaseBindingReceipt(ep, "attached")
			in.Members = []HostingDatabaseNetworkMemberProof{{Binding: in.Binding, EndpointPlan: ep, Receipt: attached, ApplicationHealthy: h, DatabasePlan: in.BindingDatabasePlan, ApplicationProjectID: in.ApplicationProjectID, DatabaseProjectID: in.DatabaseProjectID}}
			in.MembershipRevision = ep.MembershipRevision
			in.OperationID = "application_second"
			in.CreateNonce = strings.Repeat("b", 32)
			proof, err := NewHostingApplicationDatabaseExecutionProof(in)
			if err != nil {
				t.Fatal("bound replacement proof rejected", err)
			}
			s := op.Snapshot
			s.HostPort++
			p, err := CompileHostingApplicationDatabasePlan(s, *op.Release, &h, nil, in)
			if err != nil {
				t.Fatal("bound replacement plan rejected", err)
			}
			replacement := op
			replacement.ID = in.OperationID
			replacement.Snapshot = s
			replacement.Plan = &p
			replacement.ApplicationDatabase = &proof
			if ValidateHostingApplicationDatabaseOperation(replacement) != nil {
				t.Fatal("bound replacement rejected")
			}
			_, next, _ := executionCandidate(t, replacement)
			next.ContainerID = strings.Repeat("5", 64)
			next.Hash = next.DocumentHash()
			attach, err := CompileHostingApplicationDatabaseAttachPlan(p, in, next)
			if err != nil || attach.MembershipRevision != 3 {
				t.Fatal("replacement attach rejected", err)
			}
			// A historical running app with no binding must never enter this execution family.
			in.Members[0].ApplicationHealthy.Snapshot.PolicyVersion = HostingRuntimePolicyVersion
			in.Members[0].ApplicationHealthy.Snapshot.ApplicationDatabase = HostingApplicationDatabaseRef{}
			if _, err := NewHostingApplicationDatabaseExecutionProof(in); err == nil {
				t.Fatal("running unbound app admitted")
			}
			raw, err := json.Marshal(proof)
			var persisted HostingApplicationDatabaseExecutionProof
			if err != nil || json.Unmarshal(raw, &persisted) != nil || persisted.Hash != proof.Hash {
				t.Fatal("proof round trip failed", err)
			}
			restored, err := HostingApplicationDatabaseExecutionPlanInput(persisted)
			if err != nil || ValidateHostingApplicationDatabasePlan(p, restored) != nil {
				t.Fatal("serialized replacement evidence rejected", err)
			}
		})
	}
}

func TestApplicationDatabaseCandidateRejectsForgedTargets(t *testing.T) {
	op, _ := applicationExecutionFixture(t, "postgresql")
	in, c, ep := executionCandidate(t, op)
	for name, change := range map[string]func(*HostingApplicationDatabaseCandidate){
		"operation":          func(v *HostingApplicationDatabaseCandidate) { v.OperationID = "other" },
		"plan":               func(v *HostingApplicationDatabaseCandidate) { v.PlanHash = in.Binding.Hash },
		"nonce":              func(v *HostingApplicationDatabaseCandidate) { v.CreateNonce = strings.Repeat("b", 32) },
		"database_container": func(v *HostingApplicationDatabaseCandidate) { v.ContainerID = in.DatabaseHealthy.ContainerID },
		"missing_container":  func(v *HostingApplicationDatabaseCandidate) { v.ContainerID = "" },
		"image":              func(v *HostingApplicationDatabaseCandidate) { v.ImageID = in.Binding.Hash },
		"daemon":             func(v *HostingApplicationDatabaseCandidate) { v.DaemonID = "foreign" },
		"private_bridge":     func(v *HostingApplicationDatabaseCandidate) { v.DefaultNetworkID = in.Origin.NetworkID },
	} {
		t.Run(name, func(t *testing.T) {
			v := c
			change(&v)
			v.Hash = v.DocumentHash()
			if _, err := CompileHostingApplicationDatabaseAttachPlan(*op.Plan, in, v); err == nil {
				t.Fatal("forged candidate accepted")
			}
		})
	}
	for name, change := range map[string]func(*HostingDatabaseBindingEndpointPlan){"revision": func(v *HostingDatabaseBindingEndpointPlan) { v.MembershipRevision++ }, "action": func(v *HostingDatabaseBindingEndpointPlan) {
		v.Action = "retire"
		v.ExpectedEndpointID = strings.Repeat("f", 64)
	}, "target": func(v *HostingDatabaseBindingEndpointPlan) { v.Target.ContainerID = in.DatabaseHealthy.ContainerID }} {
		t.Run(name, func(t *testing.T) {
			v := ep
			change(&v)
			v.Hash = v.DocumentHash()
			if ValidateHostingApplicationDatabaseAttachPlan(v, *op.Plan, in, c) == nil {
				t.Fatal("rewritten attach accepted")
			}
		})
	}
	in.MembershipRevision = math.MaxInt64
	if _, err := CompileHostingApplicationDatabaseAttachPlan(*op.Plan, in, c); err == nil {
		t.Fatal("revision overflow accepted")
	}
}

func TestApplicationDatabaseExecutionClosedProofAndOperation(t *testing.T) {
	op, _ := applicationExecutionFixture(t, "postgresql")
	raw, err := json.Marshal(op.ApplicationDatabase)
	if err != nil {
		t.Fatal(err)
	}
	malformed := [][]byte{[]byte(`null`), []byte(`[]`), append(bytes.Clone(raw), []byte(`{}`)...), bytes.Replace(raw, []byte(`"version":`), []byte(`"Version":`), 1), bytes.Replace(raw, []byte(`"version":`), []byte(`"unknown":true,"version":`), 1), bytes.Replace(raw, []byte(`"version":`), []byte(`"version":"duplicate","version":`), 1), bytes.Replace(raw, []byte(`"members":[]`), []byte(`"members":null`), 1), append(append([]byte("{"), bytes.Repeat([]byte(" "), HostingApplicationDatabaseProofJSONLimit)...), raw[1:]...)}
	for i, bad := range malformed {
		if json.Unmarshal(bad, new(HostingApplicationDatabaseExecutionProof)) == nil {
			t.Fatalf("malformed proof accepted %d", i)
		}
	}
	for name, change := range map[string]func(*HostingApplicationDatabaseExecutionProof){"project": func(p *HostingApplicationDatabaseExecutionProof) { p.DatabaseProjectID = "foreign" }, "stale_revision": func(p *HostingApplicationDatabaseExecutionProof) { p.MembershipRevision++ }, "origin": func(p *HostingApplicationDatabaseExecutionProof) { p.Origin.NetworkID = strings.Repeat("f", 64) }, "v1_database": func(p *HostingApplicationDatabaseExecutionProof) { p.DatabasePlan = p.BindingDatabasePlan }, "healthy": func(p *HostingApplicationDatabaseExecutionProof) {
		p.DatabaseHealthy.PlanHash = p.BindingDatabasePlan.Hash
	}} {
		t.Run(name, func(t *testing.T) {
			var p HostingApplicationDatabaseExecutionProof
			if json.Unmarshal(raw, &p) != nil {
				t.Fatal("clone")
			}
			change(&p)
			p.Hash = p.DocumentHash()
			if p.Validate() == nil {
				t.Fatal("forged historical proof accepted")
			}
		})
	}
	queued := op
	queued.Plan = nil
	queued.Release = nil
	if ValidateHostingApplicationDatabaseOperation(queued) != nil {
		t.Fatal("queued operation rejected")
	}
	queued.PreparedReceipt = &HostingReceipt{}
	if ValidateHostingApplicationDatabaseOperation(queued) == nil {
		t.Fatal("queued receipt without plan accepted")
	}
	mixed := op
	mixed.DatabaseUpgrade = &HostingDatabaseUpgradeProof{}
	if ValidateHostingApplicationDatabaseOperation(mixed) == nil {
		t.Fatal("mixed sidecars accepted")
	}
	omitted := op
	omitted.ApplicationDatabase = nil
	if ValidateHostingApplicationDatabaseOperation(omitted) == nil {
		t.Fatal("bound plan without proof accepted")
	}
	// Canonical omitted retention and persisted empty retention preserve hashes.
	rawPlan, _ := json.Marshal(op.Plan)
	rawPlan = bytes.Replace(rawPlan, []byte(`"snapshot":`), []byte(`"retained_releases":[],"snapshot":`), 1)
	var plan HostingApplyPlan
	if json.Unmarshal(rawPlan, &plan) != nil || ValidateHostingApplicationDatabasePlan(plan, op.ApplicationDatabase.input()) != nil {
		t.Fatal("empty persisted retention rejected")
	}
	old := HostingOperation{ID: "legacy"}
	b, _ := json.Marshal(old)
	if bytes.Contains(b, []byte(`"application_database"`)) || bytes.Contains(b, []byte(`"database_maintenance"`)) || ValidateHostingApplicationDatabaseOperation(old) != nil || ValidateHostingDatabaseMaintenanceOperation(old) != nil {
		t.Fatal("legacy omitted sidecars changed")
	}
}
