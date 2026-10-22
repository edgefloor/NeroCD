package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
