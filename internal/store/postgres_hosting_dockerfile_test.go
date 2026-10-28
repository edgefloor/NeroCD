package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresStoredDockerfileVersionsAndBuildAuthority(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	existing, err := f.pg.GetResource(ctx, f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	content := "FROM scratch\nLABEL revision=one\n"
	src := domain.HostingGitSource{Kind: "dockerfile", ID: "stored_source", ResourceID: "stored_resource", ProjectID: f.src.ProjectID, Generation: 1, Content: content, ContentHash: domain.HostingDockerfileContentHash(content), Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}
	item, err := f.pg.CreateResource(ctx, domain.Resource{ID: src.ResourceID, ProjectID: src.ProjectID, ProjectEnvironmentID: existing.ProjectEnvironmentID, Name: "stored", Kind: "application", Source: domain.ResourceSource{Kind: "dockerfile", Dockerfile: &domain.DockerfileResourceSource{SourceID: src.ID, Generation: 1, DockerfileResourceConfiguration: domain.DockerfileResourceConfiguration{Content: content, Settings: src.Settings}}}, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, store.WithHostingGitSource(src), store.WithAudit(destinationEvent("stored_created")))
	if err != nil {
		t.Fatal(err)
	}
	if item.Source.Dockerfile.Content != content || item.Source.Dockerfile.ContentHash != src.ContentHash {
		t.Fatal("source expansion lost content")
	}
	_, err = f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "stored_binding", ResourceID: item.ID, DestinationID: "dest_build", CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("stored_bound"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(id string) (domain.HostingBuild, error) {
		return f.pg.RequestHostingBuild(ctx, id, item.ID, "usr_bootstrap", buildInput(id), buildPolicy(), buildEnqueue, destinationEvent(id))
	}
	if _, err = request("stored-no-capability"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("missing capability admitted", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingStoredDockerfileCapability); err != nil {
		t.Fatal(err)
	}
	b, err := request("stored-build")
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot.Source.Kind != "dockerfile" || b.Snapshot.Source.Content != content || b.Snapshot.Grant != nil {
		t.Fatal("incorrect frozen source")
	}
	raw, _ := json.Marshal(domain.PublicBuild(b))
	if strings.Contains(string(raw), "FROM scratch") || strings.Contains(string(raw), "repository_url") || strings.Contains(string(raw), "requested_ref") {
		t.Fatal("public source transport/content leakage")
	}
	bad := buildInput("stored-grant")
	bad.GrantID = "not-a-source-grant"
	if _, err = f.pg.RequestHostingBuild(ctx, "stored-grant", item.ID, "usr_bootstrap", bad, buildPolicy(), buildEnqueue, destinationEvent("stored_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stored source accepted Git grant", err)
	}

	b, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, destinationEvent("stored_dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	setCapability := func(enabled bool) {
		t.Helper()
		query := `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`
		if enabled {
			query = `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`
		}
		if _, e := f.db.Exec(ctx, query, f.runner, domain.HostingStoredDockerfileCapability); e != nil {
			t.Fatal(e)
		}
	}
	setCapability(false)
	claim, claimErr := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if claimErr == nil && claim.Run.ID == b.RunID {
		t.Fatal("incapable claim admitted")
	}
	setCapability(true)
	claim, err = f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != b.RunID {
		t.Fatal("capable claim denied", err)
	}
	authority := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	setCapability(false)
	if _, err = f.pg.BeginHostingBuildSource(ctx, f.runner, authority, b.Revision); err == nil {
		t.Fatal("removed live capability allowed sourcing")
	}
	setCapability(true)
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, authority, b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := domain.HostingBuildSourceReceipt{Kind: "dockerfile", ContentHash: src.ContentHash, BuildID: b.ID, SourceID: src.ID, InputID: b.Snapshot.InputID, TreeDigest: "sha256:" + strings.Repeat("b", 64)}
	wrong := receipt
	wrong.ContentHash = "sha256:" + strings.Repeat("c", 64)
	if _, err = f.pg.CommitHostingBuildSource(ctx, f.runner, authority, "stored-wrong", wrong); err == nil {
		t.Fatal("wrong content identity accepted")
	}
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, authority, "stored-source", receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.PrepareHostingBuild(ctx, f.runner, authority, b.Revision, "stored-daemon"); err != nil {
		t.Fatal(err)
	}
	for _, withSource := range []bool{false, true} {
		item.Name += "x"
		opts := []store.MutationOption{store.WithAudit(destinationEvent(item.Name))}
		if withSource {
			opts = append(opts, store.WithHostingGitSource(src))
		}
		item, err = f.pg.UpdateResource(ctx, item, item.Generation, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if item.Source.Dockerfile.Generation != 1 {
			t.Fatal("name/unchanged source advanced source generation")
		}
	}
	src.Content = "FROM scratch\nLABEL revision=two\n"
	src.ContentHash = domain.HostingDockerfileContentHash(src.Content)
	item, err = f.pg.UpdateResource(ctx, item, item.Generation, store.WithHostingGitSource(src), store.WithAudit(destinationEvent("stored_changed")))
	if err != nil {
		t.Fatal(err)
	}
	if item.Source.Dockerfile.Generation != 2 || item.Source.Dockerfile.Content != src.Content {
		t.Fatal("source edit not versioned")
	}

	src.Generation = 2
	src.Content = strings.Repeat("\x01", domain.HostingDockerfileContentLimit)
	src.ContentHash = domain.HostingDockerfileContentHash(src.Content)
	for i := range 64 {
		src.Settings.BuildVariables = append(src.Settings.BuildVariables, domain.HostingBuildVariable{Name: fmt.Sprintf("VAR_%d", i), Value: strings.Repeat("\x01", 4096)})
	}
	item, err = f.pg.UpdateResource(ctx, item, item.Generation, store.WithHostingGitSource(src), store.WithAudit(destinationEvent("stored_maximum")))
	if err != nil {
		t.Fatal("maximum escaped source/settings failed persistence", err)
	}
	if item.Source.Dockerfile.Generation != 3 || item.Source.Dockerfile.Content != src.Content {
		t.Fatal("maximum source changed")
	}
	var versions int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_source_versions WHERE source_id=$1`, src.ID).Scan(&versions); err != nil || versions != 3 {
		t.Fatal("history mismatch", versions, err)
	}
	if _, err = request("stored-stale"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale generation admitted", err)
	}
	// Deferred database consistency must reject identity replacement even if a caller bypasses Go.
	if _, err = f.db.Exec(ctx, `UPDATE resources SET source=jsonb_build_object('kind','dockerfile','dockerfile',jsonb_build_object('source_id','forged','generation',1)) WHERE id=$1`, item.ID); err == nil {
		t.Fatal("database permitted source identity replacement")
	}
}
