package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingCatalogTypedCopyAndClosedInput(t *testing.T) {
	entries := HostingCatalogTemplates()
	entry := entries[0]
	if entry.Definition.ImageReference != HostingCatalogLinkdingImage || entry.Definition.MountPath != "/etc/linkding/data" || entry.OriginalHash == entry.DefinitionHash || !strings.Contains(entry.Original, "SERVICE_PASSWORD_LINKDING") {
		t.Fatal("incomplete normalization")
	}
	entries[0].Definition.CredentialNames[0] = "changed"
	if HostingCatalogTemplates()[0].Definition.CredentialNames[0] != "LD_SUPERUSER_NAME" {
		t.Fatal("shared mutable catalog")
	}
	in := HostingCatalogInstallInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj", ProjectEnvironmentID: "env", DestinationID: "dest", Name: "Bookmarks", TemplateID: "linkding", ImmutableVersion: HostingCatalogLinkdingVersion}
	raw, _ := json.Marshal(in)
	var out HostingCatalogInstallInput
	if json.Unmarshal(raw, &out) != nil || out != in {
		t.Fatal("canonical install rejected")
	}
	for _, bad := range []string{strings.Replace(string(raw), `"name":"Bookmarks"`, `"name":"Bookmarks","name":"changed"`, 1), strings.Replace(string(raw), `"name":`, `"Name":`, 1), strings.Replace(string(raw), `"name":"Bookmarks"`, `"name":null`, 1), strings.TrimSuffix(string(raw), "}") + `,"compose":"services: {}"}`, string(raw) + "{}"} {
		if json.Unmarshal([]byte(bad), &out) == nil {
			t.Fatal("open install decoder")
		}
	}
}

// This is the template wire shape saved before catalog presentation metadata existed.
// Stored installation verification hashes this typed template as part of its body.
func TestHostingCatalogHistoricalTemplateHashRemainsStable(t *testing.T) {
	const original = `{"id":"linkding","immutable_version":"coolify-b12d1386.linkding-1.47.0.v1","name":"Linkding","upstream_repository":"https://github.com/coollabsio/coolify","upstream_commit":"b12d1386f3d458618b4c579babcd1e339c6c40b3","upstream_path":"templates/compose/linkding.yaml","original":"saved original bytes","original_hash":"saved original hash","definition":{"image_reference":"saved digest","container_port":9090,"mount_path":"/etc/linkding/data","readiness":{"path":"/health","timeout_seconds":120},"credential_names":["LD_SUPERUSER_NAME","LD_SUPERUSER_PASSWORD"]},"definition_hash":"saved definition hash"}`
	var template HostingCatalogTemplate
	if err := json.Unmarshal([]byte(original), &template); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("historical template changed on decode: got %s", raw)
	}
	var old json.RawMessage = []byte(original)
	if HostingDocumentHash(template) != HostingDocumentHash(old) {
		t.Fatal("historical template hash changed")
	}
}

func TestHostingCatalogPresentationDoesNotChangeDefinition(t *testing.T) {
	template := HostingCatalogTemplates()[0]
	if template.Category != "RSS" || template.AMDOnly || template.ARMOnly {
		t.Fatalf("pinned upstream declarations differ: %+v", template)
	}
	definitionHash := template.DefinitionHash
	template.Category = "changed"
	template.AMDOnly = true
	if HostingDocumentHash(template.Definition) != definitionHash || template.Version != HostingCatalogLinkdingVersion {
		t.Fatal("presentation metadata changed immutable deployment definition")
	}
	if HostingCatalogTemplates()[0].Category != "RSS" {
		t.Fatal("catalog presentation metadata is shared")
	}
}

func TestHostingOwnedStorageRuntimePlanPinsBothFamilies(t *testing.T) {
	id, volume := HostingStorageIdentity("binding")
	ref := HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("d", 64)}
	snap := HostingSnapshot{ResourceID: "res", BindingID: "binding", DestinationID: "dest", HostPort: 23000, PolicyVersion: HostingOwnedStorageRuntimePolicyVersion, Storage: ref, RuntimeConfiguration: HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("c", 64)}}
	rel := HostingRelease{CanonicalImage: HostingCatalogLinkdingImage, ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "daemon", OS: "linux", Architecture: "arm64"}
	rel.Hash = HostingDocumentHash(rel)
	storage := HostingStoragePlan{Reference: ref, VolumeName: volume, MountPath: "/etc/linkding/data", CreationOperationID: "op", DaemonID: "daemon"}
	storage.CreationNonce = HostingStorageCreationNonce(ref, "op", "daemon")
	plan, err := CompileHostingOwnedStorageRuntimePlan(snap, rel, nil, nil, storage)
	if err != nil || plan.RetainPrevious || !HostingPlanFamilyMatches(snap, plan) {
		t.Fatalf("combined plan %v", err)
	}
	for _, compile := range []func(HostingSnapshot, HostingRelease, *HostingHealthy, []HostingHealthy) (HostingApplyPlan, error){CompileRetainedHostingPlan} {
		if _, err = compile(snap, rel, nil, nil); err == nil {
			t.Fatal("old compiler accepted combined input")
		}
	}
	if _, err = CompileHostingOwnedStoragePlan(snap, rel, nil, nil, storage); err == nil {
		t.Fatal("old storage compiler accepted combined input")
	}
	changed := snap
	changed.RuntimeConfiguration.Generation++
	changed.RuntimeConfiguration.Hash = "sha256:" + strings.Repeat("e", 64)
	next, err := CompileHostingOwnedStorageRuntimePlan(changed, rel, nil, nil, storage)
	if err != nil || next.Hash == plan.Hash || *next.Storage != *plan.Storage {
		t.Fatal("runtime generation not pinned or storage origin changed")
	}
	for _, mutate := range []func(*HostingSnapshot){func(s *HostingSnapshot) { s.RuntimeConfiguration = HostingRuntimeConfigurationRef{} }, func(s *HostingSnapshot) { s.Storage = HostingStorageRef{} }, func(s *HostingSnapshot) { s.PolicyVersion = HostingOwnedStoragePolicyVersion }, func(s *HostingSnapshot) { s.ApplicationDatabase.BindingID = "fake" }, func(s *HostingSnapshot) { s.Database.Engine = "fake" }} {
		bad := snap
		mutate(&bad)
		if _, err = CompileHostingOwnedStorageRuntimePlan(bad, rel, nil, nil, storage); err == nil {
			t.Fatal("mixed family accepted")
		}
	}
}

