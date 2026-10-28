package store_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func TestPostgresBuildSecretsAuthorityAndHTTP(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	item, record, audit := managedFixture(t, "build_secrets_secret", 1)
	var err error
	record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte(strings.Repeat("x", 65534)+"\nY"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CreateManagedSecret(ctx, item, record, audit); err != nil {
		t.Fatal(err)
	}
	source := f.src
	source.Generation++
	source.Settings.BuildSecrets = []domain.HostingBuildSecretReference{{Name: "DEPENDENCY_TOKEN", Secret: domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}}}
	f.src, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, source, destinationEvent("build_secrets_source"))
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingBuildInput{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "selected-secrets"}
	if _, err = f.pg.RequestHostingBuild(ctx, "unsupported-secrets", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("unsupported_registry")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("missing capability admitted", err)
	}
	setCapability := func(enabled bool) {
		t.Helper()
		query := `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`
		if enabled {
			query = `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`
		}
		if _, err := f.db.Exec(ctx, query, f.runner, domain.HostingBuildSecretsCapability); err != nil {
			t.Fatal(err)
		}
	}
	setCapability(true)
	b, err := f.pg.RequestHostingBuild(ctx, "selected-secrets", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("selected_secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Snapshot.Source.Settings.BuildSecrets) != 1 || b.Snapshot.Source.Settings.BuildSecrets[0] != source.Settings.BuildSecrets[0] {
		t.Fatal("reference not pinned")
	}
	b, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, destinationEvent("secrets_dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	setCapability(false)
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err == nil && claim.Run.ID == b.RunID {
		t.Fatal("incapable claim admitted")
	}
	setCapability(true)
	claim, err = f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != b.RunID {
		t.Fatal("capable claim", err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, a, b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.HostingBuildSecretsRequest{Authority: a, DeliveryID: strings.Repeat("a", 32)}
	if _, err = f.pg.AuthorizeHostingBuildSecrets(ctx, f.runner, request); err == nil {
		t.Fatal("credential released during sourcing")
	}
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "secrets-source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := f.pg.AuthorizeHostingBuildSecrets(ctx, f.runner, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.pg.AuthorizeHostingBuildSecrets(ctx, f.runner, request)
	if err != nil || verified.Window != again.Window {
		t.Fatal("delivery replay changed window", err)
	}
	server := httptest.NewTLSServer(api.NewServer(runtimeCredentialApp(t, f.pg), slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer server.Close()
	client := runnerclient.New(server.URL, "build-fixture-token")
	client.HTTP = server.Client()
	material, err := client.BuildSecrets(ctx, f.runner, request, runnerclient.SourceCredentialTransport{})
	if err != nil {
		t.Fatal("HTTP build secrets delivery", err)
	}
	if len(material.Secrets) != 1 || material.Secrets[0].Name != "DEPENDENCY_TOKEN" || material.Secrets[0].Value != strings.Repeat("x", 65534)+"\nY" {
		t.Fatal("wrong selected material")
	}
	material.Close()
	bad := request
	bad.Authority.Fence = "wrong"
	if material, err = client.BuildSecrets(ctx, f.runner, bad, runnerclient.SourceCredentialTransport{}); err == nil {
		material.Close()
		t.Fatal("wrong fence released material")
	}
	setCapability(false)
	if material, err = client.BuildSecrets(ctx, f.runner, request, runnerclient.SourceCredentialTransport{}); err == nil {
		material.Close()
		t.Fatal("removed capability released material")
	}

	setCapability(true)
	// Active revocation is independently denied, before terminal state can explain it.
	for _, mutation := range []struct {
		name, sql string
		args      []any
	}{
		{"stale lease", `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, []any{a.LeaseID}},
	} {
		if _, err = f.db.Exec(ctx, mutation.sql, mutation.args...); err != nil {
			t.Fatal(err)
		}
		denied, denial := f.pg.AuthorizeHostingBuildSecrets(ctx, f.runner, request)
		if denial == nil || len(denied.Secrets) != 0 {
			t.Fatal(mutation.name + " returned material")
		}
		if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, a.LeaseID); err != nil {
			t.Fatal(err)
		}
	}
	setCapability(true)
	artifact := buildArtifact(b)
	completion := store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "secrets-complete", b.Revision, completion); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RevokeManagedSecret(ctx, item.ID, 1, destinationEvent("revoke_build_secrets")); err != nil {
		t.Fatal(err)
	}
	if material, err = client.BuildSecrets(ctx, f.runner, request, runnerclient.SourceCredentialTransport{}); err == nil {
		material.Close()
		t.Fatal("terminal/revoked credential delivered")
	}
	setCapability(false)
	replay, err := f.pg.RequestHostingBuild(ctx, "secrets-retry", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("secrets_retry"))
	if err != nil || replay.ID != b.ID {
		t.Fatal("historical request replay required material", err)
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "secrets-complete", b.Revision, completion); err != nil {
		t.Fatal("completed receipt replay required material", err)
	}
}

func TestPostgresBuildSecretsAdmissionAndLiveRevocation(t *testing.T) {
	for _, mode := range []string{"foreign project", "missing version", "active revocation", "stale source", "nul material", "invalid ciphertext"} {
		t.Run(mode, func(t *testing.T) {
			f := newBuildStoreFixture(t)
			ctx := t.Context()
			item, record, audit := managedFixture(t, "selected_secrets", 1)
			if mode == "foreign project" {
				if _, err := f.db.Exec(ctx, `INSERT INTO projects(id,name,description) VALUES('foreign-secrets-project','other','')`); err != nil {
					t.Fatal(err)
				}
				item.ProjectID = "foreign-secrets-project"
			}
			var err error
			record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte("build-protected-token"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "nul material" {
				record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte("first\x00second"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "invalid ciphertext" {
				record.Envelope.Data[len(record.Envelope.Data)-1] ^= 1
			}
			if _, err = f.pg.CreateManagedSecret(ctx, item, record, audit); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingBuildSecretsCapability); err != nil {
				t.Fatal(err)
			}
			source := f.src
			source.Generation++
			source.Settings.BuildSecrets = []domain.HostingBuildSecretReference{{Name: "DEPENDENCY_TOKEN", Secret: domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}}}
			if mode == "missing version" {
				source.Settings.BuildSecrets[0].Secret.Version = 2
			}
			f.src, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, source, destinationEvent("secrets_source"))
			if err != nil {
				t.Fatal(err)
			}
			input := domain.HostingBuildInput{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "secrets-case"}
			b, err := f.pg.RequestHostingBuild(ctx, "secrets-case", f.src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("secrets_request"))
			if mode == "foreign project" || mode == "missing version" {
				if err == nil {
					t.Fatal("invalid reference admitted")
				}
				var count int
				if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 0 {
					t.Fatal("denied reference persisted build", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, a := f.activate(t, b.ID)
			b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "secrets-source", sourceBuildReceipt(b))
			if err != nil {
				t.Fatal(err)
			}
			b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "daemon")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "nul material" || mode == "invalid ciphertext" {
				server := httptest.NewTLSServer(api.NewServer(runtimeCredentialApp(t, f.pg), slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
				defer server.Close()
				client := runnerclient.New(server.URL, "build-fixture-token")
				client.HTTP = server.Client()
				request := domain.HostingBuildSecretsRequest{Authority: a, DeliveryID: strings.Repeat("c", 32)}
				if material, err := client.BuildSecrets(ctx, f.runner, request, runnerclient.SourceCredentialTransport{}); err == nil {
					material.Close()
					t.Fatal("invalid protected material delivered")
				}
				var count int
				if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1 AND action='hosting.build_secrets_authorized'`, "aud_build_secrets_delivery_"+request.DeliveryID).Scan(&count); err != nil || count != 1 {
					t.Fatal("authorization not committed before rejected material", err)
				}
				return
			}
			if mode == "active revocation" {
				if _, err = f.pg.RevokeManagedSecret(ctx, item.ID, 1, destinationEvent("secrets_revoke")); err != nil {
					t.Fatal(err)
				}
			} else {
				next := f.src
				next.Generation++
				next.RequestedRef = "next"
				if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, next, destinationEvent("secrets_edit")); err != nil {
					t.Fatal(err)
				}
			}
			denied, err := f.pg.AuthorizeHostingBuildSecrets(ctx, f.runner, domain.HostingBuildSecretsRequest{Authority: a, DeliveryID: strings.Repeat("b", 32)})
			if err == nil || len(denied.Secrets) != 0 {
				t.Fatal("stale/revoked reference delivered")
			}
		})
	}
}
