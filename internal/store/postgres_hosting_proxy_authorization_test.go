package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

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
