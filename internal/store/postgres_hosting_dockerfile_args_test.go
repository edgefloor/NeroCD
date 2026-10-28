package store_test

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresDockerfileArgumentsCapabilityAndReplay(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	// Existing argument-free work remains admissible on an older build runner.
	if _, err := f.pg.RequestHostingBuild(ctx, "legacy-noargs", f.src.ResourceID, "usr_bootstrap", buildInput("legacy-noargs"), buildPolicy(), buildEnqueue, destinationEvent("legacy-noargs")); err != nil {
		t.Fatal("legacy noargs admission changed", err)
	}
	source := f.src
	source.Generation++
	disabled := false
	source.Settings.BuildVariables = []domain.HostingBuildVariable{{Name: "APP", Value: "public\nvalue"}}
	source.Settings.InjectBuildArgsToDockerfile = &disabled
	var err error
	f.src, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, source, destinationEvent("dockerfile-variables"))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := f.pg.GetResource(ctx, f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingBuildInput{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: f.src.Generation, IdempotencyKey: "dockerfile-arguments"}
	if _, err = f.pg.RequestHostingBuild(ctx, "old-runner-args", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("old-runner-args")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("args reached incompatible runner", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds WHERE id='old-runner-args'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied admission persisted a build", err)
	}
	setCapability := func(enabled bool) {
		t.Helper()
		query := `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`
		if enabled {
			query = `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`
		}
		if _, err := f.db.Exec(ctx, query, f.runner, domain.HostingDockerfileArgsCapability); err != nil {
			t.Fatal(err)
		}
	}
	setCapability(true)
	b, err := f.pg.RequestHostingBuild(ctx, "dockerfile-arguments", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("dockerfile-arguments"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Snapshot.Source.Settings.BuildVariables) != 1 || b.Snapshot.Source.Settings.InjectBuildArgsToDockerfile == nil || *b.Snapshot.Source.Settings.InjectBuildArgsToDockerfile {
		t.Fatal("explicit source settings not pinned")
	}
	b, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, destinationEvent("dispatch-arguments"))
	if err != nil {
		t.Fatal(err)
	}
	setCapability(false)
	denied, claimErr := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if claimErr == nil && denied.Run.ID == b.RunID {
		t.Fatal("queued arguments claimed without capability")
	}
	setCapability(true)
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != b.RunID {
		t.Fatal("capable claim failed", err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, a, b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	setCapability(false)
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute {
		t.Fatal("active args execution ignored capability removal", err)
	}
	if _, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "denied-source", sourceBuildReceipt(b)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("source mutation escaped capability gate", err)
	}
	setCapability(true)
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "arguments-source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	completion := store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact}
	completed, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "arguments-complete", b.Revision, completion)
	if err != nil {
		t.Fatal(err)
	}
	setCapability(false)
	retry, err := f.pg.RequestHostingBuild(ctx, "ignored-retry", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("argument-retry"))
	if err != nil || retry.ID != completed.ID {
		t.Fatal("historical metadata retry denied", err)
	}
	replay, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "arguments-complete", b.Revision, completion)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(completed) {
		t.Fatal("historical completion replay changed", err)
	}
	changed := f.src
	changed.Generation++
	changed.Settings = changed.Settings.Clone()
	enabled := true
	changed.Settings.InjectBuildArgsToDockerfile = &enabled
	if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, changed, destinationEvent("enable-injection")); err != nil {
		t.Fatal(err)
	}
	stale := input
	stale.IdempotencyKey = "stale-arguments"
	if _, err = f.pg.RequestHostingBuild(ctx, "stale-arguments", f.src.ResourceID, "usr_bootstrap", stale, buildPolicy(), buildEnqueue, destinationEvent("stale-arguments")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale source generation admitted", err)
	}
	retained, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", completed.ID)
	if err != nil || domain.HostingDocumentHash(retained.Snapshot) != domain.HostingDocumentHash(completed.Snapshot) {
		t.Fatal("source edit rewrote historical build settings", err)
	}
}
