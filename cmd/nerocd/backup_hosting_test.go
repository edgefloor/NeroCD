package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	sources "nerocd/internal/source"
	"nerocd/internal/store"
)

// This invokes the actual backup and restore CLI functions. Only executable
// lookup is redirected to the PostgreSQL tools in the task-owned container.
func TestRestoreCLIAtomicallyHoldsHostingBindingsWithoutStateRows(t *testing.T) {
	container, address := os.Getenv("NEROCD_TEST_PG_CONTAINER"), os.Getenv("NEROCD_TEST_DATABASE_URL")
	if container == "" || address == "" {
		t.Skip("set task-owned NEROCD_TEST_PG_CONTAINER and NEROCD_TEST_DATABASE_URL")
	}
	t.Setenv("NEROCD_MODE", "development")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	databaseURL := func(name string) string {
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Path = "/" + name
		parsed.RawQuery = "sslmode=disable"
		return parsed.String()
	}
	createDatabase := func(name string) string {
		t.Helper()
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// DROP DATABASE checkpoints thousands of restored relation files. Allow
			// slow container filesystems to finish while keeping cleanup bounded.
			cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error(err)
			}
		})
		return databaseURL(name)
	}
	sourceURL := createDatabase("hosting_backup_source_" + suffix)
	if err = migrateDatabase([]string{"--database-url", sourceURL}); err != nil {
		t.Fatal(err)
	}
	if err = seedDevelopmentDatabase([]string{"--database-url", sourceURL, "--seed-file", "../../db/seeds/dev.sql"}); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Close() }()
	now := time.Now().UTC()
	event := func(id string) domain.AuditEvent {
		return domain.AuditEvent{ID: "aud_backup_" + id, ActorID: "usr_bootstrap", CreatedAt: now, Metadata: map[string]any{}}
	}
	enrollment, err := pg.CreateRunnerEnrollment(ctx, domain.RunnerEnrollment{ID: "enroll_backup", RunnerID: "runner_backup", RunnerName: "backup", TokenHash: "backup_enrollment", Tags: []string{}, Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, event("enroll"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "consume_backup", CredentialHash: "backup_credential"}, event("consume"))
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := pg.CreateServerWithDestination(ctx, domain.Server{ID: "server_backup", RunnerID: runner.ID, Name: "backup", CreatedBy: "usr_bootstrap", CreatedAt: now}, domain.Destination{ID: "dest_backup", ServerID: "server_backup", Name: "default", RuntimeKind: "docker_compose", CreatedAt: now}, event("server"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.GrantDestinationProject(ctx, domain.DestinationProjectGrant{DestinationID: destination.ID, ProjectID: "proj_platform", GrantedBy: "usr_bootstrap", CreatedAt: now}, event("grant")); err != nil {
		t.Fatal(err)
	}
	environment, err := pg.CreateProjectEnvironment(ctx, domain.ProjectEnvironment{ID: "env_backup", ProjectID: "proj_platform", Name: "backup", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := pg.CreateResource(ctx, domain.Resource{ID: "res_backup", ProjectID: environment.ProjectID, ProjectEnvironmentID: environment.ID, Name: "backup", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_backup", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: now}, event("binding")); err != nil {
		t.Fatal(err)
	}
	gitConfig := domain.HostingGitSource{ID: "source_git_backup", ResourceID: "res_git_backup", ProjectID: environment.ProjectID, Generation: 1, RepositoryURL: "https://github.com/acme/backup.git", RequestedRef: "main", CredentialKind: "public", Policy: sources.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"github.com"}}, Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}
	if _, err = pg.CreateResource(ctx, domain.Resource{ID: gitConfig.ResourceID, ProjectID: environment.ProjectID, ProjectEnvironmentID: environment.ID, Name: "Git backup", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: gitConfig.ID, Generation: 1}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, store.WithHostingGitSource(gitConfig), store.WithAudit(event("git_resource"))); err != nil {
		t.Fatal(err)
	}
	source, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	// Even a draft or safely canceled onboarding record must lose restored root authority.
	ring, e := secretcrypto.New("backup", map[string][]byte{"backup": bytes.Repeat([]byte{7}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	envelope, e := ring.Seal(secretcrypto.Binding{ProjectID: "proj_platform", SecretID: "msec_backup_ssh", Version: 1}, []byte("fixture-only-private-key"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pg.CreateManagedSecret(ctx, domain.ManagedSecret{ID: "msec_backup_ssh", ProjectID: "proj_platform", Name: "SSH backup", CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: "msec_backup_ssh", Version: 1, CreatedBy: "usr_bootstrap", CreatedAt: now}, Envelope: envelope}, event("ssh_secret")); e != nil {
		t.Fatal(e)
	}
	bootstrapInput := domain.ServerBootstrapInput{ProjectID: "proj_platform", Name: "prepared", Target: domain.ServerBootstrapTarget{IP: "192.0.2.30", Port: 22, User: "root", HostFingerprint: "SHA256:" + strings.Repeat("A", 43)}, ControllerURL: "https://controller.example/", Artifact: domain.ServerBootstrapArtifact{Size: 1, SHA256: strings.Repeat("a", 64), Version: "fixture", Architecture: "arm64"}, SSHSecretID: "msec_backup_ssh", SSHSecretVersion: 1}
	if _, e = pg.CreateServerBootstrap(ctx, "usr_bootstrap", "draft", bootstrapInput, event("ssh_draft")); e != nil {
		t.Fatal(e)
	}
	bootstrapInput.Target.IP = "192.0.2.31"
	canceled, e := pg.CreateServerBootstrap(ctx, "usr_bootstrap", "canceled", bootstrapInput, event("ssh_canceled"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pg.RequestServerBootstrapCommand(ctx, "usr_bootstrap", canceled.IDs.OperationID, domain.ServerBootstrapCommand{Key: "cancel", Action: "cancel", ExpectedVersion: canceled.Version, Deadline: now.Add(time.Hour - time.Minute)}, event("ssh_cancel")); e != nil {
		t.Fatal(e)
	}

	// Preserve readiness on both an unused server and a bound server in the backup.
	// Successful restore must invalidate both pending and accepted state atomically.
	if _, err = source.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_readiness_v1') WHERE id='runner_backup'`); err != nil {
		t.Fatal(err)
	}
	challenge, err := pg.IssueServerReadiness(ctx, runner.ID, "backup_credential")
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ServerReadinessReport{SampleID: challenge.SampleID, Profile: challenge.Profile, OS: "unknown", Architecture: "unknown", Execution: "disabled", Configuration: "mismatch", Docker: "unreachable", DaemonOS: "unknown", DaemonArchitecture: "unknown", CLI: "unknown", WorkRoot: "unknown", JournalRoot: "unknown", Error: "local_check_failed"}
	if _, err = pg.ReportServerReadiness(ctx, runner.ID, "backup_credential", report); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.IssueServerReadiness(ctx, runner.ID, "backup_credential"); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `INSERT INTO runner_enrollments(id,token_hash,runner_id,runner_name,tags,capabilities,created_by,created_at,expires_at,used_at,consume_request_id,credential_hash) VALUES('enroll_unused','unused_enroll','runner_unused','unused','{}',ARRAY['compose_deploy','hosting_readiness_v1'],'usr_bootstrap',clock_timestamp(),clock_timestamp()+interval '1 hour',clock_timestamp(),'consume_unused','unused_credential')`); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `INSERT INTO runners(id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash) VALUES('runner_unused','unused','{}',ARRAY['compose_deploy','hosting_readiness_v1'],'active',clock_timestamp(),clock_timestamp(),'unused_credential')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = pg.CreateServerWithDestination(ctx, domain.Server{ID: "server_unused", RunnerID: "runner_unused", Name: "unused", CreatedBy: "usr_bootstrap", CreatedAt: now}, domain.Destination{ID: "dest_unused", ServerID: "server_unused", Name: "default", RuntimeKind: "docker_compose", CreatedAt: now}, event("unused")); err != nil {
		t.Fatal(err)
	}
	unused, err := pg.IssueServerReadiness(ctx, "runner_unused", "unused_credential")
	if err != nil {
		t.Fatal(err)
	}
	report.SampleID = unused.SampleID
	if _, err = pg.ReportServerReadiness(ctx, "runner_unused", "unused_credential", report); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.IssueServerReadiness(ctx, "runner_unused", "unused_credential"); err != nil {
		t.Fatal(err)
	}
	// A disabled configured proxy with no operations still needs restore fencing.
	if _, err = source.Exec(ctx, `INSERT INTO hosting_proxies(id,server_id,destination_id,runner_id,control_project_id,configuration,enabled,created_by) VALUES('proxy_backup','server_backup','dest_backup','runner_backup','proj_platform','{}',false,'usr_bootstrap')`); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,created_at,expires_at) VALUES('session_backup','usr_bootstrap','session_backup_hash',clock_timestamp(),clock_timestamp()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,attempt,fence,status,created_at,expires_at) VALUES('lease_backup','run_001','runner_backup',1,'fence_backup','active',clock_timestamp(),clock_timestamp()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	previous := backupCommand
	backupCommand = func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
		if name != "pg_dump" && name != "pg_restore" {
			t.Fatalf("unexpected backup tool %q", name)
		}
		// Forward the command's file descriptors through docker exec. Local socket
		// trust in this task-owned PostgreSQL container avoids copying a host passfile.
		insideArgs := []string{"exec", "-i", container, name}
		for _, arg := range args {
			if strings.HasPrefix(arg, "--dbname=") {
				parsed, err := url.Parse(strings.TrimPrefix(arg, "--dbname="))
				if err != nil {
					t.Fatal(err)
				}
				parsed.Host = ""
				query := parsed.Query()
				query.Set("host", "/var/run/postgresql")
				query.Del("passfile")
				query.Del("password")
				parsed.RawQuery = query.Encode()
				arg = "--dbname=" + parsed.String()
			}
			insideArgs = append(insideArgs, arg)
		}
		return exec.CommandContext(commandCtx, "docker", insideArgs...)
	}
	t.Cleanup(func() { backupCommand = previous })

	for _, failure := range []string{"none", "binding", "proxy", "bootstrap"} {
		failHold := failure != "none"
		if failHold {
			if _, err = source.Exec(ctx, `DROP TRIGGER IF EXISTS reject_restore_hold ON hosting_binding_restore_holds; DROP TRIGGER IF EXISTS reject_restore_hold ON hosting_proxy_restore_holds; DROP TRIGGER IF EXISTS reject_restore_hold ON server_bootstrap_operations; DROP FUNCTION IF EXISTS reject_restore_hold(); CREATE FUNCTION reject_restore_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected hold write failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			table := "hosting_binding_restore_holds"
			if failure == "proxy" {
				table = "hosting_proxy_restore_holds"
			}
			if failure == "bootstrap" {
				table = "server_bootstrap_operations"
			}
			if _, err = source.Exec(ctx, "CREATE TRIGGER reject_restore_hold BEFORE INSERT OR UPDATE ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_restore_hold()"); err != nil {
				t.Fatal(err)
			}
		}
		output := privateBackupTestDir(t)
		if err = backupDatabase([]string{"--database-url", sourceURL, "--output-dir", output}); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(output)
		if err != nil || len(entries) != 1 {
			t.Fatalf("backup archive: %v %v", entries, err)
		}
		targetName := fmt.Sprintf("hosting_backup_target_%s_%s", suffix, failure)
		targetURL := createDatabase(targetName)
		err = restoreDatabase([]string{"--database-url", targetURL, "--input-dir", filepath.Join(output, entries[0].Name()), "--allow-disposable-target", "--confirm-target-database", targetName})
		if (err != nil) != failHold {
			t.Fatalf("restore failHold=%v err=%v", failHold, err)
		}
		target, err := pgxpool.New(ctx, targetURL)
		if err != nil {
			t.Fatal(err)
		}
		var held, proxyHeld, revoked, expired bool
		err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id='binding_backup'),EXISTS(SELECT 1 FROM hosting_proxy_restore_holds WHERE proxy_id='proxy_backup'),(SELECT revoked_at IS NOT NULL FROM sessions WHERE id='session_backup'),(SELECT status='expired' FROM run_leases WHERE id='lease_backup')`).Scan(&held, &proxyHeld, &revoked, &expired)
		var readinessCount int
		if queryErr := target.QueryRow(ctx, `SELECT count(*) FROM server_readiness`).Scan(&readinessCount); queryErr != nil || (!failHold && readinessCount != 0) || (failHold && readinessCount != 2) {
			t.Fatal("readiness restore invalidation", readinessCount, queryErr)
		}
		var bootstrapHeld, bootstrapGrants int
		if queryErr := target.QueryRow(ctx, `SELECT (SELECT count(*) FROM server_bootstrap_operations WHERE restore_held),(SELECT count(*) FROM server_bootstrap_secret_grants WHERE revoked_at IS NOT NULL)`).Scan(&bootstrapHeld, &bootstrapGrants); queryErr != nil || (!failHold && (bootstrapHeld != 2 || bootstrapGrants != 2)) || (failHold && (bootstrapHeld != 0 || bootstrapGrants != 1)) {
			t.Fatal("bootstrap restore atomicity", bootstrapHeld, bootstrapGrants, queryErr)
		}

		var sourceConsistent bool
		if queryErr := target.QueryRow(ctx, `SELECT nerocd_hosting_git_resource_schema_compatible() AND EXISTS(SELECT 1 FROM resources r JOIN hosting_build_sources h ON h.id=r.source->'git'->>'source_id' AND h.generation=(r.source->'git'->>'generation')::bigint JOIN hosting_build_source_versions v ON v.source_id=h.id AND v.generation=h.generation WHERE r.id='res_git_backup' AND v.body->>'repository_url'='https://github.com/acme/backup.git')`).Scan(&sourceConsistent); queryErr != nil || !sourceConsistent {
			t.Fatal("restored Git source reference/schema", sourceConsistent, queryErr)
		}
		target.Close()
		if err != nil || held == failHold || proxyHeld == failHold || revoked == failHold || expired == failHold {
			t.Fatalf("restore invalidation atomicity: failure=%s binding=%v proxy=%v revoked=%v expired=%v err=%v", failure, held, proxyHeld, revoked, expired, err)
		}
	}
}
