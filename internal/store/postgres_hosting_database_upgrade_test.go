package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func upgradeFixture(t *testing.T, engine string) (networkCaptureFixture, domain.HostingDatabaseUpgradeInput) {
	t.Helper()
	f := newNetworkCaptureFixture(t, engine)
	c := f.request(t)
	r, err := f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(f.claim(t, c)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseUpgradeCapability); err != nil {
		t.Fatal(err)
	}
	return f, domain.HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: c.ID, ExpectedOriginHash: r.OriginHash, IdempotencyKey: "empty_upgrade"}
}

func upgradeCounts(t *testing.T, f networkCaptureFixture) string {
	t.Helper()
	var out string
	if err := f.db.QueryRow(f.ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_database_deployments),(SELECT count(*) FROM hosting_database_network_upgrades),(SELECT count(*) FROM hosting_snapshots),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM river_job),(SELECT count(*) FROM hosting_receipts),(SELECT count(*) FROM hosting_apply_plans))::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func prepareUpgrade(t *testing.T, f networkCaptureFixture, op domain.HostingOperation) networkCaptureFixture {
	t.Helper()
	if err := f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
		t.Fatal("upgrade dispatch", err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseDeploy || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("upgrade exact database task claim", err)
	}
	f.authority = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	loaded, err := f.pg.HostingRunnerPlan(f.ctx, f.runner, f.authority)
	if err != nil || loaded.DatabaseUpgrade == nil || loaded.Snapshot != op.Snapshot {
		t.Fatal("upgrade sidecar delivery", err)
	}
	f.op, err = f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: f.authority, ReceiptID: "prepared_" + op.ID, Release: op.DatabaseUpgrade.PreviousPlan.Release})
	if err != nil || !reflect.DeepEqual(f.op.DatabaseUpgrade, op.DatabaseUpgrade) || domain.ValidateHostingDatabaseUpgradeOperation(f.op) != nil || f.op.Plan == nil || f.op.Plan.Database.Network == nil || len(f.op.Plan.Database.Network.Members) != 0 {
		t.Fatal("upgrade exact proof compilation", err)
	}
	return f
}

func settleUpgrade(t *testing.T, f networkCaptureFixture) networkCaptureFixture {
	t.Helper()
	delivery := domain.HostingDatabaseCredentialRequest{Authority: f.authority, DeliveryID: domain.HostingDocumentHash(f.op.ID)[7:39], PlanHash: f.op.Plan.Hash, Mode: "deploy"}
	credentials, err := f.pg.AuthorizeHostingDatabaseCredentials(f.ctx, f.runner, f.credential, delivery)
	if err != nil {
		t.Fatal("upgrade retained credential delivery", err)
	}
	credentials.Close()
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, f.requestStorage())
	if err != nil || ack.CreatePermitted {
		t.Fatal("upgrade must reuse existing materialization", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, f.authority); err != nil {
		t.Fatal("upgrade apply begin", err)
	}
	in := domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "complete_" + f.op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: domain.HostingDocumentHash(f.op.ID)[7:], ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}
	f.op, err = f.pg.HostingComplete(f.ctx, f.runner, in)
	if err != nil || f.op.State != "succeeded" || f.op.MutationHold {
		t.Fatal("upgrade settlement", err)
	}
	return f
}

// Avoid the capture fixture's request method, which requests origin attestation.
func (f networkCaptureFixture) requestStorage() domain.HostingStorageCreateRequest {
	return f.storageMaterializationFixture.request()
}

