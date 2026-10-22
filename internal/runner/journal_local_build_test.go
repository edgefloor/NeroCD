package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nerocd/internal/domain"
)

func localJournalEntries(t *testing.T) []JournalHosting {
	t.Helper()
	_, _, built := buildJournalFixture()
	release, err := domain.NewHostingLocalBuildRelease(*built.Result.Completion.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.HostingSnapshot{LocalBuild: domain.HostingLocalBuildSource{SourceGeneration: 1, Artifact: release.LocalBuild, ArtifactHash: domain.HostingDocumentHash(release.LocalBuild)}, PolicyVersion: domain.HostingLocalBuildPolicyVersion, HostPort: 20001, BindingID: "binding"}
	healthy := domain.HostingHealthy{Snapshot: snapshot, Release: release, OperationID: "previous", ContainerID: "container"}
	a := testJournalAuthority()
	base := JournalHosting{ID: "local", Attempt: a, OperationID: "operation", CreatedAt: a.CreatedAt}
	prepared := base
	prepared.Phase = "prepared"
	prepared.Prepared = &domain.HostingPreparedRequest{HostingAuthority: domain.HostingAuthority{OperationID: base.OperationID, RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence}, Release: release}
	plan := domain.HostingApplyPlan{Snapshot: snapshot, Release: release, CompilerVersion: domain.HostingLocalBuildCompilerVersion}
	plan.Hash = domain.HostingDocumentHash(plan)
	apply := base
	apply.Phase = "apply_intent"
	apply.Plan = &plan
	previousPlan := domain.HostingApplyPlan{PreviousHealthy: &healthy}
	previousPlan.Hash = domain.HostingDocumentHash(previousPlan)
	previous := base
	previous.Phase = "apply_intent"
	previous.Plan = &previousPlan
	retainedPlan := domain.HostingApplyPlan{RetainedReleases: []domain.HostingHealthy{healthy}}
	retainedPlan.Hash = domain.HostingDocumentHash(retainedPlan)
	retained := base
	retained.Phase = "apply_intent"
	retained.Plan = &retainedPlan
	cleanupPlan, err := domain.CompileHostingCleanupPlan(healthy, "current")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := base
	cleanup.Phase = "cleanup_intent"
	cleanup.CleanupPlan = &cleanupPlan
	result := base
	result.Phase = "result"
	result.LocalBuild = true
	result.Result = &domain.HostingCompleteRequest{HostingAuthority: prepared.Prepared.HostingAuthority}
	return []JournalHosting{prepared, apply, previous, retained, cleanup, result}
}

func TestLocalBuildJournalNestedEvidenceRequiresV6(t *testing.T) {
	for i, entry := range localJournalEntries(t) {
		if !HostingJournalUsesLocalBuild(entry) {
			t.Fatalf("nested local evidence missed: %d", i)
		}
		for version := 1; version <= 5; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}) == nil {
				t.Fatalf("local entry%d accepted under version%d", i, version)
			}
		}
	}
}

func TestLocalBuildJournalFloorSurvivesRestartAndDowngradeFails(t *testing.T) {
	j, dir := openBuildJournal(t)
	entry := localJournalEntries(t)[0]
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	if j.state.Version != 6 {
		t.Fatal("local journal did not establish floor6")
	}
	_, _, built := buildJournalFixture()
	if _, err := j.AppendBuild(built); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendProxy(proxyJournalFixture(t)); err != nil {
		t.Fatal(err)
	}
	if j.state.Version != 6 {
		t.Fatal("mixed append downgraded journal")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	var state journalState
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("invalid persisted document")
	}
	for version := 1; version <= 5; version++ {
		state.Version = version
		downgraded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "journal.json"), downgraded, 0600); err != nil {
			t.Fatal(err)
		}
		if opened, err := OpenAttemptJournal(dir); err == nil {
			_ = opened.Close()
			t.Fatal("downgraded local journal opened")
		}
	}
}

func TestLocalBuildJournalMarkerCannotChangeStructuredOrigin(t *testing.T) {
	entry := localJournalEntries(t)[0]
	entry.Prepared.Release.LocalBuild = domain.HostingBuildArtifact{}
	entry.LocalBuild = true
	if validateJournalHosting(entry) == nil {
		t.Fatal("marker authorized conflicting structured origin")
	}
}
