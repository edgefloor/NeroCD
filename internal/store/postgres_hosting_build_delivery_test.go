package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

const deliveryID = "0123456789abcdef0123456789abcdef"

func privateDeliveryFixture(t *testing.T, malformed ...bool) (buildStoreFixture, domain.HostingBuild, domain.HostingBuildAuthority) {
	t.Helper()
	f := newBuildStoreFixture(t)
	src := f.src
	src.Generation = 2
	src.CredentialKind = "https_basic"
	if _, err := f.pg.ConfigureHostingBuildSource(t.Context(), "usr_bootstrap", 1, src, destinationEvent("delivery_private")); err != nil {
		t.Fatal(err)
	}
	f.src = src
	item, record, audit := managedFixture(t, "delivery_secret", 1)
	var err error
	plaintext := []byte(`{"username":"fixture-git","password":"never-log-delivery-value"}`)
	if len(malformed) > 0 && malformed[0] {
		plaintext = []byte(`{"password":"never-log-delivery-value"}`)
	}
	record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	grant := domain.HostingSourceGrant{ID: "delivery_grant", Version: 1, SourceID: src.ID, ResourceID: src.ResourceID, ProjectID: src.ProjectID, RepositoryURL: src.RepositoryURL, PolicyHash: domain.HostingDocumentHash(src.Policy), SecretID: item.ID, SecretVersion: 1, Kind: src.CredentialKind}
	if _, err = f.pg.CreateHostingSourceGrant(t.Context(), "usr_bootstrap", grant, destinationEvent("delivery_grant")); err != nil {
		t.Fatal(err)
	}
	in := buildInput("delivery")
	in.ExpectedSourceGeneration = 2
	in.GrantID = grant.ID
	if _, err = f.pg.RequestHostingBuild(t.Context(), "delivery", src.ResourceID, "usr_bootstrap", in, buildPolicy(), buildEnqueue, destinationEvent("delivery_request")); err != nil {
		t.Fatal(err)
	}
	b, a := f.activate(t, "delivery")
	return f, b, a
}
func deliveryRequest(a domain.HostingBuildAuthority) domain.HostingSourceCredentialRequest {
	return domain.HostingSourceCredentialRequest{Authority: a, DeliveryID: deliveryID}
}
func assertNoDelivery(t *testing.T, got store.HostingSourceCredentialAuthorization, err error) {
	t.Helper()
	if err == nil || len(got.Record.Envelope.Data) != 0 || got.SecretID != "" || !got.Window.UseUntil.IsZero() {
		t.Fatalf("denied delivery returned material or success: %v", err)
	}
}

func TestPostgresHostingSourceDeliveryWindowReplayAndConcurrentIdentity(t *testing.T) {
	f, _, a := privateDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	in := deliveryRequest(a)
	var group sync.WaitGroup
	values := make(chan store.HostingSourceCredentialAuthorization, 8)
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { v, e := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, in); values <- v; failures <- e })
	}
	group.Wait()
	close(values)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first store.HostingSourceCredentialAuthorization
	for value := range values {
		if first.Window.DeliveryID == "" {
			first = value
		}
		if value.Window != first.Window {
			t.Fatal("concurrent replay changed window")
		}
	}
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 hours' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, in)
	if err != nil || replay.Window != first.Window {
		t.Fatal("lease renewal extended delivery", err)
	}
	var count int
	var metadata []byte
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.source_credential_authorized'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = f.db.QueryRow(ctx, `SELECT metadata FROM audit_events WHERE id=$1`, "aud_source_delivery_"+deliveryID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"never-log-delivery-value", "fixture-git", "ciphertext", "envelope", "key_id"} {
		if bytes.Contains(metadata, []byte(forbidden)) {
			t.Fatal("audit leaked credential material")
		}
	}
	raw, _ := json.Marshal(first)
	if string(raw) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", first, first), "delivery_secret") {
		t.Fatal("authorization serialization exposed material")
	}
	// Another valid build cannot reuse a globally claimed delivery ID.
	input := buildInput("delivery-other")
	input.ExpectedSourceGeneration = 2
	input.GrantID = "delivery_grant"
	if _, err = f.pg.RequestHostingBuild(ctx, "delivery-other", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("delivery-other")); err != nil {
		t.Fatal(err)
	}
	_, other := f.activate(t, "delivery-other")
	got, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, deliveryRequest(other))
	assertNoDelivery(t, got, err)
	// Both valid builds race for a fresh globally scoped ID. One wins and one
	// conflicts; neither may wait forever or turn a deadlock into authority.
	outcomes := make(chan error, 2)
	for _, attempt := range []domain.HostingBuildAuthority{a, other} {
		group.Go(func() {
			request := deliveryRequest(attempt)
			request.DeliveryID = strings.Repeat("b", 32)
			_, e := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, request)
			outcomes <- e
		})
	}
	group.Wait()
	close(outcomes)
	successes, conflicts := 0, 0
	for outcome := range outcomes {
		if outcome == nil {
			successes++
		} else if errors.Is(outcome, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(outcome)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("global delivery race did not choose one immutable owner")
	}

	changed := in
	changed.Authority.Fence = "wrong"
	got, err = f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, changed)
	assertNoDelivery(t, got, err)
	got, err = f.pg.AuthorizeHostingSourceCredential(ctx, "other-runner", in)
	assertNoDelivery(t, got, err)
}

