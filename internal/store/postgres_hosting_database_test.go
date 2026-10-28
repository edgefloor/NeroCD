package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func hostingDatabaseApplication(t *testing.T, pg *store.PostgresStore) *app.Service {
	t.Helper()
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: managedRing(t), Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func hostingDatabaseFixture(t *testing.T, engine string) (*store.PostgresStore, *pgxpool.Pool, *app.Service, context.Context, domain.HostingDatabaseCreateInput) {
	t.Helper()
	pg, db, r, _, destination := hostingExecutionFixture(t)
	s := hostingDatabaseApplication(t, pg)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	if engine == "mysql" {
		version = domain.HostingDatabaseMySQLRecipe
	}
	in := domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("a", 32), ProjectID: r.ProjectID, ProjectEnvironmentID: r.ProjectEnvironmentID, Name: "database fixture", DestinationID: destination.ID, Engine: engine, RecipeVersion: version}
	return pg, db, s, ctx, in
}

func hostingDatabaseCounts(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var counts string
	if err := db.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT count(*) FROM resources),(SELECT count(*) FROM resource_bindings),(SELECT count(*) FROM hosting_databases),(SELECT count(*) FROM hosting_database_credentials),(SELECT count(*) FROM hosting_database_receipts),(SELECT count(*) FROM hosting_storage),(SELECT count(*) FROM hosting_storage_receipts),(SELECT count(*) FROM project_managed_secrets),(SELECT count(*) FROM project_managed_secret_versions),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM river_job))::text`).Scan(&counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestHostingDatabasePostgresAtomicCreationReplayAndSecrets(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			pg, db, s, ctx, in := hostingDatabaseFixture(t, engine)
			view, err := s.CreateHostingDatabase(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if view.Resource.Kind != "database" || view.Resource.Generation != 1 || view.Resource.ID != view.Binding.ResourceID || view.Binding.DestinationID != in.DestinationID || view.StorageRef.StorageID == "" || view.RuntimeState != domain.HostingDatabaseUnqualified || len(view.Credentials) != 2 {
				t.Fatalf("disconnected creation %+v", view)
			}
			var plainValues []string
			for _, ref := range view.Credentials {
				record, e := pg.GetManagedSecretRecord(ctx, ref.SecretID, ref.Version)
				if e != nil {
					t.Fatal(e)
				}
				plain, e := managedRing(t).Open(secretcrypto.Binding{ProjectID: in.ProjectID, SecretID: ref.SecretID, Version: ref.Version}, record.Envelope)
				if e != nil || len(plain) != 43 || bytes.Contains(record.Envelope.Data, plain) {
					t.Fatalf("credential not generated/encrypted: length=%d error=%v", len(plain), e)
				}
				plainValues = append(plainValues, string(plain))
				revealed, e := s.RevealManagedSecret(ctx, ref.SecretID, app.ManagedSecretRevealInput{Version: 1})
				if e != nil || revealed.Value != string(plain) {
					t.Fatalf("authorized reveal failed: %v", e)
				}
			}
			if plainValues[0] == plainValues[1] {
				t.Fatal("database roles share a credential")
			}
			encoded, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range append(plainValues, `"value"`, `"ciphertext"`, `"endpoint"`, `"password"`) {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatal("database metadata disclosed credential or fabricated endpoint")
				}
			}
			var audits []byte
			if err = db.QueryRow(ctx, `SELECT jsonb_agg(metadata)::text FROM audit_events`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			for _, plain := range plainValues {
				if bytes.Contains(audits, []byte(plain)) {
					t.Fatal("credential appeared in audit metadata")
				}
			}
			before := hostingDatabaseCounts(t, db)
			replay, err := s.CreateHostingDatabase(ctx, in)
			if err != nil || !reflect.DeepEqual(view, replay) || hostingDatabaseCounts(t, db) != before {
				t.Fatalf("creation replay changed identity/evidence: %v", err)
			}
			read, err := s.GetHostingDatabase(ctx, view.Resource.ID)
			if err != nil || !reflect.DeepEqual(view, read) {
				t.Fatalf("metadata read %+v %v", read, err)
			}
			changed := in
			changed.Name = "changed"
			if _, err = s.CreateHostingDatabase(ctx, changed); !errors.Is(err, store.ErrConflict) || hostingDatabaseCounts(t, db) != before {
				t.Fatalf("changed replay: %v", err)
			}
			if _, err = pg.RequestHostingDeployment(ctx, "hop_database_forbidden", view.Resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("database_forbidden")); !errors.Is(err, store.ErrConflict) || hostingDatabaseCounts(t, db) != before {
				t.Fatalf("generic database queue admission: %v", err)
			}
			if _, err = s.RequestHostingDeployment(ctx, view.Resource.ID, hostingDeploymentRequest()); err == nil {
				t.Fatal("app admitted generic database deployment")
			}
			name := "renamed"
			if _, err = s.UpdateResource(ctx, view.Resource.ID, app.ResourcePatch{Name: &name, ExpectedGeneration: 1}); err == nil {
				t.Fatal("app admitted generic database editing")
			}
			mutated := view.Resource
			mutated.Name = name
			if _, err = pg.UpdateResource(ctx, mutated, 1); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("store admitted generic database edit: %v", err)
			}
		})
	}
}

func TestHostingDatabasePostgresConcurrentRequestConvergence(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			_, db, s, principal, in := hostingDatabaseFixture(t, "postgresql")
			ctx, cancel := context.WithTimeout(principal, 10*time.Second)
			defer cancel()
			results := make(chan error, 2)
			exited := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			t.Cleanup(func() {
				cancel()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for _, done := range exited {
					select {
					case <-done:
					case <-deadline.C:
						t.Error("database creation callers did not exit")
						return
					}
				}
			})
			for n := range 2 {
				go func() {
					defer close(exited[n])
					input := in
					if conflict && n == 1 {
						input.Name = "different"
					}
					_, err := s.CreateHostingDatabase(ctx, input)
					results <- err
				}()
			}
			wins, conflicts := 0, 0
			for range 2 {
				select {
				case err := <-results:
					if err == nil {
						wins++
					} else if errors.Is(err, store.ErrConflict) {
						conflicts++
					} else {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if !conflict && wins != 2 || conflict && (wins != 1 || conflicts != 1) {
				t.Fatalf("concurrent creation wins=%d conflicts=%d", wins, conflicts)
			}
			var databases, credentials, receipts, jobs int
			if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_databases),(SELECT count(*) FROM hosting_database_credentials),(SELECT count(*) FROM hosting_database_receipts),(SELECT count(*) FROM river_job)`).Scan(&databases, &credentials, &receipts, &jobs); err != nil || databases != 1 || credentials != 2 || receipts != 1 || jobs != 0 {
				t.Fatalf("duplicate creation %d %d %d %d %v", databases, credentials, receipts, jobs, err)
			}
		})
	}
}

