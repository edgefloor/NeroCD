package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func runtimeDeliveryFixture(t *testing.T, local bool) (runtimeAdmissionFixture, domain.HostingOperation, domain.HostingRuntimeEnvironmentRequest) {
	t.Helper()
	f := newRuntimeAdmissionFixture(t, local)
	f.capabilities(t, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability)
	item, record, audit := managedFixture(t, "runtime_delivery_secret", 1)
	var err error
	record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte("private-runtime-value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	in := runtimeInput(1, 0)
	in.Entries = append(in.Entries, domain.HostingRuntimeEntry{Name: "SECRET", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 1}})
	runtimeSave(t, f.pg, f.resource, in)
	op, err := f.request(t, "runtime-delivery", "usr_bootstrap", 1)
	if err != nil {
		t.Fatal(err)
	}
	op, a, _ := f.prepare(t, op)
	return f, op, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: op.Plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("a", 32)}
}

func denyRuntimeDelivery(t *testing.T, f runtimeAdmissionFixture, in domain.HostingRuntimeEnvironmentRequest) {
	t.Helper()
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, in)
	if err == nil || got.ProjectID != "" || len(got.Records) > 0 || !got.Window.UseUntil.IsZero() {
		got.Close()
		t.Fatal("denied request returned authorization", err)
	}
	got.Close()
}

func TestPostgresRuntimeDeliveryRejectsAttemptOverflow(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	for _, attempt := range []int64{math.MaxInt32 + 1, 1<<32 + 1} {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			if int64(int(attempt)) != attempt {
				t.Skip("attempt is not representable by this architecture's int")
			}
			invalid := in
			invalid.Authority.Attempt = int(attempt)
			denyRuntimeDelivery(t, f, invalid)
			var count int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid attempt created an audit", count, err)
			}
			component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, runtimeDeliveryRunner(f.runner), f.pg, nil, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
			out, err := component.Deliver(ctx, invalid)
			if out != nil {
				out.Close()
			}
			if err != app.ErrHostingRuntimeEnvironmentUnavailable || out != nil {
				t.Fatal("invalid attempt reached value delivery", err)
			}
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid service request reached authorization", count, err)
			}
		})
	}
	// Invalid requests must not claim the delivery ID or prevent valid authority.
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, in)
	if err != nil {
		t.Fatal("valid attempt rejected", err)
	}
	defer got.Close()
	if got.Window.DeliveryID != in.DeliveryID || len(got.Records) != 1 {
		t.Fatal("valid authorization missing")
	}
}

func TestPostgresRuntimeDeliveryOriginalWindowAndAudit(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			f, _, in := runtimeDeliveryFixture(t, local)
			var group sync.WaitGroup
			results := make(chan store.HostingRuntimeEnvironmentAuthorization, 4)
			errs := make(chan error, 4)
			for range 4 {
				group.Go(func() {
					v, e := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, in)
					results <- v
					errs <- e
				})
			}
			group.Wait()
			close(results)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			var window domain.HostingRuntimeEnvironmentWindow
			for v := range results {
				if window.DeliveryID == "" {
					window = v.Window
				}
				if v.Window != window {
					t.Fatal("concurrent replay changed deadline")
				}
				v.Close()
			}
			if _, err := f.db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 hours' WHERE id=$1`, in.Authority.LeaseID); err != nil {
				t.Fatal(err)
			}
			again, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, in)
			if err != nil || again.Window != window {
				t.Fatal("renewal changed window", err)
			}
			defer again.Close()
			var metadata []byte
			var count int
			if err = f.db.QueryRow(t.Context(), `SELECT metadata FROM audit_events WHERE id=$1`, "aud_runtime_delivery_"+in.DeliveryID).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"private-runtime-value", "viewer-readable", "ciphertext", "key_id", "envelope"} {
				if bytes.Contains(metadata, []byte(forbidden)) {
					t.Fatal("audit contains values")
				}
			}
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			raw, _ := json.Marshal(again)
			if string(raw) != "{}" || strings.Contains(fmt.Sprintf("%#v", again), "runtime_delivery_secret") {
				t.Fatal("authorization exposed contents")
			}
			// A repeat remains dependent on current authority and configuration.
			runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
			denyRuntimeDelivery(t, f, in)
		})
	}
}

