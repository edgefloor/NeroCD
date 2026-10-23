package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

type automaticStoreFixture struct {
	pushStoreFixture
	bootstrap domain.HostingOperation
	loaded    domain.HostingProxyOperation
	routes    []domain.HostingRoute
	stop      func(context.Context) error
}

// Metadata fixtures exercise server contracts only; the separate compiled
// Linux gate must prove Docker readiness and trusted fixture-CA HTTPS.
func newAutomaticStoreFixture(t *testing.T) automaticStoreFixture {
	t.Helper()
	ctx := t.Context()
	f := newPushStoreFixture(t)
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, []string{domain.RunTypeLocalBuildDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}); err != nil {
		t.Fatalf("automatic fixture step 1: %v", err)
	}
	in := buildInput("automatic-manual-build")
	in.ExpectedResourceGeneration = 2
	in.ExpectedSourceGeneration = 2
	if _, err := f.pg.RequestHostingBuild(ctx, "automatic-manual-build", f.src.ResourceID, "usr_bootstrap", in, buildPolicy(), buildEnqueue, destinationEvent("automatic-manual-build")); err != nil {
		t.Fatalf("automatic fixture step 2: %v", err)
	}
	b, a := f.activate(t, "automatic-manual-build")
	var err error
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "automatic-manual-source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatalf("automatic fixture step 3: %v", err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "local-daemon")
	if err != nil {
		t.Fatalf("automatic fixture step 4: %v", err)
	}
	artifact := buildArtifact(b)
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "automatic-manual-artifact", b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
	if err != nil {
		t.Fatalf("automatic fixture step 5: %v", err)
	}
	stop, err := f.pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatalf("automatic fixture step 6: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	deploy := localBuildDeploymentInput(b.ID)
	deploy.ExpectedGeneration = 2
	deploy.ExpectedSourceGeneration = 2
	deploy.IdempotencyKey = "automatic-manual-deployment"
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "automatic-manual-deployment", f.src.ResourceID, "usr_bootstrap", deploy, destinationEvent("automatic-manual-deployment"))
	if err != nil {
		t.Fatalf("automatic fixture step 7: %v", err)
	}
	op = awaitHostingDispatch(t, f.pg, op.ID)
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("automatic fixture step 8: %v", err)
	}
	authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err = f.pg.HostingRunnerPlan(ctx, f.runner, authority)
	if err != nil {
		t.Fatalf("automatic fixture runner plan: %v", err)
	}
	release, err := domain.NewHostingLocalBuildRelease(*b.Artifact)
	if err != nil {
		t.Fatalf("automatic fixture step 9: %v", err)
	}
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: "automatic-manual-prepare", Release: release})
	if err != nil {
		t.Fatalf("automatic fixture step 10: %v", err)
	}
	op, err = f.pg.HostingApplyBegin(ctx, f.runner, authority)
	if err != nil {
		t.Fatalf("automatic fixture step 11: %v", err)
	}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "automatic-manual-complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatalf("automatic fixture step 12: %v", err)
	}
	config := domain.HostingProxyInput{ControlProjectID: f.src.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "automatic-proxy"}
	if _, err = f.pg.ConfigureHostingProxy(ctx, "automatic-proxy", "server_build", "usr_bootstrap", config, destinationEvent("automatic-proxy")); err != nil {
		t.Fatalf("automatic fixture step 13: %v", err)
	}
	var initial string
	if err = f.db.QueryRow(ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id='automatic-proxy' AND generation=1`).Scan(&initial); err != nil {
		t.Fatalf("automatic fixture step 14: %v", err)
	}
	settleProxyGeneration(t, f.pg, initial, f.runner, release.DaemonID)
	if _, err = f.pg.GrantHostingHostname(ctx, "automatic-proxy", "automatic-grant", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: f.src.ProjectID, Hostname: "auto.nerocd.invalid"}, destinationEvent("automatic-grant")); err != nil {
		t.Fatalf("automatic fixture step 15: %v", err)
	}
	out := automaticStoreFixture{pushStoreFixture: f, bootstrap: op, stop: stop}
	for n, path := range []string{"/", "/api"} {
		id := "automatic-route-" + string(rune('a'+n))
		route, e := f.pg.RequestHostingRoute(ctx, f.src.ResourceID, "", id, "usr_bootstrap", domain.HostingRouteInput{Hostname: "auto.nerocd.invalid", PathPrefix: path, ExpectedHealthyOperationID: op.ID, IdempotencyKey: id}, destinationEvent(id))
		if e != nil {
			t.Fatal(e)
		}
		out.routes = append(out.routes, route)
		at, proxy, prepared := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: op}, id)
		out.loaded, e = f.pg.RunnerProxyComplete(ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: at, ReceiptID: "complete_" + id, Result: trustedProxyResult(proxy, prepared, "applied")})
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = stop(ctx); err != nil {
		t.Fatalf("automatic fixture step 16: %v", err)
	}
	return out
}
func (f automaticStoreFixture) configuration() domain.GitHubPushConfigure {
	config := *f.policy.OwnerConfiguration
	automatic := &domain.HostingAutoDeployConfiguration{Mode: domain.HostingAutoDeployMode, ExpectedHealthyOperationID: f.bootstrap.ID, ExpectedLoadedProxyOperationID: f.loaded.ID, ExpectedLoadedGeneration: f.loaded.Generation, ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}
	for _, r := range f.routes {
		automatic.Routes = append(automatic.Routes, domain.HostingRouteSwitchSelection{RouteID: r.ID, ExpectedVersion: r.Version, ExpectedSelectedReleaseOperationID: r.SelectedReleaseOperationID})
	}
	config.AutomaticDeployment = automatic
	return domain.GitHubPushConfigure{ExpectedPolicyGeneration: f.policy.Generation, ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, RequestID: pushUUID(100), ResetBranchBaseline: true, Configuration: config}
}
func TestPostgresAutomaticDeploymentReviewedBootstrap(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	ctx := t.Context()
	in := f.configuration()
	if f.loaded.Snapshot.VerifiedSwitch != nil {
		t.Fatal("fixture unexpectedly requires an extra strict switch")
	}
	p, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, in, destinationEvent("auto-review"))
	if err != nil {
		t.Fatal(err)
	}
	if p.OwnerAutomaticDeploymentOptions != nil {
		t.Fatal("configure response claims fresh options")
	}
	if p.Enabled || p.AutomaticDeployment == nil || p.AutomaticDeployment.State != "idle" || p.OwnerConfiguration.AutomaticDeployment == nil {
		t.Fatal("explicit disabled save missing", p)
	}
	replay, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, in, destinationEvent("auto-review-replay"))
	if err != nil || replay.Generation != p.Generation || replay.OwnerAutomaticDeploymentOptions != nil {
		t.Fatal("review replay", err)
	}
	var epoch int64
	var body json.RawMessage
	if err = f.db.QueryRow(ctx, `SELECT manual_epoch,body FROM hosting_auto_deploy_controls WHERE resource_id=$1`, f.src.ResourceID).Scan(&epoch, &body); err != nil || epoch != 0 {
		t.Fatal("control identity", epoch, err)
	}
	changed := in
	changed.RequestID = pushUUID(101)
	changed.ExpectedPolicyGeneration = p.Generation
	cfg := *in.Configuration.AutomaticDeployment
	cfg.ExpectedLoadedGeneration++
	changed.Configuration.AutomaticDeployment = &cfg
	if _, err = f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, changed, destinationEvent("auto-review-stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale bootstrap accepted", err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('automatic-viewer','proj_platform','usr_viewer','viewer')`); err != nil {
		t.Fatal(err)
	}
	viewer, err := f.pg.GetGitHubPushPolicy(ctx, "usr_viewer", f.src.ResourceID)
	if err != nil || viewer.OwnerConfiguration != nil || viewer.AutomaticDeployment == nil {
		t.Fatal("viewer scope", err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO hosting_auto_deploy_origins(operation_id,kind,resource_id,demand_id,build_id,body,body_hash) VALUES('invalid-null-origin','deployment',$1,'invalid-demand','automatic-manual-build','{"version":1}',$2)`, f.src.ResourceID, "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("origin without exact operation FK accepted")
	}
	if _, err = f.db.Exec(ctx, `ALTER TABLE hosting_auto_deploy_demands DROP CONSTRAINT hosting_auto_demand_deadline`); err != nil {
		t.Fatal(err)
	}
	var compatible bool
	if err = f.db.QueryRow(ctx, `SELECT nerocd_hosting_auto_deploy_schema_compatible()`).Scan(&compatible); err != nil || compatible {
		t.Fatal("malformed schema accepted", compatible, err)
	}
}

func TestPostgresAutomaticDeploymentManualEpoch(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	ctx := t.Context()
	if _, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, f.configuration(), destinationEvent("epoch-review")); err != nil {
		t.Fatal(err)
	}
	r := f.routes[0]
	in := domain.HostingRouteInput{ExpectedRouteVersion: r.Version, IdempotencyKey: "epoch-manual-route", Hostname: r.Hostname, PathPrefix: r.PathPrefix, ExpectedHealthyOperationID: f.bootstrap.ID}
	changed, err := f.pg.RequestHostingRoute(ctx, f.src.ResourceID, r.ID, "epoch-manual-route", "usr_bootstrap", in, destinationEvent("epoch-manual-route"))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != r.Version+1 {
		t.Fatal("manual route not accepted")
	}
	if _, err = f.pg.RequestHostingRoute(ctx, f.src.ResourceID, r.ID, "ignored-replay-id", "usr_bootstrap", in, destinationEvent("ignored-replay-audit")); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err = f.db.QueryRow(ctx, `SELECT manual_epoch FROM hosting_auto_deploy_controls WHERE resource_id=$1`, f.src.ResourceID).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatal("fresh mutation must increment exactly once", epoch, err)
	}
	p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	if err != nil || p.AutomaticDeployment.State != "blocked" || p.AutomaticDeployment.Reason != "review_invalidated" {
		t.Fatal("manual choice did not invalidate review", p.AutomaticDeployment, err)
	}
}

func TestPostgresAutomaticDeploymentSchemaCompatibility(t *testing.T) {
	f := newPushStoreFixture(t)
	for _, tc := range []struct{ name, sql string }{
		{"same_named_weakened_deadline", `ALTER TABLE hosting_auto_deploy_demands DROP CONSTRAINT hosting_auto_demand_deadline; ALTER TABLE hosting_auto_deploy_demands ADD CONSTRAINT hosting_auto_demand_deadline CHECK(true)`},
		{"same_named_weakened_body", `ALTER TABLE hosting_auto_deploy_origins DROP CONSTRAINT hosting_auto_origin_body; ALTER TABLE hosting_auto_deploy_origins ADD CONSTRAINT hosting_auto_origin_body CHECK(true)`},
		{"nullable_identity", `ALTER TABLE hosting_auto_deploy_demands ALTER COLUMN build_id DROP NOT NULL`},
		{"missing_build_fk", `ALTER TABLE hosting_auto_deploy_origins DROP CONSTRAINT hosting_auto_deploy_origins_build_id_fkey`},
		{"missing_operation_uniqueness", `ALTER TABLE hosting_auto_deploy_origins DROP CONSTRAINT hosting_auto_deploy_origins_proxy_operation_id_key`},
		{"disabled_immutability", `ALTER TABLE hosting_auto_deploy_origins DISABLE TRIGGER hosting_auto_deploy_origin_immutable`},
		{"missing_due_index", `DROP INDEX hosting_auto_deploy_due`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := tx.Rollback(ctx); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error(e)
				}
			}()
			if _, err = tx.Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			var compatible bool
			if err = tx.QueryRow(ctx, `SELECT nerocd_hosting_auto_deploy_schema_compatible()`).Scan(&compatible); err != nil || compatible {
				t.Fatal("weakened durable boundary accepted", compatible, err)
			}
		})
	}
}

func TestPostgresAutomaticDeploymentOwnerOptions(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	ctx := t.Context()
	get := func() domain.GitHubPushPolicy {
		t.Helper()
		p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := get()
	o := p.OwnerAutomaticDeploymentOptions
	if !p.CanManage || o == nil || !o.Available || o.Reason != "eligible" || o.Baseline == nil || o.Baseline.HealthyOperationID != f.bootstrap.ID || o.Baseline.LoadedProxyOperationID != f.loaded.ID || o.Baseline.LoadedGeneration != f.loaded.Generation || o.Baseline.ContainerPort != 80 || o.Baseline.Readiness != f.bootstrap.Snapshot.Readiness || len(o.Baseline.Routes) != 2 {
		t.Fatal("ordinary loaded review options", o)
	}
	encoded, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"artifact", "daemon", "secret", "body_hash", "configuration_hash", "runner_id", "grant_id"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private snapshot field escaped", private)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded domain.HostingAutoDeployOptions
	if err = decoder.Decode(&decoded); err != nil {
		t.Fatal("closed response DTO mismatch", err)
	}
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "auto-options-viewer", ProjectID: f.src.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer, err := f.pg.GetGitHubPushPolicy(ctx, "usr_viewer", f.src.ResourceID)
	if err != nil || viewer.CanManage || viewer.OwnerAutomaticDeploymentOptions != nil {
		t.Fatal("viewer options leaked", err)
	}
	// The read model is also available before any push policy exists.
	if _, err = f.db.Exec(ctx, `DELETE FROM hosting_github_push_policies WHERE id=$1`, f.policy.ID); err != nil {
		t.Fatal(err)
	}
	p = get()
	if p.Status != "unconfigured" || p.OwnerAutomaticDeploymentOptions == nil || !p.OwnerAutomaticDeploymentOptions.Available {
		t.Fatal("unconfigured GET omitted options", p.OwnerAutomaticDeploymentOptions)
	}
	// A proxy row writer cannot make the options reader reverse the existing
	// resource->proxy lock order: the single MVCC read takes no proxy lock.
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := blocker.Rollback(ctx); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Error(e)
		}
	}()
	if _, err = blocker.Exec(ctx, `SELECT id FROM hosting_proxies WHERE id='automatic-proxy' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	read, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	p, err = f.pg.GetGitHubPushPolicy(read, "usr_bootstrap", f.src.ResourceID)
	if err != nil || !p.OwnerAutomaticDeploymentOptions.Available {
		t.Fatal("options waited on proxy mutation lock", err)
	}
}
func TestPostgresAutomaticDeploymentOwnerOptionsUnavailable(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"manual_baseline", `UPDATE hosting_binding_state SET healthy=NULL WHERE binding_id='binding_build'`, "manual_healthy_required"},
		{"stale_loaded", `UPDATE hosting_proxies SET loaded_generation=loaded_generation+1 WHERE id='automatic-proxy'`, "proxy_not_ready"},
		{"current_enrollment", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id='runner_build'`, "runner_unsupported"},
		{"restore_hold", `INSERT INTO hosting_binding_restore_holds(binding_id) VALUES('binding_build')`, "restore_hold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAutomaticStoreFixture(t)
			if _, err := f.db.Exec(t.Context(), tc.sql); err != nil {
				t.Fatal(err)
			}
			p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID)
			if err != nil || p.OwnerAutomaticDeploymentOptions == nil || p.OwnerAutomaticDeploymentOptions.Available || p.OwnerAutomaticDeploymentOptions.Baseline != nil || p.OwnerAutomaticDeploymentOptions.Reason != tc.reason {
				t.Fatal("invalid current options", p.OwnerAutomaticDeploymentOptions, err)
			}
		})
	}
	t.Run("overflow", func(t *testing.T) {
		f := newAutomaticStoreFixture(t)
		if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_routes(id,proxy_id,project_id,resource_id,binding_id,grant_id,hostname,path_prefix,version,selected_release_operation_id,created_by) SELECT 'overflow-'||n,proxy_id,project_id,resource_id,binding_id,grant_id,hostname,'/overflow-'||n,1,selected_release_operation_id,created_by FROM hosting_proxy_routes CROSS JOIN generate_series(1,31) n WHERE id=$1`, f.routes[0].ID); err != nil {
			t.Fatal(err)
		}
		p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID)
		if err != nil || p.OwnerAutomaticDeploymentOptions == nil || p.OwnerAutomaticDeploymentOptions.Available || p.OwnerAutomaticDeploymentOptions.Baseline != nil || p.OwnerAutomaticDeploymentOptions.Reason != "routes_unavailable" {
			t.Fatal("overflow silently truncated", p.OwnerAutomaticDeploymentOptions, err)
		}
	})
}

