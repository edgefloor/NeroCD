package store_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

func TestPostgresRegistryCredentialPullAuthorityAndHTTP(t *testing.T) {
	f := newRuntimeAdmissionFixture(t, false)
	item, record, audit := managedFixture(t, "registry_secret", 1)
	var err error
	record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte(`{"registry":"docker.io","username":"private-user","password":"private-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	in := hostingDeploymentRequest()
	in.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}
	if _, err = f.pg.RequestHostingDeployment(t.Context(), "private-no-cap", f.resource, "usr_bootstrap", in, destinationEvent("private-no-cap")); err == nil {
		t.Fatal("missing capability admitted")
	}
	f.capabilities(t, domain.HostingRegistryPullCapability, domain.HostingReleasePortsCapability)
	op, err := f.pg.RequestHostingDeployment(t.Context(), "private-pull", f.resource, "usr_bootstrap", in, destinationEvent("private-pull"))
	if err != nil {
		t.Fatal(err)
	}
	if op.Snapshot.RegistryCredential != *in.RegistryCredential {
		t.Fatal("reference not pinned")
	}
	if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(t.Context(), f.runner, authority); err != nil {
		t.Fatal(err)
	}
	req := domain.HostingRegistryCredentialRequest{Authority: authority, DeliveryID: strings.Repeat("a", 32)}
	svc := runtimeCredentialApp(t, f.pg)
	server := httptest.NewTLSServer(api.NewServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer server.Close()
	client := runnerclient.New(server.URL, "image-hosting-fixture-token")
	client.HTTP = server.Client()
	got, err := client.HostingRegistryCredential(t.Context(), f.runner, req, runnerclient.RuntimeEnvironmentTransport{})
	if err != nil {
		t.Fatal("first delivery", err)
	}
	until := got.UseUntil
	if got.Credential.Registry != "docker.io" || got.Credential.Password != "private-token" {
		t.Fatal("wrong protected material")
	}
	got.Close()
	got, err = client.HostingRegistryCredential(t.Context(), f.runner, req, runnerclient.RuntimeEnvironmentTransport{})
	if err != nil || !got.UseUntil.Equal(until) {
		t.Fatal("window extended", err)
	}
	got.Close()
	// Rotation preserves the exact pinned version; revocation will deny it below.
	_, next, _ := managedFixture(t, item.ID, 2)
	next.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 2}, []byte(`{"registry":"different.invalid","username":"other","password":"rotated"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RotateManagedSecret(t.Context(), item.ID, 1, next, destinationEvent("rotate-private")); err != nil {
		t.Fatal(err)
	}
	got, err = client.HostingRegistryCredential(t.Context(), f.runner, req, runnerclient.RuntimeEnvironmentTransport{})
	if err != nil || got.Credential.Password != "private-token" {
		t.Fatal("rotation changed immutable reference", err)
	}
	got.Close()
	// An expired prior authorization cannot be extended by a replay.
	expired := req
	expired.DeliveryID = strings.Repeat("b", 32)
	hash := domain.HostingDocumentHash(struct {
		Runner   string
		Request  domain.HostingRegistryCredentialRequest
		Snapshot domain.HostingSnapshot
	}{f.runner, expired, op.Snapshot})
	meta, _ := json.Marshal(map[string]any{"purpose": "registry_pull", "request_hash": hash, "use_until": time.Now().Add(-time.Second)})
	if _, err = f.db.Exec(t.Context(), `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,$2,'hosting.registry_credential_authorized',$3,$4,clock_timestamp())`, "aud_registry_delivery_"+expired.DeliveryID, f.runner, op.ID, meta); err != nil {
		t.Fatal(err)
	}
	if got, err = client.HostingRegistryCredential(t.Context(), f.runner, expired, runnerclient.RuntimeEnvironmentTransport{}); err == nil {
		got.Close()
		t.Fatal("expired window renewed")
	}

	bad := req
	bad.Authority.Fence = "stale-fence"
	if got, err = client.HostingRegistryCredential(t.Context(), f.runner, bad, runnerclient.RuntimeEnvironmentTransport{}); err == nil {
		got.Close()
		t.Fatal("stale fence delivered")
	}
	raw, _ := json.Marshal(req)
	invalid := strings.TrimSuffix(string(raw), "}") + `,"secret_id":"other"}`
	h, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/api/v1/runners/hosting/registry-credential", strings.NewReader(invalid))
	if err != nil {
		t.Fatal(err)
	}
	h.Header.Set("Authorization", "Bearer image-hosting-fixture-token")
	h.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(h)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 400 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("invalid decoder contract", response.StatusCode)
	}
	var count int
	var metadata string
	if err = f.db.QueryRow(t.Context(), `SELECT count(*),min(metadata::text) FROM audit_events WHERE action='hosting.registry_credential_authorized'`).Scan(&count, &metadata); err != nil || count != 2 || strings.Contains(metadata, "private-token") || strings.Contains(metadata, "private-user") {
		t.Fatal("unsafe audit", count, err)
	}
	// Each delivery rechecks mutable authority, even when reusing an accepted ID.
	for _, change := range []struct {
		name, deny, restore string
		args                []any
	}{
		{"capability", `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_registry_pull_v1') WHERE id=$1`, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_registry_pull_v1') WHERE id=$1`, []any{f.runner}},
		{"actor", `UPDATE users SET status='disabled' WHERE id=$1`, `UPDATE users SET status='active' WHERE id=$1`, []any{"usr_bootstrap"}},
		{"destination", `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, `UPDATE destination_project_grants SET revoked_at=NULL WHERE destination_id=$1`, []any{op.Snapshot.DestinationID}},
		{"bearer", `UPDATE runners SET token_hash='replaced-private-bearer' WHERE id=$1`, `UPDATE runners SET token_hash=$2 WHERE id=$1`, []any{f.runner}},
	} {
		t.Run(change.name, func(t *testing.T) {
			if _, err := f.db.Exec(t.Context(), change.deny, change.args...); err != nil {
				t.Fatal(err)
			}
			out, err := client.HostingRegistryCredential(t.Context(), f.runner, req, runnerclient.RuntimeEnvironmentTransport{})
			if out != nil {
				out.Close()
			}
			if err == nil {
				t.Fatal("changed authority delivered")
			}
			args := change.args
			if change.name == "bearer" {
				args = []any{f.runner, runtimeDeliveryCredential(f)}
			}
			if _, err = f.db.Exec(t.Context(), change.restore, args...); err != nil {
				t.Fatal(err)
			}
		})
	}

	if _, err = f.pg.RevokeManagedSecret(t.Context(), item.ID, 2, destinationEvent("revoke-private")); err != nil {
		t.Fatal(err)
	}
	if got, err = client.HostingRegistryCredential(t.Context(), f.runner, req, runnerclient.RuntimeEnvironmentTransport{}); err == nil {
		got.Close()
		t.Fatal("revoked secret delivered")
	}
	// Accepted request receipt remains readable after revocation; it grants no delivery authority.
	replay, err := f.pg.RequestHostingDeployment(t.Context(), "ignored", f.resource, "usr_bootstrap", in, destinationEvent("ignored"))
	if err != nil || replay.ID != op.ID {
		t.Fatal("historical receipt lost", err)
	}

}

func TestPostgresRegistryRoutedStoragePreparationAndDenial(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "prepare"
		if deny {
			name = "deny_before_pull"
		}
		t.Run(name, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			item, record, audit := managedFixture(t, "route_registry_secret", 1)
			var err error
			record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, []byte(`{"registry":"docker.io","username":"private-user","password":"private-token"}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingRegistryPullCapability); err != nil {
				t.Fatal(err)
			}
			in := f.input("private-routed")
			in.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}
			op, err := f.pg.RequestHostingDeployment(t.Context(), "private_route", f.resource.ID, "usr_bootstrap", in, destinationEvent("private_route"))
			if err != nil {
				t.Fatal(err)
			}
			if op.Snapshot.RegistryCredential != *in.RegistryCredential || op.Snapshot.StorageRouteContinuity.Hash == "" || op.MutationHold {
				t.Fatal("combined request did not preserve pre-pull scope")
			}
			op = awaitHostingDispatch(t, f.pg, op.ID)
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = f.pg.HostingRunnerPlan(t.Context(), f.runner, authority); err != nil {
				t.Fatal(err)
			}
			svc := runtimeCredentialApp(t, f.pg)
			principal, err := svc.AuthenticateRunnerToken(t.Context(), "image-hosting-fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			ctx := auth.WithPrincipal(t.Context(), principal)
			req := domain.HostingRegistryCredentialRequest{Authority: authority, DeliveryID: strings.Repeat("d", 32)}
			if deny {
				if _, err = f.pg.RevokeManagedSecret(t.Context(), item.ID, 1, destinationEvent("revoke_routed_registry")); err != nil {
					t.Fatal(err)
				}
				if got, err := svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token"); err == nil || got != nil {
					if got != nil {
						got.Close()
					}
					t.Fatal("revoked selected credential delivered")
				}
				finished, err := f.pg.HostingComplete(t.Context(), f.runner, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "failed_private_pull", Result: domain.HostingResult{Outcome: "failed"}})
				if err != nil || finished.MutationHold || finished.State != "failed" {
					t.Fatal("pre-pull denial did not settle safely", err)
				}
				continuityState(t, f, op.ID, "released")
				route, err := f.pg.GetHostingRoute(t.Context(), f.route.ID)
				if err != nil || route.Route.SelectedReleaseOperationID != f.op.ID || !route.Route.HTTPReady || !route.Route.HTTPSReady {
					t.Fatal("denied pre-pull changed the loaded route", err)
				}
				var loaded int64
				if err = f.db.QueryRow(t.Context(), `SELECT loaded_generation FROM hosting_proxies WHERE id='proxy_continuity'`).Scan(&loaded); err != nil || loaded != f.loaded.Generation {
					t.Fatal("pre-pull denial changed proxy generation", err)
				}
				return
			}
			got, err := svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token")
			if err != nil {
				t.Fatal("routed pre-pull delivery", err)
			}
			until := got.UseUntil
			got.Close()
			got, err = svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token")
			if err != nil || !got.UseUntil.Equal(until) {
				t.Fatal("routed delivery replay changed", err)
			}
			got.Close()
			if _, err = f.pg.HostingPrepared(t.Context(), f.runner, domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: "private_route_prepared", Release: *f.op.Release}); err != nil {
				t.Fatal("routed image preparation", err)
			}
			op, err = f.pg.GetHostingOperation(t.Context(), op.ID)
			if err != nil || op.Plan == nil || op.Plan.Snapshot.RegistryCredential != *in.RegistryCredential || op.Plan.Snapshot.StorageRouteContinuity.Hash == "" {
				t.Fatal("prepared plan dropped combined scope", err)
			}
			next := storageMaterializationFixture{f.pg, f.db, f.resource, f.runner, f.storage, op, authority}
			admitContinuityWriter(t, next)
			finished := next.complete(t, "succeeded", false)
			if finished.State != "succeeded" || finished.MutationHold {
				t.Fatal("routed private replacement did not settle")
			}
			proxyID := continuityState(t, f, op.ID, "proxy_pending")
			settleProxyGeneration(t, f.pg, proxyID, f.runner, f.op.Release.DaemonID)
			continuityState(t, f, op.ID, "loaded")
			replay, err := f.pg.RequestHostingDeployment(t.Context(), "ignored_private_route", f.resource.ID, "usr_bootstrap", in, destinationEvent("private_route_replay"))
			if err != nil || replay.ID != op.ID {
				t.Fatal("routed request replay changed", err)
			}
		})
	}
}