func TestPostgresRuntimeDeliveryDenials(t *testing.T) {
	for _, name := range []string{"runner", "runner_revoked", "lease", "attempt", "fence", "operation", "plan", "reference", "configuration", "resource", "secret_revoked", "capability", "actor", "destination", "expired", "applying", "global_id"} {
		t.Run(name, func(t *testing.T) {
			f, op, in := runtimeDeliveryFixture(t, false)
			switch name {
			case "runner":
				f.runner = "other"
			case "runner_revoked":
				if _, err := f.db.Exec(t.Context(), `UPDATE runners SET status='revoked' WHERE id=$1`, f.runner); err != nil {
					t.Fatal(err)
				}
			case "lease":
				in.Authority.LeaseID = "other"
			case "attempt":
				in.Authority.Attempt++
			case "fence":
				in.Authority.Fence = "other"
			case "operation":
				in.Authority.OperationID = "other"
				in.Authority.RunID = "run_other"
			case "plan":
				in.PlanHash = "sha256:" + strings.Repeat("b", 64)
			case "reference":
				in.RuntimeConfiguration.Generation++
			case "configuration":
				runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
			case "resource":
				if _, err := f.db.Exec(t.Context(), `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.resource); err != nil {
					t.Fatal(err)
				}
			case "secret_revoked":
				if _, err := f.pg.RevokeManagedSecret(t.Context(), "runtime_delivery_secret", 1, destinationEvent("revoke-runtime")); err != nil {
					t.Fatal(err)
				}
			case "capability":
				if _, err := f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingRuntimeEnvironmentCapability); err != nil {
					t.Fatal(err)
				}
			case "actor":
				if _, err := f.db.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
					t.Fatal(err)
				}
			case "destination":
				if _, err := f.db.Exec(t.Context(), `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, op.Snapshot.DestinationID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := f.db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, in.Authority.LeaseID); err != nil {
					t.Fatal(err)
				}
			case "applying":
				if _, err := f.pg.HostingApplyBegin(t.Context(), f.runner, in.Authority); err != nil {
					t.Fatal(err)
				}
			case "global_id":
				if _, err := f.db.Exec(t.Context(), `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,$2,'hosting.runtime_environment_authorized','other','{}',clock_timestamp())`, "aud_runtime_delivery_"+in.DeliveryID, f.runner); err != nil {
					t.Fatal(err)
				}
			}
			denyRuntimeDelivery(t, f, in)
		})
	}
}

type runtimeDeliveryRunner string

func (r runtimeDeliveryRunner) CurrentRunnerIdentity(context.Context) (app.RunnerIdentity, error) {
	return app.RunnerIdentity{RunnerID: string(r), Capabilities: []string{domain.HostingRuntimeEnvironmentCapability}}, nil
}

