package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

func TestPostgresRouteSwitchOptions(t *testing.T) {
	t.Run("snapshot_read_only_and_live_ledger", func(t *testing.T) {
		f := switchControlFixture(t)
		ctx := t.Context()
		var before string
		// Full application rows must remain unchanged, including timestamps and queues.
		snapshot := `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_proxy_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_proxy_routes x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM hosting_retained_releases x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x))::text`
		if err := f.db.QueryRow(ctx, snapshot).Scan(&before); err != nil {
			t.Fatal(err)
		}
		lock, err := f.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		if _, err = lock.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = lock.Exec(ctx, `SELECT id FROM users WHERE id='usr_bootstrap' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		view, err := f.pg.HostingRouteSwitchOptions(bounded, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if !view.Available || view.Reason != "available" || view.LoadedGeneration == nil || *view.LoadedGeneration != f.loaded.Generation || len(view.Routes) != 3 || len(view.Targets) != 2 {
			t.Fatalf("options %+v", view)
		}
		if view.Targets[0].OperationID != f.a.ID || view.Targets[0].Kind != "retained" || view.Targets[1].OperationID != f.b.ID || view.Targets[1].Kind != "current_healthy" {
			t.Fatalf("targets %+v", view.Targets)
		}
		encoded, _ := json.Marshal(view)
		for _, private := range []string{"daemon_id", "host_port", "snapshot", "configuration", "runner_id", "destination_id", "binding_id"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("private %s: %s", private, encoded)
			}
		}
		var after string
		if err := f.db.QueryRow(ctx, snapshot).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatal("GET mutated database")
		}
		if err := lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		// A historical successful release that is no longer running is not a choice.
		if _, err = f.db.Exec(ctx, `UPDATE hosting_retained_releases SET retained_running=false WHERE operation_id=$1`, f.b.ID); err != nil {
			t.Fatal(err)
		}
		view, err = f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Targets) != 1 || view.Targets[0].OperationID != f.a.ID {
			t.Fatalf("stopped targets %+v", view)
		}
		if _, err = f.db.Exec(ctx, `UPDATE hosting_retained_releases SET state='cleanup_pending',cleanup_operation_id=$1 WHERE operation_id=$1`, f.b.ID); err != nil {
			t.Fatal(err)
		}
		view, err = f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Targets) != 1 {
			t.Fatal(view)
		}
		view, err = f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "zzzz")
		if err != nil {
			t.Fatal(err)
		}
		if !view.Available || len(view.Targets) != 0 || view.NextAfterOperationID != nil {
			t.Fatalf("end page %+v", view)
		}
	})
	t.Run("authority_is_current_and_scoped", func(t *testing.T) {
		f := switchControlFixture(t)
		ctx := t.Context()
		if _, err := f.db.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('member_switch_viewer',$1,'usr_viewer','viewer')`, f.resource.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_viewer", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(ctx, `DELETE FROM project_members WHERE id='member_switch_viewer'`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_viewer", ""); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("removed member: %v", err)
		}
		if _, err := f.db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_switch_viewer',$1,'usr_bootstrap','switch','viewer','active')`, f.resource.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "sa_switch_viewer", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(ctx, `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='sa_switch_viewer'`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "sa_switch_viewer", ""); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("revoked SA: %v", err)
		}
		if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,token_hash,roles,status,created_by) VALUES('tok_switch','switch','switch-hash',ARRAY['system_admin'],'active','usr_bootstrap')`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "tok_switch", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id='tok_switch'`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "tok_switch", ""); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("expired legacy: %v", err)
		}
		if _, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "missing_actor", ""); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("unknown actor: %v", err)
		}
	})
	t.Run("old_agent_and_holds", func(t *testing.T) {
		f := switchControlFixture(t)
		ctx := t.Context()
		if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingProxyVerifiedSwitchCapability); err != nil {
			t.Fatal(err)
		}
		view, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if view.Available || view.Reason != "runner_ineligible" {
			t.Fatal(view)
		}
		if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingProxyVerifiedSwitchCapability); err != nil {
			t.Fatal(err)
		}
		op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_options_pending", "usr_bootstrap", f.input("options_pending"), destinationEvent("options_pending"))
		if err != nil {
			t.Fatal(err)
		}
		view, err = f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if view.Available || view.Reason != "busy" || view.PendingOperationID != op.ID {
			t.Fatal(view)
		}
		recovered, err := f.pg.HostingRouteSwitchOperation(ctx, f.resource.ID, "usr_bootstrap", op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Switch == nil || len(recovered.Switch.Routes) != 2 {
			t.Fatal(recovered)
		}
		if _, err := f.pg.HostingRouteSwitchOperation(ctx, f.resource.ID, "usr_bootstrap", f.loaded.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ordinary op recovery: %v", err)
		}
	})
	t.Run("bounded_keyset_pages", func(t *testing.T) {
		f := switchControlFixture(t)
		ctx := t.Context()
		// Clone valid immutable ledger fixtures with independent operation identities.
		for n := 0; n < 52; n++ {
			id := fmt.Sprintf("hop_options_%03d", n)
			if _, err := f.db.Exec(ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT $2,project_id,resource_id,binding_id,$2,request_hash,'succeeded',created_by FROM hosting_operations WHERE id=$1`, f.b.ID, id); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(ctx, `INSERT INTO hosting_retained_releases(operation_id,binding_id,destination_id,host_port,body,body_hash,state,retained_running) SELECT $2,binding_id,destination_id,host_port,jsonb_set(body,'{operation_id}',to_jsonb($2::text)),body_hash,'retained',true FROM hosting_retained_releases WHERE operation_id=$1`, f.b.ID, id); err != nil {
				t.Fatal(err)
			}
		}
		first, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Targets) != 50 || first.NextAfterOperationID == nil {
			t.Fatalf("first %+v", first)
		}
		second, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", *first.NextAfterOperationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Targets) != 4 || second.NextAfterOperationID != nil {
			t.Fatalf("second %+v", second)
		}
		if first.Targets[49].OperationID >= second.Targets[0].OperationID {
			t.Fatal("pagination repeated or unordered")
		}

		if _, err = f.db.Exec(ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_000_malformed',project_id,resource_id,binding_id,'hop_000_malformed',request_hash,'succeeded',created_by FROM hosting_operations WHERE id=$1`, f.b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec(ctx, `INSERT INTO hosting_retained_releases(operation_id,binding_id,destination_id,host_port,body,body_hash,state,retained_running) SELECT 'hop_000_malformed',binding_id,destination_id,host_port,jsonb_set(jsonb_set(body,'{operation_id}','"hop_000_malformed"'),'{release,image_id}','"private_invalid_image"'),body_hash,'retained',true FROM hosting_retained_releases WHERE operation_id=$1`, f.b.ID); err != nil {
			t.Fatal(err)
		}
		invalid, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Fatal(err)
		}
		if invalid.Available || invalid.Reason != "proxy_unavailable" || len(invalid.Targets) != 0 || invalid.NextAfterOperationID != nil {
			t.Fatalf("malformed ledger %+v", invalid)
		}
	})
}

