package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

func assertStorageInitialOptions(t *testing.T, view domain.HostingStorageDeploymentOptions, resource domain.Resource, storage domain.HostingStorage) {
	t.Helper()
	if !view.Available || view.Reason != "initial_deployment" || view.ResourceID != resource.ID || view.ResourceGeneration != resource.Generation || view.StorageRef == nil || *view.StorageRef != storage.Reference() || view.PreviousHealthyOperationID != nil || view.HostPort != nil || view.ContainerPort != nil || view.Readiness != nil || view.StorageRouteContinuity != nil || view.Routes == nil || len(view.Routes) != 0 {
		t.Fatalf("initial advice invented settings or failed eligibility: %+v", view)
	}
}

func storageInitialFixture(t *testing.T) storageMaterializationFixture {
	t.Helper()
	pg, db, resource, _ := hostingStorageFixture(t)
	runner := "runner_owned_storage"
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(array_append(array_append(capabilities,$2),$3),$4) WHERE id=$1`, runner, domain.RunTypeImageDeploy, domain.HostingOwnedStorageCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	storage := saveHostingStorage(t, pg, resource.ID, hostingStorageInput())
	return storageMaterializationFixture{pg: pg, db: db, resource: resource, runner: runner, storage: storage}
}

func TestHostingStorageDeploymentPostgresInitialConstraints(t *testing.T) {
	for _, test := range []struct{ name, reason, statement string }{
		{"storage_capability", "runner_ineligible", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_owned_storage_v1')`},
		{"restore_without_state", "restore_hold", `INSERT INTO hosting_binding_restore_holds(binding_id) SELECT id FROM resource_bindings`},
		{"initialized_unknown_writer", "no_previous_healthy", `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,daemon_id) SELECT id,destination_id,18080,'prior_daemon' FROM resource_bindings`},
		{"initialized_restore", "restore_hold", `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,restore_admitted) SELECT id,destination_id,18080,false FROM resource_bindings`},
		{"malformed_healthy", "stale_storage", `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,healthy) SELECT id,destination_id,18080,'{}'::jsonb FROM resource_bindings`},
		{"queued_without_state", "busy", `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_initial_history',r.project_id,r.id,b.id,'history','sha256:'||repeat('a',64),'queued','usr_bootstrap' FROM resources r JOIN resource_bindings b ON b.resource_id=r.id`},
		{"unknown_without_state", "busy", `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_initial_history',r.project_id,r.id,b.id,'history','sha256:'||repeat('a',64),'reconciliation_required','usr_bootstrap' FROM resources r JOIN resource_bindings b ON b.resource_id=r.id`},
		{"canceled_history_without_state", "no_previous_healthy", `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_initial_history',r.project_id,r.id,b.id,'history','sha256:'||repeat('a',64),'canceled','usr_bootstrap' FROM resources r JOIN resource_bindings b ON b.resource_id=r.id`},
		{"failed_history_without_state", "no_previous_healthy", `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_initial_history',r.project_id,r.id,b.id,'history','sha256:'||repeat('a',64),'failed','usr_bootstrap' FROM resources r JOIN resource_bindings b ON b.resource_id=r.id`},
		{"port_history_without_state", "no_previous_healthy", `INSERT INTO hosting_release_slots(destination_id,host_port,binding_id) SELECT destination_id,18080,id FROM resource_bindings`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := storageInitialFixture(t)
			initial, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
			if err != nil {
				t.Fatal(err)
			}
			assertStorageInitialOptions(t, initial, f.resource, f.storage)
			if _, err = f.db.Exec(t.Context(), test.statement); err != nil {
				t.Fatal(err)
			}
			view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
			if err != nil || view.Available || view.Reason != test.reason || view.PreviousHealthyOperationID != nil || view.HostPort != nil || view.ContainerPort != nil || view.Readiness != nil || view.StorageRouteContinuity != nil {
				t.Fatalf("unsafe initial advice %+v %v", view, err)
			}
		})
	}
	t.Run("materialized_uncertain", func(t *testing.T) {
		f := newStorageMaterializationFixture(t)
		ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
			t.Fatal(err)
		}
		f.complete(t, "failed", true)
		view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
		if err != nil || view.Available || view.Reason != "busy" || view.PreviousHealthyOperationID != nil {
			t.Fatalf("uncertain materialized writer offered initial path %+v %v", view, err)
		}
	})
}

