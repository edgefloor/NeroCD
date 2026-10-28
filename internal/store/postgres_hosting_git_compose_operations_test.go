package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func settledGitComposeOperationsFixture(t *testing.T, customize ...func(*domain.HostingStackSource)) (buildStoreFixture, domain.HostingOperation, domain.HostingBuildDeploymentInput) {
	t.Helper()
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
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || $1::text[] WHERE id=$2`, []string{domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability, domain.HostingStackLifecycleCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingDatabaseBackupComponentCapability, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.HostingComposeStorageArchiveCapability, domain.HostingOwnedStorageCapability}, f.runner); err != nil {
		t.Fatal(err)
	}
	resource, err := f.pg.GetResource(ctx, f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingBuildInput{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: f.src.Generation, IdempotencyKey: "compose-build"}
	policy := buildPolicy()
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
	source := domain.HostingStackSource{Original: "services:\n  web:\n    build: ../app\n    image: local-app:v1\n    volumes: [uploads:/data]\n  worker:\n    image: local-app:v1\n  db:\n    image: postgres:16-alpine\n    environment:\n      POSTGRES_USER: nerocd_fixture\n      POSTGRES_DB: nerocd_fixture\n    volumes: [db:/var/lib/postgresql/data]\nvolumes:\n  uploads: {}\n  db: {}\n", PublicComponent: "web", ContainerPort: 8080}
	for _, change := range customize {
		change(&source)
	}
	definition, err := domain.ParseHostingGitCompose(source, src.Settings)
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	artifact.BuildID = domain.HostingComposeBuildID(b.ID, "web")
	result := domain.HostingBuildComposeResult{SourceReceiptHash: domain.HostingDocumentHash(b.SourceReceipt), Source: source, Definition: definition, Services: []domain.HostingBuildComposeService{{Component: "web", Artifact: artifact}}}
	result.Hash = domain.HostingDocumentHash(result)
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "compose-complete", b.Revision, store.HostingBuildCompletion{State: "succeeded", ComposeResult: &result}); err != nil {
		t.Fatal(err)
	}
	helper := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: "sha256:" + strings.Repeat("d", 64), ExecutableSHA256: "sha256:" + strings.Repeat("e", 64), SourceCommit: strings.Repeat("f", 40), OS: "linux", Architecture: "arm64", DaemonID: b.DaemonID}
	helper.Hash = domain.HostingDocumentHash(helper)
	if err = f.pg.ConfigureStorageArchive(&helper, domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 19, FileBytes: 1 << 18, Members: 1000, Depth: 32, PathBytes: 255}); err != nil {
		t.Fatal(err)
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
	if op.Plan == nil || op.Plan.Stack == nil || len(op.Plan.Stack.Images) != 3 || len(op.Plan.Stack.Volumes) != 2 {
		t.Fatal("mixed stack plan incomplete")
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, authority); err != nil {
		t.Fatal(err)
	}
	volumes := append([]domain.HostingStackVolume(nil), op.Plan.Stack.Volumes...)
	for i := range volumes {
		volumes[i].CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err = f.pg.HostingStackVolumes(ctx, f.runner, domain.HostingStackVolumeRequest{HostingAuthority: authority, PlanHash: op.Plan.Hash, Volumes: volumes}); err != nil {
		t.Fatal(err)
	}
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "web", NetworkID: strings.Repeat("c", 64), Volumes: volumes, Components: []domain.HostingStackComponent{}}
	for i, image := range images {
		state.Components = append(state.Components, domain.HostingStackComponent{Component: image.Component, ContainerID: strings.Repeat("d", 63) + fmt.Sprint(i+1), ImageID: image.Release.ImageID, OperationID: op.ID, Running: true})
	}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "git-compose-settled", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash, Stack: &state}})
	if err != nil {
		t.Fatal(err)
	}
	return f, op, in
}

func editGitComposeDesiredSource(t *testing.T, f buildStoreFixture) domain.Resource {
	t.Helper()
	source := f.src
	source.Generation++
	source.RequestedRef = "new-desired-ref"
	if _, err := configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", f.src.Generation, source, destinationEvent("git-desired-edit")); err != nil {
		t.Fatal(err)
	}
	r, err := f.pg.GetResource(t.Context(), source.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresGitComposeHistoricalLifecycle(t *testing.T) {
	f, deployed, input := settledGitComposeOperationsFixture(t)
	ctx := t.Context()
	r := editGitComposeDesiredSource(t, f)
	policy := buildPolicy()
	policy.GitCompose = true
	failedInput := domain.HostingBuildInput{ExpectedResourceGeneration: r.Generation, ExpectedSourceGeneration: r.Source.Git.Generation, IdempotencyKey: "failed-new-desired-build"}
	if _, err := f.pg.RequestHostingBuild(ctx, "failed-new-desired-build", r.ID, "usr_bootstrap", failedInput, policy, buildEnqueue, destinationEvent("failed-new-desired-build")); err != nil {
		t.Fatal(err)
	}
	failed, failedAuthority := f.activate(t, "failed-new-desired-build")
	if _, err := f.pg.CompleteHostingBuild(ctx, f.runner, failedAuthority, "failed-new-desired-source", failed.Revision, store.HostingBuildCompletion{State: "failed", FailureCode: "source_failed"}); err != nil {
		t.Fatal(err)
	}
	life, err := f.pg.HostingStackLifecycle(ctx, "usr_bootstrap", r.ID)
	if err != nil || life.State != "running" || life.DeployedOperationID != deployed.ID {
		t.Fatal("desired edit hid running stack", life, err)
	}
	targets, err := f.pg.RuntimeLogTargets(ctx, "usr_bootstrap", r.ID)
	if err != nil || targets.OperationID != deployed.ID || len(targets.Components) != 3 {
		t.Fatal("desired edit hid logs", targets, err)
	}
	if _, err = f.pg.RuntimeLogTargets(ctx, "usr_unknown", r.ID); err == nil {
		t.Fatal("unauthorized logs admitted")
	}
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "git-compose-viewer", ProjectID: r.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	stopInput := domain.HostingStackStopInput{IdempotencyKey: "stop-historical", ExpectedGeneration: r.Generation, ExpectedDeployedOperationID: deployed.ID}
	if _, err = f.pg.RequestHostingStackStop(ctx, "unauthorized-stop", r.ID, "usr_unknown", stopInput, destinationEvent("bad-stop")); err == nil {
		t.Fatal("unauthorized Stop admitted")
	}
	if _, err = f.pg.RequestHostingStackStop(ctx, "viewer-stop", r.ID, "usr_viewer", stopInput, destinationEvent("viewer-stop")); err == nil {
		t.Fatal("viewer Stop admitted")
	}
	foreign := stopInput
	foreign.ExpectedDeployedOperationID = "foreign-operation"
	if _, err = f.pg.RequestHostingStackStop(ctx, "foreign-stop", r.ID, "usr_bootstrap", foreign, destinationEvent("foreign-stop")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("foreign deployment admitted", err)
	}
	dbScheduleInput := domain.HostingDatabaseBackupScheduleInput{RequestID: "git-stopped-db-schedule", ExpectedResourceGeneration: r.Generation, Component: "db", ExpectedDeployedOperationID: deployed.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: false}
	dbSchedule, err := f.pg.SaveDatabaseBackupSchedule(ctx, "git-stopped-db-schedule", r.ID, "usr_bootstrap", "", dbScheduleInput)
	if err != nil {
		t.Fatal(err)
	}
	volumeScheduleInput := domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat("5", 32), ExpectedResourceGeneration: r.Generation, Volume: "uploads", ExpectedDeployedOperationID: deployed.ID, AcknowledgeInterruption: true, Frequency: "every_minute", Timezone: "UTC", Enabled: false}
	volumeSchedule, err := f.pg.SaveStorageBackupSchedule(ctx, "git-stopped-volume-schedule", r.ID, "usr_bootstrap", "", volumeScheduleInput)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := f.pg.RequestHostingStackStop(ctx, "historical-stop", r.ID, "usr_bootstrap", stopInput, destinationEvent("historical-stop"))
	if err != nil {
		t.Fatal("historical Stop", err)
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
	stopped := *deployed.Result.Stack
	stopped.Components = []domain.HostingStackComponent{}
	if _, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "git-stack-stopped", Result: domain.HostingResult{Outcome: "succeeded", StackStopped: true, DaemonID: deployed.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &stopped}}); err != nil {
		t.Fatal(err)
	}
	before, err := f.pg.HostingStackLifecycle(ctx, "usr_bootstrap", r.ID)
	if err != nil || before.State != "stopped" || before.StopOperationID != stop.ID {
		t.Fatal(before, err)
	}
	dbScheduleInput.RequestID = "disable-stopped-git-db"
	dbScheduleInput.ExpectedVersion = dbSchedule.Revision
	if _, err = f.pg.SaveDatabaseBackupSchedule(ctx, "unused", r.ID, "usr_bootstrap", dbSchedule.ID, dbScheduleInput); err != nil {
		t.Fatal("stopped Git DB schedule control", err)
	}
	volumeScheduleInput.RequestID = strings.Repeat("6", 32)
	volumeScheduleInput.ExpectedVersion = volumeSchedule.Revision
	if _, err = f.pg.SaveStorageBackupSchedule(ctx, "unused", r.ID, "usr_bootstrap", volumeSchedule.ID, volumeScheduleInput); err != nil {
		t.Fatal("stopped Git volume schedule control", err)
	}
	var volumesBefore string
	if err = f.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY name)::text,'[]') FROM hosting_compose_volumes v WHERE binding_id=$1`, deployed.BindingID).Scan(&volumesBefore); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "stale-git-start"
	input.ExpectedGeneration = r.Generation
	input.ExpectedSourceGeneration = r.Source.Git.Generation
	input.ManagedStack.ExpectedStoppedOperationID = stop.ID
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "stale-git-start", r.ID, "usr_bootstrap", input, destinationEvent("stale-start")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("fresh Start admitted stale selected build", err)
	}
	after, err := f.pg.HostingStackLifecycle(ctx, "usr_bootstrap", r.ID)
	if err != nil || after != before {
		t.Fatal("rejected Start changed stopped receipt", err)
	}
	var volumesAfter string
	if err = f.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY name)::text,'[]') FROM hosting_compose_volumes v WHERE binding_id=$1`, deployed.BindingID).Scan(&volumesAfter); err != nil || volumesBefore != volumesAfter {
		t.Fatal("rejected Start changed volumes", err)
	}
}

func TestPostgresGitComposeHistoricalBackupsAndSchedules(t *testing.T) {
	f, deployed, _ := settledGitComposeOperationsFixture(t)
	ctx := t.Context()
	r := editGitComposeDesiredSource(t, f)
	components, err := f.pg.DatabaseBackupComponents(ctx, "usr_bootstrap", r.ID)
	if err != nil || !components.Available || len(components.Components) != 1 || components.Components[0].Component != "db" {
		t.Fatal("historical database discovery", components, err)
	}
	volumes, err := f.pg.StorageBackupVolumes(ctx, "usr_bootstrap", r.ID)
	if err != nil || !volumes.Available || len(volumes.Volumes) != 2 {
		t.Fatal("historical volume discovery", volumes, err)
	}
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "git-compose-backup-viewer", ProjectID: r.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	other, err := f.pg.CreateProject(ctx, domain.Project{ID: "git-compose-other-project", Name: "Other project", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "git-compose-other-owner", ProjectID: other.ID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	dbInput := domain.HostingDatabaseBackupScheduleInput{RequestID: "git-db-schedule", ExpectedResourceGeneration: r.Generation, Component: "db", ExpectedDeployedOperationID: deployed.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
	if _, err = f.pg.SaveDatabaseBackupSchedule(ctx, "git-db-schedule", r.ID, "usr_unknown", "", dbInput); err == nil {
		t.Fatal("unauthorized schedule admitted")
	}
	if _, err = f.pg.SaveDatabaseBackupSchedule(ctx, "viewer-db-schedule", r.ID, "usr_viewer", "", dbInput); err == nil {
		t.Fatal("viewer schedule admitted")
	}
	schedule, err := f.pg.SaveDatabaseBackupSchedule(ctx, "git-db-schedule", r.ID, "usr_bootstrap", "", dbInput)
	if err != nil {
		t.Fatal("historical DB schedule", err)
	}
	dbInput.RequestID = "disable-git-db"
	dbInput.Enabled = false
	dbInput.ExpectedVersion = schedule.Revision
	if _, err = f.pg.SaveDatabaseBackupSchedule(ctx, "unused", r.ID, "usr_bootstrap", schedule.ID, dbInput); err != nil {
		t.Fatal("disable historical DB schedule", err)
	}
	volumeInput := domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat("1", 32), ExpectedResourceGeneration: r.Generation, Volume: "uploads", ExpectedDeployedOperationID: deployed.ID, AcknowledgeInterruption: true, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
	storageSchedule, err := f.pg.SaveStorageBackupSchedule(ctx, "git-storage-schedule", r.ID, "usr_bootstrap", "", volumeInput)
	if err != nil {
		t.Fatal("historical volume schedule", err)
	}
	volumeInput.RequestID = strings.Repeat("2", 32)
	volumeInput.Enabled = false
	volumeInput.ExpectedVersion = storageSchedule.Revision
	if _, err = f.pg.SaveStorageBackupSchedule(ctx, "unused", r.ID, "usr_bootstrap", storageSchedule.ID, volumeInput); err != nil {
		t.Fatal("disable historical volume schedule", err)
	}
	backupInput := domain.HostingDatabaseBackupInput{RequestID: "historical-db-backup", ExpectedResourceGeneration: r.Generation, Component: "db", ExpectedDeployedOperationID: deployed.ID}
	service := proxyTokenApplication(t, f.pg)
	owner := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	if _, err = service.RequestDatabaseBackup(owner, r.ID, backupInput); err != nil {
		t.Fatal("historical DB backup", err)
	}
}

func TestPostgresGitComposeHistoricalVolumeArchive(t *testing.T) {
	f, deployed, _ := settledGitComposeOperationsFixture(t)
	ctx := t.Context()
	r := editGitComposeDesiredSource(t, f)
	var err error
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "archive-git-viewer", ProjectID: r.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	healthy := domain.HostingHealthy{Snapshot: deployed.Snapshot, Release: *deployed.Release, OperationID: deployed.ID, PlanHash: deployed.Plan.Hash, Stack: deployed.Result.Stack}
	for _, field := range []string{"valid", "resource", "project", "binding", "build", "source_generation", "result_hash", "resource_generation", "runner", "daemon"} {
		t.Run("sql_origin_"+field, func(t *testing.T) {
			proof := healthy
			resource, project, binding := r.ID, r.ProjectID, deployed.BindingID
			switch field {
			case "resource":
				resource = "foreign-resource"
			case "project":
				project = "foreign-project"
			case "binding":
				binding = "foreign-binding"
			case "build":
				proof.Snapshot.GitCompose.BuildID = "foreign-build"
			case "source_generation":
				proof.Snapshot.GitCompose.SourceGeneration++
			case "result_hash":
				proof.Snapshot.GitCompose.ResultHash = "sha256:" + strings.Repeat("f", 64)
			case "resource_generation":
				proof.Snapshot.ResourceGeneration++
			case "runner":
				proof.Snapshot.RunnerID = "foreign-runner"
			case "daemon":
				proof.Release.DaemonID = "foreign-daemon"
			}
			raw, err := json.Marshal(proof)
			if err != nil {
				t.Fatal(err)
			}
			var admitted bool
			if err = f.db.QueryRow(ctx, `SELECT nerocd_hosting_git_stack_archive_origin($1,$2,$3,$4::jsonb)`, resource, project, binding, raw).Scan(&admitted); err != nil || admitted != (field == "valid") {
				t.Fatal("SQL origin proof", field, admitted, err)
			}
		})
	}
	archiveInput := domain.HostingStorageBackupInput{RequestID: strings.Repeat("3", 32), ExpectedResourceGeneration: r.Generation, Volume: "uploads", ExpectedDeployedOperationID: deployed.ID, AcknowledgeInterruption: true}
	if _, err = f.pg.RequestStorageBackup(ctx, "viewer-upload-backup", r.ID, "usr_viewer", archiveInput, destinationEvent("viewer-archive")); err == nil {
		t.Fatal("viewer archive admitted")
	}
	if _, err = f.pg.RequestStorageBackup(ctx, "historical-upload-backup", r.ID, "usr_unknown", archiveInput, destinationEvent("bad-archive")); err == nil {
		t.Fatal("unauthorized archive admitted")
	}
	service := proxyTokenApplication(t, f.pg)
	owner := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	if _, err = service.RequestStorageBackup(owner, r.ID, archiveInput); err != nil {
		t.Fatal("historical application archive", err)
	}
}

func TestPostgresOrdinaryGitRejectsStackOperations(t *testing.T) {
	f, build := successfulLocalBuildFixture(t)
	ctx := t.Context()
	startBuildController(t, f)
	settleLocalDeploymentMetadata(t, f, build, "ordinary-local-image", 21001)
	life, err := f.pg.HostingStackLifecycle(ctx, "usr_bootstrap", f.src.ResourceID)
	if err != nil || life.State != "unavailable" {
		t.Fatal("ordinary Git lifecycle", life, err)
	}
	in := domain.HostingStackStopInput{IdempotencyKey: "ordinary-git-stop", ExpectedGeneration: 1, ExpectedDeployedOperationID: "foreign"}
	if _, err = f.pg.RequestHostingStackStop(ctx, "ordinary-git-stop", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("ordinary-stop")); err == nil {
		t.Fatal("ordinary Git admitted Stop")
	}
	if _, err = f.pg.DatabaseBackupComponents(ctx, "usr_bootstrap", f.src.ResourceID); err != nil {
		t.Fatal("ordinary Git component discovery", err)
	}
	volumeIn := domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat("4", 32), ExpectedResourceGeneration: 1, Volume: "uploads", ExpectedDeployedOperationID: "foreign", AcknowledgeInterruption: true, Frequency: "every_minute", Timezone: "UTC", Enabled: false}
	if _, err = f.pg.SaveStorageBackupSchedule(ctx, "ordinary-git-volume", f.src.ResourceID, "usr_bootstrap", "", volumeIn); err == nil {
		t.Fatal("ordinary Git admitted volume schedule")
	}
}

func TestPostgresGitComposeHistoricalScheduledDispatch(t *testing.T) {
	for _, family := range []string{"database", "uploads"} {
		t.Run(family, func(t *testing.T) {
			f, deployed, _ := settledGitComposeOperationsFixture(t)
			ctx := t.Context()
			r := editGitComposeDesiredSource(t, f)
			service := proxyTokenApplication(t, f.pg)
			owner := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
			if family == "database" {
				in := domain.HostingDatabaseBackupScheduleInput{RequestID: "scheduled-git-db", ExpectedResourceGeneration: r.Generation, Component: "db", ExpectedDeployedOperationID: deployed.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
				schedule, err := service.SaveDatabaseBackupSchedule(owner, r.ID, "", in)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.pg.ReconcileDatabaseBackupSchedules(ctx); err != nil {
					t.Fatal(err)
				}
				occurrences, err := service.ListDatabaseBackupOccurrences(owner, r.ID, schedule.ID)
				if err != nil || len(occurrences) != 1 || occurrences[0].OperationID == "" {
					t.Fatal("database due dispatch", occurrences, err)
				}
				backup, err := f.pg.GetDatabaseBackup(ctx, "usr_bootstrap", occurrences[0].OperationID)
				if err != nil || backup.SourceDeployedOperationID != deployed.ID || backup.SourceComponent != "db" {
					t.Fatal("database source changed", backup, err)
				}
			} else {
				in := domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat("7", 32), ExpectedResourceGeneration: r.Generation, Volume: "uploads", ExpectedDeployedOperationID: deployed.ID, AcknowledgeInterruption: true, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
				schedule, err := service.SaveStorageBackupSchedule(owner, r.ID, "", in)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.pg.ReconcileStorageBackupSchedules(ctx); err != nil {
					t.Fatal(err)
				}
				occurrences, err := service.ListStorageBackupOccurrences(owner, r.ID, schedule.ID)
				if err != nil || len(occurrences) != 1 || occurrences[0].OperationID == "" {
					t.Fatal("volume due dispatch", occurrences, err)
				}
				op, err := f.pg.GetHostingOperation(ctx, occurrences[0].OperationID)
				if err != nil || op.StorageArchive == nil || op.StorageArchive.Plan.ComposeSource == nil || op.StorageArchive.Plan.ComposeSource.Healthy.OperationID != deployed.ID {
					t.Fatal("volume source changed", err)
				}
			}
		})
	}
}
