package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/api"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
	"nerocd/web"
)

// This follows the same prepare/apply/settle sequence as the stack authority
// fixture, then exercises actual HTTP decoders and application authorization.
func TestPostgresComposeDatabaseBackupAndImportAuthority(t *testing.T) {
	pg, db, base, server, destination := hostingExecutionFixture(t)
	ctx := t.Context()
	caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseBackupComponentCapability, domain.HostingStackLifecycleCapability}
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, server.RunnerID, caps); err != nil {
		t.Fatal(err)
	}
	deploy := func(name string) (domain.Resource, domain.HostingOperation) {
		return deployBackupComponentStack(t, pg, base, server, destination, name)
	}
	source, sourceOp := deploy("a")
	target, targetOp := deploy("b")
	originalScheduleInput := domain.HostingDatabaseBackupScheduleInput{RequestID: "schedule_before_stop", ExpectedResourceGeneration: source.Generation, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: false}
	originalSchedule, err := pg.SaveDatabaseBackupSchedule(ctx, "dbs_before_stop", source.ID, "usr_bootstrap", "", originalScheduleInput)
	if err != nil {
		t.Fatal("component schedule before stop", err)
	}
	// Match the qualification's source after an explicit whole-stack Stop/Start.
	stop, err := pg.RequestHostingStackStop(ctx, "hop_backup_source_stop", source.ID, "usr_bootstrap", domain.HostingStackStopInput{IdempotencyKey: "backup-source-stop", ExpectedDeployedOperationID: sourceOp.ID, ExpectedGeneration: source.Generation}, destinationEvent("backup_source_stop"))
	if err != nil {
		t.Fatal(err)
	}
	stop = awaitHostingDispatch(t, pg, stop.ID)
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: stop.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, server.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	stoppedState := *sourceOp.Result.Stack
	stoppedState.Components = []domain.HostingStackComponent{}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "backup_source_stopped", Result: domain.HostingResult{Outcome: "succeeded", StackStopped: true, DaemonID: sourceOp.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &stoppedState}}); err != nil {
		t.Fatal(err)
	}
	if listed, e := pg.ListDatabaseBackupSchedules(ctx, "usr_bootstrap", source.ID); e != nil || len(listed) != 1 || listed[0].Component != "postgresql" {
		t.Fatal("stopped schedule list", e)
	}
	if _, e := pg.ListDatabaseBackupOccurrences(ctx, "usr_bootstrap", source.ID, originalSchedule.ID); e != nil {
		t.Fatal("stopped schedule history", e)
	}
	if replay, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", source.ID, "usr_bootstrap", "", originalScheduleInput); e != nil || replay.ID != originalSchedule.ID {
		t.Fatal("stopped schedule replay", e)
	}
	disableSchedule := originalScheduleInput
	disableSchedule.RequestID = "disable_stopped"
	disableSchedule.ExpectedVersion = 1
	disableSchedule.ExpectedDeployedOperationID = ""
	if disabled, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", source.ID, "usr_bootstrap", originalSchedule.ID, disableSchedule); e != nil || disabled.Enabled || disabled.Revision != 2 {
		t.Fatal("stopped schedule disable", e)
	}
	enabledSchedule := disableSchedule
	enabledSchedule.RequestID = "enable_stopped"
	enabledSchedule.ExpectedVersion = 2
	enabledSchedule.Enabled = true
	enabledSchedule.ExpectedDeployedOperationID = sourceOp.ID
	if _, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", source.ID, "usr_bootstrap", originalSchedule.ID, enabledSchedule); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stopped enabled schedule admitted", e)
	}
	startInput := hostingDeploymentRequest()
	startInput.IdempotencyKey = "backup-source-start"
	startInput.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true, ExpectedStoppedOperationID: stop.ID}
	start, err := pg.RequestHostingDeployment(ctx, "hop_backup_source_start", source.ID, "usr_bootstrap", startInput, destinationEvent("backup_source_start"))
	if err != nil {
		t.Fatal(err)
	}
	start = awaitHostingDispatch(t, pg, start.ID)
	claim, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a = domain.HostingAuthority{OperationID: start.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, server.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	prepared, err := pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "backup_source_start_prepared", Release: *sourceOp.Release, StackImages: sourceOp.Plan.Stack.Images})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	startedState := *sourceOp.Result.Stack
	startedState.Components = append([]domain.HostingStackComponent(nil), startedState.Components...)
	for i := range startedState.Components {
		startedState.Components[i].OperationID = start.ID
		startedState.Components[i].ContainerID = strings.Repeat("c", 63) + fmt.Sprint(i+1)
	}
	sourceOp, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "backup_source_started", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: sourceOp.Release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &startedState}})
	if err != nil {
		t.Fatal(err)
	}
	if replay, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", source.ID, "usr_bootstrap", "", originalScheduleInput); e != nil || replay.ID != originalSchedule.ID {
		t.Fatal("schedule replay after redeploy", e)
	}
	enabledSchedule.RequestID = "enable_stale_deployment"
	if _, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", source.ID, "usr_bootstrap", originalSchedule.ID, enabledSchedule); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale schedule deployment accepted", e)
	}
	service := proxyTokenApplication(t, pg)
	session, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	const browserOrigin = "https://127.0.0.1:18190"
	handler := api.NewServerWithConfig(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), api.ServerConfig{PublicOrigin: browserOrigin})
	runnerServer := httptest.NewServer(handler)
	t.Cleanup(runnerServer.Close)
	runnerClient := runnerclient.New(runnerServer.URL, "image-hosting-fixture-token")
	runnerContext := runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal("stack setup", err)
		}
		if body == nil {
			raw = nil
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
		request.AddCookie(&http.Cookie{Name: "nerocd_session", Value: session.Token})
		request.Header.Set("Origin", browserOrigin)
		request.Header.Set("X-NeroCD-CSRF", "1")
		request.Header.Set("Accept", "application/json")
		request.Header.Set("X-Request-ID", strings.Repeat("a", 32))
		request.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request)
		if out.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, out.Code, want, out.Body.String())
		}
		return out.Body.Bytes()
	}
	path := "/api/v1/resources/" + source.ID + "/database-backups"
	in := domain.HostingDatabaseBackupInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: source.Generation, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID}
	call(http.MethodGet, "/api/v1/resources/"+source.ID+"/database-backup-components", nil, 200)
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingDatabaseBackupComponentCapability); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, path, in, 409)
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingDatabaseBackupComponentCapability); err != nil {
		t.Fatal(err)
	}
	stale := in
	stale.ExpectedDeployedOperationID = "hop_stale"
	call(http.MethodPost, path, stale, 409)
	var backup domain.HostingDatabaseBackupOperation
	if err = json.Unmarshal(call(http.MethodPost, path, in, 202), &backup); err != nil {
		t.Fatal(err)
	}
	var retry domain.HostingDatabaseBackupOperation
	if err = json.Unmarshal(call(http.MethodPost, path, in, 202), &retry); err != nil || retry.ID != backup.ID {
		t.Fatal("backup idempotency", err)
	}
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	settle := func(operation domain.HostingDatabaseBackupOperation, restore bool, phase string) {
		t.Helper()
		if err := pg.PrepareDatabaseBackup(ctx, operation.ID); err != nil {
			t.Fatal("prepare", err)
		}
		if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingDatabaseBackupComponentCapability); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("old agent claimed component task", err)
		}
		if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingDatabaseBackupComponentCapability); err != nil {
			t.Fatal(err)
		}
		claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		a := domain.HostingAuthority{OperationID: operation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		p, err := runnerClient.HostingDatabaseBackupPlan(runnerContext, a)
		if err != nil {
			t.Fatal("plan", err)
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var wire any
		if err = json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if err = document.Components.Schemas["HostingDatabaseBackupPlan"].Value.VisitJSON(wire); err != nil {
			t.Fatal("component runner wire contract", err)
		}
		mutation := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent_" + operation.ID, PlanHash: p.Hash, Phase: "intent", ComponentVersion: domain.HostingDatabaseBackupComponentVersion}
		if restore {
			for _, c := range p.Target.Stack.Components {
				if c.Component != p.Component.Target {
					mutation.Writers = append(mutation.Writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ContainerID, OperationID: c.OperationID, ImageID: c.ImageID, PlanHash: p.Target.PlanHash, ReleaseHash: p.Target.Release.Hash, Running: c.Running})
				}
			}
		}
		if _, err = runnerClient.HostingDatabaseBackupMutation(runnerContext, mutation); err != nil {
			t.Fatal("intent", err)
		}
		competing := hostingDeploymentRequest()
		competing.IdempotencyKey = "competing-component-" + operation.ID
		competing.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
		resource := source.ID
		if restore {
			resource = target.ID
		}
		if _, err = pg.RequestHostingDeployment(ctx, "hop_competing_"+operation.ID, resource, "usr_bootstrap", competing, destinationEvent("competing_"+operation.ID)); !errors.Is(err, store.ErrConflict) {
			t.Fatal("competing deployment", err)
		}
		mutation.Phase = phase
		mutation.ReceiptID = phase + "_" + operation.ID
		mutation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("c", 64), Bytes: 65537, Format: "postgresql_custom"}
		mutation.WritersResumed = true
		if phase == "uncertain" {
			mutation.Blob = nil
			mutation.WritersResumed = false
			mutation.FailureCode = "execution_failed"
		}
		result, err := runnerClient.HostingDatabaseBackupMutation(runnerContext, mutation)
		if err != nil {
			t.Fatal("settle", err)
		}
		if phase == "uncertain" && !result.OwnershipHeld {
			t.Fatal("uncertain import released target")
		}
		if _, err = runnerClient.HostingDatabaseBackupMutation(runnerContext, mutation); err != nil {
			t.Fatal("historical result replay", err)
		}
	}
	settle(backup, false, "complete")
	restore := domain.HostingDatabaseBackupRestoreInput{RequestID: "component-import", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: target.ID, Component: "postgresql", ExpectedDeployedOperationID: targetOp.ID, ExpectedResourceGeneration: target.Generation, AcknowledgeReplacement: true}}
	invalid := restore
	invalid.ComponentTarget = &domain.HostingDatabaseBackupComponentTarget{ResourceID: source.ID, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID, ExpectedResourceGeneration: source.Generation, AcknowledgeReplacement: true}
	call(http.MethodPost, "/api/v1/database-backups/"+backup.ID+"/restores", invalid, 409)
	invalid = restore
	staleTarget := *restore.ComponentTarget
	staleTarget.ExpectedResourceGeneration++
	invalid.ComponentTarget = &staleTarget
	call(http.MethodPost, "/api/v1/database-backups/"+backup.ID+"/restores", invalid, 409)
	if _, err = pg.RequestDatabaseBackupRestore(ctx, "unauthorized_import", backup.ID, "usr_unknown", restore, domain.HostingDatabaseCreateInput{}, nil, destinationEvent("unauthorized_import")); err == nil {
		t.Fatal("unauthorized import admitted")
	}
	var imported domain.HostingDatabaseBackupOperation
	if err = json.Unmarshal(call(http.MethodPost, "/api/v1/database-backups/"+backup.ID+"/restores", restore, 202), &imported); err != nil {
		t.Fatal(err)
	}
	settle(imported, true, "complete")
	restore.RequestID = "uncertain-component-import"
	if err = json.Unmarshal(call(http.MethodPost, "/api/v1/database-backups/"+backup.ID+"/restores", restore, 202), &imported); err != nil {
		t.Fatal(err)
	}
	settle(imported, true, "uncertain")
	input := hostingDeploymentRequest()
	input.IdempotencyKey = "after-uncertain-import"
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	if _, err = pg.RequestHostingDeployment(ctx, "hop_after_uncertain_import", target.ID, "usr_bootstrap", input, destinationEvent("after_uncertain_import")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("uncertain import lost exclusive ownership", err)
	}
}

