package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresComposeStorageArchiveAdmissionRecoveryAndReplay(t *testing.T) {
	f := newArchiveControlFixture(t)
	caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingComposeStorageArchiveCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, caps); err != nil {
		t.Fatal(err)
	}
	server := domain.Server{ID: "server_owned_storage", RunnerID: f.runner}
	destination := domain.Destination{ID: "destination_owned_storage", ServerID: server.ID}
	resource, deployed := deployBackupComponentStack(t, f.pg, f.resource, server, destination, "a", f.helper.DaemonID)
	f.resource = resource
	discovery, err := f.service.StorageBackupVolumes(f.owner, resource.ID)
	if err != nil || !discovery.Available || discovery.OperationID != deployed.ID || len(discovery.Volumes) != 1 || discovery.Volumes[0].Volume != "db" || len(discovery.Volumes[0].Mounts) != 1 {
		t.Fatalf("discovery %#v %v", discovery, err)
	}
	in := domain.HostingStorageBackupInput{RequestID: strings.Repeat("9", 32), ExpectedResourceGeneration: resource.Generation, Volume: "db", ExpectedDeployedOperationID: deployed.ID, AcknowledgeInterruption: true}
	// Capability admission and all transaction outputs fail together.
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingComposeStorageArchiveCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RequestStorageBackup(f.owner, resource.ID, in); err == nil {
		t.Fatal("older runner admitted")
	}
	var count int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backups WHERE resource_id=$1`, resource.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed admission left backup", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2] WHERE id=$1`, f.runner, domain.HostingComposeStorageArchiveCapability); err != nil {
		t.Fatal(err)
	}
	b, err := f.service.RequestStorageBackup(f.owner, resource.ID, in)
	if err != nil {
		t.Fatal("compose admission", err)
	}
	if b.Volume != "db" || b.SourceDeployedOperationID != deployed.ID {
		t.Fatal("metadata source missing")
	}
	replay, err := f.service.RequestStorageBackup(f.owner, resource.ID, in)
	if err != nil || replay.ID != b.ID {
		t.Fatal("replay while held", err)
	}
	op, a := f.claim(t, b.OperationID, "backup")
	p := op.StorageArchive.Plan
	if p.Validate() != nil || p.ComposeSource == nil || p.SourceStorage != (domain.HostingStorage{}) || len(p.Writers) != 0 {
		t.Fatal("family mixed")
	}
	var family string
	var legacy *string
	if err = f.db.QueryRow(f.ctx, `SELECT source_family,source_storage_id FROM hosting_storage_archive_plans WHERE operation_id=$1`, op.ID).Scan(&family, &legacy); err != nil || family != "compose" || legacy != nil {
		t.Fatal("origin family", err)
	}
	op = f.begin(t, op, a)
	states := p.WriterStates()
	for i := range states {
		states[i].Running = true
	}
	op = f.progress(t, op, a, "writer_snapshot", states, nil)
	op = f.progress(t, op, a, "stop_intent", states, nil)
	stopped := append([]domain.HostingStorageArchiveWriterState(nil), states...)
	for i := range stopped {
		stopped[i].Running = false
	}
	op = f.progress(t, op, a, "stopped", stopped, nil)
	result := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "compose_archive_uncertain", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "uncertain", Uncertain: true, FailureCode: "resume_failed"}}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, result); err != nil {
		t.Fatal("held failure", err)
	}
	recovery, err := f.service.RequestStorageArchiveRecovery(f.owner, op.ID, domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("8", 32), ExpectedPlanHash: p.Hash})
	if err != nil {
		t.Fatal("recovery", err)
	}
	if recovery.StorageArchive.Plan.ComposeSource == nil || len(recovery.StorageArchive.Plan.RecoveryEvidence.ResumeRequiredContainerIDs) != len(states) {
		t.Fatal("recovery obligations")
	}
	if _, err = f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("7", 32), Name: "unsupported"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("compose accepted image restore", err)
	}
	// Current metadata and identical request history remain readable after desired
	// generation changes, even though a new backup would require redeployment.
	if _, err = f.db.Exec(f.ctx, `WITH version AS (INSERT INTO hosting_compose_source_versions(resource_id,generation,original,original_hash,definition,definition_hash) SELECT resource_id,generation+1,original,original_hash,definition,definition_hash FROM hosting_compose_source_versions WHERE resource_id=$1 RETURNING resource_id) UPDATE resources SET generation=generation+1 WHERE id IN(SELECT resource_id FROM version)`, resource.ID); err != nil {
		t.Fatal(err)
	}
	if replay, err = f.service.RequestStorageBackup(f.owner, resource.ID, in); err != nil || replay.ID != b.ID {
		t.Fatal("historical replay rejected", err)
	}
	if _, err = f.service.ListStorageBackups(f.owner, resource.ID, store.Page{}); err != nil {
		t.Fatal("history changed with runtime", err)
	}
	// Revoked destination authority must still reject a replay.
	if _, err = f.pg.RevokeDestinationProject(f.ctx, destination.ID, resource.ProjectID, destinationEvent("compose_archive_revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RequestStorageBackup(f.owner, resource.ID, in); err == nil {
		t.Fatal("revoked grant replay accepted")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), `"lease_id"`) {
		t.Fatal("source includes lease authority")
	}
}
