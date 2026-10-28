package store_test

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/store/sqlcgen"
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
	src.Settings = domain.HostingBuildSettings{BuildVariables: []domain.HostingBuildVariable{{Name: "ARG", Value: "configured"}}, Strategy: "compose", ComposeFile: "deploy/compose.yaml", PublicComponent: "web", ContainerPort: 8080}
	var err error
	f.src, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("compose-source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || $1::text[] WHERE id=$2`, []string{domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability}, f.runner); err != nil {
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
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingBuild(ctx, "old-runner-compose", f.src.ResourceID, "usr_bootstrap", input, policy, buildEnqueue, destinationEvent("old-runner-compose")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("runner without args capability admitted", err)
	}
	var rejectedCount int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds WHERE id='old-runner-compose'`).Scan(&rejectedCount); err != nil || rejectedCount != 0 {
		t.Fatal("denied build persisted effects", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingBuild(ctx, "compose-build", f.src.ResourceID, "usr_bootstrap", input, policy, buildEnqueue, destinationEvent("compose-build")); err != nil {
		t.Fatal(err)
	}
	b, err := f.pg.DispatchHostingBuild(ctx, "compose-build", buildDispatch, destinationEvent("dispatch-compose"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	denied, claimErr := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if claimErr == nil && denied.Run.ID == b.RunID {
		t.Fatal("queued Compose work reached args-incapable runner")
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	buildClaim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || buildClaim.Run.ID != b.RunID {
		t.Fatal("capable runner could not buildClaim Compose build", err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: buildClaim.Lease.ID, Attempt: buildClaim.Lease.Attempt, Fence: buildClaim.Lease.Fence}
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, a, b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	status, statusErr := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if statusErr != nil || status.MayExecute {
		t.Fatal("source authorization ignored removed args capability", statusErr)
	}
	if _, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "denied-source", sourceBuildReceipt(b)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("source effect accepted after args capability removal", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "compose-source-receipt", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "compose-daemon")
	if err != nil {
		t.Fatal(err)
	}
	source := domain.HostingStackSource{Original: "services:\n  web:\n    build: ../app\n    image: local-app:v1\n    volumes: [uploads:/data]\n  worker:\n    image: local-app:v1\n  db:\n    image: postgres:17\nvolumes:\n  uploads: {}\n", PublicComponent: "web", ContainerPort: 8080}
	source.Original = strings.Replace(source.Original, "    build: ../app\n", "    build:\n      context: ../app\n      args: {ARG: from-source, FROM: '${ARG:?required}'}\n", 1)
	definition, err := domain.ParseHostingGitCompose(source, src.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot.Source.Settings.BuildVariables[0].Value != "configured" {
		t.Fatal("build variables snapshot changed")
	}
	for _, service := range definition.Services {
		if service.Name == "web" && (service.Build.Args["ARG"] != "configured" || service.Build.Args["FROM"] != "configured") {
			t.Fatal("configured CLI precedence was not persisted")
		}
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
	for _, field := range []string{"daemon", "source", "input", "child", "coverage"} {
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var corrupt domain.HostingBuildComposeResult
		if e := json.Unmarshal(raw, &corrupt); e != nil {
			t.Fatal(e)
		}
		switch field {
		case "daemon":
			corrupt.Services[0].Artifact.DaemonID = "wrong-daemon"
		case "source":
			corrupt.Services[0].Artifact.SourceID = "wrong-source"
		case "input":
			corrupt.Services[0].Artifact.InputID = "wrong-input"
		case "child":
			corrupt.Services[0].Artifact.BuildID = b.ID
		case "coverage":
			corrupt.Services = nil
		}
		corrupt.Hash = ""
		corrupt.Hash = domain.HostingDocumentHash(corrupt)
		raw, marshalErr = json.Marshal(corrupt)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		e := sqlcgen.New(f.db).InsertHostingBuildComposeResult(ctx, sqlcgen.InsertHostingBuildComposeResultParams{BuildID: b.ID, Body: raw, BodyHash: corrupt.Hash, DefinitionHash: domain.HostingDocumentHash(corrupt.Definition)})
		var pgErr *pgconn.PgError
		if !errors.As(e, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("direct %s artifact result was not rejected by SQL scope: %v", field, e)
		}
	}
	completed, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "compose-complete", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &result})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
		t.Fatal(err)
	}
	replayBuild, replayErr := f.pg.RequestHostingBuild(ctx, "ignored-retry-build", f.src.ResourceID, "usr_bootstrap", input, policy, buildEnqueue, destinationEvent("retry-with-old-runner"))
	if replayErr != nil || replayBuild.ID != b.ID {
		t.Fatal("authorized build retry lost immutable receipt", replayErr)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingGitComposeArgsCapability); err != nil {
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
	for _, corruption := range []string{"destination", "server", "definition_hash", "owner_registry", "alias_registry"} {
		t.Run(corruption, func(t *testing.T) {
			tx, e := f.db.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			q := sqlcgen.New(tx)
			id := "corrupt-" + corruption
			snapshot := op.Snapshot
			switch corruption {
			case "destination":
				snapshot.DestinationID = "wrong-destination"
			case "server":
				snapshot.ServerID = "wrong-server"
			case "definition_hash":
				snapshot.Stack.Hash = "sha256:" + strings.Repeat("f", 64)
			}
			_, e = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: op.ProjectID, ResourceID: resource.ID, BindingID: op.BindingID, IdempotencyKey: id, RequestHash: domain.HostingDocumentHash(id), CreatedBy: "usr_bootstrap"})
			if e == nil {
				raw, marshalErr := json.Marshal(snapshot)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				_, e = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: raw, BodyHash: domain.HostingDocumentHash(snapshot)})
			}
			if e == nil && (corruption == "owner_registry" || corruption == "alias_registry") {
				encoded, marshalErr := json.Marshal(op.Plan)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var plan domain.HostingApplyPlan
				if e = json.Unmarshal(encoded, &plan); e != nil {
					t.Fatal(e)
				}
				selected := "web"
				if corruption == "alias_registry" {
					selected = "worker"
				}
				for i := range plan.Stack.Images {
					if plan.Stack.Images[i].Component == selected {
						r := domain.HostingRelease{CanonicalImage: "docker.io/library/local-app@sha256:" + strings.Repeat("d", 64), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "amd64", DaemonID: b.DaemonID}
						r.Hash = domain.HostingDocumentHash(r)
						plan.Stack.Images[i].Release = r
					}
				}
				release := *op.Release
				release.Stack.Hash = domain.HostingDocumentHash(plan.Stack.Images)
				release.Hash = ""
				release.Hash = domain.HostingDocumentHash(release)
				plan.Release = release
				plan.Hash = ""
				plan.Hash = domain.HostingDocumentHash(plan)
				raw, marshalErr := json.Marshal(release)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				_, e = q.CreateHostingRelease(ctx, sqlcgen.CreateHostingReleaseParams{OperationID: id, Body: raw, BodyHash: domain.HostingDocumentHash(release)})
				if e == nil {
					raw, marshalErr = json.Marshal(plan)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					_, e = q.CreateHostingApplyPlan(ctx, sqlcgen.CreateHostingApplyPlanParams{OperationID: id, Body: raw, BodyHash: domain.HostingDocumentHash(plan)})
				}
			}
			if e == nil {
				e = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			var pgErr *pgconn.PgError
			if !errors.As(e, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("direct corrupted %s stack was not rejected by invariant: %v", corruption, e)
			}
		})
	}
	changedSource := f.src
	changedSource.Generation++
	changedSource.Settings = changedSource.Settings.Clone()
	changedSource.Settings.BuildVariables[0].Value = "next-build-value"
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
