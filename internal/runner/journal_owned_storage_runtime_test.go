package runner

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestOwnedStorageRuntimeJournalPinsBothReferences(t *testing.T) {
	entry, _ := storageJournalFixture(t)
	snap := entry.Plan.Snapshot
	snap.PolicyVersion = domain.HostingOwnedStorageRuntimePolicyVersion
	snap.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}
	plan, err := domain.CompileHostingOwnedStorageRuntimePlan(snap, entry.Plan.Release, nil, nil, *entry.Plan.Storage)
	if err != nil {
		t.Fatal(err)
	}
	entry.Plan = &plan
	entry.StorageCreate.PlanHash = plan.Hash
	entry.RuntimeEnvironment = true
	entry.RuntimeConfiguration = snap.RuntimeConfiguration
	j, _ := openBuildJournal(t)
	if _, err = j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	apply := cloneJournalHosting(entry)
	apply.ID = "combined_apply"
	apply.Phase = "apply_intent"
	apply.StorageCreate = nil
	apply.DeliveryID = strings.Repeat("a", 32)
	if _, err = j.AppendHosting(apply); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*JournalHosting){"missing_marker": func(e *JournalHosting) { e.RuntimeEnvironment = false }, "mixed_compiler": func(e *JournalHosting) { e.Plan.CompilerVersion = domain.HostingOwnedStorageCompilerVersion }, "stale_runtime": func(e *JournalHosting) { e.RuntimeConfiguration.Generation++ }, "missing_delivery": func(e *JournalHosting) { e.DeliveryID = "" }, "stale_fence": func(e *JournalHosting) {
		e.Attempt.Fence = "changed"
		e.StorageCreate = &domain.HostingStorageCreateRequest{}
	}} {
		t.Run(name, func(t *testing.T) {
			bad := cloneJournalHosting(apply)
			mutate(&bad)
			if validateJournalState(journalState{Version: 10, Hosting: []JournalHosting{bad}}) == nil {
				t.Fatal("mixed/stale authority accepted")
			}
		})
	}
}
