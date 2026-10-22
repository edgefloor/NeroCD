package main

import (
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
			cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
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
	source, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
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
		inside := "/tmp/nerocd-hosting-backup-" + suffix + ".dump"
		run := func(commandArgs ...string) bool {
			output, err := exec.CommandContext(commandCtx, "docker", commandArgs...).CombinedOutput()
			if err != nil {
				t.Errorf("task-owned PostgreSQL tool: %v %s", err, output)
				return false
			}
			return true
		}
		switch name {
		case "pg_dump":
			if !run("exec", "--user", "0", container, "rm", "-f", inside) {
				return exec.CommandContext(commandCtx, "false")
			}
			var hostPath string
			insideArgs := []string{"exec", container, "pg_dump"}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--file=") {
					hostPath = strings.TrimPrefix(arg, "--file=")
					insideArgs = append(insideArgs, "--file="+inside)
				} else if strings.HasPrefix(arg, "postgres") {
					parsed, _ := url.Parse(arg)
					parsed.Host = "127.0.0.1:5432"
					insideArgs = append(insideArgs, parsed.String())
				} else {
					insideArgs = append(insideArgs, arg)
				}
			}
			if run(insideArgs...) && run("cp", container+":"+inside, hostPath) {
				return exec.CommandContext(commandCtx, "true")
			}
		case "pg_restore":
			if !run("cp", args[len(args)-1], container+":"+inside) {
				return exec.CommandContext(commandCtx, "false")
			}
			insideArgs := []string{"exec", container, "pg_restore"}
			for _, arg := range args[:len(args)-1] {
				if strings.HasPrefix(arg, "--dbname=") {
					parsed, _ := url.Parse(strings.TrimPrefix(arg, "--dbname="))
					parsed.Host = "127.0.0.1:5432"
					arg = "--dbname=" + parsed.String()
				}
				insideArgs = append(insideArgs, arg)
			}
			return exec.CommandContext(commandCtx, "docker", append(insideArgs, inside)...)
		}
		return exec.CommandContext(commandCtx, "false")
	}
	t.Cleanup(func() {
		backupCommand = previous
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanupCtx, "docker", "exec", "--user", "0", container, "rm", "-f", "/tmp/nerocd-hosting-backup-"+suffix+".dump").Run()
	})
	for _, failure := range []string{"none", "binding", "proxy"} {
		failHold := failure != "none"
		if failHold {
			if _, err = source.Exec(ctx, `DROP TRIGGER IF EXISTS reject_restore_hold ON hosting_binding_restore_holds; DROP FUNCTION IF EXISTS reject_restore_hold(); CREATE FUNCTION reject_restore_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected hold write failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			table := "hosting_binding_restore_holds"
			if failure == "proxy" {
				table = "hosting_proxy_restore_holds"
			}
			if _, err = source.Exec(ctx, "CREATE TRIGGER reject_restore_hold BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_restore_hold()"); err != nil {
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
		target.Close()
		if err != nil || held == failHold || proxyHeld == failHold || revoked == failHold || expired == failHold {
			t.Fatalf("restore invalidation atomicity: failure=%s binding=%v proxy=%v revoked=%v expired=%v err=%v", failure, held, proxyHeld, revoked, expired, err)
		}
	}
}
