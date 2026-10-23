package domain

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func databaseExecutionFixture(t *testing.T, engine, version string, platformIndex int) (HostingSnapshot, HostingRelease, HostingStoragePlan, HostingDatabasePlan) {
	t.Helper()
	recipe, err := HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal(err)
	}
	id, volume := HostingStorageIdentity("binding_database")
	s := HostingSnapshot{ResourceID: "resource_database", ResourceGeneration: 1, BindingID: "binding_database", DestinationID: "destination_database", ServerID: "server_database", RunnerID: "runner_database", RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: HostingDatabasePolicyVersion, Storage: HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("c", 64)}, Database: HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}}
	credentials := slices.Clone(recipe.Credentials)
	for i := range credentials {
		credentials[i].SecretID = HostingDatabaseSecretID(s.ResourceID, credentials[i].Role)
		credentials[i].Version = int64(i + 1)
	}
	hash, err := HostingDatabaseConfigurationHash(s, credentials)
	if err != nil {
		t.Fatal(err)
	}
	s.Database.ConfigurationHash = hash
	p := recipe.Platforms[platformIndex]
	r := HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + p.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: p.OS, Architecture: p.Architecture, Variant: p.Variant, DaemonID: "daemon_database"}
	r.Hash = HostingDocumentHash(r)
	storage := HostingStoragePlan{Reference: s.Storage, VolumeName: volume, MountPath: recipe.MountPath, CreationOperationID: "hop_database_origin", DaemonID: r.DaemonID}
	storage.CreationNonce = HostingStorageCreationNonce(s.Storage, storage.CreationOperationID, r.DaemonID)
	db := HostingDatabasePlan{Reference: s.Database, NetworkName: HostingDatabaseNetworkName(s.BindingID), Credentials: credentials}
	return s, r, storage, db
}

func TestDatabaseExecutionCanonicalPlansAndOwnedCopies(t *testing.T) {
	for _, recipe := range [][2]string{{"postgresql", HostingDatabasePostgresRecipe}, {"redis", HostingDatabaseRedisRecipe}} {
		for _, platform := range []int{0, 1} {
			t.Run(recipe[0]+string(rune('0'+platform)), func(t *testing.T) {
				s, r, storage, db := databaseExecutionFixture(t, recipe[0], recipe[1], platform)
				plan, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
				if err != nil || !HostingPlanFamilyMatches(s, plan) || ValidateHostingDatabasePlan(plan) != nil || ValidateHostingSnapshotRelease(s, r) != nil || ValidateHostingStorageSnapshot(s) != nil {
					t.Fatalf("canonical private plan invalid: %+v %v", plan, err)
				}
				if plan.RetainPrevious || len(plan.RetainedReleases) != 0 || plan.Snapshot.HostPort != 0 || plan.CompilerVersion != HostingDatabaseCompilerVersion {
					t.Fatal("database plan acquired retained/public writer semantics")
				}
				slices.Reverse(db.Credentials)
				ordered, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
				if err != nil || ordered.Hash != plan.Hash {
					t.Fatal("caller order changed canonical credential identity", err)
				}
				db.Credentials[0].SecretID = "caller_mutated"
				if !reflect.DeepEqual(plan.Database.Credentials, ordered.Database.Credentials) || plan.Database.Credentials[0].SecretID == "caller_mutated" {
					t.Fatal("compiled database credentials alias caller input")
				}
				healthy := HostingHealthy{Snapshot: s, Release: r, OperationID: "hop_previous", ContainerID: strings.Repeat("d", 64), PlanHash: plan.Hash, Storage: &HostingHealthyStorage{Plan: storage, CreationPlanHash: plan.Hash}}
				next := s
				next.ResourceGeneration++
				withPrevious, err := CompileHostingDatabasePlan(next, r, &healthy, storage, *plan.Database)
				if err != nil || withPrevious.RetainPrevious || withPrevious.PreviousHealthy == nil || ValidateHostingDatabasePlan(withPrevious) != nil {
					t.Fatal("exact predecessor rejected or retained running", err)
				}
				healthy.Storage.CreationPlanHash = "caller_mutated"
				if withPrevious.PreviousHealthy.Storage.CreationPlanHash != plan.Hash {
					t.Fatal("compiled predecessor storage aliases caller input")
				}
				var decoded HostingApplyPlan
				raw, err := json.Marshal(withPrevious)
				if err != nil || json.Unmarshal(raw, &decoded) != nil || ValidateHostingDatabasePlan(decoded) != nil {
					t.Fatal("private plan did not roundtrip", err)
				}
			})
		}
	}
}

