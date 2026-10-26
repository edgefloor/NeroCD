//go:build unix

package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func storageRetentionReplayEntry(t *testing.T) runner.JournalStorageBackupRetention {
	t.Helper()
	now := time.Now().UTC()
	item := domain.BackupRetentionItem{CopyID: "copy", BackupID: "backup", Location: "local", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, CreatedAt: now, Availability: "available", Selected: true, ProtectedReasons: []string{}}
	op := domain.HostingStorageBackupRetentionExecution{ID: "retention", Version: domain.HostingStorageBackupRetentionVersion, ProjectID: "project", ResourceID: "resource", PreviewID: "preview", PolicyRevision: 1, PlanHash: "sha256:" + strings.Repeat("b", 64), State: "queued", ReservationHeld: true, LocalState: "pending", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{{CopyID: item.CopyID, BackupID: item.BackupID, Location: "local", State: "pending", ReservationHeld: true}}, CreatedAt: now, UpdatedAt: now}
	copy := domain.StorageBackupRetentionCopy{Storage: domain.HostingStorageRef{StorageID: "storage", Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}, Item: item, RunnerID: "runner", DestinationID: "destination", ServerID: "server", DaemonID: "daemon", IdentityHash: "sha256:" + strings.Repeat("c", 64), Local: &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("d", 64), Device: 1, Inode: 2, Bytes: item.Bytes, Created: true}}
	p := domain.HostingStorageBackupRetentionPlan{Operation: op, ActorID: "owner", RunnerID: "runner", Copies: []domain.StorageBackupRetentionCopy{copy}, TimeoutSeconds: 60}
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: op.ID, RunID: "storage_retention_" + op.ID, LeaseID: "oldlease", Attempt: 1, Fence: "oldfence"}
	return runner.JournalStorageBackupRetention{ID: "intent", Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, CopyID: item.CopyID, Phase: "intent"}, CreatedAt: now}
}