func TestPostgresRouteSwitchReadSnapshotConsistency(t *testing.T) {
	f := switchControlFixture(t)
	ctx := t.Context()
	// Each writer transaction pairs generation and desired version. Even when the
	// predecessor becomes stale, GET must never combine two committed versions.
	if _, err := f.db.Exec(ctx, `UPDATE hosting_proxies SET desired_generation=1000 WHERE id='proxy_fixture'`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsFound := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 80; n++ {
			generation := int64(10 + n)
			tx, err := f.db.Begin(ctx)
			if err != nil {
				errorsFound <- err
				return
			}
			_, err = tx.Exec(ctx, `UPDATE hosting_proxies SET loaded_generation=$1 WHERE id='proxy_fixture'`, generation)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE hosting_proxy_routes SET version=$2 WHERE id=$1`, f.routes[0].ID, generation)
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				errorsFound <- err
				return
			}
		}
	}()
	for n := 0; n < 80; n++ {
		view, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
		if err != nil {
			t.Error(err)
			break
		}
		if view.LoadedGeneration != nil && *view.LoadedGeneration >= 10 {
			for _, route := range view.Routes {
				if route.ID == f.routes[0].ID && route.Version != *view.LoadedGeneration {
					t.Errorf("mixed snapshot generation=%d route=%+v", *view.LoadedGeneration, route)
				}
			}
		}
	}
	wg.Wait()
	select {
	case err := <-errorsFound:
		t.Fatal(err)
	default:
	}
}

func TestPostgresRouteSwitchProjectHTTP(t *testing.T) {
	f := switchControlFixture(t)
	ctx := t.Context()
	service := proxyTokenApplication(t, f.pg)
	if _, err := f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "member_switch_http", ProjectID: f.resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	admin, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := service.CreateSession(ctx, "viewer@example.local", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/resources/" + f.resource.ID + "/route-switches"
	for _, token := range []string{viewer.Token, admin.Token} {
		w := request(http.MethodGet, base, token, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("read %d %s", w.Code, w.Body)
		}
		if w = request(http.MethodGet, base+"?after_operation_id=bad%20id", token, ""); w.Code != 400 {
			t.Fatalf("bad cursor %d %s", w.Code, w.Body)
		}
	}
	input := f.input("http-switch")
	body, _ := json.Marshal(input)
	if w := request(http.MethodPost, base, viewer.Token, string(body)); w.Code != 403 {
		t.Fatalf("viewer wrote %d %s", w.Code, w.Body)
	}
	w := request(http.MethodPost, base, admin.Token, string(body))
	if w.Code != 202 {
		t.Fatalf("admin switch %d %s", w.Code, w.Body)
	}
	var initial domain.HostingProxyOperationView
	if err = json.Unmarshal(w.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Switch == nil || len(initial.Switch.Routes) != 2 {
		t.Fatalf("missing immutable summary %s", w.Body)
	}
	for _, token := range []string{viewer.Token, admin.Token} {
		w = request(http.MethodGet, base+"/"+initial.ID, token, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("recovery %d %s", w.Code, w.Body)
		}
		for _, private := range []string{"daemon_id", "host_port", "snapshot", "configuration", "runner_id", "destination_id", "binding_id", "proxy_id"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("leak %s %s", private, w.Body)
			}
		}
	}
	// A different resource in the same project does not gain switch history from
	// project membership or administrator authority.
	foreign := f.resource
	foreign.ID = "res_switch_other"
	foreign.Name = "other-resource"
	if _, err = f.pg.CreateResource(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	foreignBase := "/api/v1/resources/" + foreign.ID + "/route-switches/" + initial.ID
	for _, token := range []string{viewer.Token, admin.Token} {
		if w = request(http.MethodGet, foreignBase, token, ""); w.Code != 404 {
			t.Fatalf("foreign resource recovery %d %s", w.Code, w.Body)
		}
	}
	if _, err = f.db.Exec(ctx, `DELETE FROM project_members WHERE id='member_switch_http'`); err != nil {
		t.Fatal(err)
	}
	if w = request(http.MethodGet, base+"/"+initial.ID, viewer.Token, ""); w.Code != 403 {
		t.Fatalf("removed member read %d %s", w.Code, w.Body)
	}
}

func TestPostgresRouteSwitchForeignPendingIsGeneric(t *testing.T) {
	f := switchControlFixture(t)
	ctx := t.Context()
	second := createDestinationResource(t, f.pg, "options_second", "proj_security")
	grantDestination(t, f.pg, f.a.Snapshot.DestinationID, second.ProjectID, "options_second_destination_grant")
	var err error
	if _, err = f.pg.GrantHostingHostname(ctx, "proxy_fixture", "grant_options_second", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: second.ProjectID, Hostname: "security.nerocd.invalid"}, destinationEvent("options_second_hostname")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_options_second", ResourceID: second.ID, DestinationID: f.a.Snapshot.DestinationID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("options_second_bind")); err != nil {
		t.Fatal(err)
	}
	healthy := settleProxyImage(t, f.pg, second, f.server, "hop_options_second", 18082)
	route, err := f.pg.RequestHostingRoute(ctx, second.ID, "", "pop_options_second_route", "usr_bootstrap", domain.HostingRouteInput{Hostname: "security.nerocd.invalid", PathPrefix: "/second", ExpectedHealthyOperationID: healthy.ID, IdempotencyKey: "options-second-route"}, destinationEvent("options_second_route"))
	if err != nil {
		t.Fatal(err)
	}
	authority, op, prepared := admitSwitchOperation(t, f, route.OperationID)
	f.loaded, err = f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: authority, ReceiptID: "options_second_complete", Result: trustedProxyResult(op, prepared, "applied")})
	if err != nil {
		t.Fatal(err)
	}
	own, err := f.pg.HostingRouteSwitchOptions(ctx, second.ID, "usr_bootstrap", "")
	if err != nil || !own.Available || len(own.Routes) != 1 || len(own.Targets) != 1 || own.Targets[0].OperationID != healthy.ID {
		t.Fatalf("own scope %+v %v", own, err)
	}
	// A binding restore hold blocks this resource but discloses no hold identity.
	if _, err = f.db.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, healthy.Snapshot.BindingID); err != nil {
		t.Fatal(err)
	}
	held, err := f.pg.HostingRouteSwitchOptions(ctx, second.ID, "usr_bootstrap", "")
	if err != nil || held.Available || held.Reason != "restore_hold" || held.PendingOperationID != "" {
		t.Fatalf("own restore %+v %v", held, err)
	}
	foreignHold, err := f.pg.HostingRouteSwitchOptions(ctx, f.resource.ID, "usr_bootstrap", "")
	if err != nil || foreignHold.Reason != "busy" || foreignHold.PendingOperationID != "" {
		t.Fatalf("foreign restore %+v %v", foreignHold, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=true WHERE binding_id=$1`, healthy.Snapshot.BindingID); err != nil {
		t.Fatal(err)
	}
	pending, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_options_foreign_pending", "usr_bootstrap", f.input("foreign-pending"), destinationEvent("foreign_pending"))
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := f.pg.HostingRouteSwitchOptions(ctx, second.ID, "usr_bootstrap", "")
	if err != nil || hidden.Available || hidden.Reason != "busy" || hidden.PendingOperationID != "" {
		t.Fatalf("foreign pending %+v %v", hidden, err)
	}
	if _, err = f.pg.HostingRouteSwitchOperation(ctx, second.ID, "usr_bootstrap", pending.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign recovery %v", err)
	}
}
