package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func databaseJournalFixture(t *testing.T) []JournalHosting {
	t.Helper()
	old, evidence := storageJournalFixture(t)
	storage := *evidence.StorageMetadata
	recipe, err := domain.HostingDatabaseRecipeFor("postgresql", domain.HostingDatabasePostgresRecipe)
	if err != nil {
		t.Fatal(err)
	}
	storage.MountPath = recipe.MountPath
	storage.Hash = storage.DocumentHash()
	s := domain.HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, ServerID: "server", RunnerID: "runner", ResourceGeneration: 1, RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion, Storage: storage.Reference(), Database: domain.HostingDatabaseExecutionRef{Engine: recipe.Engine, RecipeVersion: recipe.Version}}
	refs := slices.Clone(recipe.Credentials)
	for i := range refs {
		refs[i].SecretID = domain.HostingDatabaseSecretID(s.ResourceID, refs[i].Role)
		refs[i].Version = 1
	}
	s.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(s, refs)
	if err != nil {
		t.Fatal(err)
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	descriptor := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: storage.VolumeName, MountPath: storage.MountPath, CreationOperationID: old.OperationID, DaemonID: release.DaemonID}
	descriptor.CreationNonce = domain.HostingStorageCreationNonce(descriptor.Reference, descriptor.CreationOperationID, descriptor.DaemonID)
	plan, err := domain.CompileHostingDatabasePlan(s, release, nil, descriptor, domain.HostingDatabasePlan{Reference: s.Database, NetworkName: domain.HostingDatabaseNetworkName(s.BindingID), Credentials: refs})
	if err != nil {
		t.Fatal(err)
	}
	a := old.StorageCreate.HostingAuthority
	base := JournalHosting{Attempt: old.Attempt, OperationID: old.OperationID, Database: true, DatabaseRef: s.Database, StorageRef: s.Storage, CreatedAt: old.CreatedAt}
	prepared := base
	prepared.ID = "prepared"
	prepared.Phase = "prepared"
	prepared.Prepared = &domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: prepared.ID, Release: release}
	create := base
	create.ID = "create"
	create.Phase = "storage_create_intent"
	create.Plan = &plan
	create.StorageCreate = &domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: plan.Hash, StorageRef: s.Storage, CreationNonce: descriptor.CreationNonce}
	origin := domain.HostingStorageMaterialization{StorageRef: s.Storage, State: "create_admitted", CreationOperationID: a.OperationID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: release.DaemonID}
	origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
	materialized := base
	materialized.ID = "materialized"
	materialized.Phase = "storage_materialized"
	materialized.Plan = &plan
	materialized.StorageMetadata = &storage
	materialized.StorageMaterialized = &domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: materialized.ID, PlanHash: plan.Hash, StorageRef: s.Storage, CreationOperationID: origin.CreationOperationID, CreationPlanHash: plan.Hash, CreationNonce: origin.CreationNonce, DaemonID: release.DaemonID, VolumeName: storage.VolumeName, Driver: "local", Scope: "local", Labels: origin.Labels}
	apply := base
	apply.ID = "apply"
	apply.Phase = "apply_intent"
	apply.Plan = &plan
	apply.DeliveryID = strings.Repeat("a", 32)
	result := base
	result.ID = "result"
	result.Phase = "result"
	result.Result = &domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: result.ID, Result: domain.HostingResult{Outcome: "failed", Uncertain: true}}
	return []JournalHosting{prepared, create, materialized, apply, result}
}

