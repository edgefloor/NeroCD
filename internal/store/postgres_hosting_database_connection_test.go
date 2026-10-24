package store_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

type connectionFixture struct {
	networkCaptureFixture
	application   domain.Resource
	connection    domain.HostingDatabaseConnection
	input         domain.HostingDatabaseConnectionInput
	runnerContext context.Context
	appOperation  domain.HostingOperation
	appAuthority  domain.HostingAuthority
	stage         domain.HostingApplicationDatabaseStage
	actor         string
}

func TestPostgresDatabaseConnectionMigrationUpgrade(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL migration upgrade")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal("open migration fixture")
	}
	schema := "nerocd_conn_upgrade_" + strings.ToLower(rand.Text()[:16])
	created := false
	// Register cleanup before CREATE. A collision never grants schema ownership.
	t.Cleanup(func() {
		defer admin.Close()
		if !created {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := admin.Exec(cleanup, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); e != nil {
			t.Error("drop owned migration schema", schema, e)
		}
	})
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("create migration fixture", err)
	}
	created = true
	address = databaseURLWithSearchPath(t, address, schema)
	database, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal("open migration schema")
	}
	t.Cleanup(database.Close)
	if err = applyEmbeddedMigrations(ctx, database, "", "0071_hosting_database_network_upgrades.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, address)
	if err != nil {
		t.Fatal("open accepted predecessor schema", err)
	}
	t.Cleanup(func() { closePostgresStore(t, pg) })
	var before, after string
	const history = `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM projects p),(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM principals p),(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM resources p))::text`
	if err = database.QueryRow(ctx, history).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.GetHostingDatabaseConnection(ctx, "usr_bootstrap", "missing"); !errors.Is(err, store.ErrHostingDatabaseConnectionUnavailable) {
		t.Fatal("old schema manufactured connection support", err)
	}
	if err = applyEmbeddedMigrations(ctx, database, "0071_hosting_database_network_upgrades.sql", "0072_hosting_application_database_connections.sql"); err != nil {
		t.Fatal(err)
	}
	var compatible bool
	if err = database.QueryRow(ctx, `SELECT nerocd_database_connection_schema_compatible()`).Scan(&compatible); err != nil || !compatible {
		t.Fatal("new connection schema incompatible", err)
	}
	if err = database.QueryRow(ctx, history).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed historical resource or principal data", err)
	}
}

func newConnectionFixture(t *testing.T, engine string) connectionFixture {
	t.Helper()
	f := newNetworkCaptureFixture(t, engine)
	capabilities := []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingDatabaseUpgradeCapability, domain.HostingApplicationDatabaseCapability, domain.HostingDatabaseMaintenanceCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, capabilities); err != nil {
		t.Fatal(err)
	}
	application, err := f.pg.GetResource(f.ctx, "resource_image_web")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindHostingDatabaseConnectionRunnerContext(f.ctx, f.runner, f.credential)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: f.resource.ID, ExpectedResourceGeneration: application.Generation, ExpectedDatabaseGeneration: f.resource.Generation, RequestID: strings.Repeat("e", 32)}, Deployment: hostingDeploymentRequest()}
	in.Deployment.IdempotencyKey = "connected_initial"
	return connectionFixture{networkCaptureFixture: f, application: application, input: in, runnerContext: bound}
}

