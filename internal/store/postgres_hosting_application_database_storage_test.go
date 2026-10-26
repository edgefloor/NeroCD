package store_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

type connectedStorageFixture struct {
	connectionFixture
	storage domain.HostingStorage
	writer  *domain.HostingApplicationDatabaseStorageWriterRequest
}

func newConnectedStorageFixture(t *testing.T, engine string) connectedStorageFixture {
	t.Helper()
	f := newConnectionFixture(t, engine).connect(t).prepareApp(t).ready(t).complete(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, []string{domain.HostingApplicationDatabaseStorageCapability, domain.HostingOwnedStorageCapability}); err != nil {
		t.Fatal(err)
	}
	storage := saveHostingStorage(t, f.pg, f.application.ID, hostingStorageInput())
	in := f.input.Deployment
	in.IdempotencyKey = "connected_storage_bootstrap"
	in.HostPort++
	in.ExpectedRuntimeConfigurationGeneration = 1
	in.ApplicationDatabaseStorage = &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: storage.Generation, ExpectedStorageHash: storage.Hash, AcknowledgeInterruption: true}
	before := connectionCounts(t, f)
	missing := in
	missing.ApplicationDatabaseStorage = nil
	if _, err := f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, missing); err == nil || connectionCounts(t, f) != before {
		t.Fatal("storage deployment omitted reviewed interruption")
	}
	op, err := f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, in)
	if err != nil {
		t.Fatal("storage bootstrap admission", err)
	}
	f.appOperation = awaitHostingDispatch(t, f.pg, op.ID)
	f.stage = domain.HostingApplicationDatabaseStage{}
	return connectedStorageFixture{connectionFixture: f, storage: storage}
}
func (f connectedStorageFixture) prepare(t *testing.T) connectedStorageFixture {
	t.Helper()
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != f.appOperation.ID {
		t.Fatal("storage claim", err)
	}
	f.appAuthority = domain.HostingAuthority{OperationID: f.appOperation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority); err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("d", 64), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "amd64", DaemonID: f.op.Release.DaemonID}
	release.Hash = domain.HostingDocumentHash(release)
	f.appOperation, err = f.pg.HostingPrepared(f.runnerContext, f.runner, domain.HostingPreparedRequest{HostingAuthority: f.appAuthority, ReceiptID: "storage_prepared_" + f.appOperation.ID, Release: release})
	if err != nil {
		t.Fatal("storage connected prepare", err)
	}
	delivery, err := f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, f.runner, f.credential, domain.HostingRuntimeEnvironmentRequest{Authority: f.appAuthority, DeliveryID: domain.HostingDocumentHash(f.appOperation.ID)[7:39], PlanHash: f.appOperation.Plan.Hash, RuntimeConfiguration: f.appOperation.Snapshot.RuntimeConfiguration})
	if err != nil {
		t.Fatal(err)
	}
	delivery.Close()
	if _, err = f.pg.HostingApplyBegin(f.runnerContext, f.runner, f.appAuthority); err == nil {
		t.Fatal("writer admitted before volume provenance")
	}
	m := storageMaterializationFixture{pg: f.pg, db: f.db, runner: f.runner, storage: f.storage, op: f.appOperation, authority: f.appAuthority}
	ack, err := f.pg.HostingStorageCreateBegin(f.runnerContext, f.runner, m.request())
	if err != nil {
		t.Fatal("composed materialization begin", err)
	}
	if !ack.CreatePermitted {
		t.Fatal("fresh admission did not grant the sole creation permission")
	}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority); err == nil {
		t.Fatal("ordinary Plan bypassed the admitted storage hold")
	}
	replay, err := f.pg.HostingStorageCreateBegin(f.runnerContext, f.runner, m.request())
	if err != nil || replay.CreatePermitted || replay.Storage != ack.Storage || domain.HostingDocumentHash(replay.Materialization) != domain.HostingDocumentHash(ack.Materialization) {
		t.Fatal("exact held admission did not reauthorize without renewing creation permission", err)
	}
	proof := m.proof(ack)
	proof.ReceiptID = "storage_materialized_" + f.appOperation.ID
	if _, err = f.pg.HostingStorageMaterialized(f.runnerContext, f.runner, proof); err != nil {
		t.Fatal("composed materialization", err)
	}
	f.appOperation, err = f.pg.HostingApplyBegin(f.runnerContext, f.runner, f.appAuthority)
	if err != nil {
		t.Fatal("composed apply admission", err)
	}
	return f
}

