package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func databaseBindingFixture(t *testing.T, engine string) (HostingDatabaseBinding, HostingHealthy, HostingApplyPlan, HostingDatabaseBindingEndpointPlan) {
	t.Helper()
	version := HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = HostingDatabaseRedisRecipe
	}
	s, release, storage, db := databaseExecutionFixture(t, engine, version, 1)
	database, err := CompileHostingDatabasePlan(s, release, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	app := HostingSnapshot{ResourceID: "app", ResourceGeneration: 3, BindingID: "app_binding", DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, PolicyVersion: HostingRuntimePolicyVersion, RuntimeConfiguration: HostingRuntimeConfigurationRef{Generation: 2, Hash: "sha256:" + strings.Repeat("d", 64)}}
	appRelease := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: release.DaemonID}
	appRelease.Hash = HostingDocumentHash(appRelease)
	h := HostingHealthy{OperationID: "app_deploy", ContainerID: strings.Repeat("c", 64), PlanHash: "sha256:" + strings.Repeat("e", 64), Snapshot: app, Release: appRelease}
	b := HostingDatabaseBinding{Version: HostingDatabaseBindingVersion, ID: HostingDatabaseBindingID(app.BindingID, s.BindingID), Generation: 1, ApplicationResourceID: app.ResourceID, ApplicationResourceGeneration: app.ResourceGeneration, ApplicationBindingID: app.BindingID, DatabaseResourceID: s.ResourceID, DatabaseResourceGeneration: s.ResourceGeneration, DatabaseBindingID: s.BindingID, ProjectID: "project", DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, Database: s.Database, NetworkName: HostingDatabaseNetworkName(s.BindingID), Alias: HostingDatabaseBindingAlias(s.BindingID), EnvironmentNames: HostingDatabaseBindingEnvironmentNames(engine), RuntimeConfiguration: app.RuntimeConfiguration}
	for _, ref := range db.Credentials {
		if ref.Role == "app" {
			b.Credential = ref
		}
	}
	b.Hash = b.DocumentHash()
	p := HostingDatabaseBindingEndpointPlan{Version: HostingDatabaseBindingVersion, OperationID: "binding_attach", BindingHash: b.Hash, MembershipRevision: 7, Action: "attach", Target: HostingDatabaseBindingEndpointTarget{ContainerID: h.ContainerID, ContainerOperationID: h.OperationID, ImageID: h.Release.ImageID, ReleaseHash: h.Release.Hash, ContainerPlanHash: h.PlanHash, NetworkID: strings.Repeat("f", 64), NetworkName: b.NetworkName, DaemonID: h.Release.DaemonID}}
	p.Hash = p.DocumentHash()
	if b.ValidateTargets("project", app, "project", database) != nil || p.ValidateApplication(b, h) != nil {
		t.Fatal("invalid database binding fixture")
	}
	return b, h, database, p
}

func databaseBindingReceipt(p HostingDatabaseBindingEndpointPlan, phase string) HostingDatabaseBindingEndpointReceipt {
	r := HostingDatabaseBindingEndpointReceipt{Version: HostingDatabaseBindingVersion, ReceiptID: "receipt_" + phase, OperationID: p.OperationID, PlanHash: p.Hash, BindingHash: p.BindingHash, MembershipRevision: p.MembershipRevision, Phase: phase}
	switch phase {
	case "attach_intent", "retire_intent":
		r.Intent = &HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: p.ExpectedEndpointID}
	case "attached", "detached":
		id := p.ExpectedEndpointID
		if id == "" {
			id = strings.Repeat("1", 64)
		}
		r.Observation = &HostingDatabaseBindingEndpointObservation{Endpoint: HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: id}, Present: phase == "attached"}
	case "uncertain":
		r.FailureCode = "mutation_unconfirmed"
	}
	r.Hash = r.DocumentHash()
	return r
}

