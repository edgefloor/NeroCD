//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalImportStreamPublishesExistingPrivateStage(t *testing.T) {
	e, p := externalImportTestPlan(t)
	sql := []byte("CREATE TABLE local_import_proof(id INT); INSERT INTO local_import_proof VALUES(42);\n")
	deadline := p.Stage.CreatedAt.Add(time.Hour)
	p.Stage.Version = domain.HostingDatabaseImportUploadVersion
	p.Stage.S3Source = domain.HostingDatabaseImportS3Source{}
	p.Stage.UploadSource = &domain.HostingDatabaseImportUploadSource{Filename: "display.sql", Bytes: int64(len(sql))}
	p.Stage.UploadDeadline = &deadline
	p.S3Target = domain.HostingDatabaseBackupTargetSnapshot{}
	p.Stage.UploadHash = p.UploadIdentityHash()
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid local protected plan")
	}
	raw, _ := json.Marshal(p)
	if bytes.Contains(raw, []byte("s3_target")) || bytes.Contains(raw, []byte("s3_source")) {
		t.Fatal("local plan inherited S3 authority")
	}
	var replay domain.HostingDatabaseImportStagePlan
	if json.Unmarshal(raw, &replay) != nil || replay.Validate() != nil {
		t.Fatal("local protected plan cannot replay")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/database-import-stage/upload-stream" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("wrong stream request")
			w.WriteHeader(400)
			return
		}
		var in domain.HostingDatabaseImportUploadStream
		data, _ := io.ReadAll(io.LimitReader(r.Body, 4097))
		if domain.DecodeHostingDatabaseBackupJSON(data, &in) != nil || in.PlanHash != p.Hash || in.OperationID != p.Stage.ID {
			t.Error("wrong stream authority")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sql)
	}))
	defer server.Close()
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	body, err := runnerclient.New(server.URL, "token").HostingDatabaseImportUploadStream(ctx, domain.HostingDatabaseImportUploadStream{HostingAuthority: domain.HostingAuthority{OperationID: p.Stage.ID, RunID: "db_import_stage_" + p.Stage.ID, LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: p.Hash})
	if err != nil {
		t.Fatal(err)
	}
	file, err := createExternalImportStage(ctx, e.host, p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	result, err := inspectExternalSQL(ctx, body, file.file, p.MaxBytes)
	closeErr := body.Close()
	if err != nil || closeErr != nil || result.SourceBytes != p.Stage.UploadSource.Bytes {
		t.Fatal("bounded local inspector failed", err, closeErr)
	}
	receipt, err := file.publish(p, result, nil)
	if err != nil || receipt.ValidateFor(p) != nil || receipt.SourceIdentity != nil {
		t.Fatal("private local stage did not publish", err)
	}
	if err = file.discard(); err != nil {
		t.Fatal("private stage cleanup", err)
	}
}

func TestLocalImportInterruptedStageDiscardsOnlyPartialBytes(t *testing.T) {
	e, p := externalImportTestPlan(t)
	deadline := p.Stage.CreatedAt.Add(time.Hour)
	p.Stage.Version = domain.HostingDatabaseImportUploadVersion
	p.Stage.S3Source = domain.HostingDatabaseImportS3Source{}
	p.Stage.UploadSource = &domain.HostingDatabaseImportUploadSource{Filename: "partial.sql", Bytes: 32}
	p.Stage.UploadDeadline = &deadline
	p.S3Target = domain.HostingDatabaseBackupTargetSnapshot{}
	p.Stage.UploadHash = p.UploadIdentityHash()
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	file, err := createExternalImportStage(t.Context(), e.host, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.file.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: p.Stage.ID, RunID: "db_import_stage_" + p.Stage.ID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	now := time.Now().UTC()
	path := t.TempDir()
	if err = os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := runner.JournalDatabaseImport{ID: "local_intent", Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, StagePlan: &p, Request: domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "local_intent", PlanHash: p.Hash, Phase: "stage_intent", DaemonID: p.Target.Release.DaemonID}, CreatedAt: now}
	if _, err = journal.AppendDatabaseImport(entry); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	config := Config{Database: &DatabaseExecutionConfig{ExternalImports: true, StateRoot: e.host.stateRoot, SecretRoot: e.host.secretRoot}}
	if err = reconcileDatabaseImportJournalWithCleanup(t.Context(), config, journal, nil, func(ctx context.Context, p domain.HostingDatabaseImportStagePlan) error {
		return discardInterruptedLocalUpload(ctx, e.host, p)
	}); err != errExternalImportHeld {
		t.Fatal("partial body was resumed or silently acknowledged", err)
	}
	for _, suffix := range []string{".sql", ".owner"} {
		if _, err = os.Stat(filepath.Join(e.host.stateRoot, "external-import-stages", p.Stage.ID+suffix)); !os.IsNotExist(err) {
			t.Fatal("unready private bytes survived restart")
		}
	}
	if len(journal.Snapshot().DatabaseImports) != 1 {
		t.Fatal("intent authority lost")
	}
}

func TestLocalImportRestartPreservesReadyAndForeignBytes(t *testing.T) {
	for _, kind := range []string{"ready", "consumed", "wrong_plan", "wrong_inode"} {
		t.Run(kind, func(t *testing.T) {
			engine, p := externalImportTestPlan(t)
			deadline := p.Stage.CreatedAt.Add(time.Hour)
			p.Stage.Version = domain.HostingDatabaseImportUploadVersion
			p.Stage.S3Source = domain.HostingDatabaseImportS3Source{}
			p.Stage.UploadSource = &domain.HostingDatabaseImportUploadSource{Filename: "preserve.sql", Bytes: 32}
			p.Stage.UploadDeadline = &deadline
			p.S3Target = domain.HostingDatabaseBackupTargetSnapshot{}
			p.Stage.UploadHash = p.UploadIdentityHash()
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			file, err := createExternalImportStage(t.Context(), engine.host, p)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "ready" {
				result, e := inspectExternalSQL(t.Context(), bytes.NewReader(bytes.Repeat([]byte("X"), 32)), file.file, p.MaxBytes)
				if e != nil {
					t.Fatal(e)
				}
				if _, err = file.publish(p, result, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(engine.host.stateRoot, "external-import-stages", p.Stage.ID)
			switch kind {
			case "consumed":
				if err = os.WriteFile(path+".import", []byte("private reservation"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong_plan":
				raw, e := os.ReadFile(path + ".owner")
				if e != nil {
					t.Fatal(e)
				}
				var owner externalImportStageOwner
				if json.Unmarshal(raw, &owner) != nil {
					t.Fatal("invalid fixture owner")
				}
				owner.Plan.Hash = "sha256:invalid"
				raw, _ = json.Marshal(owner)
				if err = os.WriteFile(path+".owner", raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong_inode":
				old, e := os.Open(path + ".sql")
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = old.Close() }()
				if err = os.Remove(path + ".sql"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path+".sql", []byte("foreign replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, e := os.ReadFile(path + ".sql")
			if e != nil {
				t.Fatal(e)
			}
			err = discardInterruptedLocalUpload(t.Context(), engine.host, p)
			if (kind == "wrong_plan" || kind == "wrong_inode") && err == nil {
				t.Fatal("foreign stage mismatch accepted")
			}
			after, readErr := os.ReadFile(path + ".sql")
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("protected bytes removed")
			}
		})
	}
}