func TestHostingCatalogUptimeKumaDefinitionAndLookup(t *testing.T) {
	linkding, ok := LookupHostingCatalogTemplate("linkding", HostingCatalogLinkdingVersion)
	if !ok || HostingDocumentHash(linkding) != "sha256:7db2c1bf04209cdaab977f3f09d33d893cfb15e7879404dbe04c7e928a845be3" {
		t.Fatal("accepted Linkding catalog bytes changed")
	}
	kuma, ok := LookupHostingCatalogTemplate("uptime-kuma", HostingCatalogUptimeKumaVersion)
	if !ok || kuma.Definition.ImageReference != HostingCatalogUptimeKumaImage || kuma.Definition.ContainerPort != 3001 || kuma.Definition.MountPath != "/app/data" || kuma.Definition.Readiness.Path != "/" || kuma.Definition.Readiness.TimeoutSeconds != 120 || kuma.Definition.CredentialNames == nil || len(kuma.Definition.CredentialNames) != 0 || kuma.Category != "monitoring" {
		t.Fatal("Kuma translation differs from pinned template")
	}
	doc, refs, err := CanonicalHostingRuntimeDocument([]HostingRuntimeEntry{})
	if err != nil || doc.Entries == nil || len(doc.Entries) != 0 || len(refs) != 0 {
		t.Fatal("empty runtime document rejected", err)
	}
	for _, pair := range [][2]string{{"uptime-kuma", HostingCatalogLinkdingVersion}, {"linkding", HostingCatalogUptimeKumaVersion}, {"uptime-kuma", "retired-version"}} {
		if _, found := LookupHostingCatalogTemplate(pair[0], pair[1]); found {
			t.Fatal("mismatched template/version accepted")
		}
	}
	in := HostingCatalogInstallInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj", ProjectEnvironmentID: "env", DestinationID: "dest", Name: "Monitoring", TemplateID: "uptime-kuma", ImmutableVersion: HostingCatalogUptimeKumaVersion}
	raw, _ := json.Marshal(in)
	var decoded HostingCatalogInstallInput
	if json.Unmarshal(raw, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("Kuma install decoder rejected")
	}
	in.ImmutableVersion = "retired-version"
	if in.ValidateShape() != nil || in.Validate() == nil {
		t.Fatal("historical request shape depends on live catalog")
	}
}

func TestHostingCatalogActualBudgetPinnedLiteralAndHistoricalHashes(t *testing.T) {
	kuma, _ := LookupHostingCatalogTemplate("uptime-kuma", HostingCatalogUptimeKumaVersion)
	if HostingDocumentHash(kuma) != "sha256:0bf1574847028a69e3cfaa26e7d7b24edd1ace8e0e9f80edd6a585267c939274" {
		t.Fatal("accepted Kuma catalog bytes changed")
	}
	actual, ok := LookupHostingCatalogTemplate("actualbudget", HostingCatalogActualBudgetVersion)
	if !ok || actual.Definition.ImageReference != HostingCatalogActualBudgetImage || actual.Definition.ContainerPort != 5006 || actual.Definition.MountPath != "/data" || actual.Definition.Readiness.Path != "/health" || actual.Definition.CredentialNames == nil || len(actual.Definition.CredentialNames) != 0 || len(actual.Definition.RuntimeEnvironment) != 1 || actual.Definition.RuntimeEnvironment["ACTUAL_LOGIN_METHOD"] != "password" {
		t.Fatal("Actual translation lost required settings")
	}
	actual.Definition.RuntimeEnvironment["ACTUAL_LOGIN_METHOD"] = "changed"
	fresh, _ := LookupHostingCatalogTemplate("actualbudget", HostingCatalogActualBudgetVersion)
	if fresh.Definition.RuntimeEnvironment["ACTUAL_LOGIN_METHOD"] != "password" {
		t.Fatal("shared mutable literal defaults")
	}
	// The existing canonical boundary used by catalog installation rejects
	// collisions between a curated literal and a generated secret name.
	value := "password"
	_, _, err := CanonicalHostingRuntimeDocument([]HostingRuntimeEntry{{Name: "ACTUAL_LOGIN_METHOD", Kind: "literal", Value: &value}, {Name: "ACTUAL_LOGIN_METHOD", Kind: "managed_secret", Secret: &HostingRuntimeSecretReference{SecretID: "msec_catalog", Version: 1}}})
	if err == nil {
		t.Fatal("literal/credential collision accepted")
	}
	in := HostingCatalogInstallInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj", ProjectEnvironmentID: "env", DestinationID: "dest", Name: "Budget", TemplateID: "actualbudget", ImmutableVersion: HostingCatalogActualBudgetVersion}
	raw, _ := json.Marshal(in)
	var decoded HostingCatalogInstallInput
	if json.Unmarshal(raw, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("Actual install decoder rejected")
	}
	in.ImmutableVersion = HostingCatalogUptimeKumaVersion
	if in.Validate() == nil {
		t.Fatal("cross-pair accepted")
	}
}
