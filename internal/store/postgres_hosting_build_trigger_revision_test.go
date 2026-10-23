package store_test

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/store"
	"strings"
	"testing"
	"time"
)

func triggerBuildFixture(t *testing.T) buildStoreFixture {
	t.Helper()
	f := newBuildStoreFixture(t)
	stop := startBuildController(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}
func triggerCapability(t *testing.T, f buildStoreFixture, enabled bool) {
	t.Helper()
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType}
	if enabled {
		caps = append(caps, domain.HostingBuildTriggerRevisionCapability)
	}
	if _, err := f.db.Exec(t.Context(), `UPDATE runners SET capabilities=$2 WHERE id=$1`, f.runner, caps); err != nil {
		t.Fatal(err)
	}
}
func requestTriggerBuild(t *testing.T, f buildStoreFixture, id, key, commit string) (domain.HostingBuild, error) {
	t.Helper()
	return f.pg.RequestConfiguredHostingBuildAtRevision(t.Context(), id, f.src.ResourceID, "usr_bootstrap", publicBuildInput(key), domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: commit}, destinationEvent(id))
}
func TestPostgresHostingBuildTriggerRevisionAdmissionAndIdempotency(t *testing.T) {
	f := triggerBuildFixture(t)
	commit := strings.Repeat("b", 40)
	if _, err := requestTriggerBuild(t, f, "absent", "absent", commit); !errors.Is(err, store.ErrConflict) {
		t.Fatal("legacy runner admitted revision", err)
	}
	// Nil revision manual admission still accepts this same legacy runner.
	legacy := f.request(t, "legacy")
	var legacyRequestHash string
	if err := f.db.QueryRow(t.Context(), `SELECT request_hash FROM hosting_builds WHERE id=$1`, legacy.ID).Scan(&legacyRequestHash); err != nil {
		t.Fatal(err)
	}
	wantLegacy := domain.HostingDocumentHash(struct {
		Input  domain.HostingBuildInput
		Policy domain.HostingBuildPolicy
		Actor  string
	}{buildInput("legacy"), buildPolicy(), "usr_bootstrap"})
	if legacyRequestHash != wantLegacy {
		t.Fatal("nil revision changed historical request hash")
	}
	if legacy.Snapshot.TriggerRevision != nil {
		t.Fatal("manual build acquired trigger intent")
	}
	triggerCapability(t, f, true)
	b, err := requestTriggerBuild(t, f, "trigger", "key", commit)
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot.TriggerRevision == nil || b.Snapshot.TriggerRevision.ExpectedCommit != commit || b.Snapshot.Source.RequestedRef != "main" {
		t.Fatal("configured source or revision changed")
	}
	replay, err := requestTriggerBuild(t, f, "retry", "key", commit)
	if err != nil || replay.ID != b.ID {
		t.Fatal("same revision retry", err)
	}
	if _, err = requestTriggerBuild(t, f, "changed", "key", strings.Repeat("c", 40)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("different revision reused idempotency key", err)
	}
	if _, err = f.pg.RequestConfiguredHostingBuild(t.Context(), "nil-retry", f.src.ResourceID, "usr_bootstrap", publicBuildInput("key"), destinationEvent("nil-retry")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("nil revision reused revision key", err)
	}
	triggerCapability(t, f, false)
	if _, err = f.pg.DispatchHostingBuild(t.Context(), b.ID, buildDispatch, destinationEvent("dispatch")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("dispatch ignored capability loss", err)
	}
}
func TestPostgresHostingBuildTriggerRevisionHistoricalAcknowledgment(t *testing.T) {
	f := triggerBuildFixture(t)
	triggerCapability(t, f, true)
	b, err := requestTriggerBuild(t, f, "history", "history", strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	b, a := f.activate(t, b.ID)
	receipt := sourceBuildReceipt(b)
	b, err = f.pg.CommitHostingBuildSource(t.Context(), f.runner, a, "source-receipt", receipt)
	if err != nil {
		t.Fatal(err)
	}
	triggerCapability(t, f, false)
	// Existing exact receipt is evidence, while fresh prepare is execution.
	if _, err = f.pg.CommitHostingBuildSource(t.Context(), f.runner, a, "source-receipt", receipt); err != nil {
		t.Fatal("historical source ack lost", err)
	}
	if _, err = f.pg.PrepareHostingBuild(t.Context(), f.runner, a, b.Revision, "daemon"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("fresh execution ignored missing capability", err)
	}
	status, err := f.pg.HostingBuildStatus(t.Context(), f.runner, a)
	if err != nil || status.MayExecute || len(status.Receipts) == 0 {
		t.Fatal("status failed to preserve historical evidence", err)
	}
	if hostingbuildprotocol.ValidateAcknowledgment(status.Receipts[0], a, "source", "source-receipt", hostingbuildprotocol.SourceReceiptHash(receipt)) != nil {
		t.Fatal("wrong historical evidence")
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CommitHostingBuildSource(t.Context(), f.runner, a, "source-receipt", receipt); err == nil {
		t.Fatal("historical ack bypassed revoked identity")
	}
}

func TestPostgresHostingBuildTriggerRevisionPrivateGrant(t *testing.T) {
	f, _, _ := privateDeliveryFixture(t)
	grant, err := f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: "delivery_secret", SecretVersion: 1, RequestID: strings.Repeat("d", 32)}, destinationEvent("trigger-grant"))
	if err != nil {
		t.Fatal(err)
	}
	stop := startBuildController(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingBuildTriggerRevisionCapability); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "private-trigger", GrantID: grant.ID}
	revision := domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("b", 40)}
	b, err := f.pg.RequestConfiguredHostingBuildAtRevision(ctx, "private-trigger", f.src.ResourceID, "usr_bootstrap", in, revision, destinationEvent("private-trigger"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot.Grant == nil || b.Snapshot.Grant.ID != in.GrantID || b.Snapshot.Source.RequestedRef != f.src.RequestedRef {
		t.Fatal("revision altered source grant purpose")
	}
	replay, err := f.pg.RequestConfiguredHostingBuildAtRevision(ctx, "private-retry", f.src.ResourceID, "usr_bootstrap", in, revision, destinationEvent("private-retry"))
	if err != nil || replay.ID != b.ID {
		t.Fatal("private exact retry", err)
	}
	if err = f.pg.RevokeHostingSourceGrant(ctx, "usr_bootstrap", in.GrantID, destinationEvent("revoke-private")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestConfiguredHostingBuildAtRevision(ctx, "private-revoked", f.src.ResourceID, "usr_bootstrap", in, revision, destinationEvent("private-revoked")); err == nil {
		t.Fatal("trigger retry bypassed current grant revocation")
	}
}

func TestPostgresHostingBuildTriggerRevisionClaimCapability(t *testing.T) {
	f := triggerBuildFixture(t)
	triggerCapability(t, f, true)
	b, err := requestTriggerBuild(t, f, "claim-trigger", "claim-trigger", strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DispatchHostingBuild(t.Context(), b.ID, buildDispatch, destinationEvent("claim-dispatch")); err != nil {
		t.Fatal(err)
	}
	triggerCapability(t, f, false)
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if !errors.Is(err, store.ErrNotFound) || claim.Run.ID != "" {
		t.Fatal("legacy runner claimed trigger revision", err)
	}
	triggerCapability(t, f, true)
	claim, err = f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != "build_run_"+b.ID {
		t.Fatal("capable runner could not claim exact trigger build", err)
	}
}
func TestPostgresHostingBuildTriggerRevisionConfiguredCommit(t *testing.T) {
	f := triggerBuildFixture(t)
	triggerCapability(t, f, true)
	src := f.src
	src.Generation = 2
	src.RequestedRef = strings.Repeat("c", 40)
	if _, err := configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", 1, src, destinationEvent("fixed-source")); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "fixed-source"}
	revision := domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("b", 40)}
	if _, err := f.pg.RequestConfiguredHostingBuildAtRevision(t.Context(), "wrong-fixed", src.ResourceID, "usr_bootstrap", in, revision, destinationEvent("wrong-fixed")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("configured fixed commit changed", err)
	}
	revision.ExpectedCommit = src.RequestedRef
	if _, err := f.pg.RequestConfiguredHostingBuildAtRevision(t.Context(), "same-fixed", src.ResourceID, "usr_bootstrap", in, revision, destinationEvent("same-fixed")); err != nil {
		t.Fatal(err)
	}
}