func TestPostgresDatabaseUpgradeLifecycleAndMaintenance(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in := upgradeFixture(t, engine)
			sourceID := f.op.ID
			application, err := f.pg.GetResource(f.ctx, "resource_image_web")
			if err != nil {
				t.Fatal(err)
			}
			bindingIn := domain.HostingDatabaseBindingInput{DatabaseResourceID: f.resource.ID, ExpectedResourceGeneration: application.Generation, ExpectedDatabaseGeneration: 1, RequestID: strings.Repeat("e", 32)}
			binding, err := f.pg.PutHostingDatabaseBinding(f.ctx, "usr_bootstrap", application.ID, bindingIn, destinationEvent("upgrade_binding"))
			if err != nil {
				t.Fatal("pending binding before upgrade", err)
			}
			var originalProof []byte
			if f.db.QueryRow(f.ctx, `SELECT body FROM hosting_apply_plans WHERE operation_id=(SELECT database_operation_id FROM hosting_database_bindings WHERE application_resource_id=$1)`, application.ID).Scan(&originalProof) != nil {
				t.Fatal("pending proof read failed")
			}
			op, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, in)
			if err != nil || op.DatabaseUpgrade == nil || op.DatabaseUpgrade.PreviousPlan.CompilerVersion != domain.HostingDatabaseCompilerVersion || op.Snapshot != f.op.Snapshot {
				t.Fatal("upgrade admission", err)
			}
			f = settleUpgrade(t, prepareUpgrade(t, f, op))
			first := f
			if f.op.Plan.Database.Network.MembershipRevision != 1 {
				t.Fatal("initial empty membership revision changed")
			}
			maintenance := in
			maintenance.IdempotencyKey = "empty_maintenance"
			next, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, maintenance)
			if err != nil || next.DatabaseUpgrade.PreviousHealthy.OperationID != op.ID || next.DatabaseUpgrade.PreviousPlan.CompilerVersion != domain.HostingDatabaseNetworkCompilerVersion {
				t.Fatal("empty v2 maintenance admission", err)
			}
			f = settleUpgrade(t, prepareUpgrade(t, f, next))
			if f.op.Plan.Database.Network.MembershipRevision != 1 || f.op.Plan.Database.Network.OriginHash != in.ExpectedOriginHash {
				t.Fatal("maintenance changed origin or unchanged revision")
			}
			before := upgradeCounts(t, f)
			for _, old := range []domain.HostingDatabaseUpgradeInput{in, maintenance} {
				replay, e := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, old)
				if e != nil || replay.DatabaseUpgrade == nil || replay.ID != map[string]string{in.IdempotencyKey: op.ID, maintenance.IdempotencyKey: next.ID}[old.IdempotencyKey] {
					t.Fatal("response-loss replay rejected historical predecessor", e)
				}
			}
			if upgradeCounts(t, f) != before {
				t.Fatal("replay minted operation or authority")
			}
			if replay, e := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("capture_source")); e != nil || replay.ID != sourceID {
				t.Fatal("original v1 request receipt rejected after upgrade", e)
			}
			if _, e := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.authority.LeaseID); e != nil {
				t.Fatal(e)
			}
			if _, e := f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: first.authority, ReceiptID: "prepared_" + first.op.ID, Release: *first.op.Release}); e != nil {
				t.Fatal("historical prepared receipt acquired current-predecessor requirement", e)
			}
			if _, e := f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: first.authority, ReceiptID: "complete_" + first.op.ID, Result: *first.op.Result}); e != nil {
				t.Fatal("historical complete receipt rejected after maintenance", e)
			}
			materialized := first.proof(domain.HostingStorageCreateAck{Materialization: first.op.DatabaseUpgrade.CapturePlan.Materialization})
			if _, e := f.pg.HostingStorageMaterialized(f.ctx, f.runner, materialized); e != nil {
				t.Fatal("historical materialization receipt rejected after maintenance", e)
			}
			if _, e := f.pg.HostingApplyBegin(f.ctx, f.runner, first.authority); e == nil {
				t.Fatal("historical receipt revived writer authority")
			}
			if _, e := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("downgrade")); !errors.Is(e, store.ErrConflict) {
				t.Fatal("ordinary v1 deployment accepted against v2", e)
			}
			tx, e := f.db.Begin(f.ctx)
			if e != nil {
				t.Fatal(e)
			}
			for _, query := range []string{
				`INSERT INTO hosting_database_deployments SELECT operation_id||'_downgrade',resource_id,binding_id,destination_id,reference,credentials FROM hosting_database_deployments WHERE operation_id=$1`,
				`INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,created_by,state) SELECT id||'_downgrade',project_id,resource_id,binding_id,idempotency_key||'_downgrade',request_hash,created_by,'queued' FROM hosting_operations WHERE id=$1`,
				`INSERT INTO hosting_snapshots(operation_id,body,body_hash) SELECT operation_id||'_downgrade',body,body_hash FROM hosting_snapshots WHERE operation_id=$1`,
			} {
				if _, e = tx.Exec(f.ctx, query, sourceID); e != nil {
					_ = tx.Rollback(f.ctx)
					t.Fatal("SQL downgrade setup failed", e)
				}
			}
			_, e = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			if cleanupErr := tx.Rollback(f.ctx); cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			var denial *pgconn.PgError
			if !errors.As(e, &denial) || denial.Code != "23514" || denial.Message != "database v1 downgrade denied" {
				t.Fatal("direct SQL did not reach the v1 downgrade guard", e)
			}
			var afterProof []byte
			if f.db.QueryRow(f.ctx, `SELECT body FROM hosting_apply_plans WHERE operation_id=(SELECT database_operation_id FROM hosting_database_bindings WHERE application_resource_id=$1)`, application.ID).Scan(&afterProof) != nil || string(originalProof) != string(afterProof) {
				t.Fatal("upgrade changed pending binding original v1 proof")
			}
			replayedBinding, e := f.pg.PutHostingDatabaseBinding(f.ctx, "usr_bootstrap", application.ID, bindingIn, destinationEvent("unused_replay"))
			if e != nil || !reflect.DeepEqual(binding, replayedBinding) {
				t.Fatal("pending binding receipt changed after database upgrade", e)
			}
			if _, e = f.pg.RequestHostingDeployment(f.ctx, "hop_pending_upgrade_app", application.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("pending_upgrade_app")); !errors.Is(e, store.ErrConflict) {
				t.Fatal("pending application became deployable", e)
			}
		})
	}
}