func TestHostingDatabasePostgresAuditRollbackAndCurrentAuthority(t *testing.T) {
	for _, failure := range []string{"resource.created", "resource.destination_bound", "hosting.storage.created", "managed_secret.created", "hosting.database.created", "destination_grant", "environment_scope", "owner_role"} {
		t.Run(failure, func(t *testing.T) {
			_, db, s, ctx, in := hostingDatabaseFixture(t, "redis")
			switch failure {
			case "destination_grant":
				if _, err := db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, in.DestinationID, in.ProjectID); err != nil {
					t.Fatal(err)
				}
			case "environment_scope":
				in.ProjectEnvironmentID = "missing_environment"
			case "owner_role":
				account, err := s.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: in.ProjectID, Name: "database maintainer", Role: domain.RoleMaintainer})
				if err != nil {
					t.Fatal(err)
				}
				ctx = auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
			default:
				if _, err := db.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_database_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'fixture database audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_database_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_database_audit()`, failure)); err != nil {
					t.Fatal(err)
				}
			}
			before := hostingDatabaseCounts(t, db)
			if _, err := s.CreateHostingDatabase(ctx, in); err == nil {
				t.Fatal("invalid creation admitted")
			}
			if hostingDatabaseCounts(t, db) != before {
				t.Fatal("failed creation left resource/binding/storage/secret/audit/receipt writes")
			}
		})
	}
	pg, db, s, admin, in := hostingDatabaseFixture(t, "postgresql")
	account, err := s.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: in.ProjectID, Name: "database owner", Role: domain.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
	view, err := s.CreateHostingDatabase(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, db)
	if _, err = s.CreateHostingDatabase(admin, in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different actor replay: %v", err)
	}
	if _, err = db.Exec(admin, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, in.DestinationID, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateHostingDatabase(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked grant replay: %v", err)
	}
	if _, err = pg.GetHostingDatabase(ctx, account.ServiceAccount.ID, view.Resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked grant metadata read: %v", err)
	}
	if _, err = db.Exec(admin, `UPDATE destination_project_grants SET revoked_at=NULL WHERE destination_id=$1 AND project_id=$2`, in.DestinationID, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(admin, `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, account.ServiceAccount.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.CreateHostingDatabase(ctx, account.ServiceAccount.ID, in, nil, destinationEvent("revoked_owner")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked owner replay: %v", err)
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("unauthorized replay changed creation evidence")
	}
}
