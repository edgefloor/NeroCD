package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresProxyAuthorityRecheckedAfterOwnershipWait(t *testing.T) {
	for _, test := range []struct{ phase, lock, revoke string }{
		{"prepare", "destination", "expiry"},
		{"prepare", "proxy", "expiry"},
		{"prepare", "resource", "expiry"},
		{"admit", "proxy", "expiry"},
		{"admit", "resource", "expiry"},
		{"complete", "proxy", "expiry"},
		{"prepare", "resource", "capability"},
		{"admit", "resource", "capability"},
		{"complete", "proxy", "capability"},
	} {
		t.Run(test.phase+"_"+test.lock+"_"+test.revoke, func(t *testing.T) {
			pg, database, resource, server, destination, healthy := proxyControlFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_wait", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "lock-wait", Hostname: "wait.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("wait_route"))
			if err != nil {
				t.Fatal(err)
			}
			a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
			prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "wait_prepare", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
			if test.phase != "prepare" {
				if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
					t.Fatal(err)
				}
			}
			if test.phase == "complete" {
				if _, err = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a); err != nil {
					t.Fatal(err)
				}
			}
			before, err := pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			var initialTask, initialLease string
			if err = database.QueryRow(ctx, `SELECT r.status,l.status FROM run_leases l JOIN task_runs r ON r.id=l.run_id WHERE l.id=$1`, a.LeaseID).Scan(&initialTask, &initialLease); err != nil {
				t.Fatal(err)
			}
			blocker, err := database.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
			}()
			var blockerPID int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			switch test.lock {
			case "destination":
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, destination.ID)
			case "proxy":
				_, err = blocker.Exec(ctx, `SELECT id FROM hosting_proxies WHERE id='proxy_fixture' FOR UPDATE`)
			case "resource":
				_, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, resource.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.revoke == "expiry" {
				if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, a.LeaseID); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			var work sync.WaitGroup
			work.Go(func() {
				var err error
				switch test.phase {
				case "prepare":
					_, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared)
				case "admit":
					_, err = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a)
				case "complete":
					_, err = pg.RunnerProxyComplete(ctx, server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "wait_complete", Result: domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: op.Generation, PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: prepared.DaemonID, Routes: []domain.HostingProxyRouteObservation{{RouteID: route.ID, HTTPReady: true, Certificate: domain.HostingProxyCertificate{Hostname: route.Hostname, Status: "pending"}}}}})
				}
				done <- err
			})
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
				work.Wait()
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("operation did not wait on ownership lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if test.revoke == "capability" {
				if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply); err != nil {
					t.Fatal(err)
				}
			} else {
				for {
					var expired bool
					if err = database.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, a.LeaseID).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-ticker.C:
					}
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("authority lost during ownership wait still authorized a fresh mutation")
			}
			after, err := pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			p, err := pg.GetHostingProxy(ctx, "proxy_fixture")
			if err != nil {
				t.Fatal(err)
			}
			if after.State != before.State || after.MutationHold != before.MutationHold || after.CompleteReceipt != nil || (after.PreparedReceipt != nil) != (before.PreparedReceipt != nil) || p.LoadedGeneration != 1 {
				t.Fatalf("stale transaction changed receipt or ownership: before=%+v after=%+v proxy=%+v", before, after, p)
			}
			var task, lease string
			var admissions int
			if err = database.QueryRow(ctx, `SELECT r.status,l.status,(SELECT count(*) FROM hosting_proxy_admissions WHERE operation_id=$2) FROM run_leases l JOIN task_runs r ON r.id=l.run_id WHERE l.id=$1`, a.LeaseID, op.ID).Scan(&task, &lease, &admissions); err != nil {
				t.Fatal(err)
			}
			wantAdmissions := 0
			if test.phase == "complete" {
				wantAdmissions = 1
			}
			if task != initialTask || lease != initialLease || admissions != wantAdmissions {
				t.Fatalf("rejected transaction changed task, lease or admission: task=%s lease=%s admissions=%d", task, lease, admissions)
			}
		})
	}
}