func TestHostingStorageDeploymentPostgresInitialAuthority(t *testing.T) {
	f := storageInitialFixture(t)
	ctx := t.Context()
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_viewer"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign viewer %v", err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_initial_viewer',$1,'usr_bootstrap','initial viewer','viewer','active'),('sa_initial_foreign','proj_security','usr_bootstrap','foreign initial viewer','viewer','active')`, f.resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	view, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_initial_viewer")
	if err != nil {
		t.Fatal(err)
	}
	assertStorageInitialOptions(t, view, f.resource, f.storage)
	if _, err = f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_initial_foreign"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign machine %v", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='sa_initial_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_initial_viewer"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked machine %v", err)
	}
	if _, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("initial_grant_revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_bootstrap"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked destination grant %v", err)
	}
}

func TestHostingStorageDeploymentPostgresInitialRepeatableSnapshot(t *testing.T) {
	f := storageInitialFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lock, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlock := cleanupContinuityTransaction(t, lock)
	if _, err = lock.Exec(ctx, `LOCK TABLE hosting_storage IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		view domain.HostingStorageDeploymentOptions
		err  error
	}
	done := make(chan result, 1)
	joined := false
	go func() {
		view, readErr := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_bootstrap")
		done <- result{view, readErr}
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("initial advice reader did not join")
			}
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='hosting_storage'::regclass AND NOT granted)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("initial reader never reached storage snapshot")
		case <-ticker.C:
		}
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES('hop_initial_concurrent',$1,$2,$3,'concurrent','sha256:'||repeat('a',64),'queued','usr_bootstrap')`, f.resource.ProjectID, f.resource.ID, f.storage.BindingID); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case got := <-done:
		joined = true
		if got.err != nil {
			t.Fatal(got.err)
		}
		assertStorageInitialOptions(t, got.view, f.resource, f.storage)
	case <-ctx.Done():
		t.Fatal("initial reader did not finish")
	}
	fresh, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
	if err != nil || fresh.Available || fresh.Reason != "busy" {
		t.Fatalf("fresh view ignored concurrent operation %+v %v", fresh, err)
	}
}