func connectionCounts(t *testing.T, f connectionFixture) string {
	t.Helper()
	var result string
	if err := f.db.QueryRow(f.ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_database_connections),(SELECT count(*) FROM hosting_database_bindings),(SELECT count(*) FROM hosting_runtime_configuration_versions),(SELECT count(*) FROM hosting_database_connection_executions),(SELECT count(*) FROM hosting_database_network_holds),(SELECT count(*) FROM hosting_application_database_stages),(SELECT count(*) FROM hosting_application_database_effect_authorizations),(SELECT count(*) FROM hosting_database_membership_receipts),(SELECT count(*) FROM hosting_database_connection_recoveries),(SELECT count(*) FROM run_leases),(SELECT count(*) FROM audit_events))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f connectionFixture) connect(t *testing.T) connectionFixture {
	t.Helper()
	var view domain.HostingDatabaseConnection
	var err error
	if f.actor == "" {
		view, err = f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input)
	} else {
		view, err = f.pg.ConnectHostingApplicationDatabase(f.ctx, "hcn_expiring_actor", f.application.ID, f.actor, f.input, destinationEvent("connection_expiring_actor"))
	}
	if err != nil || view.BindingID == "" || view.CaptureID == "" || view.HoldOperationID != view.UpgradeOperationID {
		t.Fatal("atomic connection admission", err)
	}
	f.connection = view
	if err = f.pg.PrepareHostingDatabaseConnection(f.ctx, view.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal("capture continuation", err)
	}
	// The capture is selected through the existing durable automatic queue.
	claim, err := f.pg.ClaimNextHostingDatabaseNetworkCapture(f.ctx, f.runner, f.credential, time.Minute)
	if err != nil || claim.Capture.ID != view.CaptureID {
		t.Fatal("connection capture claim", err)
	}
	if _, err = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(claim)); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareHostingDatabaseConnection(f.ctx, view.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal("upgrade continuation", err)
	}
	upgrade := awaitHostingDispatch(t, f.pg, view.UpgradeOperationID)
	f.networkCaptureFixture = settleUpgrade(t, prepareUpgrade(t, f.networkCaptureFixture, upgrade))
	if err = f.pg.PrepareHostingDatabaseConnection(f.ctx, view.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal("application continuation", err)
	}
	f.appOperation = awaitHostingDispatch(t, f.pg, view.ApplicationOperationID)
	if f.appOperation.ApplicationDatabase == nil || domain.ValidateHostingApplicationDatabaseOperation(f.appOperation) != nil {
		t.Fatal("missing exact application sidecar")
	}
	return f
}

func (f connectionFixture) prepareApp(t *testing.T) connectionFixture {
	t.Helper()
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != f.appOperation.ID {
		t.Fatal("bound application claim", err)
	}
	f.appAuthority = domain.HostingAuthority{OperationID: f.appOperation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority); err != nil {
		t.Fatal("bound application plan", err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("d", 64), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "amd64", DaemonID: f.op.Release.DaemonID}
	release.Hash = domain.HostingDocumentHash(release)
	f.appOperation, err = f.pg.HostingPrepared(f.runnerContext, f.runner, domain.HostingPreparedRequest{HostingAuthority: f.appAuthority, ReceiptID: "prepared_" + f.appOperation.ID, Release: release})
	if err != nil || f.appOperation.Plan == nil || domain.ValidateHostingApplicationDatabaseOperation(f.appOperation) != nil {
		t.Fatal("bound application preparation", err)
	}
	delivery, err := f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, f.runner, f.credential, domain.HostingRuntimeEnvironmentRequest{Authority: f.appAuthority, DeliveryID: domain.HostingDocumentHash(f.appOperation.ID)[7:39], PlanHash: f.appOperation.Plan.Hash, RuntimeConfiguration: f.appOperation.Snapshot.RuntimeConfiguration})
	if err != nil {
		t.Fatal("exact bound app credential lease delivery", err)
	}
	if len(delivery.Records) != 1 {
		delivery.Close()
		t.Fatal("unexpected bound credential count")
	}
	delivery.Close()
	if _, err = f.pg.HostingApplyBegin(f.runnerContext, f.runner, f.appAuthority); err != nil {
		t.Fatal("bound application apply", err)
	}
	return f
}