func TestPostgresProxyFirstMutationRejectsCanceledOrRestoredAuthority(t *testing.T) {
	for _, loss := range []string{"cancel", "restore"} {
		for _, phase := range []string{"prepare", "admit", "complete"} {
			t.Run(loss+"_"+phase, func(t *testing.T) {
				pg, database, resource, server, _, healthy := proxyControlFixture(t)
				ctx := t.Context()
				route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_loss", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "authority-loss", Hostname: "loss.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("loss_route"))
				if err != nil {
					t.Fatal(err)
				}
				a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
				prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "loss_prepare", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
				if phase != "prepare" {
					if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "complete" {
					if _, err = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a); err != nil {
						t.Fatal(err)
					}
				}
				if loss == "cancel" {
					_, err = pg.CancelHostingProxyOperation(ctx, op.ID, "usr_bootstrap", destinationEvent("loss_cancel"))
				} else {
					_, err = database.Exec(ctx, `INSERT INTO hosting_proxy_restore_holds(proxy_id) VALUES('proxy_fixture')`)
				}
				if err != nil {
					t.Fatal(err)
				}
				switch phase {
				case "prepare":
					_, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared)
				case "admit":
					_, err = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a)
				case "complete":
					_, err = pg.RunnerProxyComplete(ctx, server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "loss_complete", Result: domain.HostingProxyResult{Outcome: "uncertain", PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: prepared.DaemonID}})
				}
				if err == nil {
					t.Fatal("canceled or restored authority published a first mutation")
				}
				after, err := pg.GetHostingProxyOperation(ctx, op.ID)
				if err != nil || after.CompleteReceipt != nil || after.MutationHold != (phase == "complete") || after.RestoreOwnershipHeld != (loss == "restore") || phase == "prepare" && after.PreparedReceipt != nil {
					t.Fatalf("authority loss changed receipts or ownership: %+v %v", after, err)
				}
			})
		}
	}
}

func TestPostgresProxyRequiresServerAdminAndControlProjectOwner(t *testing.T) {
	pg, database, resource, server, _, healthy := proxyControlFixture(t)
	ctx := t.Context()
	in := domain.HostingRouteInput{IdempotencyKey: "viewer-route", Hostname: "viewer.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}
	if _, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_viewer", "usr_viewer", in, destinationEvent("viewer_route")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer route mutation %v", err)
	}
	config := domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "runner_admin_enable"}
	if _, err := database.Exec(ctx, `UPDATE users SET global_role='runner_admin' WHERE id='usr_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ConfigureHostingProxy(ctx, "ignored", server.ID, "usr_viewer", config, destinationEvent("runner_admin_without_owner")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("runner admin bypassed control owner %v", err)
	}
	if _, err := pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "proxy_control_owner", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ConfigureHostingProxy(ctx, "ignored", server.ID, "usr_viewer", config, destinationEvent("runner_admin_with_owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_viewer'`); err != nil {
		t.Fatal(err)
	}
	config.IdempotencyKey = "owner_without_admin"
	if _, err := pg.ConfigureHostingProxy(ctx, "ignored", server.ID, "usr_viewer", config, destinationEvent("owner_without_admin")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("project owner bypassed server admin %v", err)
	}
	if _, err := pg.GrantHostingHostname(ctx, "proxy_fixture", "grant_cross_overlap", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: "proj_security", Hostname: "web.apps.nerocd.invalid"}, destinationEvent("cross_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross-project wildcard overlap %v", err)
	}
	if _, err := pg.GrantHostingHostname(ctx, "proxy_fixture", "grant_other_project", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: "proj_security", Hostname: "security.nerocd.invalid"}, destinationEvent("other_grant")); err != nil {
		t.Fatal(err)
	}
	in.Hostname = "security.nerocd.invalid"
	if _, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_cross_hostname", "usr_bootstrap", in, destinationEvent("cross_hostname")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("system admin project route consumed another project's grant %v", err)
	}
}

func TestPostgresProxyOldAgentCannotClaimOrPrepare(t *testing.T) {
	pg, database, resource, server, _, healthy := proxyControlFixture(t)
	ctx := t.Context()
	route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_capability", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "capability-route", Hostname: "capability.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("capability_route"))
	if err != nil {
		t.Fatal(err)
	}
	awaitProxyDispatch(t, pg, route.OperationID)
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	if claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); err == nil {
		t.Fatalf("agent without proxy capability claimed task: %+v", claim)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "capability_prepare", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64), PlanHash: op.Plan.Hash}}
	if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err == nil {
		t.Fatal("removed capability retained fresh preparation authority")
	}
	current, err := pg.GetHostingProxyOperation(ctx, op.ID)
	if err != nil || current.PreparedReceipt != nil || current.MutationHold {
		t.Fatalf("denial mutated preparation or ownership: %+v %v", current, err)
	}
}

