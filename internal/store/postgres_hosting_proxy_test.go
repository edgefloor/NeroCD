package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func proxyControlFixture(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, domain.Resource, domain.Server, domain.Destination, domain.HostingOperation) {
	t.Helper()
	pg, database, resource, server, destination := hostingExecutionFixture(t)
	if _, err := database.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(array_append(capabilities,$2),$3) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	healthy := settleProxyImage(t, pg, resource, server, "hop_proxy_healthy", 18080)
	in := domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "configure-proxy"}
	if _, err := pg.ConfigureHostingProxy(t.Context(), "proxy_fixture", server.ID, "usr_bootstrap", in, destinationEvent("configure_proxy")); err != nil {
		t.Fatal(err)
	}
	var initial string
	if err := database.QueryRow(t.Context(), `SELECT id FROM hosting_proxy_operations WHERE proxy_id='proxy_fixture' AND generation=1`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	settleProxyGeneration(t, pg, initial, server.RunnerID, healthy.Release.DaemonID)
	if _, err := pg.GrantHostingHostname(t.Context(), "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: resource.ProjectID, Hostname: "apps.nerocd.invalid", Wildcard: true}, destinationEvent("grant_proxy_hostname")); err != nil {
		t.Fatal(err)
	}
	return pg, database, resource, server, destination, healthy
}

func settleProxyGeneration(t *testing.T, pg *store.PostgresStore, id, runner, daemon string) domain.HostingProxyOperation {
	t.Helper()
	a, op := claimProxyPlan(t, pg, id, runner)
	prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepare_" + id, HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: daemon, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
	if _, err := pg.RunnerProxyPrepared(t.Context(), runner, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.RunnerProxyApplyBegin(t.Context(), runner, a); err != nil {
		t.Fatal(err)
	}
	observations := make([]domain.HostingProxyRouteObservation, 0, len(op.Snapshot.Routes))
	for _, route := range op.Snapshot.Routes {
		observations = append(observations, domain.HostingProxyRouteObservation{RouteID: route.RouteID, HTTPReady: true, Certificate: domain.HostingProxyCertificate{Hostname: route.Hostname, Status: "pending"}})
	}
	finished, err := pg.RunnerProxyComplete(t.Context(), runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + id, Result: domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: op.Generation, PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: daemon, ContainerID: strings.Repeat("d", 64), Routes: observations}})
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func settleProxyImage(t *testing.T, pg *store.PostgresStore, resource domain.Resource, server domain.Server, id string, port int) domain.HostingOperation {
	t.Helper()
	ctx := t.Context()
	in := hostingDeploymentRequest()
	in.ExpectedGeneration = resource.Generation
	in.IdempotencyKey = id
	in.HostPort = port
	op, err := pg.RequestHostingDeployment(ctx, id, resource.ID, "usr_bootstrap", in, destinationEvent(id))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + id, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func awaitProxyDispatch(t *testing.T, pg *store.PostgresStore, id string) domain.HostingProxyOperation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		op, err := pg.GetHostingProxyOperation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.RunID != "" {
			return op
		}
		if op.State != "queued" {
			t.Fatalf("operation not queued: %+v", op)
		}
		select {
		case <-ctx.Done():
			t.Fatal("proxy dispatch timeout")
		case <-ticker.C:
		}
	}
}

