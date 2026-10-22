package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
	"nerocd/web"
)

func TestPostgresSourceGrantAtomicReplayGenerationAndRevocation(t *testing.T) {
	f, _, _ := privateDeliveryFixture(t)
	in := domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: "delivery_secret", SecretVersion: 1, RequestID: strings.Repeat("c", 32)}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	ids := make(chan string, 8)
	for range 8 {
		group.Go(func() {
			g, e := f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, in, destinationEvent("grant_retry"))
			failures <- e
			ids <- g.ID
		})
	}
	group.Wait()
	close(failures)
	close(ids)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := domain.HostingSourceGrantID(f.src.ResourceID, in.RequestID)
	for got := range ids {
		if got != id {
			t.Fatal("unstable grant identity")
		}
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE id=$1`, "aud_"+id).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry did not share atomic audit", err, count)
	}
	page, err := f.pg.ListHostingSourceGrants(t.Context(), "usr_bootstrap", f.src.ResourceID, store.Page{Limit: 1, Enabled: true, LimitSpecified: true})
	if err != nil || page.Limit != 1 || len(page.Items) != 1 {
		t.Fatal("bounded listing", err)
	}
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, []byte("secret_id")) || bytes.Contains(raw, []byte("delivery_secret")) || bytes.Contains(raw, []byte("password")) {
		t.Fatal("grant listing disclosed secrets")
	}
	changed := in
	other, record, audit := managedFixture(t, "other_delivery_secret", 1)
	if _, err = f.pg.CreateManagedSecret(t.Context(), other, record, audit); err != nil {
		t.Fatal(err)
	}
	changed.SecretID = other.ID
	if _, err = f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, changed, destinationEvent("changed_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed intent replay", err)
	}
	if _, err = f.pg.RequestHostingSourceGrant(t.Context(), "usr_viewer", f.src.ResourceID, in, destinationEvent("viewer_grant")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer minted grant", err)
	}
	if err = f.pg.RevokeResourceHostingSourceGrant(t.Context(), "usr_bootstrap", "other-resource", id, destinationEvent("wrong_revoke")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("cross resource revoke", err)
	}
	if err = f.pg.RevokeResourceHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, id, destinationEvent("revoke_request_grant")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, in, destinationEvent("revoked_replay")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked replay", err)
	}
	// A deferred audit failure must roll back the grant, even after its INSERT succeeded.
	if _, err = f.db.Exec(t.Context(), `CREATE FUNCTION fail_source_grant_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.source_grant.created' THEN RAISE EXCEPTION 'test grant audit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER source_grant_commit_failure AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_source_grant_commit()`); err != nil {
		t.Fatal(err)
	}
	in.RequestID = strings.Repeat("d", 32)
	if _, err = f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, in, destinationEvent("failed_commit_grant")); err == nil {
		t.Fatal("commit failure ignored")
	}
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_source_grants WHERE id=$1`, domain.HostingSourceGrantID(f.src.ResourceID, in.RequestID)).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed audit left grant", err, count)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, in, destinationEvent("stale_generation_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale generation accepted", err)
	}
}