func TestHostingStorageDeploymentPostgresInitialPublicCreation(t *testing.T) {
	pg, db, _, host, destination := hostingExecutionFixture(t)
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(array_append(capabilities,$2),$3) WHERE id=$1`, host.RunnerID, domain.HostingOwnedStorageCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	service := proxyTokenApplication(t, pg)
	session, err := service.CreateSession(t.Context(), "admin@example.local", "admin")
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	request := func(method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			var marshalErr error
			raw, marshalErr = json.Marshal(body)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	var environment domain.ProjectEnvironment
	w := request(http.MethodPost, "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "fresh uploads"}, http.StatusCreated)
	if err = json.Unmarshal(w.Body.Bytes(), &environment); err != nil {
		t.Fatal(err)
	}
	var resource domain.Resource
	w = request(http.MethodPost, "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "fresh uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}}, http.StatusCreated)
	if err = json.Unmarshal(w.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/resources/" + resource.ID
	request(http.MethodPost, base+"/binding", app.ResourceBindingInput{DestinationID: destination.ID}, http.StatusCreated)
	var storage domain.HostingStorage
	w = request(http.MethodPost, base+"/storage", hostingStorageInput(), http.StatusCreated)
	if err = json.Unmarshal(w.Body.Bytes(), &storage); err != nil {
		t.Fatal(err)
	}
	var states int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_binding_state`).Scan(&states); err != nil || states != 0 {
		t.Fatalf("public creation initialized writer state: %d %v", states, err)
	}
	snapshot := `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM resources x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM resource_bindings x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_storage x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY binding_id) FROM hosting_binding_state x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY storage_id) FROM hosting_storage_materializations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM hosting_storage_operation_admissions x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x))::text`
	var before, after string
	if err = db.QueryRow(t.Context(), snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	lock, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	unlock := cleanupContinuityTransaction(t, lock)
	if _, err = lock.Exec(t.Context(), `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, resource.ID); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodGet, base+"/storage-deployment-options", nil, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("initial advice may be cached")
	}
	var view domain.HostingStorageDeploymentOptions
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	assertStorageInitialOptions(t, view, resource, storage)
	if err = db.QueryRow(t.Context(), snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("initial advice changed application rows")
	}
	unlock()
	// It is advisory. The existing POST still rejects missing user-selected
	// readiness/ports and rechecks current destination authority.
	request(http.MethodPost, base+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "initial-missing-settings"}, http.StatusBadRequest)
	if _, err = pg.RevokeDestinationProject(t.Context(), destination.ID, resource.ProjectID, destinationEvent("initial_public_revoke")); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, base+"/deployments", hostingDeploymentRequest(), http.StatusConflict)
	request(http.MethodGet, base+"/storage-deployment-options", nil, http.StatusForbidden)
	grantDestination(t, pg, destination.ID, resource.ProjectID, "initial_public_regrant")
	w = request(http.MethodPost, base+"/deployments", hostingDeploymentRequest(), http.StatusAccepted)
	var operation domain.HostingOperation
	if err = json.Unmarshal(w.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Snapshot.Storage != storage.Reference() || operation.Snapshot.ContainerPort != 80 || operation.Snapshot.HostPort != 18080 {
		t.Fatalf("initial request lost chosen settings or storage: %+v", operation)
	}
}

func TestHostingStorageDeploymentPostgresReadOnlySnapshot(t *testing.T) {
	f := newStorageContinuityFixture(t)
	ctx := t.Context()
	snapshot := `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_proxy_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM hosting_proxy_routes x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY binding_id) FROM hosting_binding_state x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM hosting_storage_route_continuities x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM hosting_storage_operation_admissions x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x))::text`
	var before, after string
	if err := f.db.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	lock, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := cleanupContinuityTransaction(t, lock)
	for _, statement := range []string{`SELECT id FROM resources WHERE id=$1 FOR UPDATE`, `SELECT binding_id FROM hosting_binding_state WHERE binding_id=(SELECT id FROM resource_bindings WHERE resource_id=$1) FOR UPDATE`, `SELECT id FROM hosting_proxies WHERE id='proxy_continuity' AND $1<>'' FOR UPDATE`} {
		if _, err = lock.Exec(ctx, statement, f.resource.ID); err != nil {
			t.Fatal(err)
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view, err := f.pg.HostingStorageDeploymentOptions(bounded, f.resource.ID, "usr_bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Available || view.Reason != "available" || view.StorageRef == nil || *view.StorageRef != f.storage.Reference() || view.PreviousHealthyOperationID == nil || *view.PreviousHealthyOperationID != f.op.ID || view.HostPort == nil || *view.HostPort != f.op.Snapshot.HostPort || view.Readiness == nil || view.StorageRouteContinuity == nil || view.StorageRouteContinuity.ExpectedLoadedProxyOperationID != f.loaded.ID || len(view.Routes) != 1 || view.Routes[0].ID != f.route.ID {
		t.Fatalf("qualified choices %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"daemon_id", "snapshot", "configuration", "credential_hash", "token_hash", "runner_id"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private %s: %s", private, encoded)
		}
	}
	if err = f.db.QueryRow(ctx, snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("read changed application rows")
	}
	cleanup()
	// Advice never reserves authority: a later desired-route edit invalidates POST.
	if _, err = f.db.Exec(ctx, `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID); err != nil {
		t.Fatal(err)
	}
	in := f.input("options-stale-submit")
	in.StorageRouteContinuity = view.StorageRouteContinuity
	if _, err = f.pg.RequestHostingDeployment(ctx, "hop_stale_options", f.resource.ID, "usr_bootstrap", in, destinationEvent("stale_options")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale advice admitted: %v", err)
	}
}

