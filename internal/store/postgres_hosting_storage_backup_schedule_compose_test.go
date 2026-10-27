package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func composeStorageScheduleFixture(t *testing.T) (archiveControlFixture, domain.HostingOperation) {
	t.Helper()
	f := newArchiveControlFixture(t)
	caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingComposeStorageArchiveCapability, domain.HostingStackLifecycleCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, caps); err != nil {
		t.Fatal(err)
	}
	f.resource, f.source = deployBackupComponentStack(t, f.pg, f.resource, domain.Server{ID: "server_owned_storage", RunnerID: f.runner}, domain.Destination{ID: "destination_owned_storage", ServerID: "server_owned_storage"}, "a", f.helper.DaemonID)
	return f, f.source
}
func composeStorageScheduleInput(f archiveControlFixture) domain.HostingStorageBackupScheduleInput {
	return domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat("1", 32), ExpectedResourceGeneration: f.resource.Generation, Volume: "db", ExpectedDeployedOperationID: f.source.ID, AcknowledgeInterruption: true, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
}
func TestPostgresComposeStorageBackupScheduleAdmissionAndHeldControl(t *testing.T) {
	f, source := composeStorageScheduleFixture(t)
	if err := f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	in := composeStorageScheduleInput(f)
	schedule, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Version != domain.HostingComposeStorageBackupScheduleVersion || schedule.Volume != "db" || !schedule.AcknowledgeInterruption {
		t.Fatal("missing source metadata", schedule)
	}
	var family string
	var image *string
	if err = f.db.QueryRow(f.ctx, `SELECT source_family,storage_id FROM hosting_storage_backup_schedules WHERE id=$1`, schedule.ID).Scan(&family, &image); err != nil || family != "compose" || image != nil {
		t.Fatal("fabricated image source", err)
	}
	otherInput := in
	otherInput.RequestID = strings.Repeat("3", 32)
	otherSchedule, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", otherInput)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- f.pg.ReconcileStorageBackupSchedules(f.ctx) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	history, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, schedule.ID)
	if err != nil || len(history) != 1 || history[0].Volume != "db" || history[0].BackupID == "" || !history[0].OwnershipHeld {
		t.Fatal("atomic occurrence", history, err)
	}
	skipped, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, otherSchedule.ID)
	if err != nil || len(skipped) != 1 || skipped[0].Reason != "source_busy" || skipped[0].OperationID != "" {
		t.Fatal("overlapping volume schedule admitted", skipped, err)
	}
	var jobs int
	var raw []byte
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1),body FROM hosting_storage_archive_plans WHERE operation_id=$1`, history[0].OperationID).Scan(&jobs, &raw); err != nil || jobs != 1 {
		t.Fatal("job exactly once", jobs, err)
	}
	var plan domain.HostingStorageArchivePlan
	if json.Unmarshal(raw, &plan) != nil || plan.ComposeSource == nil || plan.ComposeSource.Healthy.OperationID != source.ID || plan.ComposeSource.Volume.Name != "db" || plan.Validate() != nil {
		t.Fatal("actual deployment not pinned")
	}
	// Replay remains stable with an outstanding mutation hold and generation drift.
	if _, err = f.db.Exec(f.ctx, `WITH version AS (INSERT INTO hosting_compose_source_versions(resource_id,generation,original,original_hash,definition,definition_hash) SELECT resource_id,generation+1,original,original_hash,definition,definition_hash FROM hosting_compose_source_versions WHERE resource_id=$1 RETURNING resource_id) UPDATE resources SET generation=generation+1 WHERE id IN(SELECT resource_id FROM version)`, f.resource.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil || replay.ID != schedule.ID {
		t.Fatal("held historical replay", err)
	}
	disabled := in
	disabled.RequestID = strings.Repeat("2", 32)
	disabled.ExpectedVersion = 1
	disabled.ExpectedResourceGeneration++
	disabled.Enabled = false
	disabled.ExpectedDeployedOperationID = ""
	disabled.AcknowledgeInterruption = false
	offline, err := store.OpenPostgres(f.ctx, f.db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := offline.Close(); e != nil {
			t.Error(e)
		}
	})
	saved, err := offline.SaveStorageBackupSchedule(f.ctx, "unused", f.resource.ID, "usr_bootstrap", schedule.ID, disabled)
	if err != nil || saved.Enabled || saved.Revision != 2 || !saved.AcknowledgeInterruption {
		t.Fatal("disable while held and archive unavailable", err)
	}
	if _, err = offline.ListStorageBackupSchedules(f.ctx, "usr_bootstrap", f.resource.ID); err != nil {
		t.Fatal("unavailable list", err)
	}
	if history, err = offline.ListStorageBackupOccurrences(f.ctx, "usr_bootstrap", f.resource.ID, schedule.ID); err != nil || len(history) != 1 || !history[0].OwnershipHeld {
		t.Fatal("disable changed admitted obligation", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in); err == nil {
		t.Fatal("revoked replay accepted")
	}
}

func TestPostgresComposeStorageBackupScheduleRejectsChangedOriginAndRemote(t *testing.T) {
	f, _ := composeStorageScheduleFixture(t)
	if err := f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	in := composeStorageScheduleInput(f)
	remote := in
	remote.RemoteTarget = &domain.BackupTargetSelection{TargetID: "target", Revision: 1}
	if _, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", remote); !errors.Is(err, domain.ErrInvalidStorageBackupSchedule) {
		t.Fatal("remote compose accepted", err)
	}
	bad := in
	bad.ExpectedDeployedOperationID = "hop_stale"
	if _, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", bad); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale save accepted", err)
	}
	saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	// Insert a historical source hash mismatch without rewriting the immutable
	// materialized volume origin. Production APIs cannot mutate either identity.
	tx, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err = tx.Exec(f.ctx, `INSERT INTO hosting_storage_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,source_reference_hash,source_family,source_volume,acknowledge_interruption) SELECT 'stale_compose_origin',project_id,resource_id,binding_id,destination_id,actor_id,1,frequency,timezone,true,$2,source_family,source_volume,true FROM hosting_storage_backup_schedules WHERE id=$1`, saved.ID, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO hosting_storage_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,acknowledge_interruption) SELECT 'stale_compose_origin',revision,actor_id,expected_resource_generation,frequency,timezone,enabled,true FROM hosting_storage_backup_schedule_versions WHERE schedule_id=$1`, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Disable the valid schedule so only the mismatched historical source is due.
	disabled := in
	disabled.RequestID = strings.Repeat("2", 32)
	disabled.ExpectedVersion = 1
	disabled.Enabled = false
	disabled.ExpectedDeployedOperationID = ""
	disabled.AcknowledgeInterruption = false
	if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, disabled); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, "stale_compose_origin")
	if err != nil || len(rows) != 1 || rows[0].Reason != "placement_changed" || rows[0].OperationID != "" {
		t.Fatal("changed origin admitted", rows, err)
	}
}