func TestPostgresRegistryCredentialManagedStackMembership(t *testing.T) {
	for _, registry := range []string{"ghcr.io", "docker.io", "unused.invalid"} {
		t.Run(registry, func(t *testing.T) {
			pg, db, resource, server, destination := hostingExecutionFixture(t)
			ctx := t.Context()
			resource.ID = "res_private_compose"
			resource.Name = "Private Compose"
			resource.Source = domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: "services:\n  app:\n    image: ghcr.io/example/private:v1\n    volumes: [uploads:/data]\n  db:\n    image: postgres:17\n    volumes: [database:/var/lib/postgresql/data]\nvolumes:\n  uploads: {}\n  database: {}\n", PublicComponent: "app", ContainerPort: 80}}
			var err error
			resource, err = pg.CreateResource(ctx, resource, store.WithHostingComposeCreate(strings.Repeat("a", 32), domain.HostingDocumentHash(resource)), store.WithAudit(destinationEvent("private_compose_create")))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_private_compose", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("private_compose_bind")); err != nil {
				t.Fatal(err)
			}
			item, record, audit := managedFixture(t, "compose_registry_secret", 1)
			raw, _ := json.Marshal(map[string]string{"registry": registry, "username": "private-user", "password": "private-token"})
			record.Envelope, err = managedRing(t).Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: item.ID, Version: 1}, raw)
			clear(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pg.CreateManagedSecret(ctx, item, record, audit); err != nil {
				t.Fatal(err)
			}
			input := hostingDeploymentRequest()
			input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
			input.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 1}
			if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || ARRAY[$2,$3,$4]::text[] WHERE id=$1`, server.RunnerID, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
				t.Fatal(err)
			}
			if _, err = pg.RequestHostingDeployment(ctx, "private_stack_no_cap", resource.ID, "usr_bootstrap", input, destinationEvent("private_stack_no_cap")); err == nil {
				t.Fatal("private stack admitted without capability")
			}
			if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingRegistryPullCapability); err != nil {
				t.Fatal(err)
			}
			op, err := pg.RequestHostingDeployment(ctx, "private_stack", resource.ID, "usr_bootstrap", input, destinationEvent("private_stack"))
			if err != nil || op.Snapshot.RegistryCredential != *input.RegistryCredential || op.StackSource == nil || op.Snapshot.ImageReference != "" {
				t.Fatal("stack selection was not pinned", err)
			}
			replay, err := pg.RequestHostingDeployment(ctx, "private_stack_replay", resource.ID, "usr_bootstrap", input, destinationEvent("private_stack_replay"))
			if err != nil || replay.ID != op.ID {
				t.Fatal("stack request replay changed", err)
			}
			changed := input
			changed.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: item.ID, Version: 2}
			if _, err = pg.RequestHostingDeployment(ctx, "private_stack_changed", resource.ID, "usr_bootstrap", changed, destinationEvent("private_stack_changed")); err == nil {
				t.Fatal("changed selection reused request key")
			}
			op = awaitHostingDispatch(t, pg, op.ID)
			claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = pg.HostingRunnerPlan(ctx, server.RunnerID, a); err != nil {
				t.Fatal(err)
			}
			svc := runtimeCredentialApp(t, pg)
			principal, err := svc.AuthenticateRunnerToken(ctx, "image-hosting-fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			ctx = auth.WithPrincipal(ctx, principal)
			req := domain.HostingRegistryCredentialRequest{Authority: a, DeliveryID: strings.Repeat("e", 32)}
			got, err := svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token")
			if registry == "unused.invalid" {
				if err == nil || got != nil {
					if got != nil {
						got.Close()
					}
					t.Fatal("credential outside immutable registry set delivered")
				}
			} else {
				if err != nil || got.Credential.Registry != registry {
					t.Fatal("matching stack registry denied", err)
				}
				until := got.UseUntil
				got.Close()
				got, err = svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token")
				if err != nil || !got.UseUntil.Equal(until) {
					t.Fatal("stack replay extended delivery window", err)
				}
				got.Close()
			}
			var preparedRequest *domain.HostingPreparedRequest
			if registry == "ghcr.io" {
				second := req
				second.DeliveryID = strings.Repeat("f", 32)
				got, err = svc.RunnerHostingRegistryCredential(ctx, second, "image-hosting-fixture-token")
				if err != nil {
					t.Fatal("independent stack service delivery denied", err)
				}
				got.Close()
				expired := req
				expired.DeliveryID = strings.Repeat("1", 32)
				hash := domain.HostingDocumentHash(struct {
					Runner   string
					Request  domain.HostingRegistryCredentialRequest
					Snapshot domain.HostingSnapshot
				}{server.RunnerID, expired, op.Snapshot})
				meta, _ := json.Marshal(map[string]any{"purpose": "registry_pull", "request_hash": hash, "use_until": time.Now().Add(-time.Second)})
				if _, err = db.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,$2,'hosting.registry_credential_authorized',$3,$4,clock_timestamp())`, "aud_registry_delivery_"+expired.DeliveryID, server.RunnerID, op.ID, meta); err != nil {
					t.Fatal(err)
				}
				if got, err = svc.RunnerHostingRegistryCredential(ctx, expired, "image-hosting-fixture-token"); err == nil || got != nil {
					if got != nil {
						got.Close()
					}
					t.Fatal("expired stack service replay renewed its window")
				}
				var images []domain.HostingStackImage
				for _, service := range op.StackSource.Services {
					normalized, _ := domain.NormalizeHostingImage(service.Image)
					release := domain.HostingRelease{CanonicalImage: normalized[:strings.LastIndex(normalized, ":")] + "@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: "daemon_private_stack"}
					release.Hash = domain.HostingDocumentHash(release)
					images = append(images, domain.HostingStackImage{Component: service.Name, Release: release})
				}
				release := domain.HostingRelease{DaemonID: "daemon_private_stack", Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
				release.Hash = domain.HostingDocumentHash(release)
				preparedRequest = &domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "private_stack_prepared", Release: release, StackImages: images}
				prepared, e := pg.HostingPrepared(ctx, server.RunnerID, *preparedRequest)
				if e != nil || prepared.Plan == nil || prepared.Plan.Snapshot.RegistryCredential != *input.RegistryCredential || prepared.MutationHold {
					t.Fatal("private stack plan lost immutable selection", e)
				}
			}

			if _, err = pg.RevokeManagedSecret(ctx, item.ID, 1, destinationEvent("private_stack_revoke")); err != nil {
				t.Fatal(err)
			}
			if got, err = svc.RunnerHostingRegistryCredential(ctx, req, "image-hosting-fixture-token"); err == nil || got != nil {
				if got != nil {
					got.Close()
				}
				t.Fatal("revoked stack credential delivered")
			}
			if preparedRequest != nil {
				if _, err = pg.HostingPrepared(ctx, server.RunnerID, *preparedRequest); err != nil {
					t.Fatal("revocation lost immutable prepared receipt replay", err)
				}
			}

			finished, err := pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stack_failed_before_pull", Result: domain.HostingResult{Outcome: "failed"}})
			if err != nil || finished.State != "failed" || finished.MutationHold || preparedRequest == nil && (finished.Plan != nil || finished.Release != nil) {
				t.Fatal("rejected credential acquired stack mutation authority", err)
			}
			replay, err = pg.RequestHostingDeployment(ctx, "private_stack_history", resource.ID, "usr_bootstrap", input, destinationEvent("private_stack_history"))
			if err != nil || replay.ID != op.ID {
				t.Fatal("revocation lost immutable request receipt", err)
			}
		})
	}
}
