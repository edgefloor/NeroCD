package store_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type legacyActorStore interface {
	destinationContractStore
	store.APITokenRepository
	store.ServiceAccountRepository
	store.OIDCRepository
}

func TestLegacyAPITokenActorContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		legacyAPITokenActorContract(t, store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash"))
	})
	t.Run("postgres", func(t *testing.T) {
		pg, database := openPostgresIntegrationStore(t, t.Context(), "legacy_actor")
		t.Cleanup(func() { closePostgresStore(t, pg) })
		legacyAPITokenActorContract(t, pg)
		var extra int
		if err := database.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE id IN ('pat_managed_actor','pat_legacy_failed')`).Scan(&extra); err != nil || extra != 0 {
			t.Fatalf("credential or rolled-back actor registered: count=%d err=%v", extra, err)
		}
		for _, query := range []string{
			`UPDATE api_tokens SET service_account_id=NULL WHERE id='pat_managed_actor'`,
			`UPDATE api_tokens SET service_account_id='svcacct_legacy_actor' WHERE id='pat_legacy_actor'`,
		} {
			if _, err := database.Exec(t.Context(), query); err == nil {
				t.Fatalf("association retarget accepted: %s", query)
			}
		}
	})
}

func legacyAPITokenActorContract(t *testing.T, s legacyActorStore) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	token := domain.APIToken{ID: "pat_legacy_actor", Name: "legacy runner admin", Kind: domain.TokenKindServiceAccount, TokenHash: "legacy-actor-hash", Roles: []string{domain.RoleRunnerAdmin}, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: now}
	event := destinationEvent("legacy_create")
	if _, err := s.CreateAPIToken(ctx, token, store.WithAudit(event)); err != nil {
		t.Fatal(err)
	}
	// A colliding audit must roll back token creation and registry registration.
	failed := token
	failed.ID = "pat_legacy_failed"
	failed.TokenHash = "legacy-failed-hash"
	if _, err := s.CreateAPIToken(ctx, failed, store.WithAudit(event)); err == nil {
		t.Fatal("audit collision accepted")
	}
	if _, err := s.GetAPITokenByHash(ctx, failed.TokenHash, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed token survived: %v", err)
	}
	collision := token
	collision.ID = "usr_bootstrap"
	collision.TokenHash = "collision-human"
	if _, err := s.CreateAPIToken(ctx, collision); err == nil {
		t.Fatal("legacy token collided with human")
	}
	expires := now.Add(time.Hour)
	account := domain.ServiceAccount{ID: "svcacct_legacy_actor", ProjectID: "proj_platform", OwnerUserID: "usr_bootstrap", Name: "managed bot", Role: domain.RoleMaintainer, CreatedAt: now, UpdatedAt: now}
	managed := domain.APIToken{ID: "pat_managed_actor", TokenHash: "managed-actor-hash", ExpiresAt: &expires, CreatedAt: now}
	if _, err := s.CreateServiceAccount(ctx, account, managed, destinationEvent("managed_actor")); err != nil {
		t.Fatal(err)
	}
	collision.ID = account.ID
	collision.TokenHash = "collision-account"
	if _, err := s.CreateAPIToken(ctx, collision); err == nil {
		t.Fatal("legacy token collided with service account")
	}
	account.ID = token.ID
	if _, err := s.CreateServiceAccount(ctx, account, domain.APIToken{ID: "pat_cross_kind", TokenHash: "cross-kind-hash", ExpiresAt: &expires, CreatedAt: now}, destinationEvent("cross_kind")); err == nil {
		t.Fatal("service account collided with legacy token")
	}
	user := domain.User{ID: token.ID, Email: "collision@example.invalid", Name: "collision", Status: domain.UserActive, CreatedAt: now}
	if err := s.ProvisionOIDCUser(ctx, user, domain.OIDCExternalIdentity{Issuer: "https://example.invalid", Subject: "collision", UserID: user.ID, CreatedAt: now}, destinationEvent("human_collision")); err == nil {
		t.Fatal("human collided with legacy token")
	}
	resource := createDestinationResource(t, s, "legacy_actor", "proj_platform")
	resource.ID = "resource_legacy_created"
	resource.Name = "legacy-created"
	resource.CreatedBy = token.ID
	if _, err := s.CreateResource(ctx, resource, store.WithAudit(destinationEvent("legacy_resource"))); err != nil {
		t.Fatalf("legacy actor resource: %v", err)
	}
	resource.ID = "resource_managed_credential"
	resource.Name = "credential-actor"
	resource.CreatedBy = managed.ID
	if _, err := s.CreateResource(ctx, resource); err == nil {
		t.Fatal("managed credential expanded actor registry")
	}
	if _, err := s.RevokeAPIToken(ctx, token.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPITokenByHash(ctx, token.TokenHash, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked token authenticated: %v", err)
	}
	resource.ID = "resource_legacy_history"
	resource.Name = "legacy-history"
	resource.CreatedBy = token.ID
	if _, err := s.CreateResource(ctx, resource); err != nil {
		t.Fatalf("revocation destroyed historical actor: %v", err)
	}
}

func TestPostgresLegacyTokenMigrationBackfillAndImmutability(t *testing.T) {
	ctx := t.Context()
	database, url := openPostgresAtMigration(t, ctx, "legacy_actor_upgrade", "0042_servers_destinations.sql")
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by,created_at,revoked_at) VALUES ('pat_old_actor','old','service_account','old-actor-hash','{runner_admin}','revoked','usr_bootstrap','2026-01-01',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if pg, err := store.OpenPostgres(ctx, url); err == nil {
		closePostgresStore(t, pg)
		t.Fatal("missing0043 ready")
	}
	if err = applyEmbeddedMigrations(ctx, database, "0042_servers_destinations.sql", ""); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer closePostgresStore(t, pg)
	var kind string
	var created time.Time
	if err = database.QueryRow(ctx, `SELECT kind,created_at FROM principals WHERE id='pat_old_actor'`).Scan(&kind, &created); err != nil || kind != "api_token" || created.Year() != 2026 {
		t.Fatalf("backfill kind=%s created=%s err=%v", kind, created, err)
	}
	for _, query := range []string{
		`UPDATE api_tokens SET id='pat_retargeted' WHERE id='pat_old_actor'`,
		`UPDATE api_tokens SET service_account_id='svcacct_missing' WHERE id='pat_old_actor'`,
		`UPDATE principals SET kind='human' WHERE id='pat_old_actor'`,
		`DELETE FROM principals WHERE id='pat_old_actor'`,
	} {
		if _, err = database.Exec(ctx, query); err == nil {
			t.Fatalf("identity mutation accepted: %s", query)
		}
	}
	if _, err = database.Exec(ctx, `ALTER TABLE api_tokens DISABLE TRIGGER api_tokens_immutable_identity`); err != nil {
		t.Fatal(err)
	}
	if ready, err := pg.SchemaCompatible(ctx); err != nil || ready {
		t.Fatalf("disabled identity trigger ready=%v err=%v", ready, err)
	}
}

func TestPostgresLegacyTokenMigrationRejectsKindCollision(t *testing.T) {
	ctx := t.Context()
	database, _ := openPostgresAtMigration(t, ctx, "legacy_actor_collision", "0042_servers_destinations.sql")
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES ('usr_bootstrap','collision','service_account','collision-hash','{}','active','usr_bootstrap')`); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(ctx, database, "0042_servers_destinations.sql", ""); err == nil {
		t.Fatal("migration accepted cross-kind identity collision")
	}
}