func TestHostingStorageDeploymentPostgresUnavailable(t *testing.T) {
	for _, test := range []struct{ name, reason, statement string }{
		{"capability", "runner_ineligible", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_owned_storage_v1')`},
		{"unsupported_image", "unsupported_source", `UPDATE resources SET source='{"kind":"image","image":{"reference":"https://invalid.example/image"}}'::jsonb,generation=generation+1`},
		{"occupied_slot", "busy", `UPDATE hosting_release_claims SET state='held'`},
		{"restore", "restore_hold", `UPDATE hosting_binding_state SET restore_admitted=false`},
		{"writer_hold", "busy", `UPDATE hosting_binding_state SET hold_operation_id=(SELECT id FROM hosting_operations LIMIT 1)`},
		{"no_healthy", "no_previous_healthy", `UPDATE hosting_binding_state SET healthy=NULL`},
		{"stopped_ledger", "stale_storage", `UPDATE hosting_retained_releases SET retained_running=false`},
		{"proxy_disabled", "proxy_unavailable", `UPDATE hosting_proxies SET enabled=false`},
		{"changed_target", "stale_routes", `UPDATE hosting_proxy_routes SET path_prefix='/changed'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			if _, err := f.db.Exec(t.Context(), test.statement); err != nil {
				t.Fatal(err)
			}
			view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
			if err != nil || view.Available || view.Reason != test.reason || view.StorageRouteContinuity != nil {
				t.Fatalf("%+v %v", view, err)
			}
		})
	}
	for _, name := range []string{"missing", "expired", "mismatched"} {
		t.Run("https_"+name, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			result := *f.loaded.Result
			result.Routes = append([]domain.HostingProxyRouteObservation(nil), result.Routes...)
			switch name {
			case "missing":
				result.Routes[0].HTTPSReady = false
			case "expired":
				result.Routes[0].Certificate.ExpiresAt = time.Now().Add(-time.Second)
			case "mismatched":
				result.Routes[0].Certificate.Hostname = "other.example.invalid"
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			// Model an old or corrupted observation only inside this isolated schema.
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cleanupContinuityTransaction(t, tx)
			if _, err = tx.Exec(t.Context(), `ALTER TABLE hosting_proxy_receipts DISABLE TRIGGER hosting_proxy_receipts_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), `UPDATE hosting_proxy_receipts SET body=$2,body_hash=$3 WHERE operation_id=$1 AND phase='complete'`, f.loaded.ID, body, domain.HostingDocumentHash(result)); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), `ALTER TABLE hosting_proxy_receipts ENABLE TRIGGER hosting_proxy_receipts_immutable`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
			if err != nil || view.Available || view.Reason != "https_unavailable" || view.StorageRouteContinuity != nil {
				t.Fatalf("%+v %v", view, err)
			}
		})
	}
}