func TestPostgresHostingSourceDeliveryCurrentAuthorityFirstAndReplay(t *testing.T) {
	for _, loss := range []string{"grant", "secret", "creator", "source_generation", "source_body", "resource", "target_grant", "enrollment", "source_receipt", "lease"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_replay_%t", loss, replay), func(t *testing.T) {
				f, b, a := privateDeliveryFixture(t)
				ctx := t.Context()
				in := deliveryRequest(a)
				if replay {
					if _, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, in); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch loss {
				case "grant":
					err = f.pg.RevokeHostingSourceGrant(ctx, "usr_bootstrap", "delivery_grant", destinationEvent("revoke_delivery_grant"))
				case "secret":
					_, err = f.pg.RevokeManagedSecret(ctx, "delivery_secret", 1, destinationEvent("revoke_delivery_secret"))
				case "creator":
					_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap';UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
				case "source_generation", "source_body":
					src := f.src
					src.Generation = 3
					if loss == "source_body" {
						src.RequestedRef = "other"
					}
					_, err = f.pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 2, src, destinationEvent("changed_source"))
				case "resource":
					_, err = f.db.Exec(ctx, `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.src.ResourceID)
				case "target_grant":
					_, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id='dest_build'`)
				case "enrollment":
					_, err = f.db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner)
				case "source_receipt":
					_, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "source_committed", sourceBuildReceipt(b))
				case "lease":
					_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID)
				case "cross_project":
					_, err = f.db.Exec(ctx, `UPDATE resources SET project_id='proj_security' WHERE id=$1`, f.src.ResourceID)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, in)
				assertNoDelivery(t, got, err)
			})
		}
	}
	t.Run("public", func(t *testing.T) {
		f := newBuildStoreFixture(t)
		_, a := f.start(t, "public-delivery")
		got, err := f.pg.AuthorizeHostingSourceCredential(t.Context(), f.runner, deliveryRequest(a))
		assertNoDelivery(t, got, err)
	})
	t.Run("old_version", func(t *testing.T) {
		f, _, a := privateDeliveryFixture(t)
		_, next, audit := managedFixture(t, "delivery_secret", 2)
		if _, err := f.pg.RotateManagedSecret(t.Context(), "delivery_secret", 1, next, audit); err != nil {
			t.Fatal(err)
		}
		got, err := f.pg.AuthorizeHostingSourceCredential(t.Context(), f.runner, deliveryRequest(a))
		if err != nil || got.SecretVersion != 1 {
			t.Fatal("selected immutable version unavailable", err)
		}
	})
}

func TestPostgresHostingSourceDeliveryAuditWaitExpiryRollback(t *testing.T) {
	f, _, a := privateDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION delivery_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.source_credential_authorized' THEN PERFORM pg_advisory_xact_lock(982755); END IF;RETURN NEW;END $$;CREATE TRIGGER delivery_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delivery_audit_wait()`); err != nil {
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
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(982755)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		got, e := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, deliveryRequest(a))
		if len(got.Record.Envelope.Data) != 0 {
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
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp(),EXISTS(SELECT 1 FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%INSERT INTO audit_events%') FROM run_leases WHERE id=$1`, a.LeaseID).Scan(&expired, &waiting); err != nil {
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
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.source_credential_authorized'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired authorization audit committed", count, err)
	}
}

