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
