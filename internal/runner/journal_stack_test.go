package runner

import (
	"encoding/json"

	"nerocd/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stackJournalFixture(t *testing.T) JournalHosting {
	t.Helper()
	entry := runtimeJournalFixture(t)
	definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\n", PublicComponent: "app", ContainerPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	images := []domain.HostingStackImage{{Component: "app", Release: entry.Plan.Release}}
	release := domain.HostingRelease{DaemonID: images[0].Release.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	snapshot := entry.Plan.Snapshot
	snapshot.Stack = domain.HostingStackRef{Generation: 1, Hash: domain.HostingDocumentHash(definition)}
	snapshot.ResourceGeneration = 1
	snapshot.ContainerPort = 80
	snapshot.PolicyVersion = domain.HostingStackPolicyVersion
	plan, err := domain.CompileHostingStackPlan(snapshot, release, nil, domain.HostingStackPlan{Definition: definition, Images: images, Volumes: []domain.HostingStackVolume{}, NetworkName: domain.HostingStackObjectID(snapshot.BindingID, "network", "private")})
	if err != nil {
		t.Fatal(err)
	}
	entry.Plan = &plan
	return entry
}

func TestStackJournalRetainsWholePlanAndRejectsOldReaderVersion(t *testing.T) {
	entry := stackJournalFixture(t)
	plan := *entry.Plan
	dir := filepath.Join(t.TempDir(), "journal")
	journal, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	if err = journal.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("discarded unresolved stack mutation intent")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state journalState
	if json.Unmarshal(raw, &state) != nil || state.Version != 23 || state.StackBaseVersion != 8 {
		t.Fatal("old agent can interpret stack journal")
	}
	journal, err = OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Snapshot().Hosting; len(got) != 1 || got[0].Plan == nil || got[0].Plan.Hash != plan.Hash {
		t.Fatal("lost stack intent on restart")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	downgraded := strings.Replace(string(raw), `"version":23`, `"version":8`, 1)
	if err = os.WriteFile(filepath.Join(dir, "journal.json"), []byte(downgraded), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenAttemptJournal(dir); err == nil {
		_ = opened.Close()
		t.Fatal("accepted downgraded stack authority")
	}
}

func TestStackStopJournalRetainsExactRemovalIntent(t *testing.T) {
	entry := stackJournalFixture(t)
	plan := *entry.Plan
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "app", NetworkID: strings.Repeat("a", 64), Components: []domain.HostingStackComponent{{Component: "app", ContainerID: strings.Repeat("b", 64), ImageID: plan.Stack.Images[0].Release.ImageID, OperationID: "hop_deployed", Running: true}}, Volumes: []domain.HostingStackVolume{}}
	stop, err := domain.CompileHostingStackStopPlan(domain.HostingHealthy{OperationID: "hop_deployed", Snapshot: plan.Snapshot, Release: plan.Release, PlanHash: plan.Hash, Stack: &state}, plan)
	if err != nil {
		t.Fatal(err)
	}
	entry.RuntimeEnvironment = false
	entry.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
	entry.DeliveryID = ""
	entry.Phase = "stack_stop_intent"
	entry.Plan = nil
	entry.StackStopPlan = &stop
	journal, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = journal.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	if err = journal.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("discarded unresolved stack removal")
	}
	if entries := journal.Snapshot().Hosting; len(entries) != 1 || entries[0].StackStopPlan == nil || entries[0].StackStopPlan.Hash != stop.Hash {
		t.Fatal("lost exact stop intent")
	}
	mixed := entry
	mixed.ID += "mixed"
	mixed.Plan = &plan
	if _, err = journal.AppendHosting(mixed); err == nil {
		t.Fatal("mixed stop/apply intent accepted")
	}
}