func TestPostgresProxyGrantRevocationSerializesAdmission(t *testing.T) {
	pg, _, resource, server, _, healthy := proxyControlFixture(t)
	ctx := t.Context()
	route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_revoke_race", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "revoke-race", Hostname: "race.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("revoke_race_route"))
	if err != nil {
		t.Fatal(err)
	}
	a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
	prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "revoke_race_prepared", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
	if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var group sync.WaitGroup
	var admission domain.HostingProxyOperation
	var admissionErr, revokeErr error
	group.Go(func() {
		<-start
		admission, admissionErr = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a)
	})
	group.Go(func() {
		<-start
		_, revokeErr = pg.RevokeHostingHostname(ctx, "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", "revoke-race", destinationEvent("revoke_race_grant"))
	})
	close(start)
	group.Wait()
	if revokeErr != nil {
		t.Fatal(revokeErr)
	}
	p, err := pg.GetHostingProxy(ctx, "proxy_fixture")
	if err != nil {
		t.Fatal(err)
	}
	current, err := pg.GetHostingRoute(ctx, route.ID)
	if err != nil || current.Route.Enabled || current.Route.Version != route.Version+1 {
		t.Fatalf("revocation did not version and withdraw route: %+v %v", current, err)
	}
	op, err = pg.GetHostingProxyOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if admissionErr == nil {
		if !admission.MutationHold || !op.MutationHold || p.HoldOperationID != op.ID || op.State != "applying" || p.LoadedGeneration != 1 {
			t.Fatalf("admitted revocation race lost ownership: proxy=%+v operation=%+v", p, op)
		}
	} else if op.MutationHold || op.State != "superseded" || p.HoldOperationID != "" {
		t.Fatalf("revoked before admission but retained false effect authority: proxy=%+v operation=%+v error=%v", p, op, admissionErr)
	}
	if p.DesiredGeneration != op.Generation+1 {
		t.Fatalf("withdrawal generation missing: %+v", p)
	}
}