func TestPostgresRuntimeDeliveryDecryptOnlyAfterCommittedAudit(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	// Rotation changes the current pointer, while this operation still selects
	// the exact active immutable version captured by its runtime document.
	_, rotated, audit := managedFixture(t, "runtime_delivery_secret", 2)
	if _, err := f.pg.RotateManagedSecret(t.Context(), "runtime_delivery_secret", 1, rotated, audit); err != nil {
		t.Fatal(err)
	}
	component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, runtimeDeliveryRunner(f.runner), f.pg, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
	if got, e := component.Deliver(ctx, in); e == nil || got != nil {
		t.Fatal("missing key delivered values")
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, "aud_runtime_delivery_"+in.DeliveryID).Scan(&count); err != nil || count != 1 {
		t.Fatal("decryption failure occurred before durable audit", count, err)
	}
	component, err = app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, runtimeDeliveryRunner(f.runner), f.pg, managedRing(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	out, err := component.Deliver(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err = out.WithValues(func(entries []hostingruntimeprotocol.Entry) error {
		if len(entries) != 2 || string(entries[1].Value) != "private-runtime-value" {
			t.Fatal("wrong values")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRuntimeDeliveryAuditAndCommitFailure(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprint(deferred), func(t *testing.T) {
			f, _, in := runtimeDeliveryFixture(t, false)
			trigger := `CREATE TRIGGER runtime_delivery_fail BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION runtime_delivery_fail()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER runtime_delivery_fail AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION runtime_delivery_fail()`
			}
			if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION runtime_delivery_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.runtime_environment_authorized' THEN RAISE EXCEPTION 'private-diagnostic'; END IF;RETURN NEW;END $$;`+trigger); err != nil {
				t.Fatal(err)
			}
			component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, runtimeDeliveryRunner(f.runner), f.pg, managedRing(t), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
			got, err := component.Deliver(ctx, in)
			if got != nil {
				got.Close()
			}
			if err != app.ErrHostingRuntimeEnvironmentUnavailable {
				t.Fatal("audit failure exposed diagnostics", err)
			}
			denyRuntimeDelivery(t, f, in)
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed transaction retained audit", count, err)
			}
		})
	}
}

func TestPostgresRuntimeDeliveryCrossProjectReferenceDenied(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	item, record, event := managedFixture(t, "cross_runtime_secret", 1)
	item.ProjectID = "proj_security"
	if _, err := f.pg.CreateManagedSecret(t.Context(), item, record, event); err != nil {
		t.Fatal(err)
	}
	update := runtimeInput(2, 1)
	update.Entries = []domain.HostingRuntimeEntry{{Name: "CROSS", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 1}}}
	if _, err := f.pg.PutHostingRuntimeConfiguration(t.Context(), "usr_bootstrap", f.resource, update, destinationEvent("cross_runtime")); err == nil {
		t.Fatal("cross-project selection saved")
	}
	// The failed selection cannot alter the currently authorized delivery.
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, in)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	for ref := range got.Records {
		if ref.SecretID == item.ID {
			t.Fatal("cross-project record delivered")
		}
	}
}

func TestPostgresRuntimeDeliveryAuditWaitExpiryRollback(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION delivery_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.runtime_environment_authorized' THEN PERFORM pg_advisory_xact_lock(982756); END IF;RETURN NEW;END $$;CREATE TRIGGER delivery_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delivery_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = blocker.Rollback(cleanup)
	}()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(982756)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, in.Authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		got, e := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, in)
		if len(got.Records) != 0 {
			done <- fmt.Errorf("returned envelope after expiry")
			return
		}
		done <- e
	})
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = blocker.Rollback(cleanup)
		group.Wait()
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired, waiting bool
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp(),EXISTS(SELECT 1 FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%INSERT INTO audit_events%') FROM run_leases WHERE id=$1`, in.Authority.LeaseID).Scan(&expired, &waiting); err != nil {
			t.Fatal(err)
		}
		if expired && waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("did not wait for audit: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("expired audit authorized delivery")
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired authorization audit committed", count, err)
	}
}

func TestPostgresRuntimeDeliveryExpiredWindowAfterRenewal(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, in.Authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	first, err := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, in.Authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if err = f.db.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, first.Window.UseUntil).Scan(&expired); err != nil {
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
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, in)
	if err == nil || len(got.Records) != 0 {
		got.Close()
		t.Fatal("expired window returned authorization")
	}
	fresh := in
	fresh.DeliveryID = strings.Repeat("f", 32)
	if _, err = f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, fresh); err != nil {
		t.Fatal("fresh ID could not open a new live window", err)
	}
}
