package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresCatalogArchiveUTCInitialDeployment(t *testing.T) {
	if os.Getenv("NEROCD_TEST_DATABASE_URL") == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	if os.Getenv("NEROCD_CATALOG_ARCHIVE_UTC_CHILD") == "1" {
		if time.Local.String() != "UTC" {
			t.Fatal("UTC child did not load the requested timezone")
		}
		TestPostgresCatalogArchivePinnedRuntimeAndReplay(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPostgresCatalogArchiveUTCInitialDeployment$", "-test.v")
	command.Env = append(os.Environ(), "TZ=UTC", "NEROCD_CATALOG_ARCHIVE_UTC_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("UTC restored deployment failed: %v\n%s", err, output)
	}
}

// Reuse the existing archive fixture and ordinary image protocol. No container
// or secret delivery is simulated by mutating healthy state directly.
func catalogArchiveSource(t *testing.T) (archiveControlFixture, domain.HostingRuntimeConfiguration) {
	t.Helper()
	f := newArchiveControlFixture(t)
	for _, cap := range []string{domain.HostingOwnedStorageRuntimeCapability, domain.HostingRuntimeEnvironmentCapability} {
		if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, cap); err != nil {
			t.Fatal(err)
		}
	}
	item, record, audit := managedFixture(t, "archive_password", 1)
	if _, err := f.pg.CreateManagedSecret(f.ctx, item, record, audit); err != nil {
		t.Fatal(err)
	}
	_, next, nextAudit := managedFixture(t, item.ID, 2)
	if _, err := f.pg.RotateManagedSecret(f.ctx, item.ID, 1, next, nextAudit); err != nil {
		t.Fatal(err)
	}
	runtimeSave(t, f.pg, f.resource.ID, runtimeInput(10, 0))
	in := runtimeInput(11, 1)
	in.Entries = append(in.Entries, domain.HostingRuntimeEntry{Name: "PASSWORD", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 2}})
	config := runtimeSave(t, f.pg, f.resource.ID, in)
	input := hostingDeploymentRequest()
	input.HostPort = 0
	input.ExpectedRuntimeConfigurationGeneration = 2
	input.IdempotencyKey = "catalog_archive_combined"
	op, err := f.pg.RequestHostingDeployment(f.ctx, "hop_catalog_archive_combined", f.resource.ID, "usr_bootstrap", input, destinationEvent("combined"))
	if err != nil {
		t.Fatal(err)
	}
	f.source = catalogArchiveSettle(t, f, op, f.storage, strings.Repeat("d", 64))
	return f, config
}

