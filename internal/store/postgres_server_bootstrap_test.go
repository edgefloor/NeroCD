package store_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type bootstrapFixture struct {
	pg   *store.PostgresStore
	db   *pgxpool.Pool
	in   domain.ServerBootstrapInput
	ring *secretcrypto.Keyring
	seq  int
}

func newBootstrapFixture(t *testing.T) *bootstrapFixture {
	t.Helper()
	pg, db := openPostgresIntegrationStore(t, t.Context(), "ssh_bootstrap")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	item, record, event := managedFixture(t, "msec_bootstrap", 1)
	if _, err := pg.CreateManagedSecret(t.Context(), item, record, event); err != nil {
		t.Fatal(err)
	}
	return &bootstrapFixture{pg: pg, db: db, ring: managedRing(t), in: domain.ServerBootstrapInput{ProjectID: item.ProjectID, Name: "server", Target: domain.ServerBootstrapTarget{IP: "192.0.2.10", Port: 22, User: "root", HostFingerprint: "SHA256:" + strings.Repeat("A", 43)}, ControllerURL: "https://controller.example/", Artifact: domain.ServerBootstrapArtifact{Size: 1, SHA256: strings.Repeat("a", 64), Version: "v1", Architecture: "amd64"}, SSHSecretID: item.ID, SSHSecretVersion: 1}}
}
func (f *bootstrapFixture) event() domain.AuditEvent {
	f.seq++
	return destinationEvent(fmt.Sprintf("bootstrap_%d", f.seq))
}
func (f *bootstrapFixture) create(t *testing.T) domain.ServerBootstrapOperation {
	t.Helper()
	op, err := f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "create", f.in, f.event())
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func (f *bootstrapFixture) command(t *testing.T, op domain.ServerBootstrapOperation, action string, review *domain.ServerBootstrapReview) domain.ServerBootstrapCommand {
	t.Helper()
	c, err := f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, domain.ServerBootstrapCommand{Key: action, Action: action, ExpectedVersion: op.Version, Deadline: time.Now().UTC().Add(time.Minute), Review: review}, f.event())
	if err != nil {
		t.Fatal(action, err)
	}
	return c
}
func (f *bootstrapFixture) get(t *testing.T, id string) domain.ServerBootstrapOperation {
	t.Helper()
	out, e := f.pg.GetServerBootstrap(t.Context(), "usr_bootstrap", id)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *bootstrapFixture) review(t *testing.T, op domain.ServerBootstrapOperation) domain.ServerBootstrapOperation {
	return f.reviewLifetime(t, op, 30*time.Second)
}
func (f *bootstrapFixture) reviewLifetime(t *testing.T, op domain.ServerBootstrapOperation, lifetime time.Duration) domain.ServerBootstrapOperation {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"version": 1, "installation_id": op.IDs.InstallationID, "install_operation_id": op.IDs.InstallOperationID, "runner_id": op.IDs.RunnerID, "credential_reference_id": op.IDs.CredentialReferenceID, "controller_url": op.Input.ControllerURL, "artifact": op.Input.Artifact, "capability_preset": domain.ServerBootstrapPreset})
	h := sha256.Sum256(raw)
	var dbNow time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	r := domain.ServerBootstrapReview{Prerequisites: domain.ServerBootstrapPrerequisites{Profile: "prepared_debian12_v1", OSState: "available", OSID: "debian", OSVersion: "12", Architecture: "amd64", SystemdState: "available", SystemdStatus: "running", DockerState: "available", DockerAPIVersion: "1.54", ComposeState: "available", ComposeVersion: "5.1.1", DiskState: "available", DiskTotalBytes: 2 << 30, DiskAvailableBytes: 1 << 30}, Manifest: raw, ManifestSHA256: hex.EncodeToString(h[:]), ProbeObservedAt: dbNow, ProbeExpiresAt: dbNow.Add(lifetime), TargetHash: domain.HostingDocumentHash(op.Input.Target)}
	f.command(t, op, "review", &r)
	return f.get(t, op.IDs.OperationID)
}
func (f *bootstrapFixture) claim(t *testing.T, op domain.ServerBootstrapOperation, action string) domain.ServerBootstrapStage {
	t.Helper()
	c := f.command(t, op, action, nil)
	s, e := f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *bootstrapFixture) receipt(stage domain.ServerBootstrapStage, outcome string) domain.ServerBootstrapReceipt {
	return domain.ServerBootstrapReceipt{CommandID: stage.Command.ID, LeaseID: stage.LeaseID, Attempt: stage.Attempt, Fence: stage.Fence, Outcome: outcome}
}
func TestPostgresServerBootstrapDurableEnrollmentRegistrationAndGrant(t *testing.T) {
	f := newBootstrapFixture(t)
	op := f.create(t)
	again, e := f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "create", f.in, f.event())
	if e != nil || again.IDs != op.IDs {
		t.Fatal("create replay", e)
	}
	op = f.review(t, op)
	stage := f.claim(t, op, "install")
	delivery, e := f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event())
	if e != nil {
		t.Fatal(e)
	}
	if len(delivery.EnrollmentToken) == 0 || !bytes.Equal(delivery.SSHPrivateKey, []byte("  private-value\n")) {
		t.Fatal("delivery missing")
	}
	encoded, _ := json.Marshal(delivery)
	if string(encoded) != "{}" {
		t.Fatal("delivery serializes")
	}
	replay, e := f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event())
	if e != nil || !bytes.Equal(replay.EnrollmentToken, delivery.EnrollmentToken) {
		t.Fatal("lost delivery response", e)
	}
	restarted, e := store.OpenPostgres(t.Context(), f.db.Config().ConnConfig.ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := restarted.Close(); err != nil {
			t.Error(err)
		}
	}()
	recovered, e := restarted.GetServerBootstrap(t.Context(), "usr_bootstrap", op.IDs.OperationID)
	if e != nil || recovered.ActiveStage == nil || recovered.ActiveCommandID != stage.Command.ID || recovered.Review == nil || !bytes.Equal(recovered.Review.Manifest, delivery.Operation.Review.Manifest) {
		t.Fatal("DB-only restart lost manifest/stage", e)
	}
	recoveredDelivery, e := restarted.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, *recovered.ActiveStage, f.ring, f.event())
	if e != nil || !bytes.Equal(recoveredDelivery.EnrollmentToken, delivery.EnrollmentToken) {
		t.Fatal("restart delivery replay", e)
	}
	digest := sha256.Sum256(delivery.EnrollmentToken)
	runner, e := f.pg.ConsumeRunnerEnrollment(t.Context(), domain.RunnerEnrollmentConsume{TokenHash: hex.EncodeToString(digest[:]), RequestID: "enroll_consume_" + strings.Repeat("e", 32), CredentialHash: "permanent_runner_hash"}, f.event())
	if e != nil || runner.ID != op.IDs.RunnerID {
		t.Fatal("consume", e)
	}
	receipt := f.receipt(stage, "installed")
	receipt.RemoteOperationID = op.IDs.InstallOperationID
	var r struct {
		ManifestSHA256 string `json:"manifest_sha256"`
	}
	raw, e := f.db.Query(t.Context(), `SELECT body->'review' FROM server_bootstrap_commands WHERE action='review'`)
	if e != nil {
		t.Fatal(e)
	}
	if !raw.Next() {
		t.Fatal("review absent")
	}
	var review []byte
	if e = raw.Scan(&review); e != nil {
		t.Fatal(e)
	}
	raw.Close()
	if json.Unmarshal(review, &r) != nil {
		t.Fatal("review decode")
	}
	receipt.ManifestSHA256 = r.ManifestSHA256
	op, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event())
	if e != nil || op.State != "installed_waiting_enrollment" || !op.RemoteHold {
		t.Fatal("install receipt", op, e)
	}
	installed := op
	stage = f.claim(t, op, "register_server")
	op, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, f.receipt(stage, "accepted"), f.event())
	if e != nil || op.State != "registered" {
		t.Fatal("registration", op, e)
	}
	if _, e = f.pg.GetDestinationProjectGrant(t.Context(), op.IDs.DestinationID, op.Input.ProjectID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("implicit project grant", e)
	}
	stage = f.claim(t, op, "grant_destination")
	grantReceipt := f.receipt(stage, "accepted")
	op, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, grantReceipt, f.event())
	if e != nil || op.State != "completed" {
		t.Fatal("explicit grant", op, e)
	}
	if _, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, grantReceipt, f.event()); e != nil {
		t.Fatal("completion response loss", e)
	}
	historical, e := f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event())
	if e != nil || historical.State != installed.State || historical.Version != installed.Version {
		t.Fatal("historical completion changed", historical, e)
	}
	changed := f.in
	changed.SSHSecretVersion = 2
	if _, e = f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "create", changed, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	if _, e = f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "another", f.in, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("duplicate owned target", e)
	}
	var enrollments, credentials, grants int
	if e = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runner_enrollments WHERE id=$1),(SELECT count(*) FROM server_bootstrap_enrollment_credentials),(SELECT count(*) FROM destination_project_grants)`, op.IDs.EnrollmentID).Scan(&enrollments, &credentials, &grants); e != nil || enrollments != 1 || credentials != 1 || grants != 1 {
		t.Fatal(enrollments, credentials, grants, e)
	}
}
func TestPostgresServerBootstrapConcurrentCreateCurrentAuthAndWrongKey(t *testing.T) {
	f := newBootstrapFixture(t)
	var wg sync.WaitGroup
	results := make(chan domain.ServerBootstrapOperation, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		event := destinationEvent(fmt.Sprintf("concurrent_bootstrap_%d", i))
		wg.Go(func() {
			op, e := f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "create", f.in, event)
			results <- op
			errs <- e
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for op := range results {
		if id != "" && id != op.IDs.OperationID {
			t.Fatal("identity changed")
		}
		id = op.IDs.OperationID
	}
	op := f.review(t, f.get(t, id))
	stage := f.claim(t, op, "install")
	wrong, _ := secretcrypto.New("test", map[string][]byte{"test": bytes.Repeat([]byte{9}, 32)})
	if _, e := f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", id, stage, wrong, f.event()); !errors.Is(e, secretcrypto.ErrDecrypt) {
		t.Fatal("wrong key", e)
	}
	var n int
	if e := f.db.QueryRow(t.Context(), `SELECT count(*) FROM server_bootstrap_enrollment_credentials`).Scan(&n); e != nil || n != 0 {
		t.Fatal("wrong key mutated", e)
	}
	if _, e := f.db.Exec(t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pg.GetServerBootstrap(t.Context(), "usr_bootstrap", id); !errors.Is(e, auth.ErrForbidden) {
		t.Fatal("revoked auth", e)
	}
}
func TestPostgresServerBootstrapLeaseExpiryIntentAndRestoreHold(t *testing.T) {
	f := newBootstrapFixture(t)
	op := f.review(t, f.create(t))
	stage := f.claim(t, op, "install")
	if _, e := f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event()); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(t.Context(), `UPDATE server_bootstrap_commands SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, stage.Command.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage.Command.ID); !errors.Is(e, store.ErrConflict) {
		t.Fatal("intent retried", e)
	}
	if !f.get(t, op.IDs.OperationID).RemoteHold {
		t.Fatal("lost hold")
	}
	held := f.get(t, op.IDs.OperationID)
	if _, e := f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, domain.ServerBootstrapCommand{Key: "unsafe_cancel", Action: "cancel", ExpectedVersion: held.Version, Deadline: time.Now().Add(time.Minute)}, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("intent canceled", e)
	}

	tx, e := f.db.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if e = store.InvalidateServerBootstrapForRestoreTx(t.Context(), tx); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	restored := f.get(t, op.IDs.OperationID)
	if !restored.RestoreHeld || !restored.RemoteHold {
		t.Fatal("restore not held")
	}
	if _, e = f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("restore delivered", e)
	}
	var revoked bool
	if e = f.db.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM runner_enrollments WHERE id=$1`, op.IDs.EnrollmentID).Scan(&revoked); e != nil || !revoked {
		t.Fatal("unused enrollment survived", e)
	}
}

func TestPostgresServerBootstrapCommandReplayCASAndAtomicAudit(t *testing.T) {
	f := newBootstrapFixture(t)
	op := f.create(t)
	input := domain.ServerBootstrapCommand{Key: "probe_once", Action: "probe", ExpectedVersion: op.Version, Deadline: time.Now().UTC().Add(time.Minute)}
	var wg sync.WaitGroup
	results := make(chan domain.ServerBootstrapCommand, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		event := destinationEvent(fmt.Sprintf("probe_concurrent_%d", i))
		wg.Go(func() {
			c, e := f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, input, event)
			results <- c
			errs <- e
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first domain.ServerBootstrapCommand
	for c := range results {
		if first.ID != "" && first.ID != c.ID {
			t.Fatal("duplicate command")
		}
		first = c
	}
	stage, e := f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	event := f.event()
	receipt := f.receipt(stage, "observed")
	if _, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, event); e != nil {
		t.Fatal(e)
	}
	completed, e := f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event())
	if e != nil || completed.Version != op.Version+1 {
		t.Fatal("receipt replay", e)
	}
	changed := input
	changed.ExpectedVersion++
	if _, e = f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, changed, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed request", e)
	}
	// Duplicate mandatory audit must roll back the command and state change.
	cancel := domain.ServerBootstrapCommand{Key: "cancel", Action: "cancel", ExpectedVersion: completed.Version, Deadline: time.Now().UTC().Add(time.Minute)}
	if _, e = f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, cancel, event); e == nil {
		t.Fatal("audit collision committed")
	}
	if got := f.get(t, op.IDs.OperationID); got.State != "draft" || got.Version != completed.Version {
		t.Fatal("partial cancel", got)
	}
	canceled, e := f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, cancel, f.event())
	if e != nil || canceled.ID == "" {
		t.Fatal(e)
	}
	var commands, audits int
	if e = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM server_bootstrap_commands),(SELECT count(*) FROM audit_events WHERE action='server_bootstrap.command_requested')`).Scan(&commands, &audits); e != nil || commands != 2 || audits != 2 {
		t.Fatal("duplicate mutation", commands, audits, e)
	}
	var revoked bool
	if e = f.db.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM server_bootstrap_secret_grants WHERE operation_id=$1`, op.IDs.OperationID).Scan(&revoked); e != nil || !revoked {
		t.Fatal("cancel grant", e)
	}
}

func TestPostgresServerBootstrapLeaseBudgetRevokedSecretAndStaleReview(t *testing.T) {
	f := newBootstrapFixture(t)
	op := f.create(t)
	stage := f.claim(t, op, "probe")
	for attempt := int32(1); attempt <= domain.ServerBootstrapMaxAttempts; attempt++ {
		if stage.Attempt != attempt {
			t.Fatal(stage.Attempt, attempt)
		}
		if _, e := f.db.Exec(t.Context(), `UPDATE server_bootstrap_commands SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, stage.Command.ID); e != nil {
			t.Fatal(e)
		}
		next, e := f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage.Command.ID)
		if attempt == domain.ServerBootstrapMaxAttempts {
			if !errors.Is(e, store.ErrConflict) {
				t.Fatal("budget reset", e)
			}
		} else {
			if e != nil {
				t.Fatal(e)
			}
			if next.Command.Deadline != stage.Command.Deadline || next.Fence <= stage.Fence {
				t.Fatal("deadline reset")
			}
			stage = next
		}
	}
	stale := f.receipt(stage, "observed")
	f.command(t, f.get(t, op.IDs.OperationID), "cancel", nil)
	if _, e := f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, stale, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("retired probe completed", e)
	}
	if canceled := f.get(t, op.IDs.OperationID); canceled.State != "canceled" || canceled.RemoteHold {
		t.Fatal("safe cancel failed", canceled)
	}
	if replacement, e := f.pg.CreateServerBootstrap(t.Context(), "usr_bootstrap", "retry_unused_target", f.in, f.event()); e != nil || replacement.IDs.OperationID == op.IDs.OperationID {
		t.Fatal("unused target not released", e)
	}
	// A separate operation tests managed-secret revocation against actual delivery.
	f2 := newBootstrapFixture(t)
	op = f2.review(t, f2.create(t))
	stage = f2.claim(t, op, "install")
	if _, e := f2.pg.RevokeManagedSecret(t.Context(), f2.in.SSHSecretID, 1, f2.event()); e != nil {
		t.Fatal(e)
	}
	if _, e := f2.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f2.ring, f2.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("revoked secret delivered", e)
	}
	var n int
	if e := f2.db.QueryRow(t.Context(), `SELECT count(*) FROM server_bootstrap_enrollment_credentials`).Scan(&n); e != nil || n != 0 {
		t.Fatal("revoked mutation", e)
	}
}

