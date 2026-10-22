package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func runtimeJournalFixture(t *testing.T) JournalHosting {
	t.Helper()
	a := testJournalAuthority()
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	ref := domain.HostingRuntimeConfigurationRef{Generation: 2, Hash: "sha256:" + strings.Repeat("c", 64)}
	snapshot := domain.HostingSnapshot{BindingID: "binding", HostPort: 20001, PolicyVersion: domain.HostingRuntimePolicyVersion, RuntimeConfiguration: ref}
	plan, err := domain.CompileRetainedHostingPlan(snapshot, release, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return JournalHosting{ID: "runtime", Attempt: a, OperationID: "operation", Phase: "apply_intent", Plan: &plan, CreatedAt: a.CreatedAt, RuntimeEnvironment: true, RuntimeConfiguration: ref, DeliveryID: strings.Repeat("a", 32)}
}

func runtimeJournalPhases(t *testing.T) map[string]JournalHosting {
	t.Helper()
	apply := runtimeJournalFixture(t)
	a := apply.Attempt
	authority := domain.HostingAuthority{OperationID: apply.OperationID, RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence}
	prepared := apply
	prepared.Phase, prepared.Plan, prepared.DeliveryID = "prepared", nil, ""
	prepared.Prepared = &domain.HostingPreparedRequest{HostingAuthority: authority, Release: apply.Plan.Release}
	result := prepared
	result.Phase, result.Prepared = "result", nil
	result.Result = &domain.HostingCompleteRequest{HostingAuthority: authority}
	healthy := domain.HostingHealthy{Snapshot: apply.Plan.Snapshot, Release: apply.Plan.Release, OperationID: "previous", ContainerID: "container"}
	cleanupPlan, err := domain.CompileHostingCleanupPlan(healthy, "current")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := apply
	cleanup.Phase, cleanup.Plan, cleanup.DeliveryID, cleanup.CleanupPlan = "cleanup_intent", nil, "", &cleanupPlan
	return map[string]JournalHosting{"apply": apply, "prepared": prepared, "result": result, "cleanup": cleanup}
}

func TestRuntimeEnvironmentJournalDowngradeAndPhases(t *testing.T) {
	for name, entry := range runtimeJournalPhases(t) {
		t.Run(name, func(t *testing.T) {
			if err := validateJournalState(journalState{Version: 8, Hosting: []JournalHosting{entry}}); err != nil {
				t.Fatal(err)
			}
			for version := 1; version <= 7; version++ {
				if err := validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}); err == nil {
					t.Fatalf("version %d downgrade error = %v", version, err)
				}
			}
		})
	}
	for version := 1; version <= 8; version++ {
		if err := validateJournalState(journalState{Version: version}); err != nil {
			t.Fatalf("legacy version %d: %v", version, err)
		}
	}
}

func TestRuntimeEnvironmentJournalMixedHistory(t *testing.T) {
	for _, previous := range []bool{false, true} {
		for _, currentEnvironment := range []bool{false, true} {
			entry := runtimeJournalFixture(t)
			old := domain.HostingHealthy{Snapshot: entry.Plan.Snapshot, Release: entry.Plan.Release, OperationID: "old", ContainerID: "old-container"}
			old.Snapshot.RuntimeConfiguration.Generation++
			old.Snapshot.RuntimeConfiguration.Hash = "sha256:" + strings.Repeat("d", 64)
			if !currentEnvironment {
				entry.Plan.Snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
				entry.Plan.Snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
				entry.Plan.CompilerVersion = domain.HostingReleaseCompilerVersion
				entry.RuntimeConfiguration, entry.DeliveryID = domain.HostingRuntimeConfigurationRef{}, ""
			}
			if previous {
				entry.Plan.PreviousHealthy = &old
			} else {
				entry.Plan.RetainedReleases = []domain.HostingHealthy{old}
			}
			entry.Plan.Hash = ""
			entry.Plan.Hash = domain.HostingDocumentHash(*entry.Plan)
			if err := validateJournalHosting(entry); err != nil {
				t.Fatal(err)
			}
			for version := 1; version <= 7; version++ {
				if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}) == nil {
					t.Fatalf("mixed history accepted version %d", version)
				}
			}
			old.Snapshot.RuntimeConfiguration.Generation = -1
			if previous {
				entry.Plan.PreviousHealthy = &old
			} else {
				entry.Plan.RetainedReleases[0] = old
			}
			if validateJournalHosting(entry) == nil {
				t.Fatal("invalid retained reference accepted")
			}
		}
	}
}

