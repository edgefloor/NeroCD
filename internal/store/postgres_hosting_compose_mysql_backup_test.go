package store_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type composeMySQLBackupFixture struct {
	pg                 *store.PostgresStore
	db                 *pgxpool.Pool
	service            *app.Service
	ctx                context.Context
	source, target     domain.HostingCatalogInstallation
	sourceOp, targetOp domain.HostingOperation
}

func newComposeMySQLBackupFixture(t *testing.T) composeMySQLBackupFixture {
	t.Helper()
	pg, db, service, ctx, base := catalogDatabaseFixture(t)
	caps := []string{domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLBackupCapability, domain.HostingDatabaseMySQLBackupComponentCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingStackLifecycleCapability}
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$1::text[]`, caps); err != nil {
		t.Fatal(err)
	}
	f := composeMySQLBackupFixture{pg: pg, db: db, service: service, ctx: ctx}
	deploy := func(name string, port int) (domain.HostingCatalogInstallation, domain.HostingOperation) {
		in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat(name, 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "WordPress " + name, TemplateID: "wordpress-with-mysql", ImmutableVersion: domain.HostingCatalogWordPressVersion, PublicURL: "https://" + name + ".example.test"}
		installed, err := service.InstallHostingCatalog(ctx, in)
		if err != nil {
			t.Fatal("catalog setup", err)
		}
		input := hostingDeploymentRequest()
		input.HostPort = port
		input.ContainerPort = 80
		input.ExpectedRuntimeConfigurationGeneration = 1
		input.IdempotencyKey = "mysql_component_" + name
		input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
		input.Readiness = installed.Template.Definition.Readiness
		op, err := pg.RequestHostingDeployment(ctx, "hop_mysql_component_"+name, installed.Resource.ID, "usr_bootstrap", input, destinationEvent("mysql_component_"+name))
		if err != nil {
			t.Fatal("deploy setup", err)
		}
		op = awaitHostingDispatch(t, pg, op.ID)
		claim, err := pg.ClaimRun(ctx, op.Snapshot.RunnerID, time.Now().UTC(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		if _, err = pg.HostingRunnerPlan(ctx, op.Snapshot.RunnerID, a); err != nil {
			t.Fatal(err)
		}
		definition, err := domain.ParseHostingStack(*installed.Resource.Source.Compose)
		if err != nil {
			t.Fatal(err)
		}
		var images []domain.HostingStackImage
		for i, s := range definition.Services {
			repo := "docker.io/library/wordpress@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834"
			if s.Name == "mysql" {
				repo = "docker.io/library/mysql@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7"
			}
			release := domain.HostingRelease{CanonicalImage: repo, ImageID: "sha256:" + strings.Repeat(fmt.Sprint(i+1), 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon_component_mysql"}
			release.Hash = domain.HostingDocumentHash(release)
			images = append(images, domain.HostingStackImage{Component: s.Name, Release: release})
		}
		release := domain.HostingRelease{DaemonID: "daemon_component_mysql", Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
		release.Hash = domain.HostingDocumentHash(release)
		prepared, err := pg.HostingPrepared(ctx, op.Snapshot.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + name, Release: release, StackImages: images})
		if err != nil {
			t.Fatal("prepare setup", err)
		}
		var credential string
		if err = db.QueryRow(ctx, `SELECT token_hash FROM runners WHERE id=$1`, op.Snapshot.RunnerID).Scan(&credential); err != nil {
			t.Fatal(err)
		}
		delivery, err := pg.AuthorizeHostingRuntimeEnvironment(ctx, op.Snapshot.RunnerID, credential, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: prepared.Plan.Hash, RuntimeConfiguration: installed.RuntimeConfiguration, DeliveryID: strings.Repeat(name, 32)})
		if err != nil {
			t.Fatal("real runtime authorization", err)
		}
		delivery.Close()
		if _, err = pg.HostingApplyBegin(ctx, op.Snapshot.RunnerID, a); err != nil {
			t.Fatal(err)
		}
		volumes := append([]domain.HostingStackVolume(nil), prepared.Plan.Stack.Volumes...)
		for i := range volumes {
			volumes[i].CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if _, err = pg.HostingStackVolumes(ctx, op.Snapshot.RunnerID, domain.HostingStackVolumeRequest{HostingAuthority: a, PlanHash: prepared.Plan.Hash, Volumes: volumes}); err != nil {
			t.Fatal(err)
		}
		state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "wordpress", NetworkID: strings.Repeat(name, 64), Volumes: volumes}
		for i, image := range images {
			state.Components = append(state.Components, domain.HostingStackComponent{Component: image.Component, ContainerID: strings.Repeat(name, 63) + fmt.Sprint(i+1), ImageID: image.Release.ImageID, OperationID: op.ID, Running: true, Health: "healthy"})
		}
		op, err = pg.HostingComplete(ctx, op.Snapshot.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "completed_" + name, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}})
		if err != nil {
			t.Fatal("complete setup", err)
		}
		return installed, op
	}
	f.source, f.sourceOp = deploy("a", 18080)
	f.target, f.targetOp = deploy("b", 18081)
	return f
}
func (f composeMySQLBackupFixture) input() domain.HostingDatabaseBackupInput {
	return domain.HostingDatabaseBackupInput{RequestID: "mysql_component_backup", ExpectedResourceGeneration: f.source.Resource.Generation, Component: "mysql", ExpectedDeployedOperationID: f.sourceOp.ID}
}
func (f composeMySQLBackupFixture) claim(t *testing.T, id string) (domain.HostingAuthority, domain.HostingDatabaseBackupPlan) {
	t.Helper()
	if err := f.pg.PrepareDatabaseBackup(f.ctx, id); err != nil {
		t.Fatal("prepare backup", err)
	}
	c, err := f.pg.ClaimRun(f.ctx, f.sourceOp.Snapshot.RunnerID, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: c.Run.ID, LeaseID: c.Lease.ID, Attempt: c.Lease.Attempt, Fence: c.Lease.Fence}
	p, err := f.pg.DatabaseBackupPlan(f.ctx, f.sourceOp.Snapshot.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestPostgresComposeMySQLBackupImportAndHeldFailure(t *testing.T) {
	for _, mode := range []string{"complete", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newComposeMySQLBackupFixture(t)
			discovery, err := f.service.DatabaseBackupComponents(f.ctx, f.source.Resource.ID)
			if err != nil || len(discovery.Components) != 1 || discovery.Components[0].Engine != "mysql" || discovery.Components[0].MajorVersion != 8 {
				t.Fatal("MySQL discovery", err)
			}
			backup, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, f.input())
			if err != nil {
				t.Fatal(err)
			}
			a, p := f.claim(t, backup.ID)
			if p.Operation.Version != domain.HostingDatabaseMySQLBackupComponentVersion || len(p.Credentials) != 0 || p.Component.Source != "mysql" {
				t.Fatal("wrong component protocol")
			}
			m := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "backup_intent", PlanHash: p.Hash, Phase: "intent", ComponentVersion: p.Operation.Version, WritersResumed: true}
			if _, err = f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, m); err != nil {
				t.Fatal(err)
			}
			m.Phase = "complete"
			m.ReceiptID = "backup_complete"
			m.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 4096, Format: "mysql_sql"}
			backup, err = f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, m)
			if err != nil {
				t.Fatal(err)
			}
			restoreInput := domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: f.target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: f.targetOp.ID, ExpectedResourceGeneration: f.target.Resource.Generation, AcknowledgeReplacement: true}}
			for _, negative := range []string{"generation", "deployment", "source_target", "component", "acknowledgement", "remote"} {
				bad := restoreInput
				target := *restoreInput.ComponentTarget
				bad.ComponentTarget = &target
				bad.RequestID = "denied_" + negative
				switch negative {
				case "generation":
					target.ExpectedResourceGeneration++
				case "deployment":
					target.ExpectedDeployedOperationID = "hop_foreign"
				case "source_target":
					target.ResourceID = f.source.Resource.ID
				case "component":
					target.Component = "wordpress"
				case "acknowledgement":
					target.AcknowledgeReplacement = false
				case "remote":
					bad.RemoteSource = &domain.BackupRemoteSource{TransferID: "foreign", ExpectedTargetRevision: 1}
				}
				if _, e := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, bad); e == nil {
					t.Fatal("invalid import target admitted", negative)
				}
			}
			restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, restoreInput)
			if err != nil {
				t.Fatal("import admission", err)
			}
			a, p = f.claim(t, restored.ID)
			var writers []domain.HostingStorageArchiveWriterState
			for _, c := range p.Target.Stack.Components {
				if c.Component != "mysql" {
					writers = append(writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ContainerID, OperationID: c.OperationID, ImageID: c.ImageID, PlanHash: p.Target.PlanHash, ReleaseHash: p.Target.Release.Hash, Running: c.Running})
				}
			}
			m = domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "import_intent", PlanHash: p.Hash, Phase: "intent", ComponentVersion: p.Operation.Version, Writers: writers}
			if _, err = f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, m); err != nil {
				t.Fatal(err)
			}
			m.Phase = mode
			m.ReceiptID = "import_result"
			if mode == "complete" {
				m.Blob = backup.Blob
				m.WritersResumed = true
			} else {
				m.FailureCode = "execution_failed"
			}
			result, err := f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, m)
			if err != nil {
				t.Fatal("import receipt", err)
			}
			replay, err := f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, m)
			if err != nil || !reflect.DeepEqual(replay, result) || result.OwnershipHeld != (mode == "uncertain") {
				t.Fatal("immutable replay/hold", err)
			}
			if mode == "uncertain" {
				input := hostingDeploymentRequest()
				input.ContainerPort = 80
				input.ExpectedRuntimeConfigurationGeneration = 1
				input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
				if _, e := f.pg.RequestHostingDeployment(f.ctx, "hop_held", f.target.Resource.ID, "usr_bootstrap", input, destinationEvent("held")); e == nil {
					t.Fatal("failed import lost target hold")
				}
			}
			original, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
			if err != nil || original.State != "succeeded" || !reflect.DeepEqual(original.Blob, backup.Blob) {
				t.Fatal("source history changed", err)
			}
		})
	}
}

func TestPostgresComposeMySQLBackupAdmissionDenials(t *testing.T) {
	for _, mode := range []string{"schema", "capability", "grant", "generation", "deployment", "remote", "schedule"} {
		t.Run(mode, func(t *testing.T) {
			f := newComposeMySQLBackupFixture(t)
			input := f.input()
			switch mode {
			case "schema":
				_, _ = f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_component_backup_schema_compatible() RENAME TO unavailable_mysql_component_schema`)
			case "capability":
				_, _ = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1)`, domain.HostingDatabaseMySQLBackupComponentCapability)
			case "grant":
				_, _ = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.source.Binding.DestinationID)
			case "generation":
				input.ExpectedResourceGeneration++
			case "deployment":
				input.ExpectedDeployedOperationID = "hop_foreign"
			case "remote":
				input.RemoteTarget = &domain.BackupTargetSelection{TargetID: "foreign_target", Revision: 1}
			case "schedule":
				_, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, "", domain.HostingDatabaseBackupScheduleInput{RequestID: "unsupported_schedule", ExpectedResourceGeneration: f.source.Resource.Generation, Component: "mysql", ExpectedDeployedOperationID: f.sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: "foreign_target", Revision: 1}})
				if err == nil {
					t.Fatal("MySQL component remote schedule admitted")
				}
				return
			}
			if _, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, input); err == nil {
				t.Fatal("unsupported or stale admission", mode)
			}
			var count int
			if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&count); err != nil || count != 0 {
				t.Fatal("denied admission retained work", err)
			}
		})
	}
}

func TestPostgresComposeMySQLBackupClaimAndLiveCapability(t *testing.T) {
	for _, stage := range []string{"claim", "live"} {
		t.Run(stage, func(t *testing.T) {
			f := newComposeMySQLBackupFixture(t)
			backup, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, f.input())
			if err != nil {
				t.Fatal(err)
			}
			if stage == "claim" {
				if err = f.pg.PrepareDatabaseBackup(f.ctx, backup.ID); err != nil {
					t.Fatal(err)
				}
			}
			var a domain.HostingAuthority
			var p domain.HostingDatabaseBackupPlan
			if stage == "live" {
				a, p = f.claim(t, backup.ID)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1)`, domain.HostingDatabaseMySQLBackupComponentCapability); err != nil {
				t.Fatal(err)
			}
			if stage == "claim" {
				if _, err = f.pg.ClaimRun(f.ctx, f.sourceOp.Snapshot.RunnerID, time.Now(), time.Minute); err == nil {
					t.Fatal("old runner claimed component work")
				}
			} else {
				if _, err = f.pg.DatabaseBackupPlan(f.ctx, p.RunnerID, a); err == nil {
					t.Fatal("revoked runner received plan")
				}
				if _, err = f.pg.DatabaseBackupMutation(f.ctx, p.RunnerID, domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "denied_intent", PlanHash: p.Hash, Phase: "intent", ComponentVersion: p.Operation.Version, WritersResumed: true}); err == nil {
					t.Fatal("revoked runner mutated")
				}
			}
			var count int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_receipts`).Scan(&count); err != nil || count != 0 {
				t.Fatal("capability denial retained receipt", err)
			}
		})
	}
}