func TestHostingDatabaseBindingProfilesAndStableIdentity(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			b, h, database, p := databaseBindingFixture(t, engine)
			want := []string{"PGDATABASE", "PGHOST", "PGPASSWORD", "PGPORT", "PGUSER"}
			if engine == "redis" {
				want = []string{"REDIS_HOST", "REDIS_PASSWORD", "REDIS_PORT", "REDIS_USERNAME"}
			}
			if !slices.Equal(b.EnvironmentNames, want) || len(b.Alias) > 63 || strings.ContainsAny(b.Alias, "_/.") {
				t.Fatal("binding did not use fixed environment and DNS alias profile")
			}
			names := HostingDatabaseBindingEnvironmentNames(engine)
			names[0] = "foreign"
			if !slices.Equal(HostingDatabaseBindingEnvironmentNames(engine), want) {
				t.Fatal("environment profiles share mutable storage")
			}
			before := b
			b.Generation++
			b.ApplicationResourceGeneration++
			b.DatabaseResourceGeneration++
			b.RuntimeConfiguration.Generation++
			b.Hash = b.DocumentHash()
			if b.Validate() != nil || b.ID != before.ID || b.NetworkName != before.NetworkName || b.Alias != before.Alias || b.Credential != before.Credential || b.Hash == before.Hash {
				t.Fatal("redeploy generation changed stable identity or failed to change desired hash")
			}
			if before.ValidateTargets("foreign", h.Snapshot, "project", database) == nil || before.ValidateTargets("project", h.Snapshot, "foreign", database) == nil || p.Validate(b) == nil {
				t.Fatal("cross-project or changed binding hash accepted")
			}
			for name, value := range map[string]any{"binding": before, "plan": p} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if name == "binding" {
					var decoded HostingDatabaseBinding
					if json.Unmarshal(raw, &decoded) != nil || decoded.Hash != before.Hash {
						t.Fatal("desired binding round trip failed")
					}
				} else {
					var decoded HostingDatabaseBindingEndpointPlan
					if json.Unmarshal(raw, &decoded) != nil || decoded != p {
						t.Fatal("endpoint plan round trip failed")
					}
				}
			}
		})
	}
	if HostingDatabaseBindingID("same", "same") != "" || HostingDatabaseBindingID("/foreign", "db") != "" || HostingDatabaseBindingAlias("") != "" || HostingDatabaseBindingEnvironmentNames("mysql") != nil {
		t.Fatal("invalid identity or unsupported profile accepted")
	}
}