func catalogArchiveSettle(t *testing.T, f archiveControlFixture, op domain.HostingOperation, st domain.HostingStorage, container string) domain.HostingOperation {
	t.Helper()
	if err := f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("combined claim mismatch", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err := f.pg.HostingRunnerPlan(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: *f.source.Release})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: st.Reference(), CreationNonce: op.Plan.Storage.CreationNonce})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageMaterialized(f.ctx, f.runner, archiveMaterializedProof(a, op.Plan.Hash, st, ack.Materialization, "materialized_"+op.ID)); err != nil {
		t.Fatal(err)
	}
	var credential string
	if err := f.db.QueryRow(f.ctx, `SELECT token_hash FROM runners WHERE id=$1`, f.runner).Scan(&credential); err != nil {
		t.Fatal(err)
	}
	delivery := domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: op.Plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("a", 32)}
	if strings.Contains(op.ID, "restored") {
		delivery.DeliveryID = strings.Repeat("b", 32)
	}
	authorized, err := f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, f.runner, credential, delivery)
	if err != nil {
		t.Fatal("combined delivery denied", err)
	}
	authorized.Close()
	if _, err := f.pg.HostingApplyBegin(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: container, ImageID: f.source.Release.ImageID, DaemonID: f.source.Release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func catalogArchiveFinishRestore(t *testing.T, f archiveControlFixture, restored domain.HostingStorageRestore) {
	t.Helper()
	op, a := f.claim(t, restored.Operation.ID, "restore")
	p := op.StorageArchive.Plan
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: p.Hash, StorageRef: restored.Storage.Reference(), CreationNonce: p.Target.Plan.CreationNonce})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageMaterialized(f.ctx, f.runner, archiveMaterializedProof(a, p.Hash, restored.Storage, ack.Materialization, "restore_materialized")); err != nil {
		t.Fatal(err)
	}
	op = f.begin(t, op, a)
	op = f.progress(t, op, a, "helper_intent", nil, nil)
	op = f.progress(t, op, a, "helper_identified", nil, nil)
	f.progress(t, op, a, "restore_verified", nil, p.Blob)
	if _, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "restore_complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "not_needed", Blob: p.Blob, RestoreVerified: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCatalogArchivePinnedRuntimeAndReplay(t *testing.T) {
	f, pinned := catalogArchiveSource(t)
	backup := f.finishBackup(t, f.backup(t))
	_, nextSecret, nextAudit := managedFixture(t, "archive_password", 3)
	if _, err := f.pg.RotateManagedSecret(f.ctx, "archive_password", 2, nextSecret, nextAudit); err != nil {
		t.Fatal(err)
	}
	latest := runtimeSave(t, f.pg, f.resource.ID, runtimeInput(12, 2))
	if latest.RuntimeGeneration != 3 {
		t.Fatal("desired mutation fixture")
	}
	in := domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "restored catalog"}
	restored, err := f.service.RequestStorageRestore(f.owner, backup.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.pg.GetHostingRuntimeConfiguration(f.ctx, "usr_bootstrap", restored.Resource.ID)
	if err != nil || saved.RuntimeGeneration != 1 || domain.HostingDocumentHash(saved.Entries) != domain.HostingDocumentHash(pinned.Entries) {
		t.Fatal("restore copied desired/latest or omitted pinned runtime", err)
	}
	if restored.Resource.ID == f.resource.ID || restored.Storage.VolumeName == f.storage.VolumeName || len(restored.Operation.StorageArchive.Plan.Writers) != 0 {
		t.Fatal("restore reused source authority")
	}
	replay, err := f.service.RequestStorageRestore(f.owner, backup.ID, in)
	if err != nil {
		t.Fatal("restore replay failed", err)
	}
	// River can dispatch the queued operation between the restore commit and
	// this replay. Prove that limited transition, then compare all other fields.
	if restored.Operation.State != "queued" || restored.Operation.RunID != "" || replay.Operation.State != "queued" && replay.Operation.State != "dispatched" || replay.Operation.UpdatedAt.Before(restored.Operation.UpdatedAt) {
		t.Fatal("restore replay changed operation outside queue dispatch")
	}
	if replay.Operation.State == "queued" && replay.Operation.RunID != "" || replay.Operation.State == "dispatched" && replay.Operation.RunID != "restore_"+restored.Operation.ID {
		t.Fatal("restore replay changed run identity")
	}
	immutableReplay := replay
	immutableReplay.Operation.State = restored.Operation.State
	immutableReplay.Operation.RunID = restored.Operation.RunID
	immutableReplay.Operation.UpdatedAt = restored.Operation.UpdatedAt
	if domain.HostingDocumentHash(immutableReplay) != domain.HostingDocumentHash(restored) {
		t.Fatal("restore replay changed immutable resource, storage, plan or receipt identity")
	}
	var runtimeVersions, secrets, catalogs int
	if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_runtime_configuration_versions WHERE resource_id=$1),(SELECT count(*) FROM project_managed_secrets),(SELECT count(*) FROM hosting_catalog_installations WHERE resource_id=$1)`, restored.Resource.ID).Scan(&runtimeVersions, &secrets, &catalogs); err != nil || runtimeVersions != 1 || secrets != 1 || catalogs != 0 {
		t.Fatal("restore replay duplicated metadata or fabricated catalog identity", err)
	}
	catalogArchiveFinishRestore(t, f, restored)
	options, err := f.pg.HostingStorageDeploymentOptions(f.ctx, restored.Resource.ID, "usr_bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	assertStorageInitialOptions(t, options, restored.Resource, restored.Storage)
	input := hostingDeploymentRequest()
	input.HostPort = 0
	input.IdempotencyKey = "archive_restored_deploy"
	input.ExpectedRuntimeConfigurationGeneration = 1
	op, err := f.pg.RequestHostingDeployment(f.ctx, "hop_archive_restored_deploy", restored.Resource.ID, "usr_bootstrap", input, destinationEvent("restored"))
	if err != nil {
		t.Fatal(err)
	}
	op = catalogArchiveSettle(t, f, op, restored.Storage, strings.Repeat("e", 64))
	if op.Snapshot.PolicyVersion != domain.HostingOwnedStorageRuntimePolicyVersion || op.Snapshot.RuntimeConfiguration.Hash != f.source.Snapshot.RuntimeConfiguration.Hash {
		t.Fatal("ordinary restored deploy lost pinned runtime")
	}
	raw, _ := json.Marshal([]any{restored, replay, op})
	if strings.Contains(string(raw), "private-value") {
		t.Fatal("plaintext escaped restore metadata")
	}
}

func TestPostgresCatalogArchiveInitialDeploymentSafety(t *testing.T) {
	for _, scenario := range []string{"pending", "failed", "unrelated_writer", "release_slot"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := catalogArchiveSource(t)
			backup := f.finishBackup(t, f.backup(t))
			restored, err := f.service.RequestStorageRestore(f.owner, backup.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "initial safety"})
			if err != nil {
				t.Fatal(err)
			}
			want := "restore_hold"
			switch scenario {
			case "failed":
				op, a := f.claim(t, restored.Operation.ID, "restore")
				if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "failed_restore", PlanHash: op.StorageArchive.Plan.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "not_needed", FailureCode: "helper_failed"}}); err != nil {
					t.Fatal(err)
				}
			case "unrelated_writer", "release_slot":
				catalogArchiveFinishRestore(t, f, restored)
				view, readErr := f.pg.HostingStorageDeploymentOptions(f.ctx, restored.Resource.ID, "usr_bootstrap")
				if readErr != nil {
					t.Fatal(readErr)
				}
				assertStorageInitialOptions(t, view, restored.Resource, restored.Storage)
				want = "no_previous_healthy"
				if scenario == "unrelated_writer" {
					_, err = f.db.Exec(f.ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES('hop_unknown_restore_writer',$1,$2,$3,'unknown_writer','sha256:'||repeat('a',64),'failed','usr_bootstrap')`, restored.Resource.ProjectID, restored.Resource.ID, restored.Storage.BindingID)
				} else {
					_, err = f.db.Exec(f.ctx, `INSERT INTO hosting_release_slots(destination_id,host_port,binding_id) VALUES($1,18080,$2)`, restored.Storage.DestinationID, restored.Storage.BindingID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			view, err := f.pg.HostingStorageDeploymentOptions(f.ctx, restored.Resource.ID, "usr_bootstrap")
			if err != nil || view.Available || view.Reason != want || view.HostPort != nil || view.ContainerPort != nil || view.Readiness != nil || view.PreviousHealthyOperationID != nil {
				t.Fatalf("unsafe restored deployment advice: %+v %v; want %s", view, err, want)
			}
		})
	}
}