func claimProxyPlan(t *testing.T, pg *store.PostgresStore, id, runner string) (domain.HostingAuthority, domain.HostingProxyOperation) {
	t.Helper()
	awaitProxyDispatch(t, pg, id)
	claim, err := pg.ClaimRun(t.Context(), runner, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err := pg.RunnerProxyPlan(t.Context(), runner, a)
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != id || claim.Run.RunSpec.Type != domain.RunTypeProxyApply || domain.ValidateProxyApplyTask(claim.Run) != nil {
		t.Fatalf("wrong internal task: %+v", claim.Run)
	}
	return a, op
}

func TestPostgresProxyRouteDispatchAndFencedSettlement(t *testing.T) {
	pg, database, resource, server, _, healthy := proxyControlFixture(t)
	ctx := t.Context()
	in := domain.HostingRouteInput{ExpectedRouteVersion: 0, IdempotencyKey: "route-api", Hostname: "web.apps.nerocd.invalid", PathPrefix: "/api", ExpectedHealthyOperationID: healthy.ID}
	route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_route_first", "usr_bootstrap", in, destinationEvent("request_proxy_route"))
	if err != nil {
		t.Fatal(err)
	}
	if route.OperationID != "pop_route_first" || route.Version != 1 {
		t.Fatalf("route result %+v", route)
	}
	replayed, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_route_replay", "usr_bootstrap", in, destinationEvent("request_proxy_route_replay"))
	if err != nil || replayed.OperationID != route.OperationID {
		t.Fatalf("route idempotency %+v %v", replayed, err)
	}
	a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
	for _, test := range []struct {
		name, runner string
		mutate       func(*domain.HostingAuthority)
	}{
		{"runner", "runner_foreign", func(*domain.HostingAuthority) {}},
		{"operation", server.RunnerID, func(a *domain.HostingAuthority) { a.OperationID = "pop_foreign" }},
		{"run", server.RunnerID, func(a *domain.HostingAuthority) { a.RunID = "proxy_pop_foreign" }},
		{"lease", server.RunnerID, func(a *domain.HostingAuthority) { a.LeaseID = "lease_foreign" }},
		{"attempt", server.RunnerID, func(a *domain.HostingAuthority) { a.Attempt++ }},
		{"fence", server.RunnerID, func(a *domain.HostingAuthority) { a.Fence += "wrong" }},
	} {
		t.Run("reject_mismatched_"+test.name, func(t *testing.T) {
			wrong := a
			test.mutate(&wrong)
			if _, err := pg.RunnerProxyPlan(ctx, test.runner, wrong); err == nil {
				t.Fatal("mismatched exact runner authority disclosed a whole proxy plan")
			}
			if _, err := pg.RunnerProxyApplyBegin(ctx, test.runner, wrong); err == nil {
				t.Fatal("mismatched exact runner authority admitted effects")
			}
		})
	}
	if op.Plan.PreviousGeneration != 1 || len(op.Snapshot.Routes) != 1 || op.Snapshot.Routes[0].HostPort != 18080 {
		t.Fatalf("wrong derived plan %+v", op.Plan)
	}
	prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "proxy_prepared", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
	for _, mutate := range []func(*domain.HostingProxyPreparedRequest){
		func(in *domain.HostingProxyPreparedRequest) { in.EngineHash = strings.Repeat("x", 64) },
		func(in *domain.HostingProxyPreparedRequest) { in.StaticHash = "private-key-material" },
		func(in *domain.HostingProxyPreparedRequest) { in.DaemonID += "\tforeign" },
	} {
		invalid := prepared
		mutate(&invalid)
		if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, invalid); err == nil {
			t.Fatal("malformed engine identity was pinned")
		}
	}
	if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
		t.Fatalf("prepared replay %v", err)
	}
	admitted, err := pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a)
	if err != nil || !admitted.MutationHold {
		t.Fatalf("proxy admission %+v %v", admitted, err)
	}
	if _, err = pg.CancelRunRequest(ctx, a.RunID, time.Now().UTC(), domain.RunLog{}, destinationEvent("generic_proxy_cancel")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generic proxy cancel bypass %v", err)
	}
	result := domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: op.Generation, PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: prepared.DaemonID, ContainerID: strings.Repeat("d", 64), Routes: []domain.HostingProxyRouteObservation{{RouteID: route.ID, HTTPReady: true, HTTPSReady: true, Certificate: domain.HostingProxyCertificate{Hostname: route.Hostname, Issuer: "fixture", Fingerprint: strings.Repeat("e", 64), ExpiresAt: time.Now().Add(time.Hour), Status: "trusted"}}}}
	request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "proxy_complete", Result: result}
	for _, mutate := range []func(*domain.HostingProxyResult){
		func(in *domain.HostingProxyResult) { in.Routes[0].Certificate.Fingerprint = "private-key-material" },
		func(in *domain.HostingProxyResult) { in.Routes[0].Certificate.Status = "self_signed" },
		func(in *domain.HostingProxyResult) { in.Routes[0].Certificate.ExpiresAt = time.Now().Add(-time.Hour) },
		func(in *domain.HostingProxyResult) { in.ContainerID = "foreign/unbounded-container" },
	} {
		invalid := request
		invalid.Result.Routes = append([]domain.HostingProxyRouteObservation(nil), result.Routes...)
		mutate(&invalid.Result)
		if _, err = pg.RunnerProxyComplete(ctx, server.RunnerID, invalid); err == nil {
			t.Fatal("invalid public TLS/identity evidence was committed")
		}
	}
	finished, err := pg.RunnerProxyComplete(ctx, server.RunnerID, request)
	if err != nil || finished.MutationHold || finished.State != "applied" {
		t.Fatalf("proxy settle %+v %v", finished, err)
	}
	if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RunnerProxyComplete(ctx, server.RunnerID, request); err != nil {
		t.Fatalf("committed replay after authority ended %v", err)
	}
	observed, err := pg.GetHostingRoute(ctx, route.ID)
	if err != nil || !observed.Route.HTTPReady || !observed.Route.HTTPSReady {
		t.Fatalf("own route status %+v %v", observed, err)
	}
}