func TestPostgresServerBootstrapFinalClockRejectsWaitedReviewAndLease(t *testing.T) {
	t.Run("review_delivery", func(t *testing.T) {
		f := newBootstrapFixture(t)
		op := f.reviewLifetime(t, f.create(t), 300*time.Millisecond)
		stage := f.claim(t, op, "install")
		if _, e := f.db.Exec(t.Context(), `CREATE FUNCTION delay_bootstrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='server_bootstrap.install_intent' THEN PERFORM pg_sleep(0.6); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_bootstrap_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delay_bootstrap_audit()`); e != nil {
			t.Fatal(e)
		}
		if _, e := f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event()); !errors.Is(e, store.ErrConflict) {
			t.Fatal("expired review accepted", e)
		}
		var credentials, enrollments int
		var intent bool
		if e := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM server_bootstrap_enrollment_credentials),(SELECT count(*) FROM runner_enrollments WHERE id=$1),(SELECT effect_intent FROM server_bootstrap_commands WHERE id=$2)`, op.IDs.EnrollmentID, stage.Command.ID).Scan(&credentials, &enrollments, &intent); e != nil || credentials != 0 || enrollments != 0 || intent {
			t.Fatal("review expiry partial effect", credentials, enrollments, intent, e)
		}
		if f.get(t, op.IDs.OperationID).RemoteHold {
			t.Fatal("rollback left invented intent")
		}
	})
	t.Run("completion_lease", func(t *testing.T) {
		f := newBootstrapFixture(t)
		op := f.create(t)
		stage := f.claim(t, op, "probe")
		if _, e := f.db.Exec(t.Context(), `UPDATE server_bootstrap_commands SET lease_expires_at=clock_timestamp()+interval '300 milliseconds' WHERE id=$1`, stage.Command.ID); e != nil {
			t.Fatal(e)
		}
		if _, e := f.db.Exec(t.Context(), `CREATE FUNCTION delay_bootstrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='server_bootstrap.stage_completed' THEN PERFORM pg_sleep(0.6); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_bootstrap_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delay_bootstrap_audit()`); e != nil {
			t.Fatal(e)
		}
		if _, e := f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, f.receipt(stage, "observed"), f.event()); !errors.Is(e, store.ErrConflict) {
			t.Fatal("expired lease accepted", e)
		}
		var receipt bool
		if e := f.db.QueryRow(t.Context(), `SELECT receipt IS NOT NULL FROM server_bootstrap_commands WHERE id=$1`, stage.Command.ID).Scan(&receipt); e != nil || receipt {
			t.Fatal("expired receipt committed", e)
		}
		if got := f.get(t, op.IDs.OperationID); got.Version != op.Version {
			t.Fatal("expired version committed")
		}
	})
}

func TestPostgresServerBootstrapSchemaAndOwnerAdmission(t *testing.T) {
	f := newBootstrapFixture(t)
	if _, e := f.db.Exec(t.Context(), `UPDATE users SET global_role='runner_admin' WHERE id='usr_viewer'`); e != nil {
		t.Fatal(e)
	}
	event := f.event()
	event.ActorID = "usr_viewer"
	if _, e := f.pg.CreateServerBootstrap(t.Context(), event.ActorID, "not_owner", f.in, event); !errors.Is(e, auth.ErrForbidden) {
		t.Fatal("runner administrator inherited project", e)
	}
	if _, e := f.db.Exec(t.Context(), `INSERT INTO project_members(id,project_id,user_id,role) VALUES('pm_bootstrap_owner','proj_platform','usr_viewer','owner')`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pg.CreateServerBootstrap(t.Context(), event.ActorID, "owner", f.in, event); e != nil {
		t.Fatal("runner admin plus owner denied", e)
	}
	if _, e := f.db.Exec(t.Context(), `DROP TRIGGER server_bootstrap_credential_consistent ON server_bootstrap_enrollment_credentials`); e != nil {
		t.Fatal(e)
	}
	if compatible, e := f.pg.SchemaCompatible(t.Context()); e != nil || compatible {
		t.Fatal("partial bootstrap schema remained ready", compatible, e)
	}
	if reopened, e := store.OpenPostgres(t.Context(), f.db.Config().ConnConfig.ConnString()); e == nil {
		_ = reopened.Close()
		t.Fatal("partial bootstrap schema opened")
	}
}

func TestPostgresServerBootstrapFinalActorExpiryRollsBack(t *testing.T) {
	f := newBootstrapFixture(t)
	principal, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_bootstrap_expiry", []string{domain.RoleSystemAdmin})
	if _, e := f.db.Exec(t.Context(), `CREATE FUNCTION delay_bootstrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='server_bootstrap.created' THEN PERFORM pg_sleep(0.6); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_bootstrap_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delay_bootstrap_audit(); UPDATE api_tokens SET expires_at=clock_timestamp()+interval '300 milliseconds' WHERE id='tok_bootstrap_expiry'`); e != nil {
		t.Fatal(e)
	}
	event := f.event()
	event.ActorID = principal.ID
	if _, e := f.pg.CreateServerBootstrap(t.Context(), principal.ID, "expired_actor", f.in, event); !errors.Is(e, auth.ErrForbidden) {
		t.Fatal("expired actor committed", e)
	}
	var operations, grants, audits int
	if e := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM server_bootstrap_operations),(SELECT count(*) FROM server_bootstrap_secret_grants),(SELECT count(*) FROM audit_events WHERE action='server_bootstrap.created')`).Scan(&operations, &grants, &audits); e != nil || operations != 0 || grants != 0 || audits != 0 {
		t.Fatal("actor expiry partial commit", operations, grants, audits, e)
	}
}
