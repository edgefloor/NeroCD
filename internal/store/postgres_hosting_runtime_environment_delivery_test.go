package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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

func runtimeCredentialApp(t *testing.T, pg *store.PostgresStore) *app.Service {
	t.Helper()
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: managedRing(t), Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runtimeCredentialEnrollment(t *testing.T, f runtimeAdmissionFixture, hash string) {
	t.Helper()
	// A current replacement enrollment must not make the original bearer current.
	// Fixture-only replacement satisfies the unique runner_id constraint without
	// claiming that the production enrollment API re-enrolls an existing runner.
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `DELETE FROM runner_enrollments WHERE runner_id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO runner_enrollments(id,token_hash,runner_id,runner_name,tags,capabilities,created_by,created_at,expires_at,used_at,consume_request_id,credential_hash)
 SELECT 'runtime_replacement_enrollment','runtime_replacement_enrollment_hash',id,name,tags,capabilities,'usr_bootstrap',clock_timestamp(),clock_timestamp()+interval '1 hour',clock_timestamp(),'runtime_replacement_request',$2 FROM runners WHERE id=$1`, f.runner, hash); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func runtimeCredentialWait(t *testing.T, ctx context.Context, f runtimeAdmissionFixture, fragment string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%'||$1||'%')`, fragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresRuntimeDeliveryCredentialRotationBeforeRunnerLock(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	s := runtimeCredentialApp(t, f.pg)
	oldToken := "image-hosting-fixture-token"
	principal, err := s.AuthenticateRunnerToken(t.Context(), oldToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(auth.WithPrincipal(t.Context(), principal), 10*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource); err != nil {
		t.Fatal(err)
	}
	component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, s, f.pg, managedRing(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		out, e := component.Deliver(ctx, in, oldToken)
		if out != nil {
			out.Close()
			done <- fmt.Errorf("rotated credential returned environment")
			return
		}
		done <- e
	})
	defer func() { cancel(); _ = blocker.Rollback(context.Background()); group.Wait() }()
	runtimeCredentialWait(t, ctx, f, "SELECT id,project_id,kind,source,generation FROM resources")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("replacement-runtime-bearer")))
	if _, err = f.pg.UpdateRunnerToken(ctx, f.runner, hash, domain.RunnerActive, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtimeCredentialEnrollment(t, f, hash)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != app.ErrHostingRuntimeEnvironmentUnavailable {
		t.Fatal("old credential authorized after placement wait", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_environment_authorized'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rotated credential committed authorization", count, err)
	}
}

func TestPostgresRuntimeDeliveryCredentialRotationDuringAudit(t *testing.T) {
	f, _, in := runtimeDeliveryFixture(t, false)
	s := runtimeCredentialApp(t, f.pg)
	oldToken := "image-hosting-fixture-token"
	principal, err := s.AuthenticateRunnerToken(t.Context(), oldToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(auth.WithPrincipal(t.Context(), principal), 10*time.Second)
	defer cancel()
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION runtime_credential_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.runtime_environment_authorized' THEN PERFORM pg_advisory_xact_lock(982759); END IF;RETURN NEW;END $$;CREATE TRIGGER runtime_credential_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION runtime_credential_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(982759)`); err != nil {
		t.Fatal(err)
	}
	pause := &runtimeCredentialPause{repository: f.pg, committed: make(chan struct{}), resume: make(chan struct{})}
	component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, s, pause, managedRing(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	rotated := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		out, e := component.Deliver(ctx, in, oldToken)
		if out != nil {
			out.Close()
			done <- fmt.Errorf("rotated credential returned environment")
			return
		}
		done <- e
	})
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		select {
		case <-pause.resume:
		default:
			close(pause.resume)
		}
		group.Wait()
	}()
	runtimeCredentialWait(t, ctx, f, "INSERT INTO audit_events")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("replacement-runtime-bearer")))
	group.Go(func() {
		_, e := f.pg.UpdateRunnerToken(ctx, f.runner, hash, domain.RunnerActive, time.Now())
		rotated <- e
	})
	runtimeCredentialWait(t, ctx, f, "UPDATE runners SET token_hash")
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pause.committed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = <-rotated; err != nil {
		t.Fatal(err)
	}
	close(pause.resume)
	if err = <-done; err != app.ErrHostingRuntimeEnvironmentUnavailable {
		t.Fatal("queued rotation failed to deny original credential", err)
	}
}

type runtimeCredentialPause struct {
	repository store.HostingRuntimeEnvironmentRepository
	committed  chan struct{}
	resume     chan struct{}
}