func TestRuntimeEnvironmentJournalRejectsConflicts(t *testing.T) {
	mutations := map[string]func(*JournalHosting){
		"missing marker":        func(e *JournalHosting) { e.RuntimeEnvironment = false },
		"missing reference":     func(e *JournalHosting) { e.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{} },
		"conflicting reference": func(e *JournalHosting) { e.RuntimeConfiguration.Generation++ },
		"missing id":            func(e *JournalHosting) { e.DeliveryID = "" },
		"uppercase id":          func(e *JournalHosting) { e.DeliveryID = strings.Repeat("A", 32) },
		"short id":              func(e *JournalHosting) { e.DeliveryID = strings.Repeat("a", 30) },
		"nonhex id":             func(e *JournalHosting) { e.DeliveryID = strings.Repeat("z", 32) },
		"wrong family":          func(e *JournalHosting) { e.Plan.CompilerVersion = domain.HostingReleaseCompilerVersion },
		"invalid snapshot":      func(e *JournalHosting) { e.Plan.Snapshot.RuntimeConfiguration.Hash = "bad" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			entry := runtimeJournalFixture(t)
			mutate(&entry)
			if validateJournalHosting(entry) == nil {
				t.Fatal("invalid runtime evidence accepted")
			}
		})
	}
	for name, entry := range runtimeJournalPhases(t) {
		if name == "apply" {
			continue
		}
		entry.DeliveryID = strings.Repeat("a", 32)
		if validateJournalHosting(entry) == nil {
			t.Fatalf("%s accepted delivery id", name)
		}
		entry.DeliveryID = ""
		entry.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
		if validateJournalHosting(entry) == nil {
			t.Fatalf("%s accepted missing reference", name)
		}
	}
}