func TestDatabaseExecutionRejectsTamperedPlansAndMixedFamilies(t *testing.T) {
	s, r, storage, db := databaseExecutionFixture(t, "postgresql", HostingDatabasePostgresRecipe, 0)
	plan, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HostingApplyPlan){
		"host_port":          func(p *HostingApplyPlan) { p.Snapshot.HostPort = 15432 },
		"negative_host_port": func(p *HostingApplyPlan) { p.Snapshot.HostPort = -1 },
		"http_probe":         func(p *HostingApplyPlan) { p.Snapshot.Readiness = HostingReadiness{Path: "/", TimeoutSeconds: 1} },
		"wrong_port":         func(p *HostingApplyPlan) { p.Snapshot.ContainerPort = 6379 },
		"unknown_recipe": func(p *HostingApplyPlan) {
			p.Snapshot.Database.RecipeVersion = "latest"
			p.Database.Reference = p.Snapshot.Database
		},
		"index_as_release": func(p *HostingApplyPlan) { p.Release.CanonicalImage = p.Snapshot.ImageReference },
		"config_as_manifest": func(p *HostingApplyPlan) {
			p.Release.CanonicalImage = "docker.io/library/postgres@" + p.Release.ImageID
		},
		"wrong_architecture": func(p *HostingApplyPlan) { p.Release.Architecture = "arm64"; p.Release.Variant = "v8" },
		"wrong_variant":      func(p *HostingApplyPlan) { p.Release.Variant = "v8" },
		"wrong_os":           func(p *HostingApplyPlan) { p.Release.OS = "windows" },
		"foreign_image": func(p *HostingApplyPlan) {
			p.Release.CanonicalImage = "docker.io/library/redis@sha256:" + strings.Repeat("e", 64)
		},
		"foreign_index": func(p *HostingApplyPlan) {
			p.Snapshot.ImageReference = "docker.io/library/postgres@sha256:" + strings.Repeat("e", 64)
		},
		"runtime_environment": func(p *HostingApplyPlan) {
			p.Snapshot.RuntimeConfiguration = HostingRuntimeConfigurationRef{Generation: 1, Hash: s.Database.ConfigurationHash}
		},
		"local_build":        func(p *HostingApplyPlan) { p.Snapshot.LocalBuild.SourceGeneration = 1 },
		"route_continuity":   func(p *HostingApplyPlan) { p.Snapshot.StorageRouteContinuity.Hash = s.Database.ConfigurationHash },
		"wrong_policy":       func(p *HostingApplyPlan) { p.Snapshot.PolicyVersion = HostingOwnedStoragePolicyVersion },
		"wrong_compiler":     func(p *HostingApplyPlan) { p.CompilerVersion = HostingOwnedStorageCompilerVersion },
		"missing_reference":  func(p *HostingApplyPlan) { p.Snapshot.Database = HostingDatabaseExecutionRef{} },
		"missing_descriptor": func(p *HostingApplyPlan) { p.Database = nil },
		"missing_storage":    func(p *HostingApplyPlan) { p.Storage = nil },
		"foreign_storage":    func(p *HostingApplyPlan) { p.Storage.Reference.Hash = "sha256:" + strings.Repeat("e", 64) },
		"wrong_mount":        func(p *HostingApplyPlan) { p.Storage.MountPath = "/tmp/data" },
		"wrong_volume":       func(p *HostingApplyPlan) { p.Storage.VolumeName = "caller_volume" },
		"wrong_daemon":       func(p *HostingApplyPlan) { p.Release.DaemonID = "other_daemon" },
		"wrong_network":      func(p *HostingApplyPlan) { p.Database.NetworkName = "bridge" },
		"wrong_role":         func(p *HostingApplyPlan) { p.Database.Credentials[0].Role = "superuser" },
		"wrong_user":         func(p *HostingApplyPlan) { p.Database.Credentials[0].Username = "root" },
		"foreign_secret": func(p *HostingApplyPlan) {
			p.Database.Credentials[0].SecretID = HostingDatabaseSecretID("other_resource", "admin")
		},
		"secret_path":      func(p *HostingApplyPlan) { p.Database.Credentials[0].SecretID = "/run/secrets/password" },
		"missing_version":  func(p *HostingApplyPlan) { p.Database.Credentials[0].Version = 0 },
		"negative_version": func(p *HostingApplyPlan) { p.Database.Credentials[0].Version = -1 },
		"duplicate_role":   func(p *HostingApplyPlan) { p.Database.Credentials[1] = p.Database.Credentials[0] },
		"extra_credential": func(p *HostingApplyPlan) {
			p.Database.Credentials = append(p.Database.Credentials, p.Database.Credentials[0])
		},
		"noncanonical_order": func(p *HostingApplyPlan) { slices.Reverse(p.Database.Credentials) },
		"retain_writer":      func(p *HostingApplyPlan) { p.RetainPrevious = true },
		"retained_writer":    func(p *HostingApplyPlan) { p.RetainedReleases = []HostingHealthy{{OperationID: "other"}} },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(plan)
			var changed HostingApplyPlan
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("clone failed")
			}
			mutate(&changed)
			changed.Release.Hash = ""
			changed.Release.Hash = HostingDocumentHash(changed.Release)
			changed.Hash = ""
			changed.Hash = HostingDocumentHash(changed)
			if ValidateHostingDatabasePlan(changed) == nil || HostingPlanFamilyMatches(changed.Snapshot, changed) {
				t.Fatal("self-rehashed invalid database plan accepted")
			}
		})
	}
	for _, compile := range []func() (HostingApplyPlan, error){
		func() (HostingApplyPlan, error) { return CompileHostingPlan(s, r, nil) },
		func() (HostingApplyPlan, error) { return CompileRetainedHostingPlan(s, r, nil, nil) },
		func() (HostingApplyPlan, error) { return CompileHostingOwnedStoragePlan(s, r, nil, nil, storage) },
		func() (HostingApplyPlan, error) { return CompileLocalBuildHostingPlan(s, r, nil, nil) },
	} {
		if _, err := compile(); err == nil {
			t.Fatal("image compiler accepted private database family")
		}
	}
}