func TestPostgresAutomaticDeploymentOwnerOptionsCurrentRole(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "auto-former-owner", ProjectID: f.src.ProjectID, UserID: "usr_bootstrap", Role: domain.RoleMaintainer}); err != nil {
		t.Fatal(err)
	}
	p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	if err != nil || p.CanManage || p.OwnerConfiguration != nil || p.OwnerAutomaticDeploymentOptions != nil {
		t.Fatal("lost current owner role still exposes review facts", err)
	}
}

func TestPostgresAutomaticDeploymentUnboundOptions(t *testing.T) {
	f := newBuildStoreFixture(t)
	configuration := f.src
	configuration.RepositoryURL = "https://github.com/acme/unbound.git"
	configuration.Policy.AllowedHosts = []string{"github.com"}
	resource, _ := createBuildGitResource(t, f.pg, "automatic-options-unbound", "proj_platform", configuration)
	p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", resource.ID)
	if err != nil || p.Status != "unconfigured" || p.OwnerAutomaticDeploymentOptions == nil || p.OwnerAutomaticDeploymentOptions.Available || p.OwnerAutomaticDeploymentOptions.Baseline != nil || p.OwnerAutomaticDeploymentOptions.Reason != "unbound" {
		t.Fatal(p.OwnerAutomaticDeploymentOptions, err)
	}
}