func TestPostgresApplicationDatabaseStorageCreationAdmissionHeldPlan(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newConnectedStorageFixture(t, engine).prepare(t)
			if f.appOperation.State != "applying" || !f.appOperation.MutationHold {
				t.Fatal("exact materialization did not advance to held writer admission")
			}
		})
	}
}
func (f connectedStorageFixture) effect(t *testing.T, action string) {
	t.Helper()
	if f.writer == nil {
		t.Fatal("missing writer state")
	}
	stage := ""
	if f.stage.Version != "" {
		stage = f.stage.Hash
	}
	in := domain.HostingApplicationDatabaseStorageEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: domain.HostingDocumentHash(f.appOperation.ID + action)[7:39], Action: action, StateHash: f.writer.Receipt.Hash, StageHash: stage}
	grant, err := f.pg.AuthorizeHostingApplicationDatabaseStorageEffect(f.ctx, f.runner, f.credential, in)
	if err != nil || grant.Validate() != nil || grant.StateHash != in.StateHash {
		t.Fatal("storage effect "+action, err)
	}
	replay, err := f.pg.AuthorizeHostingApplicationDatabaseStorageEffect(f.ctx, f.runner, f.credential, in)
	if err != nil || replay != grant {
		t.Fatal("storage grant replay", err)
	}
}
func (f connectedStorageFixture) write(t *testing.T, phase string, endpoint *domain.HostingDatabaseBindingEndpointObservation) connectedStorageFixture {
	t.Helper()
	r := domain.HostingApplicationDatabaseStorageWriterReceipt{Version: domain.HostingApplicationDatabaseStorageVersion, ReceiptID: "writer_" + phase + "_" + domain.HostingDocumentHash(f.appOperation.ID)[7:39], Phase: phase, PlanHash: f.appOperation.Plan.Hash, StorageRef: f.storage.Reference(), PredecessorHash: domain.HostingDocumentHash(f.appOperation.Plan.PreviousHealthy), PredecessorEndpoint: endpoint}
	if f.writer != nil {
		r.PreviousStateHash = f.writer.Receipt.Hash
	}
	if f.stage.Version != "" {
		r.StageHash = f.stage.Hash
		r.CandidateHash = f.stage.Candidate.Hash
	}
	if phase != "stop_intent" && phase != "resume_intent" {
		r.ObservationHash = domain.HostingDocumentHash(phase)
	}
	r.Hash = r.DocumentHash()
	request := domain.HostingApplicationDatabaseStorageWriterRequest{HostingAuthority: f.appAuthority, Receipt: r}
	op, err := f.pg.HostingApplicationDatabaseStorageWriter(f.ctx, f.runner, f.credential, request)
	if err != nil {
		t.Fatal("storage writer "+phase, err)
	}
	replay, err := f.pg.HostingApplicationDatabaseStorageWriter(f.ctx, f.runner, f.credential, request)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(op) {
		t.Fatal("writer historical exact ACK "+phase, err)
	}
	f.appOperation = op
	f.writer = &request
	return f
}
func TestPostgresApplicationDatabaseStorageBootstrapAndFailureRecovery(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newConnectedStorageFixture(t, engine).prepare(t)
			if _, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: strings.Repeat("a", 32), Action: "create"}); err == nil {
				t.Fatal("candidate before stopped proof")
			}
			f = f.write(t, "stop_intent", nil)
			f.effect(t, "stop_predecessor")
			f = f.write(t, "stopped", nil)
			var healthyBefore string
			if err := f.db.QueryRow(f.ctx, `SELECT healthy::text FROM hosting_binding_state WHERE binding_id=$1`, f.appOperation.BindingID).Scan(&healthyBefore); err != nil {
				t.Fatal(err)
			}
			f.connectionFixture = f.connectionFixture.sendStage(t, "candidate_created").sendStage(t, "attach_intent").sendStage(t, "network_configured")
			f.effect(t, "stop_candidate")
			f = f.write(t, "candidate_stopped", nil).write(t, "replacement_failed", nil)
			failed := f.appOperation
			view, err := f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
			if err != nil || view.MembershipRevision != 2 || view.HoldOperationID != failed.ID {
				t.Fatal("failure released membership hold", err)
			}
			current, err := f.pg.GetHostingOperation(f.ctx, failed.ID)
			if err != nil || current.State != "failed" || current.ApplicationDatabaseStorageState == nil || len(current.ApplicationDatabaseStorageState.RecoveryActions) != 1 || current.ApplicationDatabaseStorageState.RecoveryActions[0] != "resume_predecessor" {
				t.Fatal("missing safe resume offer", err)
			}
			input := domain.HostingDatabaseConnectionRecoveryInput{OperationID: failed.ID, Action: "resume_predecessor", StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 2, IdempotencyKey: "explicit_resume", StorageStateHash: f.writer.Receipt.Hash, ApplicationDatabaseStorage: &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: f.storage.Generation, ExpectedStorageHash: f.storage.Hash, AcknowledgeInterruption: true}}
			// A distinct active administrator can request recovery, but cannot
			// replace the original actor's retained authority after revocation.
			requester := "usr_storage_recovery"
			if _, err = f.db.Exec(f.ctx, `INSERT INTO users(id,email,name,status,global_role) VALUES($1,'storage-recovery@example.test','Storage recovery','active','system_admin')`, requester); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, failed.CreatedBy); err != nil {
				t.Fatal(err)
			}
			revoked, err := f.pg.GetHostingOperation(f.ctx, failed.ID)
			if err != nil || !revoked.MutationHold || len(revoked.ApplicationDatabaseStorageState.RecoveryActions) != 0 {
				t.Fatal("revoked original owner retained recovery offer", err)
			}
			if _, err = f.pg.RequestHostingDatabaseConnectionRecovery(f.ctx, "revoked_original_recovery", f.application.ID, requester, input, destinationEvent("revoked_original_recovery")); err == nil {
				t.Fatal("different current owner bypassed original owner revocation")
			}
			// Deliberate fixture administration restores authority for the
			// remaining positive workflow; product recovery never does this.
			if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='active' WHERE id=$1`, failed.CreatedBy); err != nil {
				t.Fatal(err)
			}
			queued, err := f.pg.RequestHostingDatabaseConnectionRecovery(f.ctx, "explicit_different_owner", f.application.ID, requester, input, destinationEvent("explicit_different_owner"))
			if err != nil || queued.State != "failed" || queued.ApplicationDatabaseStorageState.RecoveryState != "queued" {
				t.Fatal("fresh resume admission", err)
			}
			claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.RunSpec.HostingMode != "storage_recover" {
				t.Fatal("fresh recovery claim", err)
			}
			stale := f.appAuthority
			f.appAuthority = domain.HostingAuthority{OperationID: failed.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			f.appOperation, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority)
			if err != nil {
				t.Fatal("fresh recovery plan", err)
			}
			if _, err = f.pg.AuthorizeHostingApplicationDatabaseStorageEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseStorageEffectRequest{HostingAuthority: stale, AuthorizationID: strings.Repeat("c", 32), Action: "resume_predecessor", StateHash: f.writer.Receipt.Hash, StageHash: f.stage.Hash}); err == nil {
				t.Fatal("old deploy lease authorized resume")
			}
			f = f.write(t, "resume_intent", nil)
			for _, actor := range []string{failed.CreatedBy, requester} {
				if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, actor); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.AuthorizeHostingApplicationDatabaseStorageEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseStorageEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: strings.Repeat("d", 32), Action: "resume_predecessor", StateHash: f.writer.Receipt.Hash, StageHash: f.stage.Hash}); err == nil {
					t.Fatal("revoked original or recovery owner authorized fresh resume effect")
				}
				if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='active' WHERE id=$1`, actor); err != nil {
					t.Fatal(err)
				}
			}
			f.effect(t, "resume_predecessor")
			old := f.appOperation.ApplicationDatabase.Members[0]
			endpoint, _, _, ok := domain.HostingDatabaseMemberEndpoint(domain.HostingDatabaseNetworkMemberProof(old))
			if !ok {
				t.Fatal("old endpoint")
			}
			endpoint.EndpointID = strings.Repeat("9", 64)
			f = f.write(t, "resumed", &domain.HostingDatabaseBindingEndpointObservation{Endpoint: endpoint, Present: true})
			var originalTask, originalLease, recoveryTask, recoveryLease string
			if err = f.db.QueryRow(f.ctx, `SELECT original.status,original_lease.status,recovery.status,lease.status FROM task_runs original JOIN run_leases original_lease ON original_lease.id=$4 JOIN task_runs recovery ON recovery.id=$2 JOIN run_leases lease ON lease.id=$3 WHERE original.id=$1`, stale.RunID, f.appAuthority.RunID, f.appAuthority.LeaseID, stale.LeaseID).Scan(&originalTask, &originalLease, &recoveryTask, &recoveryLease); err != nil || originalTask != "failed" || originalLease != "failed" || recoveryTask != "succeeded" || recoveryLease != "succeeded" {
				t.Fatal("recovery task must succeed independently of failed original history", err)
			}
			if f.appOperation.State != "failed" || f.appOperation.MutationHold || f.appOperation.ApplicationDatabaseStorageState.RecoveryState != "succeeded" {
				t.Fatal("resume incorrectly settled original deployment")
			}
			var healthyAfter string
			if err = f.db.QueryRow(f.ctx, `SELECT healthy::text FROM hosting_binding_state WHERE binding_id=$1`, f.appOperation.BindingID).Scan(&healthyAfter); err != nil || healthyAfter != healthyBefore {
				t.Fatal("resume rewrote predecessor healthy", err)
			}
			view, err = f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
			if err != nil || view.MembershipRevision != 3 || view.HoldOperationID != "" {
				t.Fatal("resume membership atomic settlement", err)
			}
			var members []domain.HostingDatabaseNetworkMemberEvidence
			var raw []byte
			if err = f.db.QueryRow(f.ctx, `SELECT members FROM hosting_database_membership_heads WHERE database_binding_id=$1`, f.appOperation.ApplicationDatabase.Binding.DatabaseBindingID).Scan(&raw); err != nil || json.Unmarshal(raw, &members) != nil || len(members) != 1 || members[0].Resume == nil || members[0].Resume.WriterReceipt.Hash != f.writer.Receipt.Hash {
				t.Fatal("missing dedicated resumed member evidence", err)
			}
			f.connectionFixture = f.maintenance(t, 3, "maintenance_after_resume")
		})
	}
}

func TestPostgresApplicationDatabaseStorageSuccessfulBootstrapAndMaintenance(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newConnectedStorageFixture(t, engine).prepare(t).write(t, "stop_intent", nil)
			f.effect(t, "stop_predecessor")
			f = f.write(t, "stopped", nil)
			f.connectionFixture = f.connectionFixture.ready(t).retire(t).complete(t)
			if f.appOperation.Plan.Storage == nil {
				t.Fatal("missing composed storage")
			}
			f.connectionFixture = f.maintenance(t, 4, "storage_member_maintenance")
		})
	}
}