func (p *runtimeCredentialPause) AuthorizeHostingRuntimeEnvironment(ctx context.Context, runner, credentialHash string, in domain.HostingRuntimeEnvironmentRequest) (store.HostingRuntimeEnvironmentAuthorization, error) {
	a, err := p.repository.AuthorizeHostingRuntimeEnvironment(ctx, runner, credentialHash, in)
	if err != nil {
		return a, err
	}
	close(p.committed)
	select {
	case <-p.resume:
		return a, nil
	case <-ctx.Done():
		a.Close()
		return store.HostingRuntimeEnvironmentAuthorization{}, ctx.Err()
	}
}

func TestPostgresRuntimeDeliveryCredentialRotationAfterCommit(t *testing.T) {
	for _, enroll := range []bool{false, true} {
		t.Run(fmt.Sprint(enroll), func(t *testing.T) {
			f, _, in := runtimeDeliveryFixture(t, false)
			s := runtimeCredentialApp(t, f.pg)
			oldToken := "image-hosting-fixture-token"
			principal, err := s.AuthenticateRunnerToken(t.Context(), oldToken)
			if err != nil || principal.ID != f.runner {
				t.Fatal("original authentication failed", err)
			}
			ctx, cancel := context.WithTimeout(auth.WithPrincipal(t.Context(), principal), 10*time.Second)
			defer cancel()
			pause := &runtimeCredentialPause{repository: f.pg, committed: make(chan struct{}), resume: make(chan struct{})}
			component, err := app.NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, s, pause, managedRing(t), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			var group sync.WaitGroup
			group.Go(func() {
				out, e := component.Deliver(ctx, in, "image-hosting-fixture-token")
				if out != nil {
					out.Close()
					done <- fmt.Errorf("original request returned environment after rotation")
					return
				}
				done <- e
			})
			defer func() {
				cancel()
				select {
				case <-pause.resume:
				default:
					close(pause.resume)
				}
				group.Wait()
			}()
			select {
			case <-pause.committed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, "aud_runtime_delivery_"+in.DeliveryID).Scan(&count); err != nil || count != 1 {
				t.Fatal("authorization did not commit", count, err)
			}
			var metadata []byte
			if err = f.db.QueryRow(ctx, `SELECT metadata::text FROM audit_events WHERE id=$1`, "aud_runtime_delivery_"+in.DeliveryID).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(metadata, []byte(oldToken)) || bytes.Contains(metadata, []byte(runtimeDeliveryCredential(f))) {
				t.Fatal("credential binding entered audit metadata")
			}
			newToken := "replacement-runtime-bearer"
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(newToken)))
			if _, err = f.pg.UpdateRunnerToken(ctx, f.runner, hash, domain.RunnerActive, time.Now()); err != nil {
				t.Fatal(err)
			}
			if enroll {
				runtimeCredentialEnrollment(t, f, hash)
			}
			if _, err = s.AuthenticateRunnerToken(ctx, oldToken); err == nil {
				t.Fatal("old fresh authentication succeeded")
			}
			if p, e := s.AuthenticateRunnerToken(ctx, newToken); e != nil || p.ID != f.runner {
				t.Fatal("replacement fresh authentication failed", e)
			}
			close(pause.resume)
			if err = <-done; err != app.ErrHostingRuntimeEnvironmentUnavailable {
				t.Fatal("rotation did not deny original in-flight request", err)
			}
		})
	}
}

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
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, runtimeDeliveryCredential(f), in)
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
			out, err := component.Deliver(ctx, invalid, "image-hosting-fixture-token")
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
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, runtimeDeliveryCredential(f), in)
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
					v, e := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, runtimeDeliveryCredential(f), in)
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
			again, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, runtimeDeliveryCredential(f), in)
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

func (r runtimeDeliveryRunner) AuthenticateRunnerToken(_ context.Context, token string) (auth.Principal, error) {
	if token != "image-hosting-fixture-token" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{ID: string(r), Provider: domain.PrincipalRunner}, nil
}

func runtimeDeliveryCredential(f runtimeAdmissionFixture) string {
	token := "image-hosting-fixture-token"
	if f.build != nil {
		token = "build-fixture-token"
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

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
	if got, e := component.Deliver(ctx, in, "image-hosting-fixture-token"); e == nil || got != nil {
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
	out, err := component.Deliver(ctx, in, "image-hosting-fixture-token")
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
			got, err := component.Deliver(ctx, in, "image-hosting-fixture-token")
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
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(t.Context(), f.runner, runtimeDeliveryCredential(f), in)
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
		got, e := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, runtimeDeliveryCredential(f), in)
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
	first, err := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, runtimeDeliveryCredential(f), in)
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
	got, err := f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, runtimeDeliveryCredential(f), in)
	if err == nil || len(got.Records) != 0 {
		got.Close()
		t.Fatal("expired window returned authorization")
	}
	fresh := in
	fresh.DeliveryID = strings.Repeat("f", 32)
	if _, err = f.pg.AuthorizeHostingRuntimeEnvironment(ctx, f.runner, runtimeDeliveryCredential(f), fresh); err != nil {
		t.Fatal("fresh ID could not open a new live window", err)
	}
}
