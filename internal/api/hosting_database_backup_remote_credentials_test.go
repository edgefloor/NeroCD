package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
	"nerocd/web"
)

func TestPostgresBackupRemoteValidationProtectedCredentialDelivery(t *testing.T) {
	f := newCaptureHTTPFixture(t)
	if _, err := f.pool.Exec(f.admin, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, []string{domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability}); err != nil {
		t.Fatal(err)
	}
	keys, err := app.NewBackupTargetCredentials([]byte("validation-access-canary"), []byte("validation-secret-canary"))
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	target, err := f.service.SaveDatabaseBackupTarget(f.admin, "proj_platform", "", domain.HostingDatabaseBackupTargetInput{RequestID: "credential_target", Name: "Credential fixture", Endpoint: "https://backup.example", Bucket: "credential-fixture", Region: "us-east-1", Enabled: true}, keys)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := f.service.ValidateDatabaseBackupTarget(f.admin, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "credential_validation", ExpectedRevision: 1, DestinationID: f.destination})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDatabaseBackupTransfer(f.admin, transfer.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.admin, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != transfer.ID {
		t.Fatal("validation claim failed", err)
	}
	a := domain.HostingAuthority{OperationID: transfer.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	server := httptest.NewTLSServer(NewServer(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static()))
	defer server.Close()
	client := runnerclient.New(server.URL, f.token)
	client.HTTP = server.Client()
	plan, err := client.HostingDatabaseBackupTransferPlan(t.Context(), a)
	if err != nil || plan.Operation.Mode != "validate" || plan.DaemonID != "" {
		t.Fatal("validation plan failed", err)
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "dbremotecredentials_" + strings.Repeat("a", 32), PlanHash: plan.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: "credential_fixture_daemon"}
	credentials, err := client.HostingBackupRemoteCredentials(t.Context(), f.runner, request, runnerclient.RuntimeEnvironmentTransport{})
	if err != nil {
		// Retain only a finite class from a direct store probe, never SQL text,
		// credential material or arbitrary wrapped errors.
		digest := sha256.Sum256([]byte(f.token))
		_, probe := f.pg.AuthorizeBackupRemoteCredentials(f.admin, f.runner, hex.EncodeToString(digest[:]), request)
		class := "unknown"
		var pgErr *pgconn.PgError
		switch {
		case errors.As(probe, &pgErr):
			class = "sqlstate_" + pgErr.Code
		case errors.Is(probe, store.ErrConflict):
			class = "conflict"
		case errors.Is(probe, store.ErrNotFound):
			class = "not_found"
		case probe == nil:
			class = "store_authorized"
		}
		t.Fatalf("protected validation credential delivery failed; store_class=%s", class)
	}
	defer credentials.Close()
	first := credentials.Metadata()
	if first.Request != request || first.RunnerID != f.runner || !time.Now().Before(first.UseUntil) || first.UseUntil.After(claim.Lease.ExpiresAt) {
		t.Fatal("delivery lost request authority or deadline")
	}
	if err = credentials.Use(func(access, secret []byte) error {
		if string(access) != "validation-access-canary" || string(secret) != "validation-secret-canary" {
			t.Fatal("wrong immutable target credentials")
		}
		return nil
	}); err != nil {
		t.Fatal("credential use failed")
	}
	ordinary, _ := json.Marshal(credentials)
	if string(ordinary) != "{}" {
		t.Fatal("ordinary encoding disclosed credentials")
	}
	replay, err := client.HostingBackupRemoteCredentials(t.Context(), f.runner, request, runnerclient.RuntimeEnvironmentTransport{})
	if err != nil {
		t.Fatal("exact delivery replay failed")
	}
	defer replay.Close()
	if replay.Metadata() != first {
		t.Fatal("replay changed immutable delivery grant")
	}
	count := func() int {
		t.Helper()
		var n int
		if err := f.pool.QueryRow(f.admin, `SELECT count(*) FROM hosting_database_backup_remote_deliveries WHERE operation_id=$1`, transfer.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("delivery replay created duplicate audit association")
	}
	for _, kind := range []string{"wrong_fence", "wrong_plan", "changed_replay_daemon"} {
		changed := request
		switch kind {
		case "wrong_fence":
			changed.Authority.Fence += "_foreign"
		case "wrong_plan":
			changed.PlanHash = "sha256:" + strings.Repeat("b", 64)
		case "changed_replay_daemon":
			changed.DaemonID = "foreign_daemon"
		}
		got, err := client.HostingBackupRemoteCredentials(t.Context(), f.runner, changed, runnerclient.RuntimeEnvironmentTransport{})
		if got != nil {
			got.Close()
		}
		if err == nil {
			t.Fatalf("%s disclosed credentials", kind)
		}
	}
	wrongBearer := runnerclient.New(server.URL, "wrong-runner-token-canary")
	wrongBearer.HTTP = server.Client()
	if got, err := wrongBearer.HostingBackupRemoteCredentials(t.Context(), f.runner, request, runnerclient.RuntimeEnvironmentTransport{}); err == nil || got != nil {
		if got != nil {
			got.Close()
		}
		t.Fatal("wrong HTTP bearer disclosed credentials")
	}
	if count() != 1 {
		t.Fatal("denied request added delivery association")
	}
	if _, err = f.pool.Exec(f.admin, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=$1`, plan.Target.Secret.SecretID); err != nil {
		t.Fatal(err)
	}
	if got, err := client.HostingBackupRemoteCredentials(t.Context(), f.runner, request, runnerclient.RuntimeEnvironmentTransport{}); err == nil || got != nil {
		if got != nil {
			got.Close()
		}
		t.Fatal("revoked key replay disclosed credentials")
	}
}