func TestPostgresHostingSourceDeliveryCrossProjectGrantDenied(t *testing.T) {
	f, _, _ := privateDeliveryFixture(t)
	item, record, audit := managedFixture(t, "other_project_secret", 1)
	item.ProjectID = "proj_security"
	if _, err := f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	grant := domain.HostingSourceGrant{ID: "cross_project_grant", Version: 1, SourceID: f.src.ID, ResourceID: f.src.ResourceID, ProjectID: f.src.ProjectID, RepositoryURL: f.src.RepositoryURL, PolicyHash: domain.HostingDocumentHash(f.src.Policy), SecretID: item.ID, SecretVersion: 1, Kind: "https_basic"}
	if _, err := f.pg.CreateHostingSourceGrant(t.Context(), "usr_bootstrap", grant, destinationEvent("cross_project_grant")); err == nil {
		t.Fatal("cross-project secret granted to source")
	}
}
func TestPostgresHostingSourceDeliveryExpiredWindowAfterRenewal(t *testing.T) {
	f, _, a := privateDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	first, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, deliveryRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.LeaseID); err != nil {
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
	got, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, deliveryRequest(a))
	assertNoDelivery(t, got, err)
	fresh := deliveryRequest(a)
	fresh.DeliveryID = strings.Repeat("f", 32)
	if _, err = f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, fresh); err != nil {
		t.Fatal("fresh ID could not open a new live window", err)
	}
}

type deliveryRunnerIdentity string

func (r deliveryRunnerIdentity) CurrentRunnerIdentity(context.Context) (app.RunnerIdentity, error) {
	return app.RunnerIdentity{RunnerID: string(r)}, nil
}
func TestPostgresHostingSourceDeliveryDecryptOnlyAfterAuditCommit(t *testing.T) {
	for _, failure := range []string{"none", "audit", "commit", "key", "decode"} {
		t.Run(failure, func(t *testing.T) {
			f, _, a := privateDeliveryFixture(t, failure == "decode")
			keys := managedRing(t)
			if failure == "audit" {
				if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION delivery_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.source_credential_authorized' THEN RAISE EXCEPTION 'private-audit-diagnostic'; END IF;RETURN NEW;END $$;CREATE TRIGGER delivery_audit_fail BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION delivery_audit_fail()`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "commit" {
				if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION delivery_commit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.source_credential_authorized' THEN RAISE EXCEPTION 'private-commit-diagnostic'; END IF;RETURN NEW;END $$;CREATE CONSTRAINT TRIGGER delivery_commit_fail AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION delivery_commit_fail()`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "key" {
				keys = nil
			}

			component, err := app.NewHostingBuildCredentialDelivery(auth.ContextProvider{}, deliveryRunnerIdentity(f.runner), f.pg, keys, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
			delivered, err := component.Deliver(ctx, deliveryRequest(a))
			material := delivered.Take()
			defer material.Close()
			if failure == "audit" || failure == "commit" {
				got, e := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, deliveryRequest(a))
				assertNoDelivery(t, got, e)
			}
			if failure == "audit" || failure == "commit" || failure == "key" || failure == "decode" {
				if err != app.ErrHostingSourceCredentialUnavailable || len(material.Bytes) != 0 {
					t.Fatal("failure returned plaintext or diagnostics", err)
				}
			} else if err != nil || string(material.Bytes) != "never-log-delivery-value" {
				t.Fatal("committed authorization did not deliver selected version", err)
			}
			var count int
			if e := f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.source_credential_authorized'`).Scan(&count); e != nil {
				t.Fatal(e)
			}
			expected := 1
			if failure == "audit" || failure == "commit" {
				expected = 0
			}
			if count != expected {
				t.Fatal("authorization audit semantics", count)
			}
			var documents string
			if e := f.db.QueryRow(ctx, `SELECT coalesce(string_agg(metadata::text,''),'')||(SELECT string_agg(snapshot::text,'') FROM hosting_builds) FROM audit_events`).Scan(&documents); e != nil {
				t.Fatal(e)
			}
			if strings.Contains(documents, "never-log-delivery-value") || strings.Contains(documents, "fixture-git") {
				t.Fatal("credential persisted in metadata")
			}
		})
	}
}