func TestHostingDatabaseBindingRejectsSelfRehashedSubstitution(t *testing.T) {
	b, h, database, p := databaseBindingFixture(t, "postgresql")
	for name, change := range map[string]func(*HostingDatabaseBinding){
		"version":        func(v *HostingDatabaseBinding) { v.Version = "v2" },
		"identity":       func(v *HostingDatabaseBinding) { v.ID = "foreign" },
		"same_resource":  func(v *HostingDatabaseBinding) { v.ApplicationResourceID = v.DatabaseResourceID },
		"same_binding":   func(v *HostingDatabaseBinding) { v.ApplicationBindingID = v.DatabaseBindingID; v.ID = "" },
		"bad_id":         func(v *HostingDatabaseBinding) { v.ApplicationResourceID = "/foreign" },
		"generation":     func(v *HostingDatabaseBinding) { v.Generation = 0 },
		"runtime_zero":   func(v *HostingDatabaseBinding) { v.RuntimeConfiguration = HostingRuntimeConfigurationRef{} },
		"runtime_hash":   func(v *HostingDatabaseBinding) { v.RuntimeConfiguration.Hash = "foreign" },
		"config_hash":    func(v *HostingDatabaseBinding) { v.Database.ConfigurationHash = "foreign" },
		"recipe":         func(v *HostingDatabaseBinding) { v.Database.RecipeVersion = "latest" },
		"admin":          func(v *HostingDatabaseBinding) { v.Credential.Role = "admin" },
		"probe":          func(v *HostingDatabaseBinding) { v.Credential.Role = "probe" },
		"username":       func(v *HostingDatabaseBinding) { v.Credential.Username = "postgres" },
		"secret_id":      func(v *HostingDatabaseBinding) { v.Credential.SecretID = HostingDatabaseSecretID("foreign", "app") },
		"secret_version": func(v *HostingDatabaseBinding) { v.Credential.Version = 0 },
		"network":        func(v *HostingDatabaseBinding) { v.NetworkName = "bridge" },
		"alias":          func(v *HostingDatabaseBinding) { v.Alias = "localhost" },
		"environment":    func(v *HostingDatabaseBinding) { v.EnvironmentNames = []string{"DATABASE_URL"} },
		"environment_order": func(v *HostingDatabaseBinding) {
			v.EnvironmentNames = slices.Clone(v.EnvironmentNames)
			slices.Reverse(v.EnvironmentNames)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := b
			change(&v)
			v.Hash = v.DocumentHash()
			if v.Validate() == nil {
				t.Fatal("self-rehashed invalid desired binding accepted")
			}
		})
	}
	for name, change := range map[string]func(*HostingDatabaseBinding){
		"destination":         func(v *HostingDatabaseBinding) { v.DestinationID = "other" },
		"server":              func(v *HostingDatabaseBinding) { v.ServerID = "other" },
		"runner":              func(v *HostingDatabaseBinding) { v.RunnerID = "other" },
		"runtime":             func(v *HostingDatabaseBinding) { v.RuntimeConfiguration.Generation++ },
		"app_generation":      func(v *HostingDatabaseBinding) { v.ApplicationResourceGeneration++ },
		"database_generation": func(v *HostingDatabaseBinding) { v.DatabaseResourceGeneration++ },
		"credential_version":  func(v *HostingDatabaseBinding) { v.Credential.Version++ },
		"configuration":       func(v *HostingDatabaseBinding) { v.Database.ConfigurationHash = "sha256:" + strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			v := b
			change(&v)
			v.Hash = v.DocumentHash()
			if v.Validate() != nil || v.ValidateTargets("project", h.Snapshot, "project", database) == nil {
				t.Fatal("structurally valid substitution bypassed exact saved target linkage")
			}
		})
	}
	for name, change := range map[string]func(*HostingDatabaseBindingEndpointPlan){
		"container": func(v *HostingDatabaseBindingEndpointPlan) { v.Target.ContainerID = strings.Repeat("0", 64) },
		"image":     func(v *HostingDatabaseBindingEndpointPlan) { v.Target.ImageID = "sha256:" + strings.Repeat("0", 64) },
		"release": func(v *HostingDatabaseBindingEndpointPlan) {
			v.Target.ReleaseHash = "sha256:" + strings.Repeat("0", 64)
		},
		"container_plan": func(v *HostingDatabaseBindingEndpointPlan) {
			v.Target.ContainerPlanHash = "sha256:" + strings.Repeat("0", 64)
		},
		"operation": func(v *HostingDatabaseBindingEndpointPlan) { v.Target.ContainerOperationID = "other" },
		"daemon":    func(v *HostingDatabaseBindingEndpointPlan) { v.Target.DaemonID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			v := p
			change(&v)
			v.Hash = v.DocumentHash()
			if v.ValidateApplication(b, h) == nil {
				t.Fatal("foreign application container identity accepted")
			}
		})
	}
}