func (f connectionFixture) sendStage(t *testing.T, phase string) connectionFixture {
	t.Helper()
	op := f.appOperation
	p := op.ApplicationDatabase
	input, err := domain.HostingApplicationDatabaseExecutionPlanInput(*p)
	if err != nil {
		t.Fatal(err)
	}
	if f.stage.Version == "" {
		candidate := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: op.ID, PlanHash: op.Plan.Hash, CreateNonce: p.CreateNonce, ContainerID: domain.HostingDocumentHash(op.ID)[7:], ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, DefaultNetworkID: strings.Repeat("f", 64)}
		candidate.Hash = candidate.DocumentHash()
		attach, e := domain.CompileHostingApplicationDatabaseAttachPlan(*op.Plan, input, candidate)
		if e != nil {
			t.Fatal(e)
		}
		f.stage = domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, OperationID: op.ID, PlanHash: op.Plan.Hash, Candidate: candidate, AttachPlan: attach}
	}
	f.stage.Phase = phase
	f.stage.ReceiptID = "stage_" + phase + "_" + domain.HostingDocumentHash(op.ID)[7:39]
	switch phase {
	case "attach_intent":
		endpoint := domain.HostingDatabaseBindingEndpoint{Target: f.stage.AttachPlan.Target}
		r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "attach_intent_" + domain.HostingDocumentHash(op.ID)[7:39], OperationID: op.ID, PlanHash: f.stage.AttachPlan.Hash, BindingHash: p.Binding.Hash, MembershipRevision: p.MembershipRevision + 1, Phase: "attach_intent", Intent: &endpoint}
		r.Hash = r.DocumentHash()
		f.stage.EndpointReceipt = &r
	case "attached":
		r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "attached_" + domain.HostingDocumentHash(op.ID)[7:39], OperationID: op.ID, PlanHash: f.stage.AttachPlan.Hash, BindingHash: p.Binding.Hash, MembershipRevision: p.MembershipRevision + 1, Phase: "attached", Observation: &domain.HostingDatabaseBindingEndpointObservation{Endpoint: domain.HostingDatabaseBindingEndpoint{Target: f.stage.AttachPlan.Target, EndpointID: domain.HostingDocumentHash("endpoint_" + op.ID)[7:]}, Present: true}}
		r.Hash = r.DocumentHash()
		f.stage.EndpointReceipt = &r
	}
	f.stage.Hash = f.stage.DocumentHash()
	if _, err = f.pg.HostingApplicationDatabaseStage(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: f.appAuthority, Stage: f.stage}); err != nil {
		t.Fatal("stage "+phase, err)
	}
	state, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, f.appAuthority)
	if err != nil || state.MembershipRevision != p.MembershipRevision || state.HoldOperationID != op.ID || state.Stage == nil || state.Stage.Hash != f.stage.Hash || f.stage.AttachPlan.MembershipRevision != p.MembershipRevision+1 {
		t.Fatal("stage advanced committed membership or lost hold", phase, err)
	}
	return f
}

func (f connectionFixture) authorize(t *testing.T, action string) domain.HostingApplicationDatabaseEffectAuthorization {
	t.Helper()
	expected := ""
	if action != "create" {
		expected = f.stage.Hash
	}
	in := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: domain.HostingDocumentHash(action + f.appOperation.ID)[7:39], Action: action, ExpectedStageHash: expected}
	out, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, in)
	if err != nil || out.Validate() != nil || out.MembershipRevision != f.appOperation.ApplicationDatabase.MembershipRevision {
		t.Fatal("effect authorization "+action, err)
	}
	replay, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, in)
	if err != nil || replay != out {
		t.Fatal("effect replay refreshed authority", err)
	}
	return out
}

func (f connectionFixture) ready(t *testing.T) connectionFixture {
	t.Helper()
	f.authorize(t, "create")
	f = f.sendStage(t, "candidate_created").sendStage(t, "attach_intent")
	f.authorize(t, "configure")
	f = f.sendStage(t, "network_configured")
	f.authorize(t, "start")
	return f.sendStage(t, "attached").sendStage(t, "ready")
}

func (f connectionFixture) retire(t *testing.T) connectionFixture {
	t.Helper()
	plan, err := f.pg.HostingApplicationDatabaseRetirementPlan(f.ctx, f.runner, f.credential, f.appAuthority)
	if err != nil {
		t.Fatal("retire intent", err)
	}
	f.authorize(t, "retire")
	r := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "detached_" + domain.HostingDocumentHash(f.appOperation.ID)[7:39], OperationID: f.appOperation.ID, PlanHash: plan.Hash, BindingHash: plan.BindingHash, MembershipRevision: plan.MembershipRevision, Phase: "detached", Observation: &domain.HostingDatabaseBindingEndpointObservation{Endpoint: domain.HostingDatabaseBindingEndpoint{Target: plan.Target, EndpointID: plan.ExpectedEndpointID}, Present: false}}
	r.Hash = r.DocumentHash()
	if _, err = f.pg.HostingApplicationDatabaseRetirement(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: f.appAuthority, PlanHash: plan.Hash, Receipt: r}); err == nil {
		t.Fatal("endpoint hash substituted for compiled operation hash")
	}
	if _, err = f.pg.HostingApplicationDatabaseRetirement(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: f.appAuthority, PlanHash: f.appOperation.Plan.Hash, Receipt: r}); err != nil {
		t.Fatal("detached receipt", err)
	}
	return f
}

