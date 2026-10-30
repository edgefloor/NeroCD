//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func externalImportTestPlan(t *testing.T) (*databaseBackupEngine, domain.HostingDatabaseImportStagePlan) {
	t.Helper()
	e, b, _, _ := componentBackupEngineFixtureFor(t, "", true)
	now := time.Now().UTC()
	stage := domain.HostingDatabaseImportStage{ID: "external_stage", Version: domain.HostingDatabaseImportStageVersion, TargetResourceID: b.Source.Snapshot.ResourceID, Component: "mysql", ExpectedDeployedOperationID: b.Source.OperationID, ExpectedResourceGeneration: b.Source.Snapshot.ResourceGeneration, S3Source: domain.HostingDatabaseImportS3Source{TargetID: "external_target", Revision: 1, ObjectKey: "literal %2f ü.sql"}, State: "queued", CreatedAt: now, UpdatedAt: now}
	target := domain.HostingDatabaseBackupTarget{ID: "external_target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "External source", Endpoint: "https://s3.example", Bucket: "external-sql", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingDatabaseImportStagePlan{Stage: stage, ActorID: "actor", ProjectID: "project", RunnerID: b.RunnerID, Target: b.Source, TargetPlan: b.SourcePlan, S3Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target, Access: domain.BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: domain.BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, MaxBytes: domain.HostingDatabaseImportMaxBytes, TimeoutSeconds: 60}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid external test plan")
	}
	return e, p
}
func externalImportTestReady(t *testing.T, e *databaseBackupEngine, p domain.HostingDatabaseImportStagePlan) (*externalImportStageHandle, domain.HostingDatabaseImportPlan) {
	t.Helper()
	h, err := createExternalImportStage(t.Context(), e.host, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	result, err := inspectExternalSQL(t.Context(), strings.NewReader("CREATE TABLE proof(id INT);"), h.file, domain.HostingDatabaseImportMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := h.publish(p, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ip := domain.HostingDatabaseImportPlan{Operation: domain.HostingDatabaseImportOperation{ID: "external_import", Version: domain.HostingDatabaseImportVersion, StageID: p.Stage.ID, TargetResourceID: p.Stage.TargetResourceID, TargetComponent: "mysql", TargetDeployedOperationID: p.Target.OperationID, State: "queued", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}, StagePlan: p, Receipt: receipt}
	ip.Hash = domain.HostingDocumentHash(ip)
	if ip.Validate() != nil {
		t.Fatal("invalid consumption test plan")
	}
	return h, ip
}

func TestExternalStageRetainsExactPrivateInode(t *testing.T) {
	for _, mode := range []string{"valid", "replaced", "modified", "symlink", "hardlink", "wrong plan"} {
		t.Run(mode, func(t *testing.T) {
			e, p := externalImportTestPlan(t)
			h, ip := externalImportTestReady(t, e, p)
			root := filepath.Join(e.host.stateRoot, "external-import-stages")
			path := filepath.Join(root, h.id+".sql")
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "replaced":
				if os.Remove(path) != nil || os.WriteFile(path, []byte("CREATE TABLE proof(id INT);"), 0600) != nil {
					t.Fatal("replacement setup")
				}
			case "modified":
				if os.WriteFile(path, []byte("CREATE TABLE other(id INT);"), 0600) != nil {
					t.Fatal("mutation setup")
				}
			case "symlink":
				if os.Rename(path, path+".saved") != nil || os.Symlink(path+".saved", path) != nil {
					t.Fatal("symlink setup")
				}
			case "hardlink":
				if os.Link(path, path+".alias") != nil {
					t.Fatal("link setup")
				}
			case "wrong plan":
				ip.StagePlan.Stage.S3Source.ObjectKey = "other.sql"
				ip.StagePlan.Hash = ""
				ip.StagePlan.Hash = domain.HostingDocumentHash(ip.StagePlan)
				ip.Hash = ""
				ip.Hash = domain.HostingDocumentHash(ip)
			}
			opened, err := openExternalImportStage(t.Context(), e.host, ip)
			if mode != "valid" {
				if err == nil {
					_ = opened.Close()
					t.Fatal("changed file or authority admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = opened.Close() }()
			if err = opened.discard(); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("consumed private input remained")
			}
		})
	}
}

func TestExternalStageExpiryNeverSweepsSharedCASOrImportIntent(t *testing.T) {
	for _, intent := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "consuming"}[intent], func(t *testing.T) {
			e, p := externalImportTestPlan(t)
			p.Stage.CreatedAt = time.Now().UTC().Add(-3 * time.Hour)
			p.Stage.UpdatedAt = p.Stage.CreatedAt
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			h, err := createExternalImportStage(t.Context(), e.host, p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.file.WriteString("SELECT 1;"); err != nil {
				t.Fatal(err)
			}
			if intent && externalImportWriteDocument(h.root, h.id+".import", map[string]string{"intent": "retained"}) != nil {
				t.Fatal("intent setup")
			}
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			data := []byte("CREATE TABLE backup(id INT);")
			sum := sha256.Sum256(data)
			key := hex.EncodeToString(sum[:])
			if _, err = e.store.Put(t.Context(), bytes.NewReader(data), key, int64(len(data))); err != nil {
				t.Fatal(err)
			}
			if err = cleanupExpiredExternalImportStages(t.Context(), e.host); err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(e.host.stateRoot, "external-import-stages", p.Stage.ID+".sql"))
			if intent && err != nil || !intent && !os.IsNotExist(err) {
				t.Fatal("expiry/intent ownership violated")
			}
			file, _, err := e.store.Open(t.Context(), key)
			if err != nil {
				t.Fatal("stage cleanup touched backup CAS")
			}
			_ = file.Close()
		})
	}
}

func TestExternalImportJournalRestartCannotReexecuteIntent(t *testing.T) {
	e, p := externalImportTestPlan(t)
	_, ip := externalImportTestReady(t, e, p)
	a := domain.HostingAuthority{OperationID: ip.Operation.ID, RunID: "db_import_" + ip.Operation.ID, LeaseID: "lease_external", Attempt: 1, Fence: "fence_external"}
	now := time.Now().UTC()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := runner.JournalDatabaseImport{ID: "intent_external", Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, ImportPlan: &ip, Request: domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "intent_external", PlanHash: ip.Hash, Phase: "import_intent", DaemonID: p.Target.Release.DaemonID}, CreatedAt: now}
	if _, err = j.AppendDatabaseImport(entry); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(j.Snapshot())
	if err != nil || bytes.Contains(raw, []byte(e.host.stateRoot)) {
		t.Fatal("journal exposed local path")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if len(j.Snapshot().DatabaseImports) != 1 {
		t.Fatal("lost durable intent")
	}
	// With no terminal receipt, reconciliation refuses before any HTTP request.
	if reconcileDatabaseImportJournal(context.Background(), Config{Database: &DatabaseExecutionConfig{ExternalImports: true}}, j) != errExternalImportHeld {
		t.Fatal("unpaired intent did not hold")
	}
	if len(j.Snapshot().DatabaseImports) != 1 {
		t.Fatal("unpaired intent silently acknowledged")
	}
}