func TestDatabaseJournalV11RestartAndDowngrade(t *testing.T) {
	entries := databaseJournalFixture(t)
	j, dir := openBuildJournal(t)
	storage, _ := storageJournalFixture(t)
	storage.ID = "ordinary_storage"
	if _, err := j.AppendHosting(storage); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := validateJournalState(journalState{Version: 11, Hosting: []JournalHosting{entry}}); err != nil {
			t.Fatalf("%s: %v", entry.Phase, err)
		}
		for version := 1; version <= 10; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}) == nil {
				t.Fatalf("v%d accepted database %s", version, entry.Phase)
			}
		}
		if _, err := j.AppendHosting(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.DiscardAttempt(entries[0].Attempt); err == nil {
		t.Fatal("discarded unresolved database")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"create_permitted", "password", "secret-root", "bearer"} {
		if strings.Contains(strings.ToLower(string(raw)), canary) {
			t.Fatalf("journal contains forbidden material %s", canary)
		}
	}
	j, err = OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for i, entry := range entries {
		if err := j.AckHosting(entry.ID); err != nil {
			t.Fatal(err)
		}
		if i < len(entries)-1 && j.state.Version != 11 {
			t.Fatal("downgraded before last database evidence")
		}
	}
	if j.state.Version != 10 {
		t.Fatalf("storage version lost: %d", j.state.Version)
	}
	if err = j.AckHosting(storage.ID); err != nil {
		t.Fatal(err)
	}
	if j.state.Version >= 10 {
		t.Fatal("empty evidence retained database/storage format")
	}
}
func TestDatabaseJournalRejectsMixedFamilyAndMarkerContradictions(t *testing.T) {
	entries := databaseJournalFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*JournalHosting)
	}{
		{"missing marker", func(e *JournalHosting) { e.Database = false }},
		{"missing ref", func(e *JournalHosting) { e.DatabaseRef = domain.HostingDatabaseExecutionRef{} }},
		{"runtime marker", func(e *JournalHosting) { e.RuntimeEnvironment = true }},
		{"local marker", func(e *JournalHosting) { e.LocalBuild = true }},
		{"wrong storage", func(e *JournalHosting) { e.StorageRef.Generation++ }},
		{"missing delivery", func(e *JournalHosting) { e.DeliveryID = "" }},
		{"uppercase delivery", func(e *JournalHosting) { e.DeliveryID = strings.Repeat("A", 32) }},
		{"prior family", func(e *JournalHosting) {
			p := *e.Plan
			p.PreviousHealthy = &domain.HostingHealthy{Snapshot: domain.HostingSnapshot{PolicyVersion: domain.HostingOwnedStoragePolicyVersion}}
			e.Plan = &p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := cloneJournalHosting(entries[3])
			tc.change(&e)
			if validateJournalHosting(e) == nil {
				t.Fatal("accepted contradictory database evidence")
			}
		})
	}
	result := entries[4]
	result.DeliveryID = strings.Repeat("a", 32)
	if validateJournalHosting(result) == nil {
		t.Fatal("accepted result delivery identity")
	}
	ordinary, _ := storageJournalFixture(t)
	ordinary.Plan.PreviousHealthy = &domain.HostingHealthy{Snapshot: entries[3].Plan.Snapshot}
	if !HostingJournalUsesDatabase(ordinary) || validateJournalHosting(ordinary) == nil {
		t.Fatal("embedded database family escaped detection")
	}
}
func TestDatabaseJournalStrictDecoder(t *testing.T) {
	raw, err := json.Marshal(journalState{Version: 11, Hosting: databaseJournalFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"version":11`, `"version":11,"version":10`, 1)),
		[]byte(strings.Replace(string(raw), `"database":true`, `"Database":true`, 1)),
		[]byte(strings.Replace(string(raw), `"database":true`, `"database":true,"password":"canary"`, 1)),
		append(slices.Clone(raw), []byte(` {}`)...),
	} {
		var state journalState
		state.Version = 11
		if decodeStorageJournal(bad, &state) == nil {
			t.Fatal("accepted ambiguous database journal")
		}
	}
}

func TestDatabaseJournalComposeEnvironmentKeysRemainClosed(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"environment":{"MYSQL_DATABASE":"wordpress","mysql_database":"distinct"},"version":18}`, true},
		{`{"environment":{"MYSQL_DATABASE":"one","MYSQL_DATABASE":"two"}}`, false},
		{`{"Environment":{"MYSQL_DATABASE":"wordpress"}}`, false},
		{`{"environment":{"nested":{"Upper":"invalid"}}}`, false},
		{`{"operation":{"Version":18}}`, false},
	} {
		if databaseJournalUnique(json.NewDecoder(strings.NewReader(tc.body))) != tc.valid {
			t.Fatal("journal map/canonical-key boundary changed")
		}
	}
}
