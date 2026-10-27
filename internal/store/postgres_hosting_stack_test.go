package store_test

import (
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/store"
	"strings"
	"testing"
	"time"
)

func TestPostgresHostingStackAuthorityVolumesAndUncertainReplacement(t *testing.T) {
	pg, database, resource, server, destination := hostingExecutionFixture(t)
	ctx := t.Context()
	resource.ID = "res_compose_transaction"
	resource.Name = "Compose transaction"
	resource.Source = domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\n    volumes: [data:/data]\nvolumes:\n  data: {}\n", PublicComponent: "app", ContainerPort: 80}}
	var err error
	resource, err = pg.CreateResource(ctx, resource, store.WithHostingComposeCreate(strings.Repeat("a", 32), domain.HostingDocumentHash(resource)), store.WithAudit(destinationEvent("compose_create")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_compose_transaction", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("compose_bind")); err != nil {
		t.Fatal(err)
	}
	input := hostingDeploymentRequest()
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	input.IdempotencyKey = "compose-initial"
	if _, err = pg.RequestHostingDeployment(ctx, "hop_compose_old_agent", resource.ID, "usr_bootstrap", input, destinationEvent("compose_old_agent")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old agent accepted: %v", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=capabilities || ARRAY[$2,$3,$4]::text[] WHERE id=$1`, server.RunnerID, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	options, err := pg.HostingStorageDeploymentOptions(ctx, resource.ID, "usr_bootstrap")
	if err != nil || !options.Available || options.Reason != "initial_deployment" || options.StackRef == nil || options.StorageRef != nil {
		t.Fatalf("initial stack options: %+v %v", options, err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_compose_initial", resource.ID, "usr_bootstrap", input, destinationEvent("compose_initial"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := pg.RequestHostingDeployment(ctx, "hop_compose_retry", resource.ID, "usr_bootstrap", input, destinationEvent("compose_retry"))
	if err != nil || replay.ID != op.ID {
		t.Fatal("request retry lost identity", err)
	}
	changed := input
	changed.ExpectedGeneration++
	if _, err = pg.RequestHostingDeployment(ctx, "hop_compose_stale", resource.ID, "usr_bootstrap", changed, destinationEvent("compose_stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed body accepted", err)
	}
	prepare := func(op domain.HostingOperation) (domain.HostingAuthority, domain.HostingOperation) {
		t.Helper()
		op = awaitHostingDispatch(t, pg, op.ID)
		claim, e := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		plan, e := pg.HostingRunnerPlan(ctx, server.RunnerID, a)
		if e != nil {
			t.Fatal(e)
		}
		release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon_stack"}
		release.Hash = domain.HostingDocumentHash(release)
		images := []domain.HostingStackImage{{Component: "app", Release: release}}
		stackRelease := domain.HostingRelease{DaemonID: release.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
		stackRelease.Hash = domain.HostingDocumentHash(stackRelease)
		if plan.StackSource == nil {
			t.Fatal("missing exact source version")
		}
		prepared, e := pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: stackRelease, StackImages: images})
		if e != nil {
			t.Fatal(e)
		}
		return a, prepared
	}
	authority, prepared := prepare(op)
	// An image/variable preflight failure never acquired a writer grant. Its
	// released port receipt must not strand the initial-deployment UI after a fix.
	if _, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "compose_unmutated_failure", Result: domain.HostingResult{Outcome: "failed"}}); err != nil {
		t.Fatal("record unmutated failure", err)
	}
	options, err = pg.HostingStorageDeploymentOptions(ctx, resource.ID, "usr_bootstrap")
	if err != nil || !options.Available || options.Reason != "initial_deployment" {
		t.Fatalf("unmutated failure stranded retry: reason=%s available=%t err=%v", options.Reason, options.Available, err)
	}
	input.IdempotencyKey = "compose-initial-fixed"
	op, err = pg.RequestHostingDeployment(ctx, "hop_compose_initial_fixed", resource.ID, "usr_bootstrap", input, destinationEvent("compose_initial_fixed"))
	if err != nil {
		t.Fatal("retry after unmutated failure", err)
	}
	authority, prepared = prepare(op)
	stale := authority
	stale.Fence = "stale-fence"
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, stale); !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale fence accepted", err)
	}
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, authority)
	if err != nil || !admitted.StackCreatePermitted || !admitted.MutationHold {
		t.Fatal("missing fresh whole-stack admission", err)
	}
	repeated, err := pg.HostingApplyBegin(ctx, server.RunnerID, authority)
	if err != nil || repeated.StackCreatePermitted {
		t.Fatal("retry renewed volume creation permission", err)
	}
	volume := prepared.Plan.Stack.Volumes[0]
	volume.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	seal := domain.HostingStackVolumeRequest{HostingAuthority: authority, PlanHash: prepared.Plan.Hash, Volumes: []domain.HostingStackVolume{volume}}
	if _, err = pg.HostingStackVolumes(ctx, server.RunnerID, seal); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingStackVolumes(ctx, server.RunnerID, seal); err != nil {
		t.Fatal("exact seal retry failed", err)
	}
	different := seal
	different.Volumes = append([]domain.HostingStackVolume{}, seal.Volumes...)
	different.Volumes[0].CreatedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err = pg.HostingStackVolumes(ctx, server.RunnerID, different); !errors.Is(err, store.ErrConflict) {
		t.Fatal("volume origin changed", err)
	}
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "app", NetworkID: strings.Repeat("c", 64), Components: []domain.HostingStackComponent{{Component: "app", ContainerID: strings.Repeat("d", 64), ImageID: prepared.Plan.Stack.Images[0].Release.ImageID, OperationID: op.ID, Running: true}}, Volumes: seal.Volumes}
	result := domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: prepared.Release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}
	completed, err := pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "complete_stack", Result: result})
	if err != nil || completed.MutationHold || completed.State != "succeeded" {
		t.Fatal("whole stack success failed", err)
	}
	options, err = pg.HostingStorageDeploymentOptions(ctx, resource.ID, "usr_bootstrap")
	if err != nil || !options.Available || options.HostPort == nil || *options.HostPort != input.HostPort {
		t.Fatal("saved stack redeploy options lost", err)
	}
	input.IdempotencyKey = "compose-replace"
	input.HostPort = 0
	next, err := pg.RequestHostingDeployment(ctx, "hop_compose_replace", resource.ID, "usr_bootstrap", input, destinationEvent("compose_replace"))
	if err != nil || next.Snapshot.HostPort != op.Snapshot.HostPort {
		t.Fatal("stack port was not retained", err)
	}
	nextAuthority, nextPlan := prepare(next)
	if nextPlan.Plan.Stack.Volumes[0] != volume {
		t.Fatal("redeploy changed retained volume origin")
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, nextAuthority); err != nil {
		t.Fatal(err)
	}
	failed := domain.HostingResult{Outcome: "failed", Uncertain: true, DaemonID: nextPlan.Release.DaemonID, PlanHash: nextPlan.Plan.Hash, Stack: &state}
	held, err := pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: nextAuthority, ReceiptID: "failed_stack", Result: failed})
	if err != nil || !held.MutationHold || held.State != "reconciliation_required" {
		t.Fatal("failed replacement released writer authority", err)
	}
	input.IdempotencyKey = "competing"
	if _, err = pg.RequestHostingDeployment(ctx, "hop_compose_competing", resource.ID, "usr_bootstrap", input, destinationEvent("compose_competing")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("competing writer admitted during uncertainty", err)
	}
}
