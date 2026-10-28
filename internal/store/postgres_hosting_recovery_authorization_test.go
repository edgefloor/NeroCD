package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

// Create a second real connection on the same controller, in another project.
// Only metadata and simulated runner receipts are needed, as in the lifecycle
// fixtures. No database or application container is started by this test.
func recoveryConnectionInOtherProject(t *testing.T, f connectedStorageFixture) domain.Resource {
	t.Helper()
	application := createDestinationResource(t, f.pg, "recovery_other", "proj_security")
	destination := f.appOperation.Snapshot.DestinationID
	grantDestination(t, f.pg, destination, application.ProjectID, "grant_recovery_other")
	if _, err := f.pg.BindResourceDestination(f.ctx, store.ResourceBindingInput{ID: "binding_recovery_other", ResourceID: application.ID, DestinationID: destination, CreatedBy: "usr_bootstrap", CreatedAt: time.Now()}, destinationEvent("binding_recovery_other")); err != nil {
		t.Fatal("bind other application", err)
	}
	database, err := f.service.CreateHostingDatabase(f.ctx, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("9", 32), ProjectID: application.ProjectID, ProjectEnvironmentID: application.ProjectEnvironmentID, Name: "recovery other database", DestinationID: destination, Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe})
	if err != nil {
		t.Fatal("create other database", err)
	}
	row, err := sqlcgen.New(f.db).ReadHostingStorage(f.ctx, database.Resource.ID)
	if err != nil {
		t.Fatal("read other storage", err)
	}
	storage := domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	op, err := f.service.RequestHostingDatabaseDeployment(f.ctx, database.Resource.ID, databaseExecutionInput("recovery_other_database"))
	if err != nil {
		t.Fatal("request other database deployment", err)
	}
	other := prepareDatabaseExecution(t, storageMaterializationFixture{pg: f.pg, db: f.db, resource: database.Resource, runner: f.runner, storage: storage}, op)
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, other.request())
	if err != nil {
		t.Fatal("begin other storage", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, other.proof(ack)); err != nil {
		t.Fatal("materialize other storage", err)
	}
	if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, other.authority); err != nil {
		t.Fatal("apply other database", err)
	}
	if _, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: other.authority, ReceiptID: "recovery_other_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("9", 64), ImageID: other.op.Release.ImageID, DaemonID: other.op.Release.DaemonID, PlanHash: other.op.Plan.Hash}}); err != nil {
		t.Fatal("complete other database", err)
	}
	in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: database.Resource.ID, ExpectedResourceGeneration: application.Generation, ExpectedDatabaseGeneration: database.Resource.Generation, RequestID: strings.Repeat("8", 32)}, Deployment: hostingDeploymentRequest()}
	in.Deployment.IdempotencyKey = "recovery_other_connection"
	in.Deployment.HostPort = 18090
	if _, err = f.service.ConnectHostingApplicationDatabase(f.ctx, application.ID, in); err != nil {
		t.Fatal("connect other application", err)
	}
	return application
}

func TestPostgresStorageRecoveryReplayRequiresCurrentOperationResourceAuthority(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) { testStorageRecoveryReplayAuthority(t, engine) })
	}
}

func testStorageRecoveryReplayAuthority(t *testing.T, engine string) {
	t.Helper()
	f := newConnectedStorageFixture(t, engine).prepare(t)
	other := recoveryConnectionInOtherProject(t, f)
	f = f.write(t, "stop_intent", nil)
	f.effect(t, "stop_predecessor")
	f = f.write(t, "stopped", nil)
	f.connectionFixture = f.connectionFixture.sendStage(t, "candidate_created").sendStage(t, "attach_intent").sendStage(t, "network_configured")
	f.effect(t, "stop_candidate")
	f = f.write(t, "candidate_stopped", nil).write(t, "replacement_failed", nil)
	requester := "usr_replay_requester"
	if _, err := f.db.Exec(f.ctx, `INSERT INTO users(id,email,name,status,global_role) VALUES($1,'replay-requester@example.test','Recovery requester','active','system_admin')`, requester); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseConnectionRecoveryInput{OperationID: f.appOperation.ID, Action: "resume_predecessor", StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 2, IdempotencyKey: "authorized_recovery", StorageStateHash: f.writer.Receipt.Hash, ApplicationDatabaseStorage: &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: f.storage.Generation, ExpectedStorageHash: f.storage.Hash, AcknowledgeInterruption: true}}
	actor := auth.WithPrincipal(t.Context(), auth.Principal{ID: requester, Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	accepted, err := f.service.RequestHostingDatabaseConnectionRecovery(actor, f.application.ID, in)
	if err != nil {
		t.Fatal("recovery admission", err)
	}
	if replay, err := f.service.RequestHostingDatabaseConnectionRecovery(actor, f.application.ID, in); err != nil || replay.ID != accepted.ID {
		t.Fatal("authorized replay", err)
	}
	if engine == "mysql" {
		before := connectionCounts(t, f.connectionFixture)
		if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
			t.Fatal(err)
		}
		if got, err := f.service.RequestHostingDatabaseConnectionRecovery(actor, f.application.ID, in); !errors.Is(err, store.ErrConflict) || got.ID != "" || connectionCounts(t, f.connectionFixture) != before {
			t.Fatal("MySQL recovery replay bypassed current engine capability", err)
		}
		if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
			t.Fatal(err)
		}
		if replay, err := f.service.RequestHostingDatabaseConnectionRecovery(actor, f.application.ID, in); err != nil || replay.ID != accepted.ID {
			t.Fatal("restored MySQL capability did not retain exact replay", err)
		}
	}
	if _, err := f.pg.UpsertProjectMember(f.ctx, domain.ProjectMember{ID: "member_recovery_other", ProjectID: other.ProjectID, UserID: requester, Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE users SET global_role='user' WHERE id=$1`, requester); err != nil {
		t.Fatal(err)
	}
	actor = auth.WithPrincipal(t.Context(), auth.Principal{ID: requester, Provider: domain.PrincipalLocal, Roles: []string{domain.RoleUser}})
	if _, err := f.service.GetHostingDatabaseConnection(actor, other.ID); err != nil {
		t.Fatal("retained project authorization", err)
	}
	before := connectionCounts(t, f.connectionFixture)
	for _, resource := range []string{f.application.ID, other.ID} {
		if got, err := f.service.RequestHostingDatabaseConnectionRecovery(actor, resource, in); err == nil || got.ID != "" {
			t.Fatalf("revoked or cross-resource replay returned operation: id=%s err=%v", got.ID, err)
		}
	}
	if connectionCounts(t, f.connectionFixture) != before {
		t.Fatal("rejected replay mutated recovery state")
	}
}