func (f connectionFixture) complete(t *testing.T) connectionFixture {
	t.Helper()
	in := domain.HostingCompleteRequest{HostingAuthority: f.appAuthority, ReceiptID: "complete_" + f.appOperation.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: f.stage.Candidate.ContainerID, ImageID: f.appOperation.Release.ImageID, DaemonID: f.appOperation.Release.DaemonID, PlanHash: f.appOperation.Plan.Hash}}
	op, err := f.pg.HostingComplete(f.runnerContext, f.runner, in)
	if err != nil || op.State != "succeeded" || op.MutationHold {
		t.Fatal("atomic app/member settlement", err)
	}
	f.appOperation = op
	state, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, f.appAuthority)
	wantRevision := op.ApplicationDatabase.MembershipRevision + 1
	if len(op.ApplicationDatabase.Members) == 1 {
		wantRevision++
	}
	if err != nil || state.MembershipRevision != wantRevision || state.HoldOperationID != "" {
		t.Fatal("state after atomic completion", err)
	}
	before := connectionCounts(t, f)
	if replay, e := f.pg.HostingComplete(f.runnerContext, f.runner, in); e != nil || replay.ID != op.ID || connectionCounts(t, f) != before {
		t.Fatal("complete replay minted authority", e)
	}
	return f
}