func TestPostgresAutomaticDeploymentPolicyReadAvoidsMutationLocks(t *testing.T) {
	for _, mode := range []string{"unconfigured", "build_only", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomaticStoreFixture(t)
			ctx := t.Context()
			if mode == "unconfigured" {
				if _, err := f.db.Exec(ctx, `DELETE FROM hosting_github_push_policies WHERE resource_id=$1`, f.src.ResourceID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "automatic" {
				if _, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, f.configuration(), destinationEvent("read-configured")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "read-viewer", ProjectID: f.src.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := blocker.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error(err)
				}
			}()
			for _, query := range []string{`SELECT pg_advisory_xact_lock(hashtextextended('hosting-github-push-quotas',0))`, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:dest_build',0))`, `SELECT id FROM hosting_proxies WHERE id='automatic-proxy' FOR UPDATE`, `SELECT id FROM resources FOR UPDATE`, `SELECT id FROM projects FOR UPDATE`, `SELECT id FROM users FOR UPDATE`, `SELECT id FROM hosting_build_sources FOR UPDATE`} {
				if _, err = blocker.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			for _, actor := range []string{"usr_bootstrap", "usr_viewer"} {
				read, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				p, err := f.pg.GetGitHubPushPolicy(read, actor, f.src.ResourceID)
				cancel()
				if err != nil {
					t.Fatal("GET waited on mutation lock", mode, actor, err)
				}
				if actor == "usr_bootstrap" && (!p.CanManage || p.OwnerAutomaticDeploymentOptions == nil) {
					t.Fatal("owner read lost options")
				}
				if actor == "usr_viewer" && (p.CanManage || p.OwnerConfiguration != nil || p.OwnerAutomaticDeploymentOptions != nil) {
					t.Fatal("viewer received owner facts")
				}
			}
		})
	}
}

