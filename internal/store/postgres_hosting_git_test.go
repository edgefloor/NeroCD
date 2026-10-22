package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresGitResourceAtomicVersionsAndCAS(t *testing.T) {
	pg, db := openPostgresIntegrationStore(t, t.Context(), "git_atomic")
	defer closePostgresStore(t, pg)
	item, src := createBuildGitResource(t, pg, "git_atomic", "proj_platform", domain.HostingGitSource{})
	ctx := t.Context()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT source FROM resources WHERE id=$1`, item.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "repository_url") || strings.Contains(string(raw), "policy") {
		t.Fatal("configuration leaked into resource reference", string(raw))
	}
	for _, withSource := range []bool{false, true} {
		item.Name += "x"
		item.UpdatedAt = time.Now().UTC()
		opts := []store.MutationOption{store.WithAudit(destinationEvent(item.Name))}
		if withSource {
			opts = append(opts, store.WithHostingGitSource(src))
		}
		updated, err := pg.UpdateResource(ctx, item, item.Generation, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Generation != item.Generation+1 || updated.Source.Git.Generation != 1 {
			t.Fatalf("name/unchanged source: %+v", updated)
		}
		item = updated
	}
	src.RequestedRef = "release-v2"
	updated, err := pg.UpdateResource(ctx, item, item.Generation, store.WithHostingGitSource(src), store.WithAudit(destinationEvent("git_changed")))
	if err != nil || updated.Generation != 4 || updated.Source.Git.Generation != 2 {
		t.Fatalf("changed source: %+v %v", updated, err)
	}
	item = updated
	src.Generation = 2
	old, err := pg.GetHostingBuildSource(ctx, "usr_bootstrap", item.ID)
	if err != nil || old.RequestedRef != "release-v2" {
		t.Fatal(old, err)
	}
	src.RequestedRef = "must-rollback"
	if _, err = pg.UpdateResource(ctx, item, 4, store.WithHostingGitSource(src), store.WithAudit(destinationEvent("git_changed"))); err == nil {
		t.Fatal("duplicate audit committed")
	}
	item, err = pg.GetResource(ctx, item.ID)
	if err != nil || item.Generation != 4 || item.Source.Git.Generation != 2 || item.Source.Git.RequestedRef != "release-v2" {
		t.Fatal("partial write", item, err)
	}
	var versions int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_source_versions WHERE source_id=$1`, src.ID).Scan(&versions); err != nil || versions != 2 {
		t.Fatal("rollback source", versions, err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, ref := range []string{"race-a", "race-b"} {
		group.Go(func() {
			candidate := src
			candidate.RequestedRef = ref
			_, e := pg.UpdateResource(ctx, item, 4, store.WithHostingGitSource(candidate), store.WithAudit(destinationEvent(ref)))
			results <- e
		})
	}
	group.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_source_versions WHERE source_id=$1`, src.ID).Scan(&versions); err != nil || versions != 3 {
		t.Fatal("CAS appended duplicate source", versions, err)
	}
	var first string
	if err = db.QueryRow(ctx, `SELECT body->>'requested_ref' FROM hosting_build_source_versions WHERE source_id=$1 AND generation=1`, src.ID).Scan(&first); err != nil || first != "main" {
		t.Fatal("history lost", first, err)
	}
}
func TestPostgresGitResourceDeferredConsistencyAndRetiredWriter(t *testing.T) {
	pg, db := openPostgresIntegrationStore(t, t.Context(), "git_consistency")
	defer closePostgresStore(t, pg)
	item, src := createBuildGitResource(t, pg, "git_consistency", "proj_platform", domain.HostingGitSource{})
	if _, err := pg.ConfigureHostingBuildSource(t.Context(), "usr_bootstrap", 1, src, destinationEvent("retired")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("sidecar writer enabled", err)
	}
	for _, query := range []string{
		`UPDATE resources SET source=jsonb_set(source,'{git,generation}','2'),generation=generation+1 WHERE id=$1`,
		`UPDATE resources SET source='{"kind":"image","image":{"reference":"nginx:1.27"}}',generation=generation+1 WHERE id=$1`,
		`UPDATE resources SET source=jsonb_set(source,'{git,source_id}','"missing"'),generation=generation+1 WHERE id=$1`,
	} {
		if _, err := db.Exec(t.Context(), query, item.ID); err == nil {
			t.Fatal("raw inconsistent reference committed", query)
		}
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	src.Generation = 2
	src.RequestedRef = "direct-sidecar"
	raw, _ := json.Marshal(src)
	if _, err = tx.Exec(t.Context(), `UPDATE hosting_build_sources SET generation=2 WHERE id=$1`, src.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_build_source_versions(source_id,generation,body,body_hash,created_by) VALUES($1,2,$2,$3,'usr_bootstrap')`, src.ID, raw, domain.HostingDocumentHash(src)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err == nil {
		t.Fatal("independent sidecar update committed")
	}
	// Current scoped audit has resource identity even when caller supplies metadata.
	audits, err := pg.ListAuditEventsPageForScope(t.Context(), store.AuditAuthorizationScope{ProjectIDs: []string{"proj_platform"}}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits.Items {
		if audit.TargetID == item.ID && audit.Action == "resource.created" {
			found = true
			if audit.Metadata["project_id"] != item.ProjectID || audit.Metadata["untrusted"] != nil {
				t.Fatal("uncanonical resource audit", audit)
			}
		}
	}
	if !found {
		t.Fatal("resource audit invisible in project scope")
	}

	page, err := pg.ListResources(t.Context(), "proj_platform", "", store.Page{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Source.Git.SourceID != src.ID {
		t.Fatal(page, err)
	}
}
func TestPostgresGitResourceReadiness(t *testing.T) {
	pg, db := openPostgresIntegrationStore(t, t.Context(), "git_readiness")
	defer closePostgresStore(t, pg)
	for _, entry := range []struct{ table, trigger string }{{"resources", "resources_git_reference"}, {"resources", "resources_source_kind"}, {"hosting_build_sources", "hosting_sources_resource_reference"}, {"hosting_build_source_versions", "hosting_source_versions_resource_reference"}} {
		if _, err := db.Exec(t.Context(), "ALTER TABLE "+entry.table+" DISABLE TRIGGER "+entry.trigger); err != nil {
			t.Fatal(err)
		}
		if ok, err := pg.SchemaCompatible(t.Context()); err != nil || ok {
			t.Fatal("disabled trigger ready", entry, ok, err)
		}
		if _, err := store.OpenPostgres(t.Context(), db.Config().ConnConfig.ConnString()); err == nil {
			t.Fatal("startup accepted disabled trigger")
		}
		if _, err := db.Exec(t.Context(), "ALTER TABLE "+entry.table+" ENABLE TRIGGER "+entry.trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(t.Context(), `DROP FUNCTION nerocd_hosting_git_resource_schema_compatible()`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenPostgres(t.Context(), db.Config().ConnConfig.ConnString()); err == nil {
		t.Fatal("startup accepted missing0049")
	}
}

func TestPostgresGitAndImageAdmissionAreExclusive(t *testing.T) {
	f := newBuildStoreFixture(t)
	enableBuildFixtureImageAdmission(t, f)
	if _, err := f.pg.RequestHostingDeployment(t.Context(), "hop_git_denied", f.src.ResourceID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("git_deploy_denied")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("Git admitted to image queue", err)
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_operations WHERE resource_id=$1`, f.src.ResourceID).Scan(&count); err != nil || count != 0 {
		t.Fatal("Git deployment wrote intent", count, err)
	}
	image := createDestinationResource(t, f.pg, "legacy_sidecar_image", "proj_platform")
	if _, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "binding_legacy_image", ResourceID: image.ID, DestinationID: "dest_build", CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("bind_legacy_image")); err != nil {
		t.Fatal(err)
	}
	// Reproduce an applied0047/48 historical sidecar;0049 preserves those rows.
	for _, entry := range []struct{ table, trigger string }{{"hosting_build_sources", "hosting_sources_resource_reference"}, {"hosting_build_source_versions", "hosting_source_versions_resource_reference"}} {
		if _, err := f.db.Exec(t.Context(), "ALTER TABLE "+entry.table+" DISABLE TRIGGER "+entry.trigger); err != nil {
			t.Fatal(err)
		}
	}
	legacy := f.src
	legacy.ID = "source_legacy_image"
	legacy.ResourceID = image.ID
	body, _ := json.Marshal(legacy)
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_build_sources(id,resource_id,project_id,generation) VALUES($1,$2,$3,1)`, legacy.ID, image.ID, image.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_build_source_versions(source_id,generation,body,body_hash,created_by) VALUES($1,1,$2,$3,'usr_bootstrap')`, legacy.ID, body, domain.HostingDocumentHash(legacy)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ table, trigger string }{{"hosting_build_sources", "hosting_sources_resource_reference"}, {"hosting_build_source_versions", "hosting_source_versions_resource_reference"}} {
		if _, err = f.db.Exec(t.Context(), "ALTER TABLE "+entry.table+" ENABLE TRIGGER "+entry.trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.pg.GetHostingBuildSource(t.Context(), "usr_bootstrap", image.ID); err != nil {
		t.Fatal("historical image sidecar lost", err)
	}
	called := false
	enqueue := func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error { called = true; return nil }
	if _, err = f.pg.RequestHostingBuild(t.Context(), "image-denied", image.ID, "usr_bootstrap", buildInput("image-denied"), buildPolicy(), enqueue, destinationEvent("image_build_denied")); !errors.Is(err, store.ErrConflict) || called {
		t.Fatal("image admitted to build queue", called, err)
	}
	if _, err = f.pg.ResolveCurrentHostingBuildArtifact(t.Context(), "usr_bootstrap", image.ID, "absent"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("image accepted current build resolution", err)
	}
}