func TestPostgresProxyConcurrentProjectsPreserveWholeSnapshot(t *testing.T) {
	pg, _, first, server, destination, firstHealthy := proxyControlFixture(t)
	ctx := t.Context()
	second := createDestinationResource(t, pg, "proxy_second", "proj_security")
	grantDestination(t, pg, destination.ID, second.ProjectID, "proxy_second_destination_grant")
	if _, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_proxy_second", ResourceID: second.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("proxy_second_binding")); err != nil {
		t.Fatal(err)
	}
	secondHealthy := settleProxyImage(t, pg, second, server, "hop_proxy_second", 18081)
	if _, err := pg.GrantHostingHostname(ctx, "proxy_fixture", "grant_proxy_second", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: second.ProjectID, Hostname: "security.nerocd.invalid"}, destinationEvent("proxy_second_hostname")); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		route domain.HostingRoute
		err   error
	}
	answers := make(chan answer, 2)
	var requests sync.WaitGroup
	for index, resource := range []domain.Resource{first, second} {
		requests.Go(func() {
			hostname := "web.apps.nerocd.invalid"
			healthy := firstHealthy.ID
			if index == 1 {
				hostname = "security.nerocd.invalid"
				healthy = secondHealthy.ID
			}
			route, err := pg.RequestHostingRoute(ctx, resource.ID, "", []string{"pop_project_first", "pop_project_second"}[index], "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "concurrent-route", Hostname: hostname, PathPrefix: "/api", ExpectedHealthyOperationID: healthy}, destinationEvent("concurrent_"+resource.ID))
			answers <- answer{route, err}
		})
	}
	requests.Wait()
	var latest domain.HostingProxyOperation
	for range 2 {
		answer := <-answers
		if answer.err != nil {
			t.Fatal(answer.err)
		}
		op, err := pg.GetHostingProxyOperation(ctx, answer.route.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Generation > latest.Generation {
			latest = op
		}
	}
	latest = awaitProxyDispatch(t, pg, latest.ID)
	if len(latest.Snapshot.Routes) != 2 || latest.Plan.PreviousGeneration != 1 {
		t.Fatalf("concurrent update lost project or predecessor %+v", latest)
	}
	seen := map[string]uint16{}
	for _, target := range latest.Snapshot.Routes {
		seen[target.ProjectID] = target.HostPort
	}
	if seen[first.ProjectID] != 18080 || seen[second.ProjectID] != 18081 {
		t.Fatalf("wrong upstream ownership %+v", seen)
	}
}

func TestPostgresProxyStaleReportAndUncertainEffectsKeepHold(t *testing.T) {
	for _, mode := range []string{"expired", "uncertain", "rolled_back"} {
		t.Run(mode, func(t *testing.T) {
			pg, database, resource, server, _, healthy := proxyControlFixture(t)
			ctx := t.Context()
			route, err := pg.RequestHostingRoute(ctx, resource.ID, "", "pop_effects", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "effects", Hostname: "effects.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("effects_route"))
			if err != nil {
				t.Fatal(err)
			}
			a, op := claimProxyPlan(t, pg, route.OperationID, server.RunnerID)
			prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "effects_prepared", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: healthy.Release.DaemonID, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64), PlanHash: op.Plan.Hash}}
			if _, err = pg.RunnerProxyPrepared(ctx, server.RunnerID, prepared); err != nil {
				t.Fatal(err)
			}
			if _, err = pg.RunnerProxyApplyBegin(ctx, server.RunnerID, a); err != nil {
				t.Fatal(err)
			}
			result := domain.HostingProxyResult{Outcome: "uncertain", LoadedGeneration: op.Plan.PreviousGeneration, PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: prepared.DaemonID}
			if mode == "expired" {
				if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rolled_back" {
				result.Outcome = "rolled_back"
			}
			_, reportErr := pg.RunnerProxyComplete(ctx, server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "effects_result", Result: result})
			if mode == "expired" {
				if reportErr == nil {
					t.Fatal("fresh stale proxy result accepted")
				}
			} else if reportErr != nil {
				t.Fatal(reportErr)
			}
			observed, err := pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rolled_back" {
				if observed.MutationHold || observed.State != "rolled_back" {
					t.Fatalf("verified rollback held %+v", observed)
				}
			} else if !observed.MutationHold {
				t.Fatalf("unresolved effects released %+v", observed)
			}
			p, err := pg.GetHostingProxy(ctx, "proxy_fixture")
			if err != nil || p.LoadedGeneration != 1 {
				t.Fatalf("failed candidate changed loaded generation %+v %v", p, err)
			}
		})
	}
}

