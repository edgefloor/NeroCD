package store_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresHostingStackRuntimeLogTargetAndReplacement(t *testing.T) {
	pg, db, resource, server, destination := hostingExecutionFixture(t)
	ctx := t.Context()
	bearer := "image-hosting-fixture-token"
	f := runtimeLogFixture{pg: pg, db: db, resource: resource, server: server, destination: destination, bearer: bearer, hash: fmt.Sprintf("%x", sha256.Sum256([]byte(bearer)))}
	resource.ID = "res_stack_logs"
	resource.Name = "Compose component logs"
	resource.Source = domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\n    depends_on:\n      init:\n        condition: service_completed_successfully\n  init:\n    image: busybox:1.37\n    command: [\"true\"]\n", PublicComponent: "app", ContainerPort: 80}}
	var err error
	resource, err = pg.CreateResource(ctx, resource, store.WithHostingComposeCreate(strings.Repeat("a", 32), domain.HostingDocumentHash(resource)), store.WithAudit(destinationEvent("stack_log_create")))
	if err != nil {
		t.Fatal(err)
	}
	f.resource = resource
	if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_stack_logs", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("stack_log_bind")); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE runners SET capabilities=capabilities || ARRAY[$2,$3,$4,$5,$6]::text[] WHERE id=$1`, server.RunnerID, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability)
	input := hostingDeploymentRequest()
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	input.IdempotencyKey = "stack-log-first"
	op, err := pg.RequestHostingDeployment(ctx, "hop_stack_log_first", resource.ID, "usr_bootstrap", input, destinationEvent("stack_log_deploy"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	var images []domain.HostingStackImage
	for i, name := range []string{"app", "init"} {
		repo := "docker.io/library/nginx"
		if name == "init" {
			repo = "docker.io/library/busybox"
		}
		release := domain.HostingRelease{CanonicalImage: repo + "@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat(fmt.Sprint(i+1), 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon_stack_logs"}
		release.Hash = domain.HostingDocumentHash(release)
		images = append(images, domain.HostingStackImage{Component: name, Release: release})
	}
	release := domain.HostingRelease{DaemonID: "daemon_stack_logs", Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	prepared, err := pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "stack_log_prepared", Release: release, StackImages: images})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "app", NetworkID: strings.Repeat("c", 64), Components: []domain.HostingStackComponent{{Component: "app", ContainerID: strings.Repeat("d", 64), ImageID: images[0].Release.ImageID, OperationID: op.ID, Running: true}, {Component: "init", ContainerID: strings.Repeat("e", 64), ImageID: images[1].Release.ImageID, OperationID: op.ID, ExitCode: 0}}, Volumes: []domain.HostingStackVolume{}}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stack_log_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}}); err != nil {
		t.Fatal(err)
	}
	targets, err := pg.RuntimeLogTargets(ctx, "usr_bootstrap", resource.ID)
	if err != nil || targets.ResourceID != resource.ID || targets.OperationID != op.ID || len(targets.Components) != 2 {
		t.Fatal("current targets missing", err)
	}
	var sessions int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM hosting_runtime_log_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("discovery opened demand", err)
	}
	// Desired configuration can change without changing the settled log target.
	changed := resource
	source := *resource.Source.Compose
	source.ContainerPort = 81
	changed.Source.Compose = &source
	changed, err = pg.UpdateResource(ctx, changed, resource.Generation, store.WithAudit(destinationEvent("stack_log_desired_change")))
	if err != nil {
		t.Fatal(err)
	}
	if current, err := pg.RuntimeLogTargets(ctx, "usr_bootstrap", resource.ID); err != nil || current.OperationID != targets.OperationID {
		t.Fatal("desired port hid settled logs", err)
	}
	source.ContainerPort = 80
	changed.Source.Compose = &source
	resource, err = pg.UpdateResource(ctx, changed, changed.Generation, store.WithAudit(destinationEvent("stack_log_desired_restore")))
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedGeneration = resource.Generation
	f.exec(t, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingStackRuntimeLogsCapability)
	if _, err = pg.RuntimeLogTargets(ctx, "usr_bootstrap", resource.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old collector discovered stack targets", err)
	}
	f.exec(t, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingStackRuntimeLogsCapability)
	for i, component := range []string{"app", "init"} {
		in := runtimeLogOpenInput(i + 1)
		in.Component = component
		in.ExpectedOperationID = op.ID
		session, err := pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", resource.ID, in)
		if err != nil {
			t.Fatal(component, err)
		}
		if session.Target.Component != component || session.Target.ContainerID != state.Components[i].ContainerID || session.Target.ContainerName != domain.HostingStackObjectID(op.BindingID, "component", component) {
			t.Fatal("target was not derived from receipt")
		}
		replay, err := pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", resource.ID, in)
		if err != nil || replay.ID != session.ID {
			t.Fatal("open replay changed target", err)
		}
		c := f.claim(t, strings.Repeat("a", 32))
		batch := runtimeLogBatch(c, 10+i, 0, "component output")
		batch.Events[0].Component = "foreign"
		if _, err = pg.AppendRuntimeLogs(ctx, server.RunnerID, f.hash, bearer, batch); !errors.Is(err, store.ErrConflict) {
			t.Fatal("foreign component accepted", err)
		}
		batch.Events[0].Component = component
		f.append(t, batch)
		if page := f.page(t, session.ID); len(page.Items) != 1 || page.Items[0].Component != component {
			t.Fatal("component output lost")
		}
		if _, err = pg.CloseRuntimeLogSession(ctx, "usr_bootstrap", session.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i, component := range []string{"missing", "app"} {
		in := runtimeLogOpenInput(20 + i)
		in.Component = component
		in.ExpectedOperationID = op.ID
		if component == "app" {
			in.ExpectedOperationID = "hop_stale"
		}
		if _, err = pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", resource.ID, in); !errors.Is(err, store.ErrConflict) {
			t.Fatal("stale/missing selection admitted", err)
		}
	}
	in := runtimeLogOpenInput(30)
	in.Component = "app"
	in.ExpectedOperationID = op.ID
	session, err := pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", resource.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "stack-log-replace"
	input.HostPort = 0
	next, err := pg.RequestHostingDeployment(ctx, "hop_stack_log_next", resource.ID, "usr_bootstrap", input, destinationEvent("stack_log_replace"))
	if err != nil {
		t.Fatal(err)
	}
	next = awaitHostingDispatch(t, pg, next.ID)
	nextClaim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	nextAuthority := domain.HostingAuthority{OperationID: next.ID, RunID: nextClaim.Run.ID, LeaseID: nextClaim.Lease.ID, Attempt: nextClaim.Lease.Attempt, Fence: nextClaim.Lease.Fence}
	if _, err = pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: nextAuthority, ReceiptID: "stack_log_next_prepared", Release: release, StackImages: images}); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, nextAuthority); err != nil {
		t.Fatal(err)
	}
	page := f.page(t, session.ID)
	if page.Session.State != "target_changed" || page.Session.Target.OperationID != op.ID {
		t.Fatal("replacement retargeted old session")
	}
	in.RequestID = strings.Repeat("f", 32)
	if _, err = pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", resource.ID, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("replacement admitted new demand", err)
	}
}