// deployBackupComponentStack shares the existing settled stack fixture with schedule tests.
func deployBackupComponentStack(t *testing.T, pg *store.PostgresStore, base domain.Resource, server domain.Server, destination domain.Destination, name string, daemon ...string) (domain.Resource, domain.HostingOperation) {
	ctx := t.Context()
	daemonID := "daemon_component_backup"
	if len(daemon) > 0 {
		daemonID = daemon[0]
	}
	sourceText := "services:\n  app:\n    image: nginx:alpine\n  postgresql:\n    image: postgres:16-alpine\n    environment:\n      POSTGRES_USER: nerocd_fixture\n      POSTGRES_DB: nerocd_fixture\n    volumes:\n      - db:/var/lib/postgresql/data\nvolumes:\n  db:\n"

	t.Helper()
	r := base
	r.ID = "res_component_" + name
	r.Name = "Component " + name
	r.Source = domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: sourceText, PublicComponent: "app", ContainerPort: 80}}
	var err error
	r, err = pg.CreateResource(ctx, r, store.WithHostingComposeCreate(strings.Repeat(name, 32), domain.HostingDocumentHash(r)), store.WithAudit(destinationEvent("component_create_"+name)))
	if err != nil {
		t.Fatal("stack setup", err)
	}
	_, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_component_" + name, ResourceID: r.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("component_bind_"+name))
	if err != nil {
		t.Fatal("stack setup", err)
	}
	input := hostingDeploymentRequest()
	input.IdempotencyKey = "component-deploy-" + name
	if name == "b" {
		input.HostPort++
	}
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	op, err := pg.RequestHostingDeployment(ctx, "hop_component_"+name, r.ID, "usr_bootstrap", input, destinationEvent("component_deploy_"+name))
	if err != nil {
		t.Fatal("stack setup", err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal("stack setup", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, server.RunnerID, a); err != nil {
		t.Fatal("stack setup", err)
	}
	var images []domain.HostingStackImage
	for i, component := range []string{"app", "postgresql"} {
		repo := "docker.io/library/nginx"
		if i == 1 {
			repo = "docker.io/library/postgres"
		}
		release := domain.HostingRelease{CanonicalImage: repo + "@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat(fmt.Sprint(i+1), 64), OS: "linux", Architecture: "amd64", DaemonID: daemonID}
		release.Hash = domain.HostingDocumentHash(release)
		images = append(images, domain.HostingStackImage{Component: component, Release: release})
	}
	release := domain.HostingRelease{DaemonID: daemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	prepared, err := pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_component_" + name, Release: release, StackImages: images})
	if err != nil {
		t.Fatal("stack setup", err)
	}
	volumes := append([]domain.HostingStackVolume(nil), prepared.Plan.Stack.Volumes...)
	for i := range volumes {
		volumes[i].CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
		t.Fatal("stack setup", err)
	}
	if _, err = pg.HostingStackVolumes(ctx, server.RunnerID, domain.HostingStackVolumeRequest{HostingAuthority: a, PlanHash: prepared.Plan.Hash, Volumes: volumes}); err != nil {
		t.Fatal("stack setup", err)
	}
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "app", NetworkID: strings.Repeat(name, 64), Volumes: volumes, Components: []domain.HostingStackComponent{{Component: "app", ContainerID: strings.Repeat(name, 63) + "1", ImageID: images[0].Release.ImageID, OperationID: op.ID, Running: true}, {Component: "postgresql", ContainerID: strings.Repeat(name, 63) + "2", ImageID: images[1].Release.ImageID, OperationID: op.ID, Running: true}}}
	op, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_component_" + name, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}})
	if err != nil {
		t.Fatal("stack setup", err)
	}
	return r, op
}