func TestDatabaseExecutionHashBindsImmutableConfiguration(t *testing.T) {
	s, _, _, db := databaseExecutionFixture(t, "redis", HostingDatabaseRedisRecipe, 0)
	base := s.Database.ConfigurationHash
	refs := slices.Clone(db.Credentials)
	refs[0].Version++
	changed, err := HostingDatabaseConfigurationHash(s, refs)
	if err != nil || changed == base {
		t.Fatal("credential version was not bound to configuration", err)
	}
	// Domain shape validation cannot prove whether a positive version exists.
	refs[0].Version = 999999
	if _, err = HostingDatabaseConfigurationHash(s, refs); err != nil {
		t.Fatal("domain attempted to infer secret availability", err)
	}
	other := s
	other.BindingID = "binding_other"
	changed, err = HostingDatabaseConfigurationHash(other, db.Credentials)
	if err != nil || changed == base || HostingDatabaseNetworkName(other.BindingID) == db.NetworkName {
		t.Fatal("binding/network alias different scope", err)
	}
	other = s
	other.Storage.Hash = "sha256:" + strings.Repeat("e", 64)
	changed, err = HostingDatabaseConfigurationHash(other, db.Credentials)
	if err != nil || changed == base {
		t.Fatal("storage was not bound", err)
	}
	if HostingDatabaseNetworkName(s.BindingID) != db.NetworkName || HostingDatabaseNetworkName("") != "" || HostingDatabaseNetworkName("/tmp/network") != "" {
		t.Fatal("network identity is unstable or accepts local paths")
	}
}