func TestHostingStorageDeploymentPostgresRepeatableSnapshot(t *testing.T) {
	f := newStorageContinuityFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lock, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlock := cleanupContinuityTransaction(t, lock)
	if _, err = lock.Exec(ctx, `LOCK TABLE hosting_storage IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		view domain.HostingStorageDeploymentOptions
		err  error
	}
	done := make(chan result, 1)
	joined := false
	go func() {
		view, readErr := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_bootstrap")
		done <- result{view, readErr}
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("options reader did not join")
			}
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='hosting_storage'::regclass AND NOT granted)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reader never reached storage snapshot")
		case <-ticker.C:
		}
	}
	// The first scope/resource queries have established a snapshot. Subsequent
	// reads must see the same route and generation despite committed edits.
	if _, err = f.db.Exec(ctx, `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_proxy_routes SET path_prefix='/changed' WHERE id=$1`, f.route.ID); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case got := <-done:
		joined = true
		if got.err != nil || !got.view.Available || got.view.ResourceGeneration != f.resource.Generation || len(got.view.Routes) != 1 || got.view.Routes[0].PathPrefix != f.route.PathPrefix {
			t.Fatalf("mixed snapshot %+v %v", got.view, got.err)
		}
	case <-ctx.Done():
		t.Fatal("reader did not finish")
	}
	fresh, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
	if err != nil || fresh.ResourceGeneration != f.resource.Generation+1 || fresh.Available || fresh.Reason != "stale_routes" {
		t.Fatalf("fresh snapshot %+v %v", fresh, err)
	}
}

func TestHostingStorageDeploymentPostgresCurrentAuthority(t *testing.T) {
	f := newStorageContinuityFixture(t)
	ctx := t.Context()
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_viewer"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign viewer: %v", err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_storage_options',$1,'usr_bootstrap','storage options','viewer','active'),('sa_storage_foreign','proj_security','usr_bootstrap','foreign options','viewer','active')`, f.resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_storage_options"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_storage_foreign"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign machine: %v", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='sa_storage_options'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "sa_storage_options"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked machine: %v", err)
	}
	next := f.successor(t)
	for _, project := range []string{f.resource.ProjectID, "proj_security"} {
		sibling := createDestinationResource(t, f.pg, "options_scope_"+project, project)
		if _, err := f.pg.HostingStorageRouteContinuityStatus(ctx, sibling.ID, "usr_bootstrap", next.op.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign resource/project status: %v", err)
		}
	}
	if _, err := f.pg.HostingStorageRouteContinuityStatus(ctx, f.resource.ID, "sa_storage_foreign", next.op.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign status: %v", err)
	}
	if _, err := f.pg.HostingStorageRouteContinuityStatus(ctx, f.resource.ID, "usr_bootstrap", f.op.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ordinary operation: %v", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, f.op.Snapshot.DestinationID, f.resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.HostingStorageDeploymentOptions(ctx, f.resource.ID, "usr_bootstrap"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked grant options: %v", err)
	}
	if _, err := f.pg.HostingStorageRouteContinuityStatus(ctx, f.resource.ID, "usr_bootstrap", next.op.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked grant status: %v", err)
	}
}

