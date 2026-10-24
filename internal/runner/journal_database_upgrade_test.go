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

func upgradeJournalFixture(t *testing.T) []JournalHosting {
	t.Helper()
	capture := captureJournalFixture(t, "postgresql")
	p := capture[0].Claim.Capture.Plan
	proof := domain.HostingDatabaseUpgradeProof{Version: domain.HostingDatabaseUpgradeVersion, CapturePlan: p, OriginReceipt: *capture[3].Ack, PreviousPlan: p.SourcePlan, PreviousHealthy: p.SourceHealthy}
	proof.Hash = proof.DocumentHash()
	in, err := domain.HostingDatabaseUpgradePlanInput(proof)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := domain.CompileHostingDatabasePlanV2(p.SourcePlan.Snapshot, p.SourcePlan.Release, p.SourceHealthy, p.SourcePlan, *p.SourcePlan.Storage, *p.SourcePlan.Database, in)
	if err != nil {
		t.Fatal(err)
	}
	entries := databaseJournalFixture(t)
	for i := range entries {
		e := &entries[i]
		e.OperationID = "hop_upgrade"
		e.DatabaseUpgrade = &proof
		e.DatabaseRef, e.StorageRef = plan.Snapshot.Database, plan.Snapshot.Storage
		a := domain.HostingAuthority{OperationID: e.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
		if e.Prepared != nil {
			e.Prepared.HostingAuthority = a
			e.Prepared.Release = plan.Release
		}
		if e.Plan != nil {
			e.Plan = &plan
		}
		if e.StorageCreate != nil {
			e.StorageCreate.HostingAuthority = a
			e.StorageCreate.PlanHash = plan.Hash
			e.StorageCreate.StorageRef = e.StorageRef
			e.StorageCreate.CreationNonce = plan.Storage.CreationNonce
		}
		if e.StorageMaterialized != nil {
			e.StorageMaterialized = &domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: e.ID, PlanHash: plan.Hash, StorageRef: e.StorageRef, CreationOperationID: p.Materialization.CreationOperationID, CreationPlanHash: p.Materialization.CreationPlanHash, CreationNonce: p.Materialization.CreationNonce, DaemonID: p.Materialization.DaemonID, VolumeName: p.Storage.VolumeName, Driver: "local", Scope: "local", Labels: p.Materialization.Labels}
			e.StorageMetadata = &p.Storage
		}
		if e.Result != nil {
			e.Result.HostingAuthority = a
		}
		if err := validateJournalHosting(*e); err != nil {
			t.Fatalf("%s: %v", e.Phase, err)
		}
	}
	return entries
}

func TestDatabaseUpgradeJournalV14EveryPhaseAndCaptureCoexistence(t *testing.T) {
	entries := upgradeJournalFixture(t)
	j, dir := openBuildJournal(t)
	for _, e := range entries {
		if _, err := j.AppendHosting(e); err != nil {
			t.Fatal(err)
		}
		if j.state.Version != 14 {
			t.Fatal("upgrade did not mark v14")
		}
		before := j.Snapshot()
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		j, err = OpenAttemptJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, j.Snapshot()) {
			t.Fatal("restart lost exact upgrade proof")
		}
	}
	defer func() { _ = j.Close() }()
	capture := captureJournalFixture(t, "redis")
	appendCapture(t, j, capture)
	if j.state.Version != 14 {
		t.Fatal("capture append downgraded upgrade evidence")
	}
	if err := validateJournalState(j.state); err != nil {
		t.Fatal(err)
	}
	if j.DiscardAttempt(entries[0].Attempt) == nil {
		t.Fatal("discarded unresolved upgrade")
	}
	for _, entry := range entries {
		if err := j.AckHosting(entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	if j.state.Version != 13 {
		t.Fatal("retiring upgrade did not preserve capture version")
	}
}

func TestDatabaseUpgradeJournalRejectsMissingChangedAndOldFormatProof(t *testing.T) {
	entries := upgradeJournalFixture(t)
	for _, e := range entries {
		for version := 1; version < 14; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{e}}) == nil {
				t.Fatalf("v%d admitted upgrade %s", version, e.Phase)
			}
		}
		without := cloneJournalHosting(e)
		without.DatabaseUpgrade = nil
		if validateJournalState(journalState{Version: 14, Hosting: []JournalHosting{without}}) == nil {
			t.Fatal("missing entire proof admitted")
		}
		if e.Phase != "prepared" {
			if validateJournalState(journalState{Version: 14, Hosting: []JournalHosting{entries[0], without}}) == nil {
				t.Fatal("mixed phases dropped proof")
			}
		}
	}
	j, _ := openBuildJournal(t)
	if _, err := j.AppendHosting(entries[0]); err != nil {
		t.Fatal(err)
	}
	without := cloneJournalHosting(entries[4])
	without.DatabaseUpgrade = nil
	if _, err := j.AppendHosting(without); err == nil {
		t.Fatal("append mixed v1 result into upgrade chain")
	}
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return bytes.Replace(b, []byte(`"version":14`), []byte(`"version":11`), 1) },
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"database_upgrade":{`), []byte(`"database_upgrade":{"unexpected":true,`), 1)
		},
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"database_upgrade":{`), []byte(`"database_upgrade":null,"database_upgrade":{`), 1)
		},
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"hash":"sha256:`), []byte(`"hash":"sha256:`+strings.Repeat("f", 64)), 1)
		},
	} {
		dir := t.TempDir()
		raw, err := json.Marshal(journalState{Version: 14, Hosting: entries})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "journal.json"), mutate(raw), 0600); err != nil {
			t.Fatal(err)
		}
		opened, err := OpenAttemptJournal(dir)
		if err == nil {
			_ = opened.Close()
			t.Fatal("tampered upgrade journal loaded")
		}
	}
}