func TestDatabaseExecutionPredecessorScopeAndImageFamilyIsolation(t *testing.T) {
	s, r, storage, db := databaseExecutionFixture(t, "postgresql", HostingDatabasePostgresRecipe, 0)
	plan, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	healthy := HostingHealthy{Snapshot: s, Release: r, OperationID: "hop_previous", ContainerID: strings.Repeat("d", 64), PlanHash: plan.Hash, Storage: &HostingHealthyStorage{Plan: storage, CreationPlanHash: plan.Hash}}
	for _, mutate := range []func(*HostingHealthy){
		func(p *HostingHealthy) { p.Snapshot.BindingID = "foreign_binding" },
		func(p *HostingHealthy) { p.Snapshot.ResourceID = "foreign_resource" },
		func(p *HostingHealthy) { p.Snapshot.Database.ConfigurationHash = "sha256:" + strings.Repeat("e", 64) },
		func(p *HostingHealthy) { p.Release.DaemonID = "foreign_daemon" },
		func(p *HostingHealthy) {
			p.Snapshot.PolicyVersion = HostingReleasePolicyVersion
			p.Snapshot.Database = HostingDatabaseExecutionRef{}
		},
		func(p *HostingHealthy) { p.Storage = nil },
	} {
		bad := healthy
		mutate(&bad)
		if _, err := CompileHostingDatabasePlan(s, r, &bad, storage, db); err == nil {
			t.Fatal("foreign predecessor accepted")
		}
	}
	imageSnapshot := s
	imageSnapshot.Database = HostingDatabaseExecutionRef{}
	imageSnapshot.PolicyVersion = HostingOwnedStoragePolicyVersion
	imageSnapshot.HostPort = 20001
	imageSnapshot.Readiness = HostingReadiness{Path: "/", TimeoutSeconds: 5}
	imagePlan, err := CompileHostingOwnedStoragePlan(imageSnapshot, r, nil, nil, storage)
	if err != nil {
		t.Fatal(err)
	}
	imagePlan.Database = plan.Database
	if HostingPlanFamilyMatches(imageSnapshot, imagePlan) {
		t.Fatal("old image plan accepted injected database descriptor")
	}
	imagePlan.Database = nil
	imagePlan.PreviousHealthy = &healthy
	if HostingPlanFamilyMatches(imageSnapshot, imagePlan) {
		t.Fatal("old image plan accepted database predecessor family")
	}
	imagePlan.PreviousHealthy = nil
	imagePlan.RetainedReleases = []HostingHealthy{healthy}
	if HostingPlanFamilyMatches(imageSnapshot, imagePlan) {
		t.Fatal("old image plan accepted retained database family")
	}
	imagePlan.RetainedReleases = nil
	imagePlan.Snapshot.Database = s.Database
	if HostingPlanFamilyMatches(imagePlan.Snapshot, imagePlan) {
		t.Fatal("old image plan accepted injected database reference")
	}
}

func TestDatabaseExecutionPublicInputAndTypedTasks(t *testing.T) {
	in := HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "database_deploy"}
	raw, err := json.Marshal(in)
	var decoded HostingDatabaseDeploymentInput
	if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != in {
		t.Fatal("valid public deployment input failed", err)
	}
	for _, bad := range []string{`null`, `[]`, `{}`, string(raw) + `{}`, `{"expected_generation":0,"idempotency_key":"x"}`, `{"expected_generation":1,"Expected_generation":1,"idempotency_key":"x"}`, `{"expected_generation":1,"expected_generation":2,"idempotency_key":"x"}`, `{"expected_generation":1,"idempotency_key":"x","host_port":5432}`, `{"expected_generation":1,"idempotency_key":"x","password":"secret"}`, `{"expected_generation":1,"idempotency_key":"bad key"}`} {
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatalf("invalid public database request accepted: %s", bad)
		}
	}
	for _, mode := range []string{"deploy", "observe"} {
		prefix := "run_"
		if mode == "observe" {
			prefix = "observe_"
		}
		run := TaskRun{ID: prefix + "hop_database", RunSpec: RunSpec{Type: RunTypeDatabaseDeploy, HostingOperationID: "hop_database", HostingMode: mode}}
		if ValidateDatabaseDeploymentTask(run) != nil || !ContainsHostingExecution(run.RunSpec, Workflow{}) || ValidateImageDeploymentTask(run) == nil {
			t.Fatal("typed database task bypassed family identity")
		}
		for _, mutate := range []func(*TaskRun){func(r *TaskRun) { r.RunSpec.HostingMode = "cleanup"; r.ID = "cleanup_hop_database" }, func(r *TaskRun) { r.RunSpec.HostingOperationID = "/tmp/operation" }, func(r *TaskRun) { r.RunSpec.HostingBuildID = "build" }, func(r *TaskRun) { r.RunSpec.ProxyMode = "deploy" }, func(r *TaskRun) { r.ID = "wrong_run" }, func(r *TaskRun) { r.RunSpec.Inputs = map[string]any{"password": "secret"} }} {
			bad := run
			mutate(&bad)
			if ValidateDatabaseDeploymentTask(bad) == nil {
				t.Fatal("generic or substituted database task accepted")
			}
		}
	}
}