func TestPostgresPrivateHostingAPIGrantBuildAndCredential(t *testing.T) {
	f := newBuildStoreFixture(t)
	setPrivateGitCapability(t, f, true)
	pg := f.pg
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: managedRing(t), Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	if err = service.ConfigureHostingGitHosts([]string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	source := domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: f.src.RepositoryURL, RequestedRef: f.src.RequestedRef, CredentialKind: "https_basic", Settings: f.src.Settings}}}
	resource, err := service.UpdateResource(admin, f.src.ResourceID, app.ResourcePatch{ExpectedGeneration: 1, Source: &source})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := service.CreateManagedSecret(admin, app.ManagedSecretInput{ProjectID: f.src.ProjectID, Name: "private-git", Value: `{"username":"private-git-user","password":"private-git-password"}`})
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateAPIToken(admin, app.APITokenInput{Name: "grant-owner", Roles: []string{domain.RoleSystemAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	server := api.NewServer(service, slog.New(slog.NewTextHandler(&logs, nil)), web.Static())
	httpServer := httptest.NewTLSServer(server)
	defer httpServer.Close()
	request := func(method, path, token string, body any, status int) []byte {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := httpServer.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = resp.Body.Close() }()
		out, e := io.ReadAll(resp.Body)
		if e != nil || resp.StatusCode != status {
			t.Fatalf("%s %s status=%d expected=%d err=%v", method, path, resp.StatusCode, status, e)
		}
		return out
	}
	grantIn := domain.HostingSourceGrantRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, SecretID: secret.ID, SecretVersion: 1, RequestID: strings.Repeat("e", 32)}
	grantPath := "/api/v1/resources/" + resource.ID + "/source-grants"
	var granted struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}
	if err = json.Unmarshal(request("POST", grantPath, token.Token, grantIn, 201), &granted); err != nil {
		t.Fatal(err)
	}
	if granted.Grant.SourceGeneration != 2 {
		t.Fatal("grant lost exact source generation")
	}
	request("POST", grantPath, token.Token, grantIn, 201)
	list := request("GET", grantPath+"?limit=1", token.Token, nil, 200)
	if bytes.Contains(list, []byte(secret.ID)) || bytes.Contains(list, []byte("private-git-password")) {
		t.Fatal("list leaked secret")
	}
	startBuildController(t, f)
	in := domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "private-api", GrantID: granted.Grant.ID}
	// Existing foundational grants have no atomic source-generation evidence.
	current, err := pg.GetHostingBuildSource(t.Context(), "usr_bootstrap", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := domain.HostingSourceGrant{ID: "legacy_api_grant", Version: 1, SourceID: current.ID, ResourceID: resource.ID, ProjectID: resource.ProjectID, RepositoryURL: current.RepositoryURL, PolicyHash: domain.HostingDocumentHash(current.Policy), SecretID: secret.ID, SecretVersion: 1, Kind: "https_basic"}
	if _, err = pg.CreateHostingSourceGrant(t.Context(), "usr_bootstrap", legacy, destinationEvent("legacy_api_grant")); err != nil {
		t.Fatal(err)
	}
	missingEvidence := in
	missingEvidence.GrantID = legacy.ID
	missingEvidence.IdempotencyKey = "legacy-api"
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, missingEvidence, 409)
	legacyRequest := domain.HostingBuildInput(missingEvidence)
	legacyBuild, err := pg.RequestHostingBuild(t.Context(), "legacy-api-replay", resource.ID, token.APIToken.ID, legacyRequest, buildPolicy(), buildEnqueue, domain.AuditEvent{ID: "legacy_api_replay", ActorID: token.APIToken.ID, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, missingEvidence, 409)
	// Settle this foundation-only fixture so it cannot compete with the real claim.
	if _, err = pg.CancelHostingBuild(t.Context(), token.APIToken.ID, legacyBuild.ID, legacyBuild.Revision, domain.AuditEvent{ID: "cancel_legacy_api", ActorID: token.APIToken.ID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	raw := request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, in, 202)
	if err = json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	var replayAccepted struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	if err = json.Unmarshal(request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, in, 202), &replayAccepted); err != nil || replayAccepted.Build.ID != accepted.Build.ID {
		t.Fatal("current exact retry duplicated build", err)
	}
	var queued int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE kind='hosting.prepare_build.v1' AND args->>'build_id'=$1`, accepted.Build.ID).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("private retry did not share one enqueue", err, queued)
	}
	if bytes.Contains(raw, []byte(granted.Grant.ID)) || bytes.Contains(raw, []byte(secret.ID)) {
		t.Fatal("public build leaked grant/secret")
	}
	deadline := time.Now().Add(10 * time.Second)
	var b domain.HostingBuild
	for {
		b, err = pg.GetHostingBuild(t.Context(), "usr_bootstrap", accepted.Build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.RunID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private River dispatch missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.Snapshot.Grant == nil || b.Snapshot.Grant.ID != granted.Grant.ID {
		t.Fatal("grant not frozen in build")
	}
	runnerCtx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
	claim, err := service.ClaimRun(runnerCtx)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	client := runnerclient.New(httpServer.URL, "build-fixture-token")
	client.HTTP = httpServer.Client()
	if _, err = client.BuildSourceBegin(t.Context(), hostingbuildprotocol.SourceBeginRequest{Authority: a, ExpectedRevision: b.Revision}); err != nil {
		t.Fatal(err)
	}
	delivery := domain.HostingSourceCredentialRequest{Authority: a, DeliveryID: strings.Repeat("f", 32)}
	material, err := client.BuildSourceCredential(t.Context(), f.runner, delivery, runnerclient.SourceCredentialTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	if material.Username != "private-git-user" || string(material.Bytes) != "private-git-password" {
		t.Fatal("wrong delivered material")
	}
	replay, err := client.BuildSourceCredential(t.Context(), f.runner, delivery, runnerclient.SourceCredentialTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if !replay.UseUntil.Equal(material.UseUntil) {
		t.Fatal("replay extended window")
	}
	request("POST", grantPath+"/"+granted.Grant.ID+"/revoke", token.Token, struct{}{}, 200)
	if denied, e := client.BuildSourceCredential(t.Context(), f.runner, delivery, runnerclient.SourceCredentialTransport{}); e == nil || len(denied.Bytes) > 0 {
		denied.Close()
		t.Fatal("revoked delivery accepted")
	}
	grantIn.RequestID = strings.Repeat("a", 32)
	var beforeChange struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}
	if err = json.Unmarshal(request("POST", grantPath, token.Token, grantIn, 201), &beforeChange); err != nil {
		t.Fatal(err)
	}
	beforeResourceChange := domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "before-resource-change", GrantID: beforeChange.Grant.ID}
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, beforeResourceChange, 202)
	name := "renamed-private-resource"
	if _, err = service.UpdateResource(admin, resource.ID, app.ResourcePatch{ExpectedGeneration: 2, Name: &name}); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, beforeResourceChange, 409)
	beforeSourceChange := beforeResourceChange
	beforeSourceChange.ExpectedResourceGeneration = 3
	beforeSourceChange.IdempotencyKey = "before-source-change"
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, beforeSourceChange, 202)
	source.Git.RequestedRef = "next"
	if _, err = service.UpdateResource(admin, resource.ID, app.ResourcePatch{ExpectedGeneration: 3, Source: &source}); err != nil {
		t.Fatal(err)
	}
	stale := domain.HostingBuildRequest{ExpectedResourceGeneration: 4, ExpectedSourceGeneration: 3, IdempotencyKey: "stale-source-grant", GrantID: beforeChange.Grant.ID}
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, stale, 409)
	// The original private idempotency key cannot revive changed source intent.
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, in, 409)
	request("POST", "/api/v1/resources/"+resource.ID+"/builds", token.Token, beforeSourceChange, 409)
	if strings.Contains(logs.String(), "private-git-password") || strings.Contains(logs.String(), "private-git-user") {
		t.Fatal("credential reached logs")
	}
}

func TestPostgresSourceGrantExpiresAfterAuditWait(t *testing.T) {
	f, _, _ := privateDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	actor := "grant_expiring_owner"
	_, err := f.pg.CreateAPIToken(ctx, domain.APIToken{ID: actor, Name: actor, Kind: domain.TokenKindServiceAccount, TokenHash: actor, Roles: []string{domain.RoleSystemAdmin}, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION source_grant_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.source_grant.created' THEN PERFORM pg_advisory_xact_lock(982756); END IF;RETURN NEW;END $$;CREATE TRIGGER source_grant_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION source_grant_audit_wait()`); err != nil {
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
	var pid int32
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(982756)`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: "delivery_secret", SecretVersion: 1, RequestID: strings.Repeat("b", 32)}
	done := make(chan error, 1)
	go func() {
		_, e := f.pg.RequestHostingSourceGrant(ctx, actor, f.src.ResourceID, in, domain.AuditEvent{ID: "ignored", ActorID: actor, CreatedAt: time.Now().UTC()})
		done <- e
	}()
	joined := false
	defer func() {
		if !joined {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("grant request did not stop")
			}
		}
	}()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			joined = true
			t.Fatal("request did not wait", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	for {
		var expired bool
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`, actor).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("expired grant owner accepted", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_source_grants WHERE id=$1`, domain.HostingSourceGrantID(f.src.ResourceID, in.RequestID)).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired authorization left grant", err, count)
	}
}

func TestPostgresSourceGrantListExcludesUnsupportedLegacyTransport(t *testing.T) {
	f := newBuildStoreFixture(t)
	src := f.src
	src.Generation = 2
	src.CredentialKind = "ssh_private_key"
	src.RepositoryURL = strings.Replace(src.RepositoryURL, "https://", "ssh://git@", 1)
	src.Policy.AllowedSchemes = []string{"ssh"}
	src.Policy.SSHHostFingerprints = []string{"SHA256:legacy-test-fingerprint"}
	if _, err := configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", 1, src, destinationEvent("legacy_ssh_source")); err != nil {
		t.Fatal(err)
	}
	item, record, audit := managedFixture(t, "legacy_ssh_secret", 1)
	if _, err := f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	g := domain.HostingSourceGrant{ID: "legacy_ssh_grant", Version: 1, SourceID: src.ID, ResourceID: src.ResourceID, ProjectID: src.ProjectID, RepositoryURL: src.RepositoryURL, PolicyHash: domain.HostingDocumentHash(src.Policy), SecretID: item.ID, SecretVersion: 1, Kind: src.CredentialKind}
	if _, err := f.pg.CreateHostingSourceGrant(t.Context(), "usr_bootstrap", g, destinationEvent("legacy_ssh_grant")); err != nil {
		t.Fatal(err)
	}
	page, err := f.pg.ListHostingSourceGrants(t.Context(), "usr_bootstrap", src.ResourceID, store.Page{Enabled: true, Limit: 1, LimitSpecified: true})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("unsupported legacy transport advertised", err, page.Total)
	}
}