func TestStorageRetentionStartupLostAcknowledgmentAndSupersession(t *testing.T) {
	for _, mode := range []string{"committed_result", "unpaired_without_resume", "unpaired_with_resume"} {
		t.Run(mode, func(t *testing.T) {
			entry := storageRetentionReplayEntry(t)
			path := filepath.Join(t.TempDir(), "journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.AppendStorageBackupRetention(entry); err != nil {
				t.Fatal(err)
			}
			result := entry
			result.ID = "deleted"
			result.Request.ReceiptID = "deleted"
			result.Request.Phase = "deleted"
			if mode == "committed_result" {
				if _, err = j.AppendStorageBackupRetention(result); err != nil {
					t.Fatal(err)
				}
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			mutations, retirements := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/storage-backup-retention/mutation":
					mutations++
					var in domain.HostingStorageBackupRetentionMutation
					if json.NewDecoder(r.Body).Decode(&in) != nil || domain.HostingDocumentHash(in) != domain.HostingDocumentHash(result.Request) {
						t.Error("replay changed committed request")
						w.WriteHeader(400)
						return
					}
					out := entry.Plan.Operation
					out.State = "succeeded"
					out.ReservationHeld = false
					out.LocalState = "succeeded"
					out.Items[0].State = "deleted"
					out.Items[0].ReservationHeld = false
					json.NewEncoder(w).Encode(out)
				case "/api/v1/runners/hosting/storage-backup-retention/retirement":
					retirements++
					var in domain.HostingStorageBackupRetentionRetirementInput
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != entry.Request.HostingAuthority || in.PlanHash != entry.Plan.Hash {
						t.Error("retirement changed old identity")
						w.WriteHeader(400)
						return
					}
					if mode != "unpaired_with_resume" {
						w.WriteHeader(409)
						return
					}
					json.NewEncoder(w).Encode(domain.HostingStorageBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: 2, ReplayOnly: true})
				default:
					t.Errorf("startup attempted effect/claim %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			err = reconcileStorageBackupRetentionJournal(t.Context(), Config{Server: server.URL, Token: "synthetic", StorageArchive: &StorageArchiveExecutionConfig{BackupRetention: true, TrustedLoopbackHTTP: true}}, j)
			if mode == "committed_result" {
				if err != nil || mutations != 1 || retirements != 0 || j.Depth() != 0 {
					t.Fatalf("committed replay: mutation=%d retirement=%d depth=%d err=%v", mutations, retirements, j.Depth(), err)
				}
			} else {
				if mutations != 0 || retirements != 1 || j.Depth() != 1 {
					t.Fatal("unpaired intent mutated or lost")
				}
				if (err == nil) != (mode == "unpaired_with_resume") {
					t.Fatalf("supersession: %v", err)
				}
			}
		})
	}
}

func TestStorageBackupDeletionStartupLostAcknowledgmentAndSupersession(t *testing.T) {
	for _, mode := range []string{"committed_result", "unpaired_without_resume", "unpaired_with_resume"} {
		t.Run(mode, func(t *testing.T) {
			entry := storageRetentionReplayEntry(t)
			entry.Plan.Operation.Version = domain.HostingStorageBackupDeletionVersion
			entry.Plan.Operation.Origin = domain.StorageBackupManualSelection
			entry.Plan.Operation.BackupID = "backup"
			entry.Plan.Operation.ResourceGeneration = 2
			entry.Plan.Operation.PolicyRevision = 0
			entry.Plan.Hash = ""
			entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
			entry.Request.PlanHash = entry.Plan.Hash
			path := filepath.Join(t.TempDir(), "journal")
			j, err := runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = j.AppendStorageBackupRetention(entry); err != nil {
				t.Fatal(err)
			}
			result := entry
			result.ID = "deleted"
			result.Request.ReceiptID = "deleted"
			result.Request.Phase = "deleted"
			if mode == "committed_result" {
				if _, err = j.AppendStorageBackupRetention(result); err != nil {
					t.Fatal(err)
				}
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			mutations, retirements := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/storage-backup-retention/mutation":
					mutations++
					var in domain.HostingStorageBackupRetentionMutation
					if json.NewDecoder(r.Body).Decode(&in) != nil || domain.HostingDocumentHash(in) != domain.HostingDocumentHash(result.Request) {
						t.Error("replay changed committed request")
						w.WriteHeader(400)
						return
					}
					out := entry.Plan.Operation
					out.State = "succeeded"
					out.ReservationHeld = false
					out.LocalState = "succeeded"
					out.Items[0].State = "deleted"
					out.Items[0].ReservationHeld = false
					json.NewEncoder(w).Encode(out)
				case "/api/v1/runners/hosting/storage-backup-retention/retirement":
					retirements++
					var in domain.HostingStorageBackupRetentionRetirementInput
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != entry.Request.HostingAuthority || in.PlanHash != entry.Plan.Hash {
						t.Error("retirement changed old identity")
						w.WriteHeader(400)
						return
					}
					if mode != "unpaired_with_resume" {
						w.WriteHeader(409)
						return
					}
					json.NewEncoder(w).Encode(domain.HostingStorageBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: 2, ReplayOnly: true})
				default:
					t.Errorf("startup attempted effect/claim %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			err = reconcileStorageBackupRetentionJournal(t.Context(), Config{Server: server.URL, Token: "synthetic", StorageArchive: &StorageArchiveExecutionConfig{BackupRetention: true, TrustedLoopbackHTTP: true}}, j)
			if mode == "committed_result" {
				if err != nil || mutations != 1 || retirements != 0 || j.Depth() != 0 {
					t.Fatalf("committed replay: mutation=%d retirement=%d depth=%d err=%v", mutations, retirements, j.Depth(), err)
				}
			} else {
				if mutations != 0 || retirements != 1 || j.Depth() != 1 {
					t.Fatal("unpaired intent mutated or lost")
				}
				if (err == nil) != (mode == "unpaired_with_resume") {
					t.Fatalf("supersession: %v", err)
				}
			}
		})
	}
}
