package store_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func TestPostgresGitComposeRegistrySelectionAndDelivery(t *testing.T) {
	for _, registry := range []string{"docker.io", "ghcr.io"} {
		t.Run(registry, func(t *testing.T) {
			f, prior, input := settledGitComposeOperationsFixture(t, func(source *domain.HostingStackSource) {
				source.Original = strings.ReplaceAll(source.Original, "local-app:v1", "ghcr.io/private/local:v1")
			})
			ctx := t.Context()
			item, record, event := managedFixture(t, "git_registry", 1)
			raw, err := json.Marshal(map[string]string{"registry": registry, "username": "private-user", "password": "private-token"})
			if err != nil {
				t.Fatal(err)
			}
			record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, raw)
			clear(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.CreateManagedSecret(ctx, item, record, event); err != nil {
				t.Fatal(err)
			}
			input.IdempotencyKey = "private-git-stack"
			input.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}
			if _, err = f.pg.RequestHostingBuildDeployment(ctx, "private-no-cap", prior.ResourceID, "usr_bootstrap", input, destinationEvent("no-cap")); err == nil {
				t.Fatal("missing registry capability admitted")
			}
			if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingRegistryPullCapability); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "registry-viewer", ProjectID: prior.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
				t.Fatal(err)
			}
			for _, actor := range []string{"usr_viewer", "usr_unknown"} {
				if _, err = f.pg.RequestHostingBuildDeployment(ctx, "private-denied", prior.ResourceID, actor, input, destinationEvent("denied")); err == nil {
					t.Fatal("unauthorized registry admission")
				}
			}
			wrong := input
			wrong.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 2}
			if _, err = f.pg.RequestHostingBuildDeployment(ctx, "private-missing-version", prior.ResourceID, "usr_bootstrap", wrong, destinationEvent("missing-version")); err == nil {
				t.Fatal("missing version admitted")
			}
			foreign, foreignRecord, foreignEvent := managedFixture(t, "foreign_registry", 1)
			foreign.ProjectID = "proj_security"
			if _, err = f.pg.CreateManagedSecret(ctx, foreign, foreignRecord, foreignEvent); err != nil {
				t.Fatal(err)
			}
			wrong.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: foreign.ID, Version: 1}
			if _, err = f.pg.RequestHostingBuildDeployment(ctx, "private-foreign", prior.ResourceID, "usr_bootstrap", wrong, destinationEvent("foreign")); err == nil {
				t.Fatal("cross-project credential admitted")
			}
			for _, sourceGeneration := range []bool{false, true} {
				stale := input
				if sourceGeneration {
					stale.ExpectedSourceGeneration++
				} else {
					stale.ExpectedGeneration++
				}
				if _, err = f.pg.RequestHostingBuildDeployment(ctx, "stale-private", prior.ResourceID, "usr_bootstrap", stale, destinationEvent("stale-private")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("stale build generation admitted credential", err)
				}
			}
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_operations WHERE resource_id=$1`, prior.ResourceID).Scan(&count); err != nil || count != 1 {
				t.Fatal("rejected requests persisted operations", count, err)
			}
			op, err := f.pg.RequestHostingBuildDeployment(ctx, "private-git-stack", prior.ResourceID, "usr_bootstrap", input, destinationEvent("private-stack"))
			if err != nil {
				t.Fatal(err)
			}
			if op.Snapshot.RegistryCredential != *input.RegistryCredential || op.Snapshot.GitCompose.BuildID != input.BuildID {
				t.Fatal("selected proof changed")
			}
			replay, err := f.pg.RequestHostingBuildDeployment(ctx, "private-replay", prior.ResourceID, "usr_bootstrap", input, destinationEvent("private-replay"))
			if err != nil || replay.ID != op.ID {
				t.Fatal("identical replay", err)
			}
			wrong = input
			wrong.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 2}
			if _, err = f.pg.RequestHostingBuildDeployment(ctx, "private-changed", prior.ResourceID, "usr_bootstrap", wrong, destinationEvent("private-changed")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("changed ref replay accepted", err)
			}
			if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, authority); err != nil {
				t.Fatal(err)
			}
			req := domain.HostingRegistryCredentialRequest{Authority: authority, DeliveryID: strings.Repeat("e", 32)}
			tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte("build-fixture-token")))
			allowed, err := f.pg.AuthorizeHostingRegistryCredential(ctx, f.runner, tokenHash, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(allowed.Registries) != 1 || allowed.Registries[0] != "docker.io" {
				t.Fatal("local owner/alias registry entered pull authority", allowed.Registries)
			}
			allowed.Close()
			svc := runtimeCredentialApp(t, f.pg)
			principal, err := svc.AuthenticateRunnerToken(ctx, "build-fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			runnerCtx := auth.WithPrincipal(ctx, principal)
			got, err := svc.RunnerHostingRegistryCredential(runnerCtx, req, "build-fixture-token")
			if registry == "ghcr.io" {
				if got != nil {
					got.Close()
				}
				if err == nil {
					t.Fatal("credential for local-only owner/alias delivered")
				}
			} else {
				if err != nil || got == nil || got.Credential.Registry != registry {
					t.Fatal("matching registry delivery", err)
				}
				until := got.UseUntil
				got.Close()
				got, err = svc.RunnerHostingRegistryCredential(runnerCtx, req, "build-fixture-token")
				if err != nil || !got.UseUntil.Equal(until) {
					t.Fatal("retry extended credential window", err)
				}
				got.Close()
			}
			stale := req
			stale.Authority.Fence = "stale"
			if got, err = svc.RunnerHostingRegistryCredential(runnerCtx, stale, "build-fixture-token"); err == nil {
				got.Close()
				t.Fatal("stale lease delivered")
			}
			if _, err = f.pg.RevokeManagedSecret(ctx, item.ID, 1, destinationEvent("revoke-registry")); err != nil {
				t.Fatal(err)
			}
			if got, err = svc.RunnerHostingRegistryCredential(runnerCtx, req, "build-fixture-token"); err == nil {
				got.Close()
				t.Fatal("revoked credential delivered")
			}
		})
	}
}

func TestPostgresLocalArtifactRejectsRegistrySelection(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	input := localBuildDeploymentInput(b.ID)
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	input.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "secret_registry", Version: 1}
	if _, err := f.pg.RequestHostingBuildDeployment(t.Context(), "bad-private-local", f.src.ResourceID, "usr_bootstrap", input, destinationEvent("bad-private-local")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("ordinary local build accepted selection", err)
	}
}
