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