func TestRuntimeEnvironmentJournalPersistenceAndCopies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := runtimeJournalFixture(t)
	if _, err = j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	if _, err = j.AppendEvent(JournalEvent{ID: "legacy-event", Attempt: entry.Attempt, Sequence: 1, Stream: "system", Message: "public output", CreatedAt: entry.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	entry.Plan.Snapshot.RuntimeConfiguration.Generation++
	snapshot := j.Snapshot()
	snapshot.Hosting[0].Plan.Snapshot.RuntimeConfiguration.Generation++
	if j.state.Version != 8 || j.Snapshot().Hosting[0].Plan.Snapshot.RuntimeConfiguration != j.Snapshot().Hosting[0].RuntimeConfiguration {
		t.Fatal("version floor or clone isolation lost")
	}
	if err = j.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("runtime intent discarded after authority loss")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.Snapshot(), reopened.Snapshot()) {
		t.Fatal("restart changed runtime authority")
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state journalState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 7; version++ {
		state.Version = version
		downgraded, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err = os.WriteFile(filepath.Join(dir, "journal.json"), downgraded, 0600); err != nil {
			t.Fatal(err)
		}
		if opened, openErr := OpenAttemptJournal(dir); openErr == nil {
			_ = opened.Close()
			t.Fatal("downgraded runtime journal opened")
		}
	}
}

func TestRuntimeEnvironmentJournalAppendFailureRestoresState(t *testing.T) {
	j, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	before, version := j.Snapshot(), j.state.Version
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = j.AppendHosting(runtimeJournalFixture(t)); err == nil {
		t.Fatal("append through closed store succeeded")
	}
	if j.state.Version != version || !reflect.DeepEqual(before, j.Snapshot()) {
		t.Fatal("failed append changed version or evidence")
	}
}

func TestRuntimeEnvironmentJournalLocalOrigin(t *testing.T) {
	entry := localJournalEntries(t)[1]
	entry.RuntimeEnvironment = true
	entry.RuntimeConfiguration = runtimeJournalFixture(t).RuntimeConfiguration
	entry.DeliveryID = strings.Repeat("e", 32)
	entry.Plan.Snapshot.RuntimeConfiguration = entry.RuntimeConfiguration
	entry.Plan.Snapshot.PolicyVersion = domain.HostingLocalBuildRuntimePolicyVersion
	entry.Plan.CompilerVersion = domain.HostingLocalBuildRuntimeCompilerVersion
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(*entry.Plan)
	if err := validateJournalState(journalState{Version: 8, Hosting: []JournalHosting{entry}}); err != nil {
		t.Fatal(err)
	}
	entry.Plan.Snapshot.PolicyVersion = domain.HostingRuntimePolicyVersion
	if validateJournalHosting(entry) == nil {
		t.Fatal("local origin accepted image runtime policy")
	}
}

func TestRuntimeEnvironmentJournalLegacyEncodingAndMarkers(t *testing.T) {
	entry := runtimeJournalFixture(t)
	entry.RuntimeEnvironment = false
	entry.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
	entry.DeliveryID = ""
	entry.Plan.Snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
	entry.Plan.Snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
	entry.Plan.CompilerVersion = domain.HostingReleaseCompilerVersion
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(*entry.Plan)
	for version := 3; version <= 8; version++ {
		if err := validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"runtime_environment", "runtime_configuration", "delivery_id"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("legacy encoding gained %s", field)
		}
	}
	entry.DeliveryID = strings.Repeat("a", 32)
	if validateJournalHosting(entry) == nil {
		t.Fatal("legacy intent accepted delivery id")
	}
	entry.DeliveryID, entry.RuntimeEnvironment = "", true
	if validateJournalHosting(entry) == nil {
		t.Fatal("legacy snapshot accepted unsupported marker")
	}
}

func TestRuntimeEnvironmentJournalCompilerMarker(t *testing.T) {
	for _, compiler := range []string{domain.HostingRuntimeCompilerVersion, domain.HostingLocalBuildRuntimeCompilerVersion} {
		for _, retainedEnvironment := range []bool{false, true} {
			for version := 3; version <= 8; version++ {
				t.Run(fmt.Sprintf("%s/retained=%t/version=%d", compiler, retainedEnvironment, version), func(t *testing.T) {
					entry := runtimeJournalFixture(t)
					if retainedEnvironment {
						entry.Plan.RetainedReleases = []domain.HostingHealthy{{Snapshot: entry.Plan.Snapshot, Release: entry.Plan.Release, OperationID: "old", ContainerID: "old-container"}}
					}
					entry.Plan.Snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
					entry.Plan.Snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
					entry.Plan.CompilerVersion = domain.HostingReleaseCompilerVersion
					entry.RuntimeConfiguration, entry.DeliveryID = domain.HostingRuntimeConfigurationRef{}, ""
					entry.RuntimeEnvironment = retainedEnvironment
					entry.Plan.Hash = ""
					entry.Plan.Hash = domain.HostingDocumentHash(*entry.Plan)
					legacyVersion := version
					if retainedEnvironment {
						legacyVersion = 8
					}
					if err := validateJournalState(journalState{Version: legacyVersion, Hosting: []JournalHosting{entry}}); err != nil {
						t.Fatalf("valid legacy family rejected: %v", err)
					}
					entry.Plan.CompilerVersion = compiler
					entry.Plan.Hash = ""
					entry.Plan.Hash = domain.HostingDocumentHash(*entry.Plan)
					if !HostingJournalUsesRuntimeEnvironment(entry) {
						t.Error("runtime compiler marker was not detected")
					}
					if err := validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}); err == nil {
						t.Fatal("runtime compiler accepted without matching current runtime snapshot")
					}
				})
			}
		}
	}
}