func TestPostgresAutomaticDeploymentPolicyReadFreshAuthority(t *testing.T) {
	for _, mode := range []string{"owner_to_viewer", "lost_viewer", "archived", "token_revoked", "token_expired", "token_natural_expiry", "service_account_revoked", "parent_disabled", "project_archived"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomaticStoreFixture(t)
			ctx := t.Context()
			if _, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, f.configuration(), destinationEvent("read-auth")); err != nil {
				t.Fatal(err)
			}
			actor := "usr_bootstrap"
			if strings.HasPrefix(mode, "token_") {
				actor = "auto_read_token"
				if _, err := f.pg.CreateAPIToken(ctx, domain.APIToken{ID: actor, Name: actor, Kind: domain.TokenKindServiceAccount, TokenHash: strings.Repeat("7", 64), Roles: []string{domain.RoleSystemAdmin}, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "token_natural_expiry" {
				if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '300 milliseconds' WHERE id='auto_read_token'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "service_account_revoked" || mode == "parent_disabled" {
				actor = "push_bot"
				if _, err := f.db.Exec(ctx, `UPDATE service_accounts SET role='owner' WHERE id='push_bot'`); err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := blocker.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error(err)
				}
			}()
			if _, err = blocker.Exec(ctx, `LOCK TABLE hosting_proxy_routes IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			type result struct {
				p   domain.GitHubPushPolicy
				err error
			}
			done := make(chan result, 1)
			go func() { p, err := f.pg.GetGitHubPushPolicy(ctx, actor, f.src.ResourceID); done <- result{p, err} }()
			until := time.Now().Add(time.Second)
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='hosting_proxy_routes'::regclass AND NOT granted)`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(until) {
					t.Fatal("GET did not reach snapshot barrier")
				}
				time.Sleep(time.Millisecond)
			}
			switch mode {
			case "owner_to_viewer":
				if _, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; INSERT INTO project_members(id,project_id,user_id,role) VALUES('read-demoted','proj_platform','usr_bootstrap','viewer') ON CONFLICT(project_id,user_id) DO UPDATE SET role='viewer'`); err != nil {
					t.Fatal(err)
				}
			case "lost_viewer":
				_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap';DELETE FROM project_members WHERE user_id='usr_bootstrap'`)
			case "archived":
				_, err = f.db.Exec(ctx, `UPDATE users SET archived_at=clock_timestamp() WHERE id='usr_bootstrap'`)
			case "token_revoked":
				_, err = f.db.Exec(ctx, `UPDATE api_tokens SET revoked_at=clock_timestamp() WHERE id='auto_read_token'`)
			case "token_expired":
				_, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id='auto_read_token'`)
			case "service_account_revoked":
				_, err = f.db.Exec(ctx, `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='push_bot'`)
			case "parent_disabled":
				_, err = f.db.Exec(ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`)
			case "token_natural_expiry":
				time.Sleep(400 * time.Millisecond)
			case "project_archived":
				_, err = f.db.Exec(ctx, `UPDATE projects SET archived_at=clock_timestamp() WHERE id='proj_platform'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				if mode == "owner_to_viewer" {
					if out.err != nil || out.p.CanManage || out.p.OwnerConfiguration != nil || out.p.OwnerAutomaticDeploymentOptions != nil {
						t.Fatal("owner loss disclosed private fields", out.err)
					}
				} else if !errors.Is(out.err, auth.ErrForbidden) {
					t.Fatal("stale authority disclosed snapshot", mode, out.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("GET did not join after authorization change")
			}
		})
	}
}

func TestPostgresAutomaticDeploymentPolicyReadWholeDeadlineAndSingleConnection(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	ctx := t.Context()
	cfg := f.db.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pg := store.AutomaticPolicyReadTestStore(pool)
	if _, err = pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID); err != nil {
		t.Fatal("single connection self-wait", err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	connection.Release()
	if err == nil || time.Since(started) > 3*time.Second || time.Since(started) < 1500*time.Millisecond {
		t.Fatal("whole GET poolwait deadline not enforced", time.Since(started), err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := blocker.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	}()
	if _, err = blocker.Exec(ctx, `LOCK TABLE resources IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	_, err = pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	if err == nil || time.Since(started) > 3*time.Second || time.Since(started) < 1500*time.Millisecond {
		t.Fatal("whole GET statement deadline not enforced", time.Since(started), err)
	}
}

func TestPostgresAutomaticDeploymentPolicyReadResourceProjectBinding(t *testing.T) {
	f := newAutomaticStoreFixture(t)
	if _, err := f.db.Exec(t.Context(), `INSERT INTO projects(id,name,description) VALUES('read-other-project','other','')`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ resource, project string }{{f.src.ResourceID, "read-other-project"}, {"missing-read-resource", f.src.ProjectID}} {
		rank, err := sqlcgen.New(f.db).AutoReadActorRank(t.Context(), sqlcgen.AutoReadActorRankParams{ActorID: "usr_bootstrap", ResourceID: scope.resource, ProjectID: scope.project})
		if err != nil || rank != 0 {
			t.Fatal("final disclosure authority was not bound to original resource/project", rank, err)
		}
	}
}