func (f connectionFixture) maintenance(t *testing.T, revision int64, key string) connectionFixture {
	t.Helper()
	maintenance := domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: revision, IdempotencyKey: key}
	op, err := f.service.RequestHostingDatabaseMaintenance(f.ctx, f.resource.ID, maintenance)
	if err != nil || op.DatabaseMaintenance == nil || len(op.DatabaseMaintenance.Members) != 1 {
		t.Fatal("member maintenance admission", err)
	}
	if err = f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("maintenance claim", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingPrepared(f.runnerContext, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: op.DatabaseMaintenance.PreviousPlan.Release})
	if err != nil || !reflect.DeepEqual(op.Plan.Storage, f.op.Plan.Storage) || op.Plan.Database.Network.MembershipRevision != revision || len(op.Plan.Database.Network.Members) != 1 {
		t.Fatal("maintenance compiler identity", err)
	}
	mf := f.networkCaptureFixture
	mf.ctx = f.runnerContext
	mf.op = op
	mf.authority = a
	for _, fingerprint := range []string{"", strings.Repeat("0", 64)} {
		credentials, e := f.pg.AuthorizeHostingDatabaseCredentials(f.ctx, f.runner, fingerprint, domain.HostingDatabaseCredentialRequest{Authority: a, DeliveryID: strings.Repeat("8", 32), PlanHash: op.Plan.Hash, Mode: "deploy"})
		credentials.Close()
		if e == nil {
			t.Fatal("maintenance delivery inferred authenticated fingerprint")
		}
	}
	mf = settleUpgrade(t, mf)
	if mf.op.DatabaseMaintenance == nil {
		t.Fatal("maintenance became empty upgrade")
	}
	f.networkCaptureFixture = mf
	var raw []byte
	var proof domain.HostingStorageMaterializedRequest
	if err = f.db.QueryRow(f.ctx, `SELECT body FROM hosting_storage_materialized_receipts WHERE operation_id=$1`, op.ID).Scan(&raw); err != nil || json.Unmarshal(raw, &proof) != nil {
		t.Fatal("retained maintenance receipt", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(f.runnerContext, f.runner, proof); err != nil {
		t.Fatal("historical maintenance ACK", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, proof); err == nil {
		t.Fatal("historical maintenance ACK inferred fingerprint")
	}
	stale, e := store.BindHostingDatabaseConnectionRunnerContext(t.Context(), f.runner, strings.Repeat("0", 64))
	if e != nil {
		t.Fatal(e)
	}
	if _, err = f.pg.HostingStorageMaterialized(stale, f.runner, proof); err == nil {
		t.Fatal("historical maintenance ACK accepted stale fingerprint")
	}
	before := connectionCounts(t, f)
	if replay, e := f.service.RequestHostingDatabaseMaintenance(f.ctx, f.resource.ID, maintenance); e != nil || replay.ID != op.ID || connectionCounts(t, f) != before {
		t.Fatal("maintenance replay", e)
	}
	return f
}

func TestPostgresDatabaseConnectionWorkflowReplacementAndMaintenance(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newConnectionFixture(t, engine).connect(t).prepareApp(t).ready(t)
			view, err := f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
			if err != nil || view.MembershipRevision != 1 || view.HoldOperationID != f.appOperation.ID || view.State == "active" {
				t.Fatal("ready stage incorrectly settled", err)
			}
			initial := f.complete(t)
			f = initial
			view, err = f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
			if err != nil || view.State != "active" || view.MembershipRevision != 2 || view.HoldOperationID != "" {
				t.Fatal("initial atomic membership", err)
			}
			_, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_deployments SELECT (jsonb_populate_record(NULL::hosting_database_deployments,to_jsonb(d)||jsonb_build_object('operation_id','forged_empty_maintenance'))).* FROM hosting_database_deployments d WHERE operation_id=$1`, f.op.ID)
			var rejected *pgconn.PgError
			if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "connected database requires member-aware maintenance" {
				t.Fatal("SQL empty family bypassed committed head", err)
			}
			before := connectionCounts(t, f)
			if replay, e := f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input); e != nil || replay.ID != f.connection.ID || connectionCounts(t, f) != before {
				t.Fatal("connect receipt after settlement", e)
			}
			replace := f.input.Deployment
			replace.IdempotencyKey = "connected_replacement"
			replace.ExpectedRuntimeConfigurationGeneration = 1
			replace.HostPort++
			f.appOperation, err = f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, replace)
			if err != nil {
				t.Fatal("replacement admission", err)
			}
			f.appOperation = awaitHostingDispatch(t, f.pg, f.appOperation.ID)
			f.stage = domain.HostingApplicationDatabaseStage{}
			f = f.prepareApp(t).ready(t)
			if _, err = f.pg.HostingComplete(f.runnerContext, f.runner, domain.HostingCompleteRequest{HostingAuthority: f.appAuthority, ReceiptID: "premature_replacement", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: f.stage.Candidate.ContainerID, ImageID: f.appOperation.Release.ImageID, DaemonID: f.appOperation.Release.DaemonID, PlanHash: f.appOperation.Plan.Hash}}); err == nil {
				t.Fatal("replacement omitted exact retirement")
			}
			f = f.retire(t).complete(t)
			view, err = f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
			if err != nil || view.MembershipRevision != 4 || view.HoldOperationID != "" {
				t.Fatal("replacement revision must advance attach+retire", err)
			}
			f = f.maintenance(t, 4, "member_maintenance")
			if len(f.op.DatabaseMaintenance.Retired) != 0 {
				t.Fatal("retirements predating DB baseline became new removals")
			}
			f = f.maintenance(t, 4, "member_maintenance_repeat")
			if len(f.op.DatabaseMaintenance.Retired) != 0 {
				t.Fatal("unchanged maintenance replayed old removal")
			}
			replace.IdempotencyKey = "replacement_after_maintenance"
			replace.HostPort++
			f.appOperation, err = f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, replace)
			if err != nil {
				t.Fatal("replacement after maintenance", err)
			}
			f.appOperation = awaitHostingDispatch(t, f.pg, f.appOperation.ID)
			f.stage = domain.HostingApplicationDatabaseStage{}
			f = f.prepareApp(t).ready(t).retire(t).complete(t)
			f = f.maintenance(t, 6, "maintenance_after_retirement")
			if len(f.op.DatabaseMaintenance.Retired) != 1 {
				t.Fatal("exact predecessor removal missing")
			}
			f = f.maintenance(t, 6, "maintenance_after_retirement_repeat")
			if len(f.op.DatabaseMaintenance.Retired) != 0 {
				t.Fatal("retained removal became newly removed twice")
			}
			if _, err = f.service.RequestHostingDatabaseNetworkUpgrade(f.ctx, f.resource.ID, domain.HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: f.connection.CaptureID, ExpectedOriginHash: f.op.DatabaseMaintenance.OriginReceipt.OriginHash, IdempotencyKey: "forbidden_empty_upgrade"}); err == nil {
				t.Fatal("empty upgrade erased current member")
			}
			// Original stage ACK is historical evidence after both app and DB advance.
			if _, err = initial.pg.HostingApplicationDatabaseStage(initial.ctx, initial.runner, initial.credential, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: initial.appAuthority, Stage: initial.stage}); err != nil {
				t.Fatal("historical stage replay", err)
			}
		})
	}
}

func TestPostgresDatabaseConnectionAdmissionRollbackAndConcurrentReplay(t *testing.T) {
	f := newConnectionFixture(t, "postgresql")
	before := connectionCounts(t, f)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMaintenanceCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input); err == nil || connectionCounts(t, f) != before {
		t.Fatal("missing maintenance capability partially admitted", err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMaintenanceCapability); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan domain.HostingDatabaseConnection, 2)
	failures := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			v, e := f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input)
			results <- v
			failures <- e
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("concurrent exact replay", err)
		}
	}
	id := ""
	for v := range results {
		if id != "" && v.ID != id {
			t.Fatal("two connections for same immutable request")
		}
		id = v.ID
	}
	var connections, bindings, holds, versions int
	if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_connections),(SELECT count(*) FROM hosting_database_bindings),(SELECT count(*) FROM hosting_database_network_holds),(SELECT count(*) FROM hosting_runtime_configuration_versions)`).Scan(&connections, &bindings, &holds, &versions); err != nil || connections != 1 || bindings != 1 || holds != 1 || versions != 1 {
		t.Fatal("connection not atomic", err)
	}
	_, err := f.db.Exec(f.ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'forged_connection',project_id,application_resource_id,(body->>'application_binding_id'),'forged',body_hash,'queued',created_by FROM hosting_database_bindings LIMIT 1`)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" || !strings.Contains(pgerr.Message, "pending") {
		t.Fatal("ordinary SQL family bypass", err)
	}
}

func TestPostgresDatabaseConnectionClaimRequiresPinnedContext(t *testing.T) {
	for _, fault := range []string{"missing", "wrong_runner", "stale_credential", "revoked_enrollment"} {
		t.Run(fault, func(t *testing.T) {
			f := newConnectionFixture(t, "postgresql").connect(t)
			ctx := f.runnerContext
			switch fault {
			case "missing":
				ctx = f.ctx
			case "wrong_runner":
				var err error
				ctx, err = store.BindHostingDatabaseConnectionRunnerContext(f.ctx, "other", f.credential)
				if err != nil {
					t.Fatal(err)
				}
			case "stale_credential":
				if _, err := f.db.Exec(f.ctx, `UPDATE runners SET token_hash=repeat('a',64) WHERE id=$1`, f.runner); err != nil {
					t.Fatal(err)
				}
			case "revoked_enrollment":
				if _, err := f.db.Exec(f.ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner); err != nil {
					t.Fatal(err)
				}
			}
			before := connectionCounts(t, f)
			if _, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute); err == nil {
				t.Fatal("claim inferred current credential")
			}
			if connectionCounts(t, f) != before {
				t.Fatal("denied claim left authority or audit")
			}
		})
	}
}

func TestPostgresDatabaseConnectionEffectAndObservationHold(t *testing.T) {
	f := newConnectionFixture(t, "redis").connect(t).prepareApp(t)
	f.authorize(t, "create")
	f = f.sendStage(t, "candidate_created")
	if _, err := f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: f.appAuthority, AuthorizationID: strings.Repeat("1", 32), Action: "start", ExpectedStageHash: f.stage.Hash}); err == nil {
		t.Fatal("stage ACK authorized start")
	}
	uncertain := f.stage
	uncertain.Phase = "uncertain"
	uncertain.ReceiptID = "uncertain_stage"
	uncertain.FailureCode = "mutation_unconfirmed"
	uncertain.Hash = uncertain.DocumentHash()
	if _, err := f.pg.HostingApplicationDatabaseStage(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseStageRequest{HostingAuthority: f.appAuthority, Stage: uncertain}); err != nil {
		t.Fatal(err)
	}
	f.stage = uncertain
	failed := domain.HostingCompleteRequest{HostingAuthority: f.appAuthority, ReceiptID: "uncertain_complete", Result: domain.HostingResult{Outcome: "failed", Uncertain: true, ContainerID: f.stage.Candidate.ContainerID, ImageID: f.appOperation.Release.ImageID, DaemonID: f.appOperation.Release.DaemonID, PlanHash: f.appOperation.Plan.Hash}}
	op, err := f.pg.HostingComplete(f.runnerContext, f.runner, failed)
	if err != nil || op.State != "reconciliation_required" || !op.MutationHold {
		t.Fatal("uncertainty did not retain hold", err)
	}
	_, err = f.db.Exec(f.ctx, `DELETE FROM hosting_database_network_holds WHERE operation_id=$1`, op.ID)
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "unresolved database network hold cannot be released" {
		t.Fatal("SQL removed unresolved hold", err)
	}
	in := domain.HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: op.ID, StageHash: uncertain.Hash, CandidateHash: uncertain.Candidate.Hash, ExpectedMembershipRevision: 1, IdempotencyKey: "observe_same_attempt"}
	observing, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in)
	if err != nil || observing.ReconciliationRunID != "observe_"+op.ID {
		t.Fatal("observe-only admission", err)
	}
	before := connectionCounts(t, f)
	if replay, e := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in); e != nil || replay.ID != observing.ID || replay.ReconciliationRunID != observing.ReconciliationRunID || connectionCounts(t, f) != before {
		t.Fatal("observation response-loss replay created authority", e)
	}
	changed := in
	changed.IdempotencyKey = "second_observation"
	if _, err = f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, changed); !errors.Is(err, store.ErrConflict) || connectionCounts(t, f) != before {
		t.Fatal("second observation reset original task", err)
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != observing.ReconciliationRunID {
		t.Fatal("observe-only claim", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, a); err != nil {
		t.Fatal(err)
	}
	state, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, a)
	if err != nil || state.Stage == nil || state.Stage.Hash != uncertain.Hash || state.HoldOperationID != op.ID {
		t.Fatal("observer lost exact original candidate", err)
	}
	if _, err = f.pg.HostingReconciliation(f.runnerContext, f.runner, domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: f.appOperation.Release.DaemonID, PlanHash: op.Plan.Hash, ReleaseHash: op.Release.Hash, NoOwnedContainers: true}); err != nil {
		t.Fatal("bounded observation report", err)
	}
	view, err := f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
	if err != nil || view.HoldOperationID != op.ID || view.MembershipRevision != 1 {
		t.Fatal("observation lifted uncertainty hold", err)
	}
	in.Action = "resume"
	if _, err = f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in); err == nil {
		t.Fatal("expired authority recovery silently resumed")
	}
}

func TestPostgresDatabaseConnectionDirectSQLCandidateGuard(t *testing.T) {
	f := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t).sendStage(t, "candidate_created")
	forged := f.stage
	forged.Phase = "attach_intent"
	forged.ReceiptID = "forged_stage"
	forged.Candidate.ContainerID = strings.Repeat("0", 64)
	forged.Candidate.Hash = forged.Candidate.DocumentHash()
	forged.Hash = forged.DocumentHash()
	authority, _ := json.Marshal(f.appAuthority)
	body, _ := json.Marshal(forged)
	_, err := f.db.Exec(f.ctx, `INSERT INTO hosting_application_database_stages(operation_id,receipt_id,phase,previous_hash,authority,stage,stage_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, f.appOperation.ID, forged.ReceiptID, forged.Phase, f.stage.Hash, authority, body, forged.Hash)
	var rejected *pgconn.PgError
	if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "connection stage candidate or authority mismatch" {
		t.Fatal("direct SQL replaced actual candidate", err)
	}
}

func TestPostgresDatabaseConnectionUnknownCreateCannotBecomeAbsence(t *testing.T) {
	f := newConnectionFixture(t, "redis").connect(t).prepareApp(t)
	f.authorize(t, "create")
	op, err := f.pg.GetHostingOperation(f.ctx, f.appOperation.ID)
	if err != nil || op.State != "applying" || !op.MutationHold || op.Result != nil {
		t.Fatal("unknown create hold", err)
	}
	in := domain.HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: op.ID, ExpectedMembershipRevision: 1, IdempotencyKey: "observe_unknown"}
	observing, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != observing.ReconciliationRunID {
		t.Fatal("unknown observer claim", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, a); err != nil {
		t.Fatal(err)
	}
	state, err := f.pg.GetHostingApplicationDatabaseState(f.ctx, f.runner, f.credential, a)
	if err != nil || state.Stage != nil || state.HoldOperationID != op.ID {
		t.Fatal("unknown candidate was fabricated", err)
	}
	before := connectionCounts(t, f)
	if _, err = f.pg.HostingReconciliation(f.runnerContext, f.runner, domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: op.Release.DaemonID, PlanHash: op.Plan.Hash, ReleaseHash: op.Release.Hash, NoOwnedContainers: true}); err == nil || connectionCounts(t, f) != before {
		t.Fatal("unknown create was reported as positive absence", err)
	}
}

func TestPostgresDatabaseConnectionSelectionLimitsBeforeMutation(t *testing.T) {
	for _, fault := range []string{"owned_storage", "running_application"} {
		t.Run(fault, func(t *testing.T) {
			f := newConnectionFixture(t, "postgresql")
			if fault == "owned_storage" {
				saveHostingStorage(t, f.pg, f.application.ID, hostingStorageInput())
			} else {
				input := hostingDeploymentRequest()
				input.IdempotencyKey = "prior_application"
				input.HostPort = 18090
				op, err := f.pg.RequestHostingDeployment(f.ctx, "hop_prior_application", f.application.ID, "usr_bootstrap", input, destinationEvent("prior_application"))
				if err != nil {
					t.Fatal(err)
				}
				op = awaitHostingDispatch(t, f.pg, op.ID)
				a, prepared := prepareHostingAttempt(t, f.pg, op, f.runner)
				op, err = f.pg.GetHostingOperation(f.ctx, op.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, a); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "prior_app_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("9", 64), ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: op.Plan.Hash}}); err != nil {
					t.Fatal(err)
				}
			}
			before := connectionCounts(t, f)
			if options, err := f.service.ListEligibleHostingDatabases(f.ctx, f.application.ID); err != nil || len(options) != 0 {
				t.Fatal("unsupported application offered eligible DB", err)
			}
			if _, err := f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input); err == nil || connectionCounts(t, f) != before {
				t.Fatal("unsupported application partially connected", err)
			}
		})
	}
}