func TestHostingStorageDeploymentPostgresOrdinaryAndPrerequisites(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
	if err != nil || view.Available || view.Reason != "busy" || view.PreviousHealthyOperationID != nil || view.HostPort != nil {
		t.Fatalf("in-flight first deployment %+v %v", view, err)
	}
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	f.op = f.complete(t, "succeeded", false)
	view, err = f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
	if err != nil || !view.Available || view.StorageRouteContinuity != nil || len(view.Routes) != 0 || view.HostPort == nil || *view.HostPort != f.op.Snapshot.HostPort {
		t.Fatalf("ordinary choices %+v %v", view, err)
	}
	// This is a fixed endpoint choice; no route admission is implied.
	in := hostingDeploymentRequest()
	in.IdempotencyKey = "ordinary-options"
	in.HostPort = *view.HostPort
	if _, err = f.pg.RequestHostingDeployment(t.Context(), "hop_options_ordinary", f.resource.ID, "usr_bootstrap", in, destinationEvent("options_ordinary")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		want   string
		change func(*testing.T, storageContinuityFixture)
	}{
		{"runtime", "runtime_configuration_unsupported", func(t *testing.T, f storageContinuityFixture) {
			t.Helper()
			runtimeSave(t, f.pg, f.resource.ID, runtimeInput(1, 0))
		}},
		{"foreign_reference", "foreign_port_reference", func(t *testing.T, f storageContinuityFixture) {
			t.Helper()
			if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_routes(id,proxy_id,project_id,resource_id,binding_id,grant_id,hostname,path_prefix,version,selected_release_operation_id,enabled,created_by) SELECT 'options_foreign_route',proxy_id,project_id,resource_id,binding_id,grant_id,'foreign.apps.nerocd.invalid',path_prefix,1,selected_release_operation_id,false,created_by FROM hosting_proxy_routes WHERE id=$1`, f.route.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_release_references(operation_id,route_id,release_operation_id) VALUES($1,'options_foreign_route',$2)`, f.loaded.ID, f.op.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled_own_route_still_loaded", "foreign_port_reference", func(t *testing.T, f storageContinuityFixture) {
			t.Helper()
			if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET enabled=false`); err != nil {
				t.Fatal(err)
			}
		}},
		{"own_reservation", "busy", func(t *testing.T, f storageContinuityFixture) { t.Helper(); f.successor(t) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			test.change(t, f)
			view, err := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
			if err != nil || view.Available || view.Reason != test.want || view.StorageRouteContinuity != nil {
				t.Fatalf("%+v %v", view, err)
			}
		})
	}
}

func TestHostingStorageDeploymentPostgresStatusHistorical(t *testing.T) {
	for _, outcome := range []string{"loaded", "proxy_failed", "unresolved", "released"} {
		t.Run(outcome, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			next := f.successor(t)
			check := func(state, reason string, loaded bool) domain.HostingStorageRouteContinuityStatus {
				t.Helper()
				view, err := f.pg.HostingStorageRouteContinuityStatus(t.Context(), f.resource.ID, "usr_bootstrap", next.op.ID)
				if err != nil || view.State != state || view.Reason != reason || (view.LoadedGeneration != nil) != loaded {
					t.Fatalf("status %+v %v", view, err)
				}
				return view
			}
			check("reserved", "pending", false)
			admitContinuityWriter(t, next)
			if outcome == "released" {
				_, err := f.pg.HostingComplete(t.Context(), f.runner, domain.HostingCompleteRequest{HostingAuthority: next.authority, ReceiptID: "options_restored", Result: domain.HostingResult{Outcome: "failed", Restored: true, PreviousContainerID: f.op.Result.ContainerID, DaemonID: next.op.Release.DaemonID, PlanHash: next.op.Plan.Hash}})
				if err != nil {
					t.Fatal(err)
				}
				check("released", "released", false)
				return
			}
			if outcome == "unresolved" {
				if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID); err != nil {
					t.Fatal(err)
				}
			}
			next.complete(t, "succeeded", false)
			if outcome == "unresolved" {
				check("unresolved", "reconciliation_required", false)
				options, readErr := f.pg.HostingStorageDeploymentOptions(t.Context(), f.resource.ID, "usr_bootstrap")
				if readErr != nil || options.Available || options.Reason != "continuity_unresolved" {
					t.Fatalf("unresolved choices %+v %v", options, readErr)
				}
				return
			}
			pending := check("proxy_pending", "pending", false)
			if pending.SuccessorProxyOperationID == "" {
				t.Fatal("successor missing")
			}
			if outcome == "proxy_failed" {
				a, op, preparation := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.op}, pending.SuccessorProxyOperationID)
				_, err := f.pg.RunnerProxyComplete(t.Context(), f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "options_proxy_failed", Result: domain.HostingProxyResult{Outcome: "refused", PlanHash: op.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID, LoadedGeneration: op.Plan.PreviousGeneration}})
				if err != nil {
					t.Fatal(err)
				}
				check("proxy_failed", "reconciliation_required", false)
				return
			}
			settleProxyGeneration(t, f.pg, pending.SuccessorProxyOperationID, f.runner, f.op.Release.DaemonID)
			loaded := check("loaded", "loaded_historical", true)
			// Current proxy state may subsequently change. The immutable receipt
			// remains historical evidence and never supplies current HTTPS health.
			if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxies SET enabled=false`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_restore_holds(proxy_id) VALUES('proxy_continuity')`); err != nil {
				t.Fatal(err)
			}
			again := check("loaded", "loaded_historical", true)
			if *again.LoadedGeneration != *loaded.LoadedGeneration {
				t.Fatal("historical generation changed")
			}
		})
	}
}
