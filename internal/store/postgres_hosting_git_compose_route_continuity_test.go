package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func routedGitComposeFixture(t *testing.T) (buildStoreFixture, domain.HostingOperation, domain.HostingBuildDeploymentInput, domain.HostingProxyOperation, domain.HostingRoute) {
	t.Helper()
	f, op, in := settledGitComposeOperationsFixture(t)
	ctx := t.Context()
	resource, err := f.pg.GetResource(ctx, op.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	config := domain.HostingProxyInput{ControlProjectID: op.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "git-continuity-proxy"}
	if _, err = f.pg.ConfigureHostingProxy(ctx, "proxy_git_continuity", op.Snapshot.ServerID, "usr_bootstrap", config, destinationEvent("git-proxy-configure")); err != nil {
		t.Fatal(err)
	}
	var first string
	if err = f.db.QueryRow(ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id='proxy_git_continuity' AND generation=1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	settleProxyGeneration(t, f.pg, first, f.runner, op.Release.DaemonID)
	if _, err = f.pg.GrantHostingHostname(ctx, "proxy_git_continuity", "grant_git_continuity", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: op.ProjectID, Hostname: "apps.nerocd.invalid", Wildcard: true}, destinationEvent("git-proxy-grant")); err != nil {
		t.Fatal(err)
	}
	route, err := f.pg.RequestHostingRoute(ctx, resource.ID, "", "pop_git_route", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "git-route", Hostname: "git.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: op.ID}, destinationEvent("git-route"))
	if err != nil {
		t.Fatal(err)
	}
	loaded := settleStorageContinuityGeneration(t, storageMaterializationFixture{pg: f.pg, db: f.db, resource: resource, runner: f.runner, op: op}, route.OperationID)
	return f, op, in, loaded, route
}

func TestPostgresGitComposeRoutedReplacement(t *testing.T) {
	f, prior, input, loaded, route := routedGitComposeFixture(t)
	ctx := t.Context()
	input.IdempotencyKey = "git-routed-replacement"
	input.HostPort = prior.Snapshot.HostPort
	if _, err := f.pg.RequestHostingBuildDeployment(ctx, "git-missing-continuity", prior.ResourceID, "usr_bootstrap", input, destinationEvent("git-missing-continuity")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("routed replacement without explicit proof admitted", err)
	}
	// Exercise the same serialized body used by the public decoder.
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["storage_route_continuity"] = domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: loaded.Generation, ExpectedLoadedProxyOperationID: loaded.ID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: route.ID, ExpectedVersion: route.Version, ExpectedSelectedReleaseOperationID: prior.ID}}}
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	options, err := f.pg.HostingStorageDeploymentOptions(ctx, prior.ResourceID, "usr_bootstrap")
	if err != nil || !options.Available || options.StorageRouteContinuity == nil || options.StackRef == nil || *options.StackRef != prior.Snapshot.Stack || options.HostPort == nil || *options.HostPort != prior.Snapshot.HostPort || options.PreviousHealthyOperationID == nil || *options.PreviousHealthyOperationID != prior.ID {
		t.Fatalf("current Git stack route options unavailable: %+v %v", options, err)
	}
	for _, field := range []string{"proxy_generation", "proxy_operation", "route_version", "route_target", "source_generation", "resource_generation", "build"} {
		copyRaw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var changed domain.HostingBuildDeploymentInput
		if err = json.Unmarshal(copyRaw, &changed); err != nil {
			t.Fatal(err)
		}
		changed.IdempotencyKey = "invalid-" + field
		switch field {
		case "proxy_generation":
			changed.StorageRouteContinuity.ExpectedLoadedGeneration++
		case "proxy_operation":
			changed.StorageRouteContinuity.ExpectedLoadedProxyOperationID = "pop_wrong"
		case "route_version":
			changed.StorageRouteContinuity.Routes[0].ExpectedVersion++
		case "route_target":
			changed.StorageRouteContinuity.Routes[0].ExpectedSelectedReleaseOperationID = "hop_wrong"
		case "source_generation":
			changed.ExpectedSourceGeneration++
		case "resource_generation":
			changed.ExpectedGeneration++
		case "build":
			changed.BuildID = "unknown-build"
		}
		if _, err = f.pg.RequestHostingBuildDeployment(ctx, "invalid-"+field, prior.ResourceID, "usr_bootstrap", changed, destinationEvent("invalid-"+field)); err == nil {
			t.Fatalf("invalid %s proof accepted", field)
		}
	}
	var reservations int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_storage_route_continuities`).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal("rejected request persisted continuity", err)
	}
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "git-routed-replacement", prior.ResourceID, "usr_bootstrap", input, destinationEvent("git-routed-replacement"))
	if err != nil {
		t.Fatal("explicit routed Git Compose replacement rejected", err)
	}
	if op.Snapshot.StorageRouteContinuity.Hash == "" {
		t.Fatal("continuity proof absent")
	}
	var accepted []byte
	var requestHash string
	if err = f.db.QueryRow(ctx, `SELECT c.accepted_request,o.request_hash FROM hosting_storage_route_continuities c JOIN hosting_operations o ON o.id=c.operation_id WHERE c.operation_id=$1`, op.ID).Scan(&accepted, &requestHash); err != nil {
		t.Fatal(err)
	}
	var saved domain.HostingBuildDeploymentInput
	if err = json.Unmarshal(accepted, &saved); err != nil || saved.BuildID != input.BuildID || saved.ExpectedSourceGeneration != input.ExpectedSourceGeneration || domain.HostingDocumentHash(saved) != domain.HostingDocumentHash(input) || requestHash != domain.HostingDocumentHash(input) {
		t.Fatal("full accepted build request/hash changed", err)
	}
	replay, err := f.pg.RequestHostingBuildDeployment(ctx, "ignored-replay", prior.ResourceID, "usr_bootstrap", input, destinationEvent("replay"))
	if err != nil || replay.ID != op.ID || replay.Snapshot.StorageRouteContinuity != op.Snapshot.StorageRouteContinuity {
		t.Fatal("identical retry changed receipt", err)
	}
	changed := input
	changed.BuildID = "different-build"
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "changed-replay", prior.ResourceID, "usr_bootstrap", changed, destinationEvent("changed-replay")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed BuildID replay accepted", err)
	}
	// Settle dispatch before cancellation's initial/locked run-ID comparison.
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CancelHostingOperation(ctx, op.ID, destinationEvent("cancel-routed")); err != nil {
		t.Fatal(err)
	}
	status, err := f.pg.HostingStorageRouteContinuityStatus(ctx, prior.ResourceID, "usr_bootstrap", op.ID)
	if err != nil || status.State != "released" {
		t.Fatal("unmutated reservation not released", status, err)
	}
	current := editGitComposeDesiredSource(t, f)
	options, err = f.pg.HostingStorageDeploymentOptions(ctx, prior.ResourceID, "usr_bootstrap")
	if err != nil || !options.Available || options.ResourceGeneration != current.Generation || options.StackRef == nil || *options.StackRef != prior.Snapshot.Stack {
		t.Fatal("desired edit lost deployed stack route proof", options, err)
	}
	replay, err = f.pg.RequestHostingBuildDeployment(ctx, "historical-replay", prior.ResourceID, "usr_bootstrap", input, destinationEvent("historical-replay"))
	if err != nil || replay.ID != op.ID {
		t.Fatal("historical exact request replay denied", err)
	}
	input.IdempotencyKey = "stale-after-source-edit"
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "stale-after-source-edit", prior.ResourceID, "usr_bootstrap", input, destinationEvent("stale-after-source-edit")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale build admitted after source edit", err)
	}
}

func TestPostgresGitComposeStoppedRouteOptions(t *testing.T) {
	f, deployed, _, _, _ := routedGitComposeFixture(t)
	ctx := t.Context()
	resource := editGitComposeDesiredSource(t, f)
	stop, err := f.pg.RequestHostingStackStop(ctx, "route-stop", resource.ID, "usr_bootstrap", domain.HostingStackStopInput{IdempotencyKey: "route-stop", ExpectedGeneration: resource.Generation, ExpectedDeployedOperationID: deployed.ID}, destinationEvent("route-stop"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(ctx, stop.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.HostingAuthority{OperationID: stop.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, authority); err != nil {
		t.Fatal(err)
	}
	admitted, err := f.pg.HostingApplyBegin(ctx, f.runner, authority)
	if err != nil {
		t.Fatal(err)
	}
	state := *deployed.Result.Stack
	state.Components = []domain.HostingStackComponent{}
	if _, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "route-stopped", Result: domain.HostingResult{Outcome: "succeeded", StackStopped: true, DaemonID: deployed.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &state}}); err != nil {
		t.Fatal(err)
	}
	options, err := f.pg.HostingStorageDeploymentOptions(ctx, resource.ID, "usr_bootstrap")
	if err != nil || !options.Available || options.StoppedOperationID == nil || *options.StoppedOperationID != stop.ID || options.StorageRouteContinuity == nil || options.StackRef == nil || *options.StackRef != deployed.Snapshot.Stack {
		t.Fatal("stopped route options lost historical proof", options, err)
	}
}

func TestPostgresOrdinaryBuildRejectsRouteContinuity(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	if _, err := f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	resource, err := f.pg.GetResource(t.Context(), f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ConfigureHostingProxy(t.Context(), "proxy_local_misuse", "server_build", "usr_bootstrap", domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "local-proxy"}, destinationEvent("local-proxy")); err != nil {
		t.Fatal(err)
	}
	input := localBuildDeploymentInput(b.ID)
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	input.StorageRouteContinuity = &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_loaded", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_web", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_prior"}}}
	if _, err := f.pg.RequestHostingBuildDeployment(t.Context(), "bad-local-continuity", f.src.ResourceID, "usr_bootstrap", input, destinationEvent("bad-local-continuity")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("ordinary local artifact accepted stack continuity", err)
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("misuse persisted operation", err)
	}
}