func TestPostgresDatabaseConnectionExistingCapturePrerequisites(t *testing.T) {
	for _, mode := range []string{"completed_former_owner", "foreign_unfinished", "same_owner_expired", "same_owner_queued"} {
		t.Run(mode, func(t *testing.T) {
			f := newConnectionFixture(t, "postgresql")
			actor := "usr_bootstrap"
			if mode == "completed_former_owner" || mode == "foreign_unfinished" {
				principal, _ := proxyLegacyToken(t, f.pg, f.service, "tok_former_capture_owner", []string{domain.RoleSystemAdmin})
				actor = principal.ID
			}
			capture, err := f.pg.RequestHostingDatabaseNetworkCapture(f.ctx, f.op.ID, actor, destinationEvent("existing_capture"))
			if err != nil {
				t.Fatal("existing capture setup", err)
			}
			if mode == "completed_former_owner" || mode == "same_owner_expired" {
				claim := f.claim(t, capture)
				if mode == "completed_former_owner" {
					if _, err = f.pg.RecordHostingDatabaseNetworkOrigin(f.ctx, f.runner, f.credential, captureOriginRequest(claim)); err != nil {
						t.Fatal("existing immutable origin", err)
					}
					if _, err = f.db.Exec(f.ctx, `UPDATE api_tokens SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, actor); err != nil {
						t.Fatal(err)
					}
				} else if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.Lease.ID); err != nil {
					t.Fatal(err)
				}
			}
			var original, after string
			const evidence = `SELECT jsonb_build_array((SELECT to_jsonb(c) FROM hosting_database_network_captures c WHERE c.id=$1),(SELECT to_jsonb(t) FROM task_runs t WHERE t.id=$2),(SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM run_leases l WHERE l.run_id=$2))::text`
			if err = f.db.QueryRow(f.ctx, evidence, capture.ID, capture.RunID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			before := connectionCounts(t, f)
			view, err := f.service.ConnectHostingApplicationDatabase(f.ctx, f.application.ID, f.input)
			if mode == "foreign_unfinished" || mode == "same_owner_expired" {
				if !errors.Is(err, store.ErrConflict) || connectionCounts(t, f) != before {
					t.Fatal("unfinishable capture reserved connection", err)
				}
			} else {
				if err != nil {
					t.Fatal("supported capture admission", err)
				}
				if err = f.pg.PrepareHostingDatabaseConnection(f.ctx, view.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
					t.Fatal("reuse capture continuation", err)
				}
				view, err = f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
				if err != nil || view.CaptureID != capture.ID {
					t.Fatal("existing capture correlation", err)
				}
				if mode == "completed_former_owner" && view.State != "upgrade_requested" && view.State != "upgrade_waiting" {
					t.Fatal("completed former-owner capture did not reach upgrade", view.State)
				}
				if mode == "same_owner_queued" && view.State != "capture_waiting" {
					t.Fatal("same-owner capture did not remain waiting", view.State)
				}
			}
			if err = f.db.QueryRow(f.ctx, evidence, capture.ID, capture.RunID).Scan(&after); err != nil || original != after {
				t.Fatal("connection relabeled capture or renewed its authority", err)
			}
		})
	}
}