func TestPostgresDatabaseUpgradeAdmissionAndAtomicity(t *testing.T) {
	for _, scenario := range []string{"concurrent", "changed_replay", "origin_hash", "capture_id", "capability", "revoked_grant", "audit_rollback", "river_rollback"} {
		t.Run(scenario, func(t *testing.T) {
			f, in := upgradeFixture(t, "postgresql")
			before := upgradeCounts(t, f)
			if scenario == "concurrent" {
				type result struct {
					op  domain.HostingOperation
					err error
				}
				ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
				defer cancel()
				done := make(chan result, 2)
				joined := 0
				defer func() {
					cancel()
					cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					for joined < 2 {
						select {
						case <-done:
							joined++
						case <-cleanup.Done():
							t.Error("concurrent upgrade request did not join")
							return
						}
					}
				}()
				for range 2 {
					go func() {
						op, e := f.service.RequestHostingDatabaseNetworkUpgrade(ctx, f.resource.ID, in)
						done <- result{op, e}
					}()
				}
				var id string
				for range 2 {
					select {
					case r := <-done:
						joined++
						if r.err != nil || id != "" && r.op.ID != id {
							t.Error("concurrent request did not converge", r.err)
						}
						id = r.op.ID
					case <-ctx.Done():
						t.Fatal("concurrent request failed to join")
					}
				}
				var proof, audit int
				if f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_network_upgrades),(SELECT count(*) FROM audit_events WHERE action='hosting.database_network_upgrade.requested')`).Scan(&proof, &audit) != nil || proof != 1 || audit != 1 {
					t.Fatal("concurrent upgrade duplicated proof/audit")
				}
				return
			}
			switch scenario {
			case "changed_replay":
				if _, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, in); err != nil {
					t.Fatal(err)
				}
				before = upgradeCounts(t, f)
				in.ExpectedGeneration++
			case "origin_hash":
				in.ExpectedOriginHash = "sha256:" + strings.Repeat("e", 64)
			case "capture_id":
				in.CaptureID = "hnc_" + strings.Repeat("e", 32)
			case "capability":
				if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseUpgradeCapability); e != nil {
					t.Fatal(e)
				}
			case "revoked_grant":
				if _, e := f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, f.op.Snapshot.DestinationID, f.op.ProjectID); e != nil {
					t.Fatal(e)
				}
			case "audit_rollback", "river_rollback":
				table, condition := "audit_events", "NEW.action='hosting.database_network_upgrade.requested'"
				if scenario == "river_rollback" {
					table, condition = "river_job", "true"
				}
				if _, e := f.db.Exec(f.ctx, `CREATE FUNCTION upgrade_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'upgrade rollback test'; END IF; RETURN NEW; END $$; CREATE TRIGGER upgrade_fail BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION upgrade_fail()`); e != nil {
					t.Fatal(e)
				}
			}
			if _, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, in); err == nil {
				t.Fatal("invalid upgrade accepted")
			}
			if upgradeCounts(t, f) != before {
				t.Fatal("rejected upgrade leaked operation/proof/audit/job")
			}
		})
	}
}

func TestPostgresDatabaseUpgradeDirectSQLConstraints(t *testing.T) {
	f, in := upgradeFixture(t, "postgresql")
	op, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"capture", "receipt", "previous", "proof", "snapshot"} {
		t.Run(mutation, func(t *testing.T) {
			tx, e := f.db.Begin(f.ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(f.ctx) }()
			var row struct {
				proof                            []byte
				hash, capture, receipt, previous string
			}
			if e = tx.QueryRow(f.ctx, `SELECT proof,proof_hash,capture_id,origin_receipt_id,previous_operation_id FROM hosting_database_network_upgrades WHERE operation_id=$1`, op.ID).Scan(&row.proof, &row.hash, &row.capture, &row.receipt, &row.previous); e != nil {
				t.Fatal(e)
			}
			// Existing associations are immutable, even if a complete valid proof
			// is offered with a substituted target or predecessor.
			var doc map[string]any
			if json.Unmarshal(row.proof, &doc) != nil {
				t.Fatal("proof fixture invalid")
			}
			switch mutation {
			case "capture":
				row.capture = "hnc_" + strings.Repeat("f", 32)
			case "receipt":
				row.receipt = "wrong_receipt"
			case "previous":
				row.previous = op.ID
			case "proof":
				doc["version"] = "forged"
			case "snapshot":
				doc["previous_plan"].(map[string]any)["snapshot"].(map[string]any)["resource_id"] = "forged"
			}
			raw, _ := json.Marshal(doc)
			_, e = tx.Exec(f.ctx, `UPDATE hosting_database_network_upgrades SET capture_id=$2,origin_receipt_id=$3,previous_operation_id=$4,proof=$5 WHERE operation_id=$1`, op.ID, row.capture, row.receipt, row.previous, raw)
			if e == nil {
				t.Fatal("immutable upgrade proof changed")
			}
		})
	}
	// A v2 plan without its operation proof cannot enter the apply-plan table.
	f = prepareUpgrade(t, f, op)
	for _, mutation := range []string{"capture_plan", "origin_receipt", "previous_healthy", "v2_without_proof", "nonempty_members", "new_origin"} {
		t.Run("insert_"+mutation, func(t *testing.T) {
			tx, e := f.db.Begin(f.ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(f.ctx) }()
			for _, query := range []string{
				`INSERT INTO hosting_database_deployments SELECT operation_id||'_forged',resource_id,binding_id,destination_id,reference,credentials FROM hosting_database_deployments WHERE operation_id=$1`,
				`INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,created_by,state) SELECT id||'_forged',project_id,resource_id,binding_id,idempotency_key||'_forged',request_hash,created_by,'queued' FROM hosting_operations WHERE id=$1`,
				`INSERT INTO hosting_snapshots(operation_id,body,body_hash) SELECT operation_id||'_forged',body,body_hash FROM hosting_snapshots WHERE operation_id=$1`,
				`INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) SELECT a.id||'_forged',a.actor_id,a.action,a.target_id||'_forged',a.metadata,a.created_at FROM audit_events a JOIN hosting_database_network_upgrades u ON u.audit_id=a.id WHERE u.operation_id=$1`,
			} {
				if _, e = tx.Exec(f.ctx, query, op.ID); e != nil {
					t.Fatal("forged-row setup failed", e)
				}
			}
			proof := *op.DatabaseUpgrade
			switch mutation {
			case "capture_plan":
				proof.CapturePlan.RunnerID = "wrong_runner"
			case "origin_receipt":
				proof.OriginReceipt.Origin.NetworkID = strings.Repeat("e", 64)
			case "previous_healthy":
				proof.PreviousHealthy.ContainerID = strings.Repeat("e", 64)
			}
			raw, _ := json.Marshal(proof)
			if mutation != "v2_without_proof" {
				_, e = tx.Exec(f.ctx, `INSERT INTO hosting_database_network_upgrades(operation_id,capture_id,origin_receipt_id,previous_operation_id,proof,proof_hash,audit_id) SELECT operation_id||'_forged',capture_id,origin_receipt_id,previous_operation_id,$2,proof_hash,audit_id||'_forged' FROM hosting_database_network_upgrades WHERE operation_id=$1`, op.ID, raw)
				if e != nil {
					t.Fatal("forged association setup failed", e)
				}
			}
			if mutation == "v2_without_proof" || mutation == "nonempty_members" || mutation == "new_origin" {
				plan := *f.op.Plan
				db := *plan.Database
				network := *db.Network
				db.Network = &network
				plan.Database = &db
				if mutation == "nonempty_members" {
					network.Members = []domain.HostingDatabaseNetworkMember{{BindingID: "forged"}}
				}
				if mutation == "new_origin" {
					network.NetworkID = strings.Repeat("e", 64)
				}
				raw, _ = json.Marshal(plan)
				if _, e = tx.Exec(f.ctx, `INSERT INTO hosting_apply_plans(operation_id,body,body_hash) VALUES($1,$2,$3)`, op.ID+"_forged", raw, plan.Hash); e != nil {
					t.Fatal("forged plan setup failed", e)
				}
			}
			_, e = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			expected := "database network upgrade proof association mismatch"
			switch mutation {
			case "previous_healthy":
				expected = "database network upgrade receipt or audit mismatch"
			case "v2_without_proof":
				expected = "database v2 requires immutable upgrade proof"
			case "nonempty_members", "new_origin":
				expected = "database upgrade plan substitution"
			}
			var denial *pgconn.PgError
			if !errors.As(e, &denial) || denial.Code != "23514" || denial.Message != expected {
				t.Fatal("direct SQL did not reach the intended upgrade guard", mutation, e)
			}
		})
	}
	tx, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `DELETE FROM hosting_database_network_upgrades WHERE operation_id=$1`, op.ID)
	if err == nil {
		t.Fatal("upgrade proof deleted beneath prepared v2 plan")
	}
}

func TestPostgresDatabaseUpgradeRevokedCapabilityBoundaries(t *testing.T) {
	for _, stage := range []string{"dispatch", "claim", "prepare", "delivery", "status", "storage", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			f, in := upgradeFixture(t, "postgresql")
			op, err := f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "claim" || stage == "prepare" {
				if err = f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "prepare" {
				claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				f.authority = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
				if _, e = f.pg.HostingRunnerPlan(f.ctx, f.runner, f.authority); e != nil {
					t.Fatal("valid preparation authority failed before capability fault", e)
				}
			}
			if stage == "delivery" || stage == "storage" || stage == "receipt" {
				f = prepareUpgrade(t, f, op)
			}
			if stage == "receipt" {
				f = settleUpgrade(t, f)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseUpgradeCapability); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "dispatch":
				err = f.pg.PrepareDeployment(f.ctx, op.ID)
				if err == nil {
					var state string
					if f.db.QueryRow(f.ctx, `SELECT state FROM hosting_operations WHERE id=$1`, op.ID).Scan(&state) != nil {
						t.Fatal("dispatch state read failed")
					}
					if state != "failed" {
						// River may have committed the dispatch before revocation.
						// That existing task must still fail current claim admission.
						if _, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); e == nil {
							t.Fatal("revoked capability claimed dispatched upgrade")
						}
					}
					return
				}
			case "claim":
				_, err = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
			case "prepare":
				_, err = f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: f.authority, ReceiptID: "revoked_prepared", Release: op.DatabaseUpgrade.PreviousPlan.Release})
			case "delivery":
				ack, e := f.pg.AuthorizeHostingDatabaseCredentials(f.ctx, f.runner, f.credential, domain.HostingDatabaseCredentialRequest{Authority: f.authority, DeliveryID: strings.Repeat("d", 32), PlanHash: f.op.Plan.Hash, Mode: "deploy"})
				ack.Close()
				err = e
			case "status":
				_, err = f.pg.GetHostingOperation(f.ctx, op.ID)
			case "storage":
				_, err = f.pg.HostingStorageCreateBegin(f.ctx, f.runner, f.requestStorage())
			case "receipt":
				_, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "complete_" + f.op.ID, Result: *f.op.Result})
			}
			if err == nil {
				t.Fatal("revoked upgrade capability accepted at", stage)
			}
		})
	}
}

func TestPostgresDatabaseUpgradeAuthorityAfterWait(t *testing.T) {
	for _, path := range []string{"request_audit_actor", "request_deferred_actor", "prepared_deferred_lease", "delivery_audit_lease", "delivery_audit_actor"} {
		t.Run(path, func(t *testing.T) {
			f, in := upgradeFixture(t, "postgresql")
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			f.ctx = ctx
			actor, _ := proxyLegacyToken(t, f.pg, f.service, "tok_upgrade_wait", []string{domain.RoleSystemAdmin})
			request := strings.HasPrefix(path, "request_")
			var op domain.HostingOperation
			if !request {
				var err error
				op, err = f.pg.RequestHostingDatabaseNetworkUpgrade(ctx, "hop_upgrade_wait", f.resource.ID, actor.ID, in, destinationEvent("upgrade_wait_request"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(path, "delivery_") {
					f = prepareUpgrade(t, f, op)
				} else {
					if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
						t.Fatal(err)
					}
					claim, e := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
					if e != nil {
						t.Fatal(e)
					}
					f.authority = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
					if _, e = f.pg.HostingRunnerPlan(ctx, f.runner, f.authority); e != nil {
						t.Fatal("valid preparation authority failed before expiry fault", e)
					}
				}
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started, joined := false, false
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error(e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("upgrade waiter did not join")
					}
				}
			}()
			var key int64
			var pid int
			if blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':upgrade-wait',0),pg_backend_pid()`).Scan(&key, &pid) != nil {
				t.Fatal("barrier identity failed")
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			ddl := `CREATE FUNCTION upgrade_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'hosting.database%' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':upgrade-wait',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER upgrade_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION upgrade_wait()`
			if strings.Contains(path, "deferred") {
				var table string
				if request {
					table = "hosting_database_network_upgrades"
				} else {
					table = "hosting_apply_plans"
				}
				ddl = `CREATE FUNCTION upgrade_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':upgrade-wait',0)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER upgrade_wait AFTER INSERT ON ` + table + ` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION upgrade_wait()`
			}
			if _, err = f.db.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}
			var expiry time.Time
			if strings.HasSuffix(path, "_lease") {
				if f.db.QueryRow(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, f.authority.LeaseID).Scan(&expiry) != nil {
					t.Fatal("lease expiry setup failed")
				}
			} else {
				if f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, actor.ID).Scan(&expiry) != nil {
					t.Fatal("actor expiry setup failed")
				}
			}
			before := upgradeCounts(t, f)
			started = true
			go func() {
				var e error
				switch {
				case request:
					_, e = f.pg.RequestHostingDatabaseNetworkUpgrade(ctx, "hop_upgrade_wait", f.resource.ID, actor.ID, in, destinationEvent("upgrade_wait_request"))
				case strings.HasPrefix(path, "prepared_"):
					_, e = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: f.authority, ReceiptID: "upgrade_wait_prepared", Release: op.DatabaseUpgrade.PreviousPlan.Release})
				default:
					ack, x := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, f.credential, domain.HostingDatabaseCredentialRequest{Authority: f.authority, DeliveryID: strings.Repeat("f", 32), PlanHash: f.op.Plan.Hash, Mode: "deploy"})
					ack.Close()
					e = x
				}
				done <- e
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting, expired bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))),clock_timestamp()>$2`, pid, expiry).Scan(&waiting, &expired) != nil {
					t.Fatal("wait observation failed")
				}
				if waiting && expired {
					break
				}
				select {
				case e := <-done:
					joined = true
					t.Fatal("operation escaped barrier", e)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("barrier deadline")
				}
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				joined = true
				if e == nil {
					t.Fatal("expired authority committed after wait")
				}
			case <-ctx.Done():
				t.Fatal("waiter deadline")
			}
			if upgradeCounts(t, f) != before {
				t.Fatal("expired request leaked durable effects")
			}
		})
	}
}

func TestPostgresDatabaseUpgradeCapabilityAfterRunnerWait(t *testing.T) {
	for _, stage := range []string{"request", "status"} {
		t.Run(stage, func(t *testing.T) {
			f, in := upgradeFixture(t, "postgresql")
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			var op domain.HostingOperation
			if stage == "status" {
				var err error
				op, err = f.service.RequestHostingDatabaseNetworkUpgrade(ctx, f.resource.ID, in)
				if err != nil {
					t.Fatal(err)
				}
				f = settleUpgrade(t, prepareUpgrade(t, f, op))
			}
			before := upgradeCounts(t, f)
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started, joined := false, false
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error(e)
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("runner waiter did not join")
					}
				}
			}()
			var pid int
			if blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM runners WHERE id=$1 FOR UPDATE`, f.runner).Scan(&pid) != nil {
				t.Fatal("runner blocker failed")
			}
			started = true
			go func() {
				var e error
				if stage == "request" {
					_, e = f.service.RequestHostingDatabaseNetworkUpgrade(ctx, f.resource.ID, in)
				} else {
					_, e = f.pg.GetHostingOperation(ctx, op.ID)
				}
				done <- e
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
					t.Fatal("runner wait observation failed")
				}
				if waiting {
					break
				}
				select {
				case e := <-done:
					joined = true
					t.Fatal("upgrade escaped runner lock", e)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("runner barrier deadline")
				}
			}
			if _, err = blocker.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseUpgradeCapability); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				joined = true
				if e == nil {
					t.Fatal("upgrade used capability read before runner lock")
				}
			case <-ctx.Done():
				t.Fatal("runner waiter deadline")
			}
			if upgradeCounts(t, f) != before {
				t.Fatal("runner revocation leaked upgrade effects")
			}
		})
	}
}