func TestPostgresComposeStorageBackupScheduleUsesRedeployedOrigin(t *testing.T) {
	f, previous := composeStorageScheduleFixture(t)
	in := composeStorageScheduleInput(f)
	in.Enabled = false
	saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	deploy := hostingDeploymentRequest()
	deploy.IdempotencyKey = "schedule-redeploy"
	deploy.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	op, err := f.pg.RequestHostingDeployment(f.ctx, "hop_schedule_redeploy", f.resource.ID, "usr_bootstrap", deploy, destinationEvent("schedule_redeploy"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, f.pg, op.ID)
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "schedule_redeploy_prepared", Release: *previous.Release, StackImages: previous.Plan.Stack.Images})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	state := *previous.Result.Stack
	state.Components = append([]domain.HostingStackComponent(nil), state.Components...)
	for i := range state.Components {
		state.Components[i].OperationID = op.ID
		state.Components[i].ContainerID = strings.Repeat("c", 63) + string(rune('1'+i))
	}
	completed, err := f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "schedule_redeployed", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: f.helper.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	enabled := in
	enabled.Enabled = true
	enabled.RequestID = strings.Repeat("2", 32)
	enabled.ExpectedVersion = 1
	enabled.ExpectedDeployedOperationID = completed.ID
	if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, enabled); err != nil {
		t.Fatal("same origin after redeploy", err)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, saved.ID)
	if err != nil || len(rows) != 1 || rows[0].OperationID == "" {
		t.Fatal("redeployed admission", rows, err)
	}
	var source string
	if err = f.db.QueryRow(f.ctx, `SELECT body->'compose_source'->'healthy'->>'operation_id' FROM hosting_storage_archive_plans WHERE operation_id=$1`, rows[0].OperationID).Scan(&source); err != nil || source != completed.ID {
		t.Fatal("scheduled old deployment", source, err)
	}
}

func TestPostgresComposeStorageBackupScheduleStoppedControl(t *testing.T) {
	f, source := composeStorageScheduleFixture(t)
	in := composeStorageScheduleInput(f)
	saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := f.pg.RequestHostingStackStop(f.ctx, "hop_schedule_stop", f.resource.ID, "usr_bootstrap", domain.HostingStackStopInput{IdempotencyKey: "schedule-stop", ExpectedDeployedOperationID: source.ID, ExpectedGeneration: f.resource.Generation}, destinationEvent("schedule_stop"))
	if err != nil {
		t.Fatal(err)
	}
	stop = awaitHostingDispatch(t, f.pg, stop.ID)
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: stop.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	admitted, err := f.pg.HostingApplyBegin(f.ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	state := *source.Result.Stack
	state.Components = []domain.HostingStackComponent{}
	if _, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "schedule_stopped", Result: domain.HostingResult{Outcome: "succeeded", StackStopped: true, DaemonID: f.helper.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &state}}); err != nil {
		t.Fatal(err)
	}
	if err = f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	if replay, e := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in); e != nil || replay.ID != saved.ID {
		t.Fatal("stopped replay", e)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, saved.ID)
	if err != nil || len(rows) != 1 || rows[0].Reason != "source_unavailable" || rows[0].OperationID != "" {
		t.Fatal("stopped source admitted", rows, err)
	}
	disabled := in
	disabled.RequestID = strings.Repeat("2", 32)
	disabled.ExpectedVersion = 1
	disabled.Enabled = false
	disabled.ExpectedDeployedOperationID = ""
	disabled.AcknowledgeInterruption = false
	if changed, e := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, disabled); e != nil || changed.Enabled || !changed.AcknowledgeInterruption {
		t.Fatal("stopped disable", e)
	}
	if _, err = f.service.ListStorageBackupSchedules(f.owner, f.resource.ID); err != nil {
		t.Fatal("stopped history", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_storage_backup_schedules SET source_volume='other' WHERE id=$1`, saved.ID); err == nil {
		t.Fatal("immutable origin changed")
	}
}