func TestHostingDatabaseBindingEndpointReceiptsAndReplay(t *testing.T) {
	b, _, _, attach := databaseBindingFixture(t, "redis")
	for _, action := range []string{"attach", "retire"} {
		t.Run(action, func(t *testing.T) {
			p := attach
			p.Action = action
			if action == "retire" {
				p.OperationID = "binding_retire"
				p.MembershipRevision++
				p.ExpectedEndpointID = strings.Repeat("1", 64)
			}
			p.Hash = p.DocumentHash()
			intent := databaseBindingReceipt(p, action+"_intent")
			phase := "attached"
			if action == "retire" {
				phase = "detached"
			}
			settled := databaseBindingReceipt(p, phase)
			uncertain := databaseBindingReceipt(p, "uncertain")
			if intent.ValidateTransition(nil, p, b) != nil || settled.ValidateTransition(&intent, p, b) != nil || uncertain.ValidateTransition(&intent, p, b) != nil || settled.ValidateTransition(&settled, p, b) != nil || settled.ValidateTransition(nil, p, b) == nil || settled.ValidateTransition(&uncertain, p, b) == nil || uncertain.ValidateTransition(&settled, p, b) == nil {
				t.Fatal("receipt transition or exact historical replay violated intent/settlement rules")
			}
			if _, ok := intent.ObservedMembership(p, b); ok {
				t.Fatal("intent counted as observed endpoint ownership")
			}
			if _, ok := uncertain.ObservedMembership(p, b); ok {
				t.Fatal("uncertainty counted as observed endpoint ownership")
			}
			if _, ok := settled.ObservedMembership(p, b); ok != (action == "attach") {
				t.Fatal("observation did not distinguish attached from detached")
			}
			for _, r := range []HostingDatabaseBindingEndpointReceipt{intent, settled, uncertain} {
				raw, err := json.Marshal(r)
				var decoded HostingDatabaseBindingEndpointReceipt
				if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Validate(p, b) != nil {
					t.Fatal("receipt round trip failed")
				}
				hold := HostingDatabaseBindingNetworkHold{Purpose: HostingDatabaseBindingHoldPurpose, OperationID: p.OperationID, PlanHash: p.Hash, BindingHash: b.Hash, MembershipRevision: p.MembershipRevision, ReceiptID: r.ReceiptID, ReceiptHash: r.Hash}
				if (hold.Validate(p, b, r) == nil) != (r.Phase != phase) {
					t.Fatal("network effect hold disagreed with unsettled receipt")
				}
				hold.MembershipRevision++
				if hold.Validate(p, b, r) == nil {
					t.Fatal("stale membership revision held another effect")
				}
				if strings.Contains(string(raw), `"lease_id"`) || strings.Contains(string(raw), `"fence"`) || strings.Contains(string(raw), `"password"`) {
					t.Fatal("persisted endpoint evidence expanded authority or secrets")
				}
			}
			changed := settled
			changed.ReceiptID = intent.ReceiptID
			changed.Hash = changed.DocumentHash()
			if changed.ValidateTransition(&intent, p, b) == nil {
				t.Fatal("changed body reused receipt identity")
			}
			request := HostingDatabaseBindingEndpointRequest{HostingAuthority: HostingAuthority{OperationID: p.OperationID, RunID: "run_binding", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Receipt: intent}
			if request.Validate(p, b) != nil {
				t.Fatal("existing authority tuple shape rejected")
			}
			request.Attempt = 0
			if request.Validate(p, b) == nil {
				t.Fatal("invalid authority tuple shape accepted")
			}
		})
	}
}

func TestHostingDatabaseBindingClosedJSON(t *testing.T) {
	input := `{"database_resource_id":"database","expected_resource_generation":1,"expected_runtime_generation":0,"expected_database_generation":1,"request_id":"` + strings.Repeat("a", 32) + `"}`
	var in HostingDatabaseBindingInput
	if json.Unmarshal([]byte(input), &in) != nil {
		t.Fatal("closed selection input rejected")
	}
	for name, raw := range map[string]string{
		"unknown":           strings.TrimSuffix(input, "}") + `,"network":"bridge"}`,
		"secret":            strings.TrimSuffix(input, "}") + `,"credential":{"secret_id":"private_canary"}}`,
		"duplicate":         strings.TrimSuffix(input, "}") + `,"request_id":"` + strings.Repeat("a", 32) + `"}`,
		"case":              strings.Replace(input, "database_resource_id", "Database_Resource_Id", 1),
		"null_runtime":      strings.Replace(input, `"expected_runtime_generation":0`, `"expected_runtime_generation":null`, 1),
		"missing_runtime":   strings.Replace(input, `"expected_runtime_generation":0,`, "", 1),
		"negative":          strings.Replace(input, `"expected_runtime_generation":0`, `"expected_runtime_generation":-1`, 1),
		"uppercase_request": strings.Replace(input, strings.Repeat("a", 32), strings.Repeat("A", 32), 1),
		"fractional":        strings.Replace(input, `"expected_database_generation":1`, `"expected_database_generation":1.5`, 1),
		"trailing":          input + `{}`,
		"unicode":           strings.Replace(input, `"database"`, `"\ud800"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(raw), &in); err == nil || strings.Contains(err.Error(), "private_canary") {
				t.Fatal("invalid selection accepted or diagnostic exposed a protected value")
			}
		})
	}
	b, _, _, p := databaseBindingFixture(t, "postgresql")
	intent := databaseBindingReceipt(p, "attach_intent")
	attached := databaseBindingReceipt(p, "attached")
	for name, change := range map[string]func(*HostingDatabaseBindingEndpointReceipt){
		"unknown_phase":           func(r *HostingDatabaseBindingEndpointReceipt) { r.Phase = "connected" },
		"intent_with_observation": func(r *HostingDatabaseBindingEndpointReceipt) { r.Observation = attached.Observation },
		"intent_endpoint":         func(r *HostingDatabaseBindingEndpointReceipt) { r.Intent.EndpointID = strings.Repeat("1", 64) },
		"uncertain_with_intent": func(r *HostingDatabaseBindingEndpointReceipt) {
			r.Phase = "uncertain"
			r.FailureCode = "mutation_unconfirmed"
		},
		"freeform_failure": func(r *HostingDatabaseBindingEndpointReceipt) {
			r.Intent = nil
			r.Phase = "uncertain"
			r.FailureCode = "private_canary"
		},
		"wrong_operation": func(r *HostingDatabaseBindingEndpointReceipt) { r.OperationID = "foreign" },
		"wrong_plan":      func(r *HostingDatabaseBindingEndpointReceipt) { r.PlanHash = "sha256:" + strings.Repeat("0", 64) },
		"wrong_revision":  func(r *HostingDatabaseBindingEndpointReceipt) { r.MembershipRevision++ },
		"wrong_binding":   func(r *HostingDatabaseBindingEndpointReceipt) { r.BindingHash = "sha256:" + strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			r := databaseBindingReceipt(p, "attach_intent")
			change(&r)
			r.Hash = r.DocumentHash()
			if r.Validate(p, b) == nil {
				t.Fatal("ambiguous or substituted receipt accepted")
			}
		})
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.TrimSuffix(string(raw), "}") + `,"observation":null}`,
		strings.TrimSuffix(string(raw), "}") + `,"failure_code":""}`,
		strings.Replace(string(raw), `"container_id":`, `"Container_ID":`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"authority":{"fence":"private_canary"}}`,
	} {
		var decoded HostingDatabaseBindingEndpointReceipt
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatal("unknown or ambiguous receipt payload accepted")
		}
	}
	raw, err = json.Marshal(attached)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingDatabaseBindingEndpointReceipt
	if json.Unmarshal([]byte(strings.Replace(string(raw), `,"present":true`, "", 1)), &decoded) == nil {
		t.Fatal("observation without explicit presence accepted")
	}
	if DecodeHostingDatabaseBindingJSON([]byte(strings.Repeat(" ", HostingDatabaseBindingJSONLimit)+"{}"), &decoded) == nil {
		t.Fatal("oversized binding document accepted")
	}
}