func TestPostgresProxyControlProjectCannotBeArchived(t *testing.T) {
	pg, database, resource, _, _, _ := proxyControlFixture(t)
	ctx := t.Context()
	for _, audited := range []bool{false, true} {
		var options []store.MutationOption
		if audited {
			options = append(options, store.WithAudit(destinationEvent("archive_control_project")))
		}
		if _, err := pg.ArchiveProject(ctx, resource.ProjectID, time.Now().UTC(), options...); !errors.Is(err, store.ErrHostingProxyControlProject) {
			t.Fatalf("audited=%v archived proxy authority: %v", audited, err)
		}
	}
	if _, err := pg.SetHostingProxyEnabled(ctx, "proxy_fixture", "usr_bootstrap", "disable-before-archive", false, destinationEvent("disable_before_archive")); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ArchiveProject(ctx, resource.ProjectID, time.Now().UTC()); !errors.Is(err, store.ErrHostingProxyControlProject) {
		t.Fatalf("disabled proxy lost immutable control authority %v", err)
	}
	var archived bool
	var audits int
	if err := database.QueryRow(ctx, `SELECT (SELECT archived_at IS NOT NULL FROM projects WHERE id=$1),(SELECT count(*) FROM audit_events WHERE id='aud_destination_archive_control_project')`, resource.ProjectID).Scan(&archived, &audits); err != nil || archived || audits != 0 {
		t.Fatalf("failed archive was not atomic archived=%v audits=%d err=%v", archived, audits, err)
	}
}

func TestPostgresProxyConfigurationSerializesProjectArchive(t *testing.T) {
	for round := 0; round < 3; round++ {
		t.Run(strings.Repeat("x", round+1), func(t *testing.T) {
			pg, database, resource, server, _ := hostingExecutionFixture(t)
			ctx := t.Context()
			if _, err := database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.RunTypeProxyApply); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var requests sync.WaitGroup
			var configureErr, archiveErr error
			requests.Go(func() {
				<-start
				_, configureErr = pg.ConfigureHostingProxy(ctx, "proxy_archive_race", server.ID, "usr_bootstrap", domain.HostingProxyInput{ControlProjectID: resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "race-archive"}, destinationEvent("race_archive_configure"))
			})
			requests.Go(func() {
				<-start
				_, archiveErr = pg.ArchiveProject(ctx, resource.ProjectID, time.Now().UTC(), store.WithAudit(destinationEvent("race_archive")))
			})
			close(start)
			requests.Wait()
			if configureErr == nil && archiveErr == nil {
				t.Fatal("configuration and archive both committed")
			}
			var archived, configured bool
			if err := database.QueryRow(ctx, `SELECT (SELECT archived_at IS NOT NULL FROM projects WHERE id=$1),EXISTS(SELECT 1 FROM hosting_proxies WHERE control_project_id=$1)`, resource.ProjectID).Scan(&archived, &configured); err != nil {
				t.Fatal(err)
			}
			if archived && configured || !archived && !configured {
				t.Fatalf("authority orphaned or neither outcome committed archive=%v proxy=%v configErr=%v archiveErr=%v", archived, configured, configureErr, archiveErr)
			}
			if configured && !errors.Is(archiveErr, store.ErrHostingProxyControlProject) {
				t.Fatalf("archive conflict not explicit %v", archiveErr)
			}
		})
	}
}
