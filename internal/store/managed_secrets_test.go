package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type managedContractStore interface {
	store.ManagedSecretRepository
	store.AuditRepository
}

func managedRing(t *testing.T) *secretcrypto.Keyring {
	t.Helper()
	ring, err := secretcrypto.New("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
func managedFixture(t *testing.T, id string, version int64) (domain.ManagedSecret, store.ManagedSecretRecord, domain.AuditEvent) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := domain.ManagedSecret{ID: id, ProjectID: "proj_platform", Name: id, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}
	envelope, err := managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: id, Version: version}, []byte("  private-value\n"))
	if err != nil {
		t.Fatal(err)
	}
	record := store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: id, Version: version, CreatedBy: item.CreatedBy, CreatedAt: now}, Envelope: envelope}
	audit := domain.AuditEvent{ID: fmt.Sprintf("aud_%s_%d", id, version), ActorID: item.CreatedBy, CreatedAt: now, Metadata: map[string]any{"value": "must be stripped", "key_id": "must be stripped"}}
	return item, record, audit
}
func TestManagedSecretsRepositoryContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		managedRepositoryContract(t, store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash"))
	})
	t.Run("postgres", func(t *testing.T) {
		pg, _ := openPostgresIntegrationStore(t, t.Context(), "managed_contract")
		t.Cleanup(func() { closePostgresStore(t, pg) })
		managedRepositoryContract(t, pg)
	})
}
func managedRepositoryContract(t *testing.T, s managedContractStore) {
	t.Helper()
	ctx := t.Context()
	item, record, audit := managedFixture(t, "msec_contract", 1)
	created, err := s.CreateManagedSecret(ctx, item, record, audit)
	if err != nil || created.CurrentVersion != 1 || created.Status != "active" {
		t.Fatalf("create: %+v %v", created, err)
	}
	stored, err := s.GetManagedSecretRecord(ctx, item.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.Envelope.Data, []byte("private-value")) {
		t.Fatal("stored plaintext")
	}
	stored.Envelope.Data[0] ^= 1
	original, err := s.GetManagedSecretRecord(ctx, item.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := managedRing(t).Open(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, original.Envelope)
	if err != nil || string(plain) != "  private-value\n" {
		t.Fatalf("encrypted roundtrip: %v", err)
	}
	if encoded, err := json.Marshal(original); err != nil || string(encoded) != "{}" {
		t.Fatal("storage envelope serializes")
	}
	failed, failedRecord, _ := managedFixture(t, "msec_failed", 1)
	if _, err = s.CreateManagedSecret(ctx, failed, failedRecord, audit); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("create audit collision %v", err)
	}
	if _, err = s.GetManagedSecret(ctx, failed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("audit failure leaked create")
	}
	_, next, _ := managedFixture(t, item.ID, 2)
	if _, err = s.RotateManagedSecret(ctx, item.ID, 1, next, audit); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rotate audit collision: %v", err)
	}
	if _, err = s.GetManagedSecretRecord(ctx, item.ID, 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("audit failure leaked version")
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := audit
			event.ID = fmt.Sprintf("aud_cas_%d", i)
			_, err := s.RotateManagedSecret(ctx, item.ID, 1, next, event)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners=%d", wins)
	}
	if _, err = s.RotateManagedSecret(ctx, item.ID, math.MaxInt64, next, audit); !errors.Is(err, store.ErrConflict) {
		t.Fatal("overflow version accepted")
	}
	versions, err := s.ListManagedSecretVersions(ctx, item.ID, store.Page{Enabled: true, Limit: 1, LimitSpecified: true})
	if err != nil || versions.Total != 2 || len(versions.Items) != 1 || versions.Items[0].Version != 2 {
		t.Fatalf("versions page: %+v %v", versions, err)
	}
	if err = s.AuthorizeManagedSecretReveal(ctx, item.ID, 1, audit); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reveal audit collision: %v", err)
	}
	reveal := audit
	reveal.ID = "aud_reveal_one"
	if err = s.AuthorizeManagedSecretReveal(ctx, item.ID, 1, reveal); err != nil {
		t.Fatal(err)
	}
	reveal.ID = "aud_reveal_two"
	if err = s.AuthorizeManagedSecretReveal(ctx, item.ID, 1, reveal); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevokeManagedSecret(ctx, item.ID, 2, audit); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoke audit collision accepted")
	}
	active, err := s.GetManagedSecret(ctx, item.ID)
	if err != nil || active.Status != "active" {
		t.Fatal("failed revoke changed state")
	}
	revoke := audit
	revoke.ID = "aud_revoke"
	revoked, err := s.RevokeManagedSecret(ctx, item.ID, 2, revoke)
	if err != nil || revoked.Status != "revoked" || revoked.RevokedAt == nil {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	for _, v := range []int64{1, 2} {
		reveal.ID = fmt.Sprintf("aud_after_revoke_%d", v)
		if err = s.AuthorizeManagedSecretReveal(ctx, item.ID, v, reveal); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("revoked reveal: %v", err)
		}
	}
	_, third, thirdAudit := managedFixture(t, item.ID, 3)
	if _, err = s.RotateManagedSecret(ctx, item.ID, 2, third, thirdAudit); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoked rotate: %v", err)
	}
	duplicate, dupRecord, dupAudit := managedFixture(t, "msec_duplicate", 1)
	duplicate.Name = item.Name
	if _, err = s.CreateManagedSecret(ctx, duplicate, dupRecord, dupAudit); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked name reused")
	}
	events, err := s.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reveals := 0
	for _, event := range events {
		if event.TargetID != item.ID {
			continue
		}
		if event.Action == "managed_secret.revealed" {
			reveals++
		}
		for key := range event.Metadata {
			if key != "project_id" && key != "version" && key != "request_id" {
				t.Fatalf("unsafe audit key %s", key)
			}
		}
	}
	if reveals != 2 {
		t.Fatalf("fresh reveal audit count=%d", reveals)
	}
}

