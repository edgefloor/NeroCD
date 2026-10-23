package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func hostingStorageFixture(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, domain.Resource, domain.ResourceBinding) {
	t.Helper()
	pg, db := openPostgresIntegrationStore(t, t.Context(), "owned_storage")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	resource := createDestinationResource(t, pg, "owned_storage", "proj_platform")
	_, destination := createDestinationTarget(t, pg, "owned_storage")
	grantDestination(t, pg, destination.ID, resource.ProjectID, "owned_storage")
	binding, err := pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "binding_owned_storage", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("owned_storage_binding"))
	if err != nil {
		t.Fatal(err)
	}
	return pg, db, resource, binding
}
func hostingStorageInput() domain.HostingStorageInput {
	return domain.HostingStorageInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, MountPath: "/app/uploads"}
}
func saveHostingStorage(t *testing.T, pg *store.PostgresStore, resource string, in domain.HostingStorageInput) domain.HostingStorage {
	t.Helper()
	item, err := pg.CreateHostingStorage(t.Context(), "usr_bootstrap", resource, in, destinationEvent("ignored"))
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func storageCounts(t *testing.T, db *pgxpool.Pool, want int) {
	t.Helper()
	var metadata, receipts, audits int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_storage),(SELECT count(*) FROM hosting_storage_receipts),(SELECT count(*) FROM audit_events WHERE action='hosting.storage.created')`).Scan(&metadata, &receipts, &audits); err != nil {
		t.Fatal(err)
	}
	if metadata != want || receipts != want || audits != want {
		t.Fatalf("metadata=%d receipts=%d audits=%d want=%d", metadata, receipts, audits, want)
	}
}

func TestHostingStoragePostgresReplayRenameAndIdentity(t *testing.T) {
	pg, db, resource, binding := hostingStorageFixture(t)
	ctx := t.Context()
	if _, err := pg.GetHostingStorage(ctx, "usr_bootstrap", resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unconfigured read: %v", err)
	}
	in := hostingStorageInput()
	saved := saveHostingStorage(t, pg, resource.ID, in)
	if saved.Validate() != nil || saved.BindingID != binding.ID || saved.DestinationID != binding.DestinationID || saved.ResourceID != resource.ID || saved.ProjectID != resource.ProjectID {
		t.Fatalf("identity: %+v", saved)
	}
	storageCounts(t, db, 1)
	replay := saveHostingStorage(t, pg, resource.ID, in)
	if !reflect.DeepEqual(replay, saved) {
		t.Fatalf("response-loss retry changed metadata: %+v / %+v", saved, replay)
	}
	current, err := pg.GetResource(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != resource.Generation {
		t.Fatal("storage changed resource generation")
	}
	current.Name = "renamed_uploads"
	renamed, err := pg.UpdateResource(ctx, current, current.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Generation != 2 {
		t.Fatal(renamed.Generation)
	}
	replay = saveHostingStorage(t, pg, resource.ID, in)
	read, err := pg.GetHostingStorage(ctx, "usr_bootstrap", resource.ID)
	if err != nil || !reflect.DeepEqual(replay, saved) || !reflect.DeepEqual(read, saved) {
		t.Fatalf("rename changed storage: replay=%+v read=%+v err=%v", replay, read, err)
	}
	for _, changed := range []domain.HostingStorageInput{
		{RequestID: in.RequestID, ExpectedResourceGeneration: 1, MountPath: "/other"},
		{RequestID: strings.Repeat("b", 32), ExpectedResourceGeneration: 2, MountPath: in.MountPath},
		{RequestID: strings.Repeat("c", 32), ExpectedResourceGeneration: 1, MountPath: in.MountPath},
	} {
		if _, err = pg.CreateHostingStorage(ctx, "usr_bootstrap", resource.ID, changed, destinationEvent("conflict")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("conflicting request accepted: %+v %v", changed, err)
		}
	}
	var metadata []byte
	if err = db.QueryRow(ctx, `SELECT metadata FROM audit_events WHERE id=$1`, domain.HostingStorageAuditID(resource.ID, in.RequestID)).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var audit map[string]any
	if json.Unmarshal(metadata, &audit) != nil || audit["storage_hash"] != saved.Hash || audit["binding_id"] != binding.ID || audit["untrusted"] != nil {
		t.Fatalf("audit was not server-derived: %s", metadata)
	}
	storageCounts(t, db, 1)
}

func TestHostingStoragePostgresConcurrentRequests(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "duplicate"
		if conflict {
			name = "conflict"
		}
		t.Run(name, func(t *testing.T) {
			pg, db, resource, _ := hostingStorageFixture(t)
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for index := range 2 {
				wg.Go(func() {
					in := hostingStorageInput()
					if conflict && index == 1 {
						in.MountPath = "/different"
					}
					_, err := pg.CreateHostingStorage(t.Context(), "usr_bootstrap", resource.ID, in, destinationEvent("concurrent"))
					results <- err
				})
			}
			wg.Wait()
			close(results)
			successes, conflicts := 0, 0
			for err := range results {
				if err == nil {
					successes++
				} else if errors.Is(err, store.ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if conflict && (successes != 1 || conflicts != 1) || !conflict && (successes != 2 || conflicts != 0) {
				t.Fatalf("success=%d conflict=%d", successes, conflicts)
			}
			storageCounts(t, db, 1)
		})
	}
}

func TestHostingStoragePostgresCurrentAuthorityAndGrant(t *testing.T) {
	pg, db, resource, binding := hostingStorageFixture(t)
	ctx := t.Context()
	if _, err := pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "storage_owner", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	in := hostingStorageInput()
	if _, err := pg.CreateHostingStorage(ctx, "usr_viewer", resource.ID, in, destinationEvent("owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=$1 AND user_id='usr_viewer'`, resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateHostingStorage(ctx, "usr_viewer", resource.ID, in, destinationEvent("replay")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked write returned prior receipt: %v", err)
	}
	if _, err := pg.GetHostingStorage(ctx, "usr_viewer", resource.ID); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM project_members WHERE project_id='proj_security' AND user_id='usr_viewer'`); err != nil {
		t.Fatal(err)
	}
	other := createDestinationResource(t, pg, "storage_other_project", "proj_security")
	if _, err := pg.GetHostingStorage(ctx, "usr_viewer", other.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross-project read: %v", err)
	}
	if _, err := pg.CreateHostingStorage(ctx, "usr_viewer", other.ID, in, destinationEvent("other")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross-project create: %v", err)
	}
	if _, err := pg.RevokeDestinationProject(ctx, binding.DestinationID, resource.ProjectID, destinationEvent("revoke_storage")); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetHostingStorage(ctx, "usr_viewer", resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked grant read: %v", err)
	}
	if _, err := pg.CreateHostingStorage(ctx, "usr_bootstrap", resource.ID, in, destinationEvent("revoked_grant")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("admin replay bypassed revoked grant: %v", err)
	}
	storageCounts(t, db, 1)
}

func TestHostingStoragePostgresAuditFailureRollsBack(t *testing.T) {
	pg, db, resource, _ := hostingStorageFixture(t)
	if _, err := db.Exec(t.Context(), `CREATE FUNCTION fail_storage_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage.created' THEN RAISE EXCEPTION 'test audit commit failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER storage_audit_failure AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_storage_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateHostingStorage(t.Context(), "usr_bootstrap", resource.ID, hostingStorageInput(), destinationEvent("rollback")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("audit failure: %v", err)
	}
	storageCounts(t, db, 0)
}