func TestPostgresCatalogArchiveCurrentAuthorityAndRollback(t *testing.T) {
	for _, scenario := range []string{"secret_revoked", "grant_revoked", "viewer", "old_runner", "audit_rollback"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := catalogArchiveSource(t)
			backup := f.finishBackup(t, f.backup(t))
			actor := f.owner
			switch scenario {
			case "secret_revoked":
				catalogArchiveRevokeSecret(t, f)
			case "grant_revoked":
				if _, err := f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`); err != nil {
					t.Fatal(err)
				}
			case "viewer":
				if _, err := f.db.Exec(f.ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_archive_viewer','proj_platform','usr_bootstrap','viewer','viewer','active')`); err != nil {
					t.Fatal(err)
				}
				actor = auth.WithPrincipal(context.Background(), auth.Principal{ID: "sa_archive_viewer", Provider: domain.PrincipalServiceAccount, Roles: []string{domain.RoleViewer}})
			case "old_runner":
				if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1)`, domain.HostingOwnedStorageRuntimeCapability); err != nil {
					t.Fatal(err)
				}
			case "audit_rollback":
				if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION catalog_archive_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage_archive.requested' THEN RAISE EXCEPTION 'fixture rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER catalog_archive_fail BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION catalog_archive_fail()`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.service.RequestStorageRestore(actor, backup.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "denied restored catalog"})
			if err == nil {
				t.Fatal("unavailable authority or failed atomic commit admitted restore")
			}
			var targets, versions, audits int
			if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM resources WHERE name='denied restored catalog'),(SELECT count(*) FROM hosting_runtime_configuration_versions WHERE resource_id<>$1),(SELECT count(*) FROM audit_events WHERE action='hosting.runtime_configuration.updated' AND target_id<>$1)`, f.resource.ID).Scan(&targets, &versions, &audits); err != nil || targets != 0 || versions != 0 || audits != 0 {
				t.Fatal("failed restore retained partial target/runtime/audit", err)
			}
		})
	}
	f, _ := catalogArchiveSource(t)
	backup := f.finishBackup(t, f.backup(t))
	in := domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "restored before revocation"}
	if _, err := f.service.RequestStorageRestore(f.owner, backup.ID, in); err != nil {
		t.Fatal(err)
	}
	catalogArchiveRevokeSecret(t, f)
	if _, err := f.service.RequestStorageRestore(f.owner, backup.ID, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restore replay ignored current secret authority", err)
	}
}

func TestPostgresCatalogArchiveReplayRechecksOwnerAndDestination(t *testing.T) {
	for _, scenario := range []string{"viewer", "destination"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := catalogArchiveSource(t)
			backup := f.finishBackup(t, f.backup(t))
			in := domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "restored replay authority"}
			restored, err := f.service.RequestStorageRestore(f.owner, backup.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			actor := f.owner
			if scenario == "viewer" {
				// The replay uses the same original actor. Remove its system role and
				// establish viewer membership, so receipt identity cannot mask lost role.
				if _, err := f.db.Exec(f.ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'`); err != nil {
					t.Fatal(err)
				}
				member, err := f.pg.UpsertProjectMember(f.ctx, domain.ProjectMember{ID: "archive_replay_viewer", ProjectID: f.resource.ProjectID, UserID: "usr_bootstrap", Role: domain.RoleViewer, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
				if err != nil || member.Role != domain.RoleViewer {
					t.Fatal("original owner membership was not demoted", err)
				}
				// Keep the original principal, including its stale system-admin role.
				// The database must deny replay under current stored authority.
			} else {
				if _, err := f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.RequestStorageRestore(actor, backup.ID, in); !errors.Is(err, auth.ErrForbidden) {
				t.Fatal("identical restore replay retained lost authority", err)
			}
			var targets int
			if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM resources WHERE name=$1`, restored.Resource.Name).Scan(&targets); err != nil || targets != 1 {
				t.Fatal("denied replay created another target", err)
			}
		})
	}
}

func TestPostgresCatalogArchiveOldRunnerCannotClaim(t *testing.T) {
	for _, capability := range []string{domain.HostingOwnedStorageRuntimeCapability, domain.HostingRuntimeEnvironmentCapability} {
		t.Run(capability, func(t *testing.T) {
			f, _ := catalogArchiveSource(t)
			backup := f.backup(t)
			if err := f.pg.PrepareStorageArchive(f.ctx, backup.OperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, capability); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); err == nil {
				t.Fatal("old runner claimed combined archive")
			}
			var leases int
			if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM run_leases WHERE run_id=(SELECT run_id FROM hosting_operations WHERE id=$1)`, backup.OperationID).Scan(&leases); err != nil || leases != 0 {
				t.Fatal("denied combined claim left lease authority", err)
			}
		})
	}
}

func catalogArchiveRevokeSecret(t *testing.T, f archiveControlFixture) {
	t.Helper()
	revoked, err := f.pg.RevokeManagedSecret(f.ctx, "archive_password", 2, domain.AuditEvent{ID: "aud_catalog_archive_revoke", ActorID: "usr_bootstrap", CreatedAt: time.Now().UTC()})
	if err != nil || revoked.Status != "revoked" || revoked.RevokedAt == nil {
		t.Fatal("managed secret lifecycle revocation failed", err)
	}
}