func TestDatabaseExecutionZeroFieldsPreserveLegacyImageHashes(t *testing.T) {
	r := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon-1"}
	r.Hash = HostingDocumentHash(r)
	s := HostingSnapshot{BindingID: "binding_1", RunnerID: "runner_1", HostPort: 18080, PolicyVersion: HostingPolicyVersion}
	p, err := CompileHostingPlan(s, r, nil)
	if err != nil || HostingDocumentHash(s) != "sha256:2210a8dbe4e9e6ea4555018b296a7702d5a49db37b33ea85e444be8b8bf34305" || p.Hash != "sha256:cf4a6720e1a9a06153289db7fec5143f32669909a24f541344b1f78c200f2d4f" {
		t.Fatalf("legacy image wire/hash changed: snapshot=%s plan=%s err=%v", HostingDocumentHash(s), p.Hash, err)
	}
	raw, err := json.Marshal(p)
	if err != nil || strings.Contains(string(raw), `"database"`) {
		t.Fatal("zero database fields changed old image wire", err)
	}
}

func TestDatabaseExecutionRejectsSelfRehashedForeignBindingVolume(t *testing.T) {
	s, r, storage, db := databaseExecutionFixture(t, "postgresql", HostingDatabasePostgresRecipe, 0)
	original, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
	if err != nil {
		t.Fatal(err)
	}
	foreign := s
	foreign.BindingID = "binding_foreign"
	foreign.Database.ConfigurationHash, err = HostingDatabaseConfigurationHash(foreign, db.Credentials)
	if err != nil {
		t.Fatal("could not construct self-consistent configuration hash", err)
	}
	foreignDB := db
	foreignDB.Reference = foreign.Database
	foreignDB.NetworkName = HostingDatabaseNetworkName(foreign.BindingID)
	// Shape and hash consistency cannot establish volume ownership. Keep the
	// original volume/reference while rehashing every binding-dependent field.
	if storage.Validate(foreign, r) != nil || foreignDB.Validate(foreign, r) != nil {
		t.Fatal("fixture is not otherwise self-consistent")
	}
	for _, withPrevious := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_plan", true: "matching_forged_predecessor"}[withPrevious], func(t *testing.T) {
			forged := HostingApplyPlan{Snapshot: foreign, Release: r, Storage: &storage, Database: &foreignDB, CompilerVersion: HostingDatabaseCompilerVersion}
			forged.Hash = HostingDocumentHash(forged)
			if withPrevious {
				forged.PreviousHealthy = &HostingHealthy{Snapshot: foreign, Release: r, OperationID: "hop_previous", ContainerID: strings.Repeat("d", 64), PlanHash: forged.Hash, Storage: &HostingHealthyStorage{Plan: storage, CreationPlanHash: original.Hash}}
				forged.Hash = ""
				forged.Hash = HostingDocumentHash(forged)
			}
			if _, err := CompileHostingDatabasePlan(foreign, r, forged.PreviousHealthy, storage, foreignDB); err == nil {
				t.Error("compiler accepted another binding's derived volume")
			}
			if ValidateHostingDatabasePlan(forged) == nil {
				t.Error("self-rehashed plan validator accepted another binding's derived volume")
			}
			if HostingPlanFamilyMatches(foreign, forged) {
				t.Error("family validator accepted another binding's derived volume")
			}
		})
	}
}