func TestHostingStoragePostgresImmutableSchemaAndFailClosed(t *testing.T) {
	pg, db, resource, _ := hostingStorageFixture(t)
	saveHostingStorage(t, pg, resource.ID, hostingStorageInput())
	for _, statement := range []string{
		`UPDATE hosting_storage SET mount_path='/moved'`,
		`UPDATE hosting_storage SET volume_name='adopted'`,
		`UPDATE hosting_storage SET generation=2`,
		`DELETE FROM hosting_storage`,
		`UPDATE hosting_storage_receipts SET request_hash='sha256:'||repeat('0',64)`,
		`DELETE FROM hosting_storage_receipts`,
	} {
		_, err := db.Exec(t.Context(), statement)
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
			t.Fatalf("immutable mutation accepted: %s: %v", statement, err)
		}
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE hosting_storage DISABLE TRIGGER hosting_storage_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetHostingStorage(t.Context(), "usr_bootstrap", resource.ID); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("read accepted incompatible schema: %v", err)
	}
	if _, err := pg.CreateHostingStorage(t.Context(), "usr_bootstrap", resource.ID, hostingStorageInput(), destinationEvent("incompatible")); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("replay accepted incompatible schema: %v", err)
	}
}

func TestHostingStoragePostgresRequiresCurrentImageBinding(t *testing.T) {
	pg, db, resource, binding := hostingStorageFixture(t)
	ctx := t.Context()
	unbound := createDestinationResource(t, pg, "storage_unbound", resource.ProjectID)
	if _, err := pg.CreateHostingStorage(ctx, "usr_bootstrap", unbound.ID, hostingStorageInput(), destinationEvent("unbound")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unbound resource accepted: %v", err)
	}
	git, _ := createBuildGitResource(t, pg, "storage_git", resource.ProjectID, domain.HostingGitSource{})
	if _, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_storage_git", ResourceID: git.ID, DestinationID: binding.DestinationID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("git_binding")); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateHostingStorage(ctx, "usr_bootstrap", git.ID, hostingStorageInput(), destinationEvent("git_storage")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Git source accepted: %v", err)
	}
	if _, err := pg.GetHostingStorage(ctx, "usr_bootstrap", git.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Git source read accepted: %v", err)
	}
	for _, value := range []string{"/app/uploads", "/processor", "/data/./upload", "/dev/shm", "/", "/app,readonly"} {
		var accepted bool
		if err := db.QueryRow(ctx, `SELECT nerocd_hosting_storage_path_valid($1)`, value).Scan(&accepted); err != nil {
			t.Fatal(err)
		}
		if accepted != (domain.ValidateHostingStorageMountPath(value) == nil) {
			t.Fatalf("SQL/domain mount disagreement for %q", value)
		}
	}
	storageCounts(t, db, 0)
}