func proxyTokenApplication(t *testing.T, pg *store.PostgresStore) *app.Service {
	t.Helper()
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func proxyLegacyToken(t *testing.T, pg *store.PostgresStore, service *app.Service, id string, roles []string) (auth.Principal, string) {
	t.Helper()
	plaintext := "local-fixture-" + id
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(plaintext)))
	_, err := pg.CreateAPIToken(t.Context(), domain.APIToken{ID: id, Name: id, Kind: domain.TokenKindServiceAccount, TokenHash: hash, Roles: roles, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateSessionToken(t.Context(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != id || principal.Provider != domain.PrincipalAPIToken {
		t.Fatalf("unexpected authenticated identity: %+v", principal)
	}
	return principal, plaintext
}

func TestPostgresProxyAuthenticatedLegacyAdminToken(t *testing.T) {
	pg, database, resource, server, _, _ := proxyControlFixture(t)
	service := proxyTokenApplication(t, pg)
	principal, _ := proxyLegacyToken(t, pg, service, "tok_proxy_admin", []string{domain.RoleSystemAdmin})
	ctx := auth.WithPrincipal(t.Context(), principal)
	in := domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "legacy-admin-configure"}
	if _, err := service.ConfigureHostingProxy(ctx, server.ID, in); err != nil {
		t.Fatalf("authenticated legacy admin could not configure proxy: %v", err)
	}
	var audits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='hosting.proxy_generation_requested'`, principal.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("legacy actor audit count=%d err=%v", audits, err)
	}
}

func TestPostgresProxyLegacyTokenDoesNotInheritAuthority(t *testing.T) {
	pg, database, resource, server, _, _ := proxyControlFixture(t)
	service := proxyTokenApplication(t, pg)
	for _, roles := range [][]string{{domain.RoleRunnerAdmin}, {domain.RoleOwner}, {domain.RoleMaintainer}, {domain.RoleViewer}} {
		t.Run(strings.Join(roles, "_"), func(t *testing.T) {
			principal, _ := proxyLegacyToken(t, pg, service, "tok_deny_"+roles[0], roles)
			ctx := auth.WithPrincipal(t.Context(), principal)
			_, err := service.ConfigureHostingProxy(ctx, server.ID, domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "deny-token"})
			if !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("token inherited issuer ownership: %v", err)
			}
			// The repository independently refuses role strings and issuer membership.
			if _, err = pg.GrantHostingHostname(ctx, "proxy_fixture", "deny_grant", principal.ID, domain.HostingHostnameGrantInput{ProjectID: "proj_security", Hostname: "isolated.nerocd.invalid"}, destinationEvent("deny_grant")); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("repository granted cross-project authority: %v", err)
			}
		})
	}
	for _, loss := range []string{"expired", "revoked", "roles_removed"} {
		t.Run(loss, func(t *testing.T) {
			principal, plaintext := proxyLegacyToken(t, pg, service, "tok_"+loss, []string{domain.RoleSystemAdmin})
			query := `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`
			if loss == "revoked" {
				query = `UPDATE api_tokens SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`
			}
			if loss == "roles_removed" {
				query = `UPDATE api_tokens SET roles=ARRAY['viewer']::text[] WHERE id=$1`
			}
			if _, err := database.Exec(t.Context(), query, principal.ID); err != nil {
				t.Fatal(err)
			}
			if loss != "roles_removed" {
				if _, err := service.AuthenticateSessionToken(t.Context(), plaintext); !errors.Is(err, auth.ErrUnauthenticated) {
					t.Fatalf("lost credential authenticated: %v", err)
				}
			}
			// A previously authenticated request cannot carry stale global authority.
			if _, err := service.DisableHostingProxy(auth.WithPrincipal(t.Context(), principal), server.ID, domain.HostingProxyMutationInput{IdempotencyKey: loss}); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("stale token authorized mutation: %v", err)
			}
		})
	}
}

func TestPostgresProxyManagedTokenKeepsScopedServiceAccountAuthority(t *testing.T) {
	pg, database, resource, server, _, healthy := proxyControlFixture(t)
	service := proxyTokenApplication(t, pg)
	plaintext := "managed-proxy-test-credential"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(plaintext)))
	now, expiry := time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	account := domain.ServiceAccount{ID: "sa_proxy_scoped", ProjectID: resource.ProjectID, OwnerUserID: "usr_bootstrap", Name: "scoped", Role: domain.RoleMaintainer, Status: "active", CreatedAt: now, UpdatedAt: now}
	if _, err := pg.CreateServiceAccount(t.Context(), account, domain.APIToken{ID: "tok_managed_proxy", Name: "managed", Kind: domain.TokenKindServiceAccount, TokenHash: hash, Roles: []string{}, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: &expiry}, destinationEvent("managed_proxy")); err != nil {
		t.Fatal(err)
	}
	// Persisted managed credentials cannot acquire legacy global-role strings.
	if _, err := database.Exec(t.Context(), `UPDATE api_tokens SET roles=ARRAY['system_admin']::text[] WHERE id='tok_managed_proxy'`); err == nil {
		t.Fatal("managed scope constraint accepted global-role strings")
	}
	principal, err := service.AuthenticateSessionToken(t.Context(), plaintext)
	if err != nil || principal.ID != account.ID || principal.Provider != domain.PrincipalServiceAccount || len(principal.Roles) != 0 {
		t.Fatalf("managed identity changed: %+v %v", principal, err)
	}
	ctx := auth.WithPrincipal(t.Context(), principal)
	if _, err = service.DisableHostingProxy(ctx, server.ID, domain.HostingProxyMutationInput{IdempotencyKey: "managed-admin"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("managed credential became server admin: %v", err)
	}
	if _, err = pg.SetHostingProxyEnabled(ctx, "proxy_fixture", "tok_managed_proxy", "managed-row", false, destinationEvent("managed_row")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("managed token ID entered legacy branch: %v", err)
	}
	in := domain.HostingRouteInput{IdempotencyKey: "managed-route", Hostname: "managed.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}
	if _, err = service.RequestHostingRoute(ctx, resource.ID, in); err != nil {
		t.Fatalf("scoped maintainer route denied: %v", err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_proxy_other','proj_security','usr_bootstrap','other','owner','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RequestHostingRoute(ctx, resource.ID, "", "pop_cross_account", "sa_proxy_other", in, destinationEvent("cross_account")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("scoped account crossed projects: %v", err)
	}
	if _, err = pg.SetHostingProxyEnabled(ctx, "proxy_fixture", server.RunnerID, "runner-admin", false, destinationEvent("runner_admin")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("runner identity entered legacy branch: %v", err)
	}
	if _, err = database.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RequestHostingRoute(ctx, resource.ID, "", "pop_disabled_owner", account.ID, in, destinationEvent("disabled_owner")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("disabled owner retained scoped mutation: %v", err)
	}
}

func proxyMutationCounts(t *testing.T, ctx context.Context, database *pgxpool.Pool) [5]int64 {
	t.Helper()
	var counts [5]int64
	if err := database.QueryRow(ctx, `SELECT desired_generation,(SELECT count(*) FROM hosting_proxy_operations),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM river_job),(SELECT count(*) FROM hosting_proxy_routes) FROM hosting_proxies WHERE id='proxy_fixture'`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4]); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestPostgresProxyLegacyTokenExpiryRollsBackAfterWait(t *testing.T) {
	for _, lock := range []string{"destination", "proxy", "resource", "audit", "replay"} {
		t.Run(lock, func(t *testing.T) {
			pg, database, resource, _, destination, healthy := proxyControlFixture(t)
			service := proxyTokenApplication(t, pg)
			principal, _ := proxyLegacyToken(t, pg, service, "tok_wait", []string{domain.RoleSystemAdmin})
			ctx, cancel := context.WithTimeout(auth.WithPrincipal(t.Context(), principal), 10*time.Second)
			defer cancel()
			in := domain.HostingRouteInput{IdempotencyKey: "expiry-wait", Hostname: "wait-token.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}
			if lock == "replay" {
				route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_token_wait", principal.ID, in, destinationEvent("token_wait"))
				if err != nil {
					t.Fatal(err)
				}
				settleProxyGeneration(t, pg, route.OperationID, "runner_image_host", healthy.Release.DaemonID)
			}
			before := proxyMutationCounts(t, ctx, database)
			blocker, err := database.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
			}()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			switch lock {
			case "destination":
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, destination.ID)
			case "proxy", "replay":
				_, err = blocker.Exec(ctx, `SELECT id FROM hosting_proxies WHERE id='proxy_fixture' FOR UPDATE`)
			case "resource":
				_, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, resource.ID)
			case "audit":
				_, err = database.Exec(ctx, `CREATE FUNCTION token_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='tok_wait' THEN PERFORM pg_advisory_xact_lock(982744); END IF;RETURN NEW;END $$;CREATE TRIGGER token_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION token_audit_wait()`)
				if err == nil {
					_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(982744)`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, principal.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			var group sync.WaitGroup
			group.Go(func() {
				_, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_token_wait", principal.ID, in, destinationEvent("token_wait"))
				done <- err
			})
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
				group.Wait()
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("mutation did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			for {
				var expired bool
				if err = database.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`, principal.ID).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("expired mutation result: %v", err)
			}
			if after := proxyMutationCounts(t, ctx, database); after != before {
				t.Fatalf("expired mutation committed generation/operation/audit/job/route: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPostgresProxyLegacyTokenRevocationSerializesMutation(t *testing.T) {
	pg, database, resource, _, destination, healthy := proxyControlFixture(t)
	service := proxyTokenApplication(t, pg)
	principal, _ := proxyLegacyToken(t, pg, service, "tok_revoke_wait", []string{domain.RoleSystemAdmin})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = blocker.Rollback(cleanup)
	}()
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, destination.ID); err != nil {
		t.Fatal(err)
	}
	done, revoke := make(chan error, 1), make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		_, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_revocation_wait", principal.ID, domain.HostingRouteInput{IdempotencyKey: "revoke-wait", Hostname: "revoke-token.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("revoke_wait"))
		done <- err
	})
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = blocker.Rollback(cleanup)
		group.Wait()
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("mutation did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	group.Go(func() { _, err := pg.RevokeAPIToken(ctx, principal.ID, time.Now().UTC()); revoke <- err })
	// The mutation's SHARE lock must block the revocation writer until commit.
	for {
		var waiting bool
		if err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query LIKE '%UPDATE api_tokens SET status =%' AND cardinality(pg_blocking_pids(pid))>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-revoke:
			t.Fatalf("revocation bypassed authorization lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-revoke; err != nil {
		t.Fatal(err)
	}
	if _, err = pg.SetHostingProxyEnabled(ctx, "proxy_fixture", principal.ID, "after-revoked", false, destinationEvent("after_revoked")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("committed revocation did not fence later mutation: %v", err)
	}
}
