package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/serverbootstrap"
	"nerocd/internal/store"
	"testing"
	"time"
)

func bootstrapTrustedProbeReview(t *testing.T, f *bootstrapFixture, op domain.ServerBootstrapOperation) domain.ServerBootstrapOperation {
	t.Helper()
	stage := f.claim(t, op, "probe")
	receipt := f.receipt(stage, "observed")
	receipt.Probe = &domain.ServerBootstrapProbeReceipt{Prerequisites: domain.ServerBootstrapPrerequisites{Profile: "prepared_debian12_v1", OSState: "available", OSID: "debian", OSVersion: "12", Architecture: op.Input.Artifact.Architecture, SystemdState: "available", SystemdStatus: "running", DockerState: "available", DockerAPIVersion: "1.54", ComposeState: "available", ComposeVersion: "5.1.1", DiskState: "available", DiskTotalBytes: 2 << 30, DiskAvailableBytes: 1 << 30}}
	if _, e := f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event()); e != nil {
		t.Fatal(e)
	}
	op = f.get(t, op.IDs.OperationID)
	var probe *domain.ServerBootstrapProbeReceipt
	for _, c := range op.Commands {
		if c.Command.ID == stage.Command.ID {
			probe = c.Receipt.Probe
		}
	}
	if probe == nil || probe.ObservedAt.IsZero() || probe.ExpiresAt.Sub(probe.ObservedAt) != 30*time.Second {
		t.Fatal("probe missing DB clock expiry")
	}
	m := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: op.Input.ControllerURL, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
	raw, hash, e := serverbootstrap.CanonicalManifest(m)
	if e != nil {
		t.Fatal(e)
	}
	review := &domain.ServerBootstrapReview{Prerequisites: probe.Prerequisites, Manifest: raw, ManifestSHA256: hash, ProbeObservedAt: probe.ObservedAt, ProbeExpiresAt: probe.ExpiresAt, TargetHash: domain.HostingDocumentHash(op.Input.Target)}
	command := domain.ServerBootstrapCommand{Key: "review", Action: "review", ExpectedVersion: op.Version, Deadline: op.CreatedAt.Add(time.Hour), Review: review, ProbeReceiptID: "fabricated"}
	if _, e = f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, command, f.event()); !errors.Is(e, store.ErrNotFound) {
		t.Fatalf("fabricated receipt %v", e)
	}
	command.ProbeReceiptID = stage.Command.ID
	if _, e = f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, command, f.event()); e != nil {
		t.Fatal(e)
	}
	return f.get(t, op.IDs.OperationID)
}
func TestPostgresServerBootstrapTypedProbeAndResponseLossRecovery(t *testing.T) {
	f := newBootstrapFixture(t)
	f.in.ArtifactID = "qualified"
	f.in.Artifact.Version = "v1.2.3"
	op := f.create(t)
	op = bootstrapTrustedProbeReview(t, f, op)
	grant := false
	command := domain.ServerBootstrapCommand{Key: "install", Action: "install", ExpectedVersion: op.Version, Deadline: op.CreatedAt.Add(time.Hour), ManifestSHA256: op.Review.ManifestSHA256, InstallAcknowledged: true, GrantProject: &grant}
	c, e := f.pg.RequestServerBootstrapCommand(t.Context(), "usr_bootstrap", op.IDs.OperationID, command, f.event())
	if e != nil {
		t.Fatal(e)
	}
	stage, e := f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pg.AuthorizeServerBootstrapDelivery(t.Context(), "usr_bootstrap", op.IDs.OperationID, stage, f.ring, f.event()); e != nil {
		t.Fatal(e)
	}
	// Simulate controller response loss after the helper committed installation.
	if _, e = f.db.Exec(t.Context(), `UPDATE server_bootstrap_commands SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, c.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pg.ClaimServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, c.ID); !errors.Is(e, store.ErrConflict) {
		t.Fatal("lost install retried", e)
	}
	op = f.get(t, op.IDs.OperationID)
	if op.State != "uncertain" || !op.RemoteHold {
		t.Fatalf("missing retained ownership %+v", op)
	}
	inspect := f.claim(t, op, "inspect_owned")
	receipt := f.receipt(inspect, "observed")
	receipt.Owned = &domain.ServerBootstrapOwnedReceipt{InstallationID: op.IDs.InstallationID, ManifestSHA256: op.Review.ManifestSHA256, OperationID: "wrong", State: "installed", ArtifactMatches: true, ServiceActive: true}
	if _, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("boolean inspection accepted", e)
	}
	receipt.Owned.OperationID = op.IDs.InstallOperationID
	receipt.Owned.ServiceJobPending = true
	if _, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event()); !errors.Is(e, store.ErrConflict) {
		t.Fatal("nonquiescent inspection accepted", e)
	}
	receipt.Owned.ServiceJobPending = false
	if _, e = f.pg.CompleteServerBootstrapStage(t.Context(), "usr_bootstrap", op.IDs.OperationID, receipt, f.event()); e != nil {
		t.Fatal(e)
	}
	op = f.get(t, op.IDs.OperationID)
	if op.State != "installed_waiting_enrollment" || !op.RemoteHold {
		t.Fatal("reconciliation failed", op.State)
	}
	consumed, e := f.pg.ServerBootstrapEnrollmentConsumed(t.Context(), "usr_bootstrap", op.IDs.OperationID)
	if e != nil || consumed {
		t.Fatal("SSH receipt invented enrollment", e)
	}
	var pending int
	if e = f.db.QueryRow(t.Context(), `SELECT count(*) FROM server_bootstrap_commands WHERE operation_id=$1 AND action='install' AND receipt IS NULL`, op.IDs.OperationID).Scan(&pending); e != nil || pending != 0 {
		t.Fatal("original intent not settled", pending, e)
	}
	// Registration command is now admissible without another install or new grant.
	f.command(t, op, "register_server", nil)
}
func TestPostgresServerBootstrapQueueRecoveryWithoutBuildPolicy(t *testing.T) {
	f := newBootstrapFixture(t)
	calls := make(chan string, 10)
	if e := f.pg.ConfigureServerBootstrapProcessor(func(_ context.Context, id string) error { calls <- id; return nil }); e != nil {
		t.Fatal(e)
	}

	for {
		tx, e := f.db.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		changed, e := hostingjobs.MigrateNextTx(t.Context(), tx)
		if e != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(e)
		}
		if e = tx.Commit(t.Context()); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
	}
	stop, e := f.pg.StartHostingExecution(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(ctx); e != nil {
			t.Error(e)
		}
	}()
	op := f.create(t)
	f.command(t, op, "probe", nil)
	select {
	case id := <-calls:
		if id != op.IDs.OperationID {
			t.Fatal(id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap absent with build disabled")
	}
	var raw []byte
	if e = f.db.QueryRow(t.Context(), `SELECT args FROM river_job WHERE kind='hosting.server_bootstrap.v1' LIMIT 1`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || len(args) != 1 || args["operation_id"] != op.IDs.OperationID {
		t.Fatalf("job metadata %s", raw)
	}
	// Remove River history. The business command remains discoverable by existing recovery.
	if _, e = f.db.Exec(t.Context(), `DELETE FROM river_job WHERE kind='hosting.server_bootstrap.v1'`); e != nil {
		t.Fatal(e)
	}
	select {
	case id := <-calls:
		if id != op.IDs.OperationID {
			t.Fatal(id)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("business command did not recover")
	}
}
