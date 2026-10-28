package store_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func sshSourceFixture(t *testing.T) (buildStoreFixture, domain.PublicHostingSourceGrant, []byte) {
	t.Helper()
	f := newBuildStoreFixture(t)
	src := f.src
	src.Generation = 2
	src.CredentialKind = "ssh_private_key"
	src.RepositoryURL = strings.Replace(src.RepositoryURL, "https://", "ssh://git@", 1)
	src.Policy.AllowedSchemes = []string{"ssh"}
	src.Policy.SSHHostFingerprints = []string{"SHA256:" + strings.Repeat("A", 43)}
	if _, err := configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", 1, src, destinationEvent("ssh_source")); err != nil {
		t.Fatal(err)
	}
	f.src = src
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	item, record, audit := managedFixture(t, "ssh_secret", 1)
	record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	grant, err := f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", src.ResourceID, domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: item.ID, SecretVersion: 1, RequestID: strings.Repeat("c", 32)}, destinationEvent("ssh_grant"))
	if err != nil {
		t.Fatal(err)
	}
	return f, grant, raw
}
func sshCapability(t *testing.T, f buildStoreFixture, enabled bool) {
	t.Helper()
	q := `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`
	if enabled {
		q = `UPDATE runners SET capabilities=array_append(array_remove(capabilities,$2),$2) WHERE id=$1`
	}
	if _, err := f.db.Exec(t.Context(), q, f.runner, domain.HostingSSHGitCapability); err != nil {
		t.Fatal(err)
	}
}
func sshBuildInput(grant domain.PublicHostingSourceGrant, key string) domain.HostingBuildInput {
	return domain.HostingBuildInput{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: key, GrantID: grant.ID}
}
func TestPostgresSSHSourceGrantVersionRotationAndLiveCapability(t *testing.T) {
	f, grant, key := sshSourceFixture(t)
	defer clear(key)
	if grant.Kind != "ssh_private_key" || grant.SourceGeneration != 2 {
		t.Fatal("SSH grant identity lost")
	}
	page, err := f.pg.ListHostingSourceGrants(t.Context(), "usr_bootstrap", f.src.ResourceID, store.Page{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0] != grant {
		t.Fatal("audited SSH grant unavailable", err, page.Total)
	}
	resource, err := f.pg.GetResource(t.Context(), f.src.ResourceID)
	if err != nil || resource.Source.Git.SSHHostFingerprints[0] != f.src.Policy.SSHHostFingerprints[0] {
		t.Fatal("persisted fingerprints not expanded", err)
	}
	setPrivateGitCapability(t, f, true)
	in := sshBuildInput(grant, "ssh")
	if _, err = f.pg.RequestHostingBuild(t.Context(), "ssh", f.src.ResourceID, "usr_bootstrap", in, buildPolicy(), buildEnqueue, destinationEvent("ssh_missing_cap")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("HTTPS-only runner admitted SSH", err)
	}
	sshCapability(t, f, true)
	if _, err = f.pg.RequestHostingBuild(t.Context(), "ssh", f.src.ResourceID, "usr_bootstrap", in, buildPolicy(), buildEnqueue, destinationEvent("ssh_request")); err != nil {
		t.Fatal(err)
	}
	_, authority := f.activate(t, "ssh")
	component, err := app.NewHostingBuildCredentialDelivery(auth.ContextProvider{}, deliveryRunnerIdentity(f.runner), f.pg, managedRing(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
	request := deliveryRequest(authority)
	deliver := func() {
		t.Helper()
		out, err := component.Deliver(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		material := out.Take()
		defer material.Close()
		if material.Kind != "ssh_private_key" || material.Username != "" || !bytes.Equal(material.Bytes, key) {
			t.Fatal("selected key changed")
		}
	}
	deliver()
	_, next, audit := managedFixture(t, "ssh_secret", 2)
	next.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: f.src.ProjectID, SecretID: "ssh_secret", Version: 2}, []byte("different invalid key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RotateManagedSecret(t.Context(), "ssh_secret", 1, next, audit); err != nil {
		t.Fatal(err)
	}
	deliver() // Rotation does not silently replace this immutable selected key version.
	sshCapability(t, f, false)
	denied, err := f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, request)
	assertNoDelivery(t, denied, err)
	sshCapability(t, f, true)
	if err = f.pg.RevokeResourceHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, grant.ID, destinationEvent("ssh_revoke")); err != nil {
		t.Fatal(err)
	}
	denied, err = f.pg.AuthorizeHostingSourceCredential(ctx, f.runner, request)
	assertNoDelivery(t, denied, err)
}
func TestPostgresSSHCapabilityDispatchAndClaim(t *testing.T) {
	for _, phase := range []string{"dispatch", "claim"} {
		t.Run(phase, func(t *testing.T) {
			f, grant, key := sshSourceFixture(t)
			defer clear(key)
			sshCapability(t, f, true)
			in := sshBuildInput(grant, "ssh-cap")
			if _, err := f.pg.RequestHostingBuild(t.Context(), "ssh-cap", f.src.ResourceID, "usr_bootstrap", in, buildPolicy(), buildEnqueue, destinationEvent("ssh_cap_request")); err != nil {
				t.Fatal(err)
			}
			if phase == "dispatch" {
				sshCapability(t, f, false)
				if _, err := f.pg.DispatchHostingBuild(t.Context(), "ssh-cap", buildDispatch, destinationEvent("ssh_cap_dispatch")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("SSH dispatch ignored removed capability", err)
				}
				return
			}
			build, err := f.pg.DispatchHostingBuild(t.Context(), "ssh-cap", buildDispatch, destinationEvent("ssh_cap_dispatch"))
			if err != nil {
				t.Fatal(err)
			}
			sshCapability(t, f, false)
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err == nil && claim.Run.ID == build.RunID {
				t.Fatal("SSH claim ignored removed capability")
			}
			var count int
			if err = f.db.QueryRow(context.Background(), `SELECT count(*) FROM run_leases WHERE run_id=$1`, build.RunID).Scan(&count); err != nil || count != 0 {
				t.Fatal("unsupported SSH claim retained lease", err, count)
			}
		})
	}
}