func TestHostingDatabaseBindingPlanAndAuthorityBoundaries(t *testing.T) {
	b, _, database, p := databaseBindingFixture(t, "postgresql")
	for name, change := range map[string]func(*HostingDatabaseBindingEndpointPlan){
		"version":                  func(v *HostingDatabaseBindingEndpointPlan) { v.Version = "v2" },
		"revision":                 func(v *HostingDatabaseBindingEndpointPlan) { v.MembershipRevision = 0 },
		"network":                  func(v *HostingDatabaseBindingEndpointPlan) { v.Target.NetworkName = "bridge" },
		"short_network_id":         func(v *HostingDatabaseBindingEndpointPlan) { v.Target.NetworkID = "short" },
		"short_container_id":       func(v *HostingDatabaseBindingEndpointPlan) { v.Target.ContainerID = "short" },
		"uppercase_id":             func(v *HostingDatabaseBindingEndpointPlan) { v.Target.NetworkID = strings.Repeat("F", 64) },
		"attach_existing_endpoint": func(v *HostingDatabaseBindingEndpointPlan) { v.ExpectedEndpointID = strings.Repeat("1", 64) },
		"retire_unknown_endpoint":  func(v *HostingDatabaseBindingEndpointPlan) { v.Action = "retire" },
		"unknown_action":           func(v *HostingDatabaseBindingEndpointPlan) { v.Action = "connect" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			change(&changed)
			changed.Hash = changed.DocumentHash()
			if changed.Validate(b) == nil {
				t.Fatal("invalid or ambiguous endpoint plan accepted")
			}
		})
	}
	r := databaseBindingReceipt(p, "attach_intent")
	in := HostingDatabaseBindingEndpointRequest{HostingAuthority: HostingAuthority{OperationID: p.OperationID, RunID: "run_binding", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Receipt: r}
	for name, change := range map[string]func(*HostingDatabaseBindingEndpointRequest){
		"operation": func(v *HostingDatabaseBindingEndpointRequest) { v.OperationID = "other" },
		"run":       func(v *HostingDatabaseBindingEndpointRequest) { v.RunID = "/other" },
		"lease":     func(v *HostingDatabaseBindingEndpointRequest) { v.LeaseID = "" },
		"fence":     func(v *HostingDatabaseBindingEndpointRequest) { v.Fence = "" },
		"attempt":   func(v *HostingDatabaseBindingEndpointRequest) { v.Attempt = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := in
			change(&changed)
			if changed.Validate(p, b) == nil {
				t.Fatal("invalid current authority shape accepted")
			}
		})
	}
	raw, err := json.Marshal(in)
	var decoded HostingDatabaseBindingEndpointRequest
	if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Validate(p, b) != nil {
		t.Fatal("existing hosting authority request did not round trip")
	}
	for _, bad := range []string{
		strings.TrimSuffix(string(raw), "}") + `,"token":"protected_canary"}`,
		strings.TrimSuffix(string(raw), "}") + `,"attempt":1}`,
		strings.Replace(string(raw), `"attempt":1`, `"attempt":null`, 1),
	} {
		if err := json.Unmarshal([]byte(bad), &decoded); err == nil || strings.Contains(err.Error(), "protected_canary") {
			t.Fatal("noncanonical authority document accepted or protected diagnostic leaked")
		}
	}
	// The new contracts do not modify or expand the old database apply family.
	before := HostingDocumentHash(database)
	database.Snapshot.RuntimeConfiguration = b.RuntimeConfiguration
	if ValidateHostingDatabasePlan(database) == nil {
		t.Fatal("v1 database validator admitted application runtime configuration")
	}
	database.Snapshot.RuntimeConfiguration = HostingRuntimeConfigurationRef{}
	if ValidateHostingDatabasePlan(database) != nil || HostingDocumentHash(database) != before {
		t.Fatal("v1 database compiler or hash changed")
	}
}
