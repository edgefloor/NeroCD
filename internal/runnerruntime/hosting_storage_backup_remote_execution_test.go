//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"nerocd/internal/runner"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestStorageRemoteDownloadAlwaysGETAndCodecValidation(t *testing.T) {
	for _, wrongFormat := range []bool{false, true} {
		name := "valid_cached"
		if wrongFormat {
			name = "wrong_format"
		}
		t.Run(name, func(t *testing.T) {
			f := archiveFixture(t)
			data := archiveTestBytes(t)
			if wrongFormat {
				data = bytes.Repeat([]byte("x"), 1536)
			}
			digest := sha256.Sum256(data)
			key := hex.EncodeToString(digest[:])
			blob := domain.HostingStorageArchiveBlob{BackupID: f.p.BackupID, DestinationID: f.p.DestinationID, DaemonID: f.p.Helper.DaemonID, SHA256: "sha256:" + key, SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
			if !wrongFormat {
				if _, err := f.e.store.Put(t.Context(), bytes.NewReader(data), key, int64(len(data))); err != nil {
					t.Fatal(err)
				}
			}
			gets := 0
			remote, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected request")
				}
				gets++
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				_, _ = w.Write(data)
			})
			plan := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "download", Mode: "download", Blob: &blob}, Limits: f.p.Limits, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: domain.StorageRemoteObjectKey(f.p.BackupID, blob)}
			err := executeStorageRemoteObject(t.Context(), f.e, remote, plan, func(_ context.Context) error { return nil })
			if (err != nil) != wrongFormat {
				t.Fatalf("format accepted incorrectly: %v", err)
			}
			if gets != 1 {
				t.Fatalf("remote GET count %d", gets)
			}
			if wrongFormat {
				handle, _, openErr := f.e.store.Open(t.Context(), key)
				if openErr == nil {
					handle.Close()
					t.Fatal("unverified format entered CAS")
				}
			}
			if f.mutations != 0 || f.starts != 0 {
				t.Fatal("download mutated source or started helper")
			}
		})
	}
}

func TestStorageRemoteDownloadRemovesStagingOnEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "invalid", "truncated", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := archiveFixture(t)
			data := archiveTestBytes(t)
			if outcome == "invalid" {
				data = bytes.Repeat([]byte("x"), 1536)
			}
			digest := sha256.Sum256(data)
			blob := domain.HostingStorageArchiveBlob{BackupID: f.p.BackupID, DestinationID: f.p.DestinationID, DaemonID: f.p.Helper.DaemonID, SHA256: "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			remote, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				if outcome == "cancelled" {
					cancel()
				}
				if outcome == "truncated" {
					_, _ = w.Write(data[:4])
				} else {
					_, _ = w.Write(data)
				}
			})
			p := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "download_cleanup", Mode: "download", Blob: &blob}, Limits: f.p.Limits, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: domain.StorageRemoteObjectKey(f.p.BackupID, blob)}
			err := executeStorageRemoteObject(ctx, f.e, remote, p, func(context.Context) error { return nil })
			if (err == nil) != (outcome == "success") {
				t.Fatalf("outcome %s: %v", outcome, err)
			}
			if _, err = f.e.temporary.Lstat("download_cleanup.partial"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("download retained staging file", err)
			}
		})
	}
}
func TestStorageRemoteStagingCleanupPreservesUnrelatedEntries(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	config := &StorageArchiveExecutionConfig{TemporaryRoot: rootPath}
	plan := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "owned", Mode: "download"}}
	owned := filepath.Join(rootPath, "owned.partial")
	other := filepath.Join(rootPath, "other.partial")
	if err := os.WriteFile(owned, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStorageRemoteReplay(config, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned staging remained", err)
	}
	if err := os.Symlink(other, owned); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStorageRemoteReplay(config, plan); err == nil {
		t.Fatal("replay accepted a symlink")
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "unrelated" {
		t.Fatal("unrelated target changed", err)
	}
	if _, err := storageRemotePartialName("../other"); err == nil {
		t.Fatal("cleanup accepted a caller path")
	}
}

func TestStorageRemoteReplayRetiresReceiptAndExactAbandonedStaging(t *testing.T) {
	f := archiveFixture(t)
	now := time.Now().UTC()
	data := archiveTestBytes(t)
	digest := sha256.Sum256(data)
	blob := domain.HostingStorageArchiveBlob{BackupID: f.p.BackupID, DestinationID: f.p.DestinationID, DaemonID: f.p.Helper.DaemonID, SHA256: "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: f.p.ProjectID, Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "remote", Endpoint: "https://backup.example", Bucket: "storage-backups", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "replay_download", Version: domain.HostingStorageBackupTransferVersion, ProjectID: f.p.ProjectID, TargetID: target.ID, TargetRevision: 1, DestinationID: f.p.DestinationID, Mode: "download", BackupID: blob.BackupID, RestoreOperationID: "restore", State: "queued", Blob: &blob, CreatedAt: now, UpdatedAt: now}, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target, Access: domain.BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: domain.BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, ActorID: "owner", RunnerID: f.p.RunnerID, ServerID: f.p.ServerID, DaemonID: blob.DaemonID, LocationID: blob.BackupID, ObjectKey: domain.StorageRemoteObjectKey(blob.BackupID, blob), MaxBytes: f.p.Limits.ArchiveBytes, TimeoutSeconds: 30, Limits: f.p.Limits}
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: p.Operation.ID, RunID: "storage_transfer_" + p.Operation.ID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	request := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "complete", PlanHash: p.Hash, Phase: "complete", DaemonID: p.DaemonID}
	j, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	_, err = j.AppendStorageBackupTransfer(runner.JournalStorageBackupTransfer{ID: request.ReceiptID, Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: request, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, p.Operation.ID+".partial")
	if err = os.WriteFile(partial, []byte("interrupted staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated.partial")
	if err = os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/hosting/storage-backup-transfer/mutation" {
			t.Error("replay requested fresh execution authority")
			w.WriteHeader(403)
			return
		}
		var in domain.HostingStorageBackupTransferMutation
		if json.NewDecoder(r.Body).Decode(&in) != nil || in != request {
			t.Error("replay changed exact committed request")
			w.WriteHeader(409)
			return
		}
		posts++
		out := p.Operation
		out.State = "succeeded"
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	if err = reconcileStorageBackupTransferJournal(t.Context(), Config{Server: server.URL, Token: "runner", StorageArchive: &StorageArchiveExecutionConfig{RemoteBackups: true, TrustedLoopbackHTTP: true, TemporaryRoot: root}}, j); err != nil {
		t.Fatal("startup remained blocked by committed history", err)
	}
	if posts != 1 || j.Depth() != 0 {
		t.Fatal("receipt not retired", posts, j.Depth())
	}
	if _, err = os.Lstat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exact abandoned staging retained", err)
	}
	if contents, err := os.ReadFile(unrelated); err != nil || string(contents) != "keep" {
		t.Fatal("unrelated staging changed", err)
	}
}
