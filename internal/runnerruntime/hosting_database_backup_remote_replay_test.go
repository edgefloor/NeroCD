//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func remoteReplayPlan(t *testing.T) (*databaseBackupEngine, *databaseFake, domain.HostingDatabaseBackupTransferPlan, []byte) {
	t.Helper()
	engine, f, backup := backupEngineFixture(t)
	now := time.Now().UTC()
	data := []byte("PGDMPfixture remote archive")
	digest := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Format: "postgresql_custom"}
	if _, err := engine.store.Put(t.Context(), bytes.NewReader(data), hex.EncodeToString(digest[:]), blob.Bytes); err != nil {
		t.Fatal(err)
	}
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: backup.Operation.ProjectID, Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingDatabaseBackupTransferPlan{Operation: domain.HostingDatabaseBackupTransfer{ID: "transfer", Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: target.ProjectID, TargetID: target.ID, TargetRevision: 1, DestinationID: backup.Source.Snapshot.DestinationID, Mode: "upload", BackupID: backup.Operation.ID, Blob: &blob, State: "queued", CreatedAt: now, UpdatedAt: now}, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target, Access: domain.BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: domain.BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, ActorID: backup.ActorID, RunnerID: backup.RunnerID, ServerID: backup.Source.Snapshot.ServerID, DaemonID: backup.Source.Release.DaemonID, LocationID: backup.Operation.ID, ObjectKey: domain.BackupRemoteObjectKey(backup.Operation.ID, blob), MaxBytes: backup.MaxBytes, TimeoutSeconds: 30}
	p.Hash = domain.HostingDocumentHash(p)
	return engine, f, p, data
}

func TestBackupRemoteCoordinatorLostAckReplayDoesNotRepeatUpload(t *testing.T) {
	e, f, p, data := remoteReplayPlan(t)
	var mu sync.Mutex
	puts := 0
	var object []byte
	s3server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			if object == nil {
				w.WriteHeader(404)
			} else {
				w.Header().Set("Content-Length", fmt.Sprint(len(object)))
			}
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing no-clobber condition")
			}
			object, _ = io.ReadAll(r.Body)
			puts++
		case http.MethodGet:
			w.Header().Set("Content-Length", fmt.Sprint(len(object)))
			_, _ = w.Write(object)
		default:
			t.Error("unexpected S3 request")
			w.WriteHeader(400)
		}
	}))
	defer s3server.Close()
	p.Target.Target.Endpoint = s3server.URL
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "lease", RunID: "db_transfer_" + p.Operation.ID, RunnerID: p.RunnerID, Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: lease.RunID, ProjectID: p.Operation.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: p.Operation.ID, HostingMode: "upload"}}, Lease: lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID}}
	path := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	completePosts, deliveryPosts := 0, 0
	var accepted domain.HostingDatabaseBackupTransferMutation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/database-backup-transfer/plan":
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v1/runners/lease":
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/hosting/database-backup-transfer/credentials":
			deliveryPosts++
			var in domain.BackupRemoteCredentialRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("invalid delivery request")
			}
			m := domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseBackupRemoteCredentialPurpose, Request: in, RunnerID: p.RunnerID, UseUntil: time.Now().UTC().Add(30 * time.Second)}
			m.RequestHash = domain.HostingDocumentHash(struct {
				Runner  string
				Request domain.BackupRemoteCredentialRequest
			}{m.RunnerID, in})
			credentials, err := domain.NewBackupRemoteCredentials(m, []byte("access-canary"), []byte("secret-canary"))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer credentials.Close()
			w.Header().Set("Cache-Control", "no-store")
			_ = domain.WriteBackupRemoteCredentials(w, credentials)
		case "/api/v1/runners/hosting/database-backup-transfer/mutation":
			var in domain.HostingDatabaseBackupTransferMutation
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("invalid mutation")
				w.WriteHeader(400)
				return
			}
			found := false
			for _, entry := range j.Snapshot().DatabaseBackupTransfers {
				found = found || domain.HostingDocumentHash(entry.Request) == domain.HostingDocumentHash(in)
			}
			if !found {
				t.Error("mutation sent before durable journal")
			}
			out := p.Operation
			out.State = "applying"
			if in.Phase == "complete" {
				completePosts++
				out.State = "succeeded"
				if completePosts == 1 {
					accepted = in
					w.WriteHeader(503)
					return
				}
				if !reflect.DeepEqual(accepted, in) {
					t.Error("replay changed committed request")
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Error("unexpected controller request")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, lease)
	err = executeDatabaseBackupTransferWithEngine(s, j, func(context.Context) error { return nil }, server.URL, "runner", claim, "", &DatabaseExecutionConfig{LogicalBackups: true, RemoteBackups: true, TrustedLoopbackHTTP: true, RemoteFixtureOrigin: s3server.URL}, func(context.Context, DatabaseExecutionConfig, string) (*databaseBackupEngine, error) { return e, nil })
	s.Close()
	if err == nil || len(j.Snapshot().DatabaseBackupTransfers) != 2 {
		t.Fatal("lost ACK failed to preserve intent and result", err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal("journal19 reopen", err)
	}
	if err = reconcileDatabaseBackupTransferJournal(ctx, Config{Server: server.URL, Token: "runner", Database: &DatabaseExecutionConfig{LogicalBackups: true, RemoteBackups: true, TrustedLoopbackHTTP: true}}, j); err != nil {
		t.Fatal("exact result replay", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 || !bytes.Equal(object, data) || completePosts != 2 || deliveryPosts != 1 || j.Depth() != 0 || f.creates != 1 {
		t.Fatal("replay repeated effects or failed retirement")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "pg_dump") || strings.Contains(call, "pg_restore") {
			t.Fatal("transfer executed database tool")
		}
	}
}

func TestBackupRemoteStartupRequiresExactOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		configured, advertised, wantError bool
	}{
		{name: "enabled_matching", configured: true, advertised: true},
		{name: "disabled_matching"},
		{name: "advertised_without_configuration", advertised: true, wantError: true},
		{name: "configured_without_capability", configured: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability}
			if tc.advertised {
				caps = append(caps, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/runners/identity" {
					t.Error("startup reached effect endpoint")
					w.WriteHeader(400)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "remote_runner", "capabilities": caps})
			}))
			defer server.Close()
			config := Config{Server: server.URL, RunnerID: "remote_runner", Token: "fixture", Database: &DatabaseExecutionConfig{LogicalBackups: true, RemoteBackups: tc.configured, TrustedLoopbackHTTP: true}}
			if err := validateLocalBuildRuntimeIdentity(t.Context(), config); (err != nil) != tc.wantError {
				t.Fatal("runtime startup capability mismatch")
			}
		})
	}
}
