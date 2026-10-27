package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresGitComposeResultAndDeployment(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	src := f.src
	src.Generation++
	src.Settings = domain.HostingBuildSettings{Strategy: "compose", ComposeFile: "deploy/compose.yaml", PublicComponent: "web", ContainerPort: 8080}
	var err error
	f.src, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("compose-source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || $1::text[] WHERE id=$2`, []string{domain.HostingGitComposeBuildCapability, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability}, f.runner); err != nil {
		t.Fatal(err)
	}
	resource, err := f.pg.GetResource(ctx, f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingBuildInput{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: f.src.Generation, IdempotencyKey: "compose-build"}
	policy := buildPolicy()
	if _, err = f.pg.RequestHostingBuild(ctx, "disabled-compose", f.src.ResourceID, "usr_bootstrap", input, policy, buildEnqueue, destinationEvent("disabled-compose")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("disabled Compose build admitted", err)
	}
	policy.GitCompose = true
	if _, err = f.pg.RequestHostingBuild(ctx, "compose-build", f.src.ResourceID, "usr_bootstrap", input, policy, buildEnqueue, destinationEvent("compose-build")); err != nil {
		t.Fatal(err)
	}
	b, a := f.activate(t, "compose-build")
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "compose-source-receipt", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "compose-daemon")
	if err != nil {
		t.Fatal(err)
	}
	source := domain.HostingStackSource{Original: "services:\n  web:\n    build: ../app\n    image: local-app:v1\n    volumes: [uploads:/data]\n  worker:\n    image: local-app:v1\n  db:\n    image: postgres:17\nvolumes:\n  uploads: {}\n", PublicComponent: "web", ContainerPort: 8080}
	definition, err := domain.ParseHostingGitCompose(source, src.Settings)
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	artifact.BuildID = domain.HostingComposeBuildID(b.ID, "web")
	result := domain.HostingBuildComposeResult{SourceReceiptHash: domain.HostingDocumentHash(b.SourceReceipt), Source: source, Definition: definition, Services: []domain.HostingBuildComposeService{{Component: "web", Artifact: artifact}}}
	result.Hash = domain.HostingDocumentHash(result)
	wrong := a
	wrong.Fence += "old"
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, wrong, "wrong-lease", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &result}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale lease stored Compose result", err)
	}
	bad := result
	bad.Services = nil
	bad.Hash = ""
	bad.Hash = domain.HostingDocumentHash(bad)
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "bad-coverage", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &bad}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("incomplete components stored", err)
	}
	completed, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "compose-complete", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "compose-complete", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &result})
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(completed) {
		t.Fatal("completion receipt replay changed", err)
	}
	if completed.Artifact != nil || completed.ComposeResult == nil || completed.ComposeResult.Hash != result.Hash {
		t.Fatal("build result union changed")
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_compose_results SET body=body WHERE build_id=$1`, b.ID); err == nil {
		t.Fatal("prepared result was mutable")
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "compose-complete", b.Revision, store.HostingBuildCompletion{State: "failed", FailureCode: "source_failed"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("contradictory completion replay accepted", err)
	}
	failedInput := input
	failedInput.IdempotencyKey = "failed-next-source"
	if _, err = f.pg.RequestHostingBuild(ctx, "failed-next-source", f.src.ResourceID, "usr_bootstrap", failedInput, policy, buildEnqueue, destinationEvent("failed-next-source")); err != nil {
		t.Fatal(err)
	}
	failed, failedAuthority := f.activate(t, "failed-next-source")
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, failedAuthority, "failed-fetch", failed.Revision, store.HostingBuildCompletion{State: "failed", FailureCode: "source_failed"}); err != nil {
		t.Fatal(err)
	}
	retained, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", b.ID)
	if err != nil || retained.ComposeResult == nil || retained.ComposeResult.Hash != result.Hash {
		t.Fatal("failed fetch replaced successful result", err)
	}
	var operationCount int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_operations WHERE resource_id=$1`, resource.ID).Scan(&operationCount); err != nil || operationCount != 0 {
		t.Fatal("source failure mutated a workload", err)
	}
	startBuildController(t, f)
	in := localBuildDeploymentInput(b.ID)
	in.ExpectedGeneration = resource.Generation
	in.ExpectedSourceGeneration = f.src.Generation
	in.ContainerPort = 8080
	in.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "git-compose-op", resource.ID, "usr_bootstrap", in, destinationEvent("git-compose-deploy"))
	if err != nil {
		t.Fatal(err)
	}
	if op.ComposeResult == nil || op.ComposeResult.Hash != result.Hash || op.StackSource == nil || op.Snapshot.GitCompose.BuildID != b.ID || op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		t.Fatal("prepared result lost during admission")
	}
	replayOp, err := f.pg.RequestHostingBuildDeployment(ctx, "ignored-git-compose", resource.ID, "usr_bootstrap", in, destinationEvent("git-compose-replay"))
	if err != nil || replayOp.ID != op.ID {
		t.Fatal("deployment replay changed", err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err = f.pg.HostingRunnerPlan(ctx, f.runner, authority)
	if err != nil || op.ComposeResult == nil {
		t.Fatal("runner did not receive selected result", err)
	}
	images := []domain.HostingStackImage{}
	for _, service := range definition.Services {
		if local, ok := domain.HostingComposeBuildArtifact(result, service.Name); ok {
			release, e := domain.NewHostingLocalBuildRelease(local)
			if e != nil {
				t.Fatal(e)
			}
			images = append(images, domain.HostingStackImage{Component: service.Name, Release: release})
		} else {
			release := domain.HostingRelease{CanonicalImage: "docker.io/library/postgres@sha256:" + strings.Repeat("d", 64), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "amd64", DaemonID: b.DaemonID}
			release.Hash = domain.HostingDocumentHash(release)
			images = append(images, domain.HostingStackImage{Component: service.Name, Release: release})
		}
	}
	release := domain.HostingRelease{DaemonID: b.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: "git-stack-prepared", Release: release, StackImages: images})
	if err != nil {
		t.Fatal(err)
	}
	if op.Plan == nil || op.Plan.Stack == nil || len(op.Plan.Stack.Images) != 3 || len(op.Plan.Stack.Volumes) != 1 {
		t.Fatal("mixed stack plan incomplete")
	}
	changedSource := f.src
	changedSource.Generation++
	changedSource.RequestedRef = "next-branch"
	changedSource, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, changedSource, destinationEvent("desired-source-edited"))
	if err != nil {
		t.Fatal(err)
	}
	retainedOperation, err := f.pg.GetHostingOperation(ctx, op.ID)
	if err != nil || retainedOperation.ComposeResult == nil || retainedOperation.ComposeResult.Hash != result.Hash || retainedOperation.StackSource == nil {
		t.Fatal("historical selected source lost after desired edit", err)
	}
	edited, err := f.pg.GetResource(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := in
	stale.IdempotencyKey = "old-build-new-source"
	stale.ExpectedGeneration = edited.Generation
	stale.ExpectedSourceGeneration = changedSource.Generation
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "stale-selected-build", resource.ID, "usr_bootstrap", stale, destinationEvent("stale-selected-build")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("fresh deployment admitted historical build after source edit", err)
	}
	current, err := f.pg.GetResource(ctx, resource.ID)
	if err != nil || current.Source.Kind != "git" || current.Source.Git.Generation != changedSource.Generation {
		t.Fatal("Git resource was converted", err)
	}
}