func TestPostgresManagedSecretsConstraintsAndReadiness(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "managed_constraints")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	item, record, event := managedFixture(t, "msec_constraints", 1)
	if _, err := pg.CreateManagedSecret(ctx, item, record, event); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE project_managed_secret_versions SET ciphertext=decode('00','hex') WHERE secret_id='msec_constraints'`, `DELETE FROM project_managed_secret_versions WHERE secret_id='msec_constraints'`, `UPDATE project_managed_secrets SET project_id='proj_security' WHERE id='msec_constraints'`, `UPDATE project_managed_secrets SET current_version=3 WHERE id='msec_constraints'`} {
		if _, err := db.Exec(ctx, sql); err == nil {
			t.Fatalf("unsafe mutation accepted: %s", sql)
		}
	}
	var plaintextPresent bool
	if err := db.QueryRow(ctx, `SELECT position(convert_to('private-value','UTF8') in ciphertext)>0 FROM project_managed_secret_versions WHERE secret_id=$1`, item.ID).Scan(&plaintextPresent); err != nil || plaintextPresent {
		t.Fatalf("encrypted at rest: %v", err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("ready schema: %v %v", ok, err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE project_managed_secret_versions DISABLE TRIGGER managed_secret_versions_immutable`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatalf("disabled immutability trigger ready: %v %v", ok, err)
	}
}

func TestPostgresManagedSecretsRevealRevokeSerialization(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "managed_lock")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	item, record, event := managedFixture(t, "msec_lock", 1)
	if _, err := pg.CreateManagedSecret(ctx, item, record, event); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM project_managed_secrets WHERE id=$1 FOR UPDATE`, item.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	event.ID = "aud_lock_reveal"
	go func() { close(started); done <- pg.AuthorizeManagedSecretReveal(ctx, item.ID, 1, event) }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("reveal bypassed row lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err = tx.Exec(ctx, `UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("reveal after revoke: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reveal did not resume")
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range events {
		if a.ID == event.ID {
			t.Fatal("failed reveal committed audit")
		}
	}
}

func TestPostgresManagedSecretRevealCommitFailureReturnsNoPlaintext(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "managed_commit")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, ManagedSecrets: pg, SecretKeyring: managedRing(t), Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	item, err := service.CreateManagedSecret(actor, app.ManagedSecretInput{ProjectID: "proj_platform", Name: "commit failure", Value: "private-value"})
	if err != nil {
		t.Fatal(err)
	}
	// A deferred constraint trigger fails only when the repository attempts COMMIT.
	if _, err = db.Exec(ctx, `CREATE FUNCTION fail_managed_reveal_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='managed_secret.revealed' THEN RAISE EXCEPTION 'test audit commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER managed_reveal_commit_failure AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_managed_reveal_commit()`); err != nil {
		t.Fatal(err)
	}
	result, err := service.RevealManagedSecret(actor, item.ID, app.ManagedSecretRevealInput{Version: 1})
	if err == nil || result.Value != "" {
		t.Fatal("commit failure disclosed plaintext")
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == "managed_secret.revealed" {
			t.Fatal("failed commit retained reveal audit")
		}
	}
}

func TestPostgresManagedSecretsMissingMigrationBlocksStartup(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "managed_missing")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	if _, err := db.Exec(ctx, `DROP TABLE project_managed_secrets,project_managed_secret_versions; DROP FUNCTION nerocd_managed_secrets_schema_compatible(); DROP FUNCTION nerocd_managed_secret_update(); DROP FUNCTION nerocd_managed_secret_version_immutable()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing migration readiness=%v %v", ok, err)
	}
	if restored, err := store.OpenPostgres(ctx, db.Config().ConnConfig.ConnString()); err == nil {
		_ = restored.Close()
		t.Fatal("missing migration allowed startup")
	}
	if err := applyEmbeddedMigrations(ctx, db, "0040_hosting_resources.sql", "0041_project_managed_secrets.sql"); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("restored migration readiness=%v %v", ok, err)
	}
}
