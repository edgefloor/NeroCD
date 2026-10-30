package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func databaseDownloadClientFixture(t *testing.T) (domain.HostingDatabaseBackupDownloadPlan, domain.HostingDatabaseBackupDownloadStream) {
	t.Helper()
	p := domain.HostingDatabaseBackupDownloadPlan{Version: domain.HostingDatabaseBackupDownloadVersion, BackupVersion: domain.HostingDatabaseMySQLBackupVersion, ServerID: "server", SessionID: "session", ActorID: "actor", ProjectID: "project", ResourceID: "resource", BindingID: "binding", BackupID: "backup", CopyID: "copy", RunnerID: "runner", DestinationID: "destination", DaemonID: "daemon", Blob: domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 7, Format: "mysql_sql"}, LocalCopy: domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("b", 64), Device: 1, Inode: 2, Bytes: 7, Created: true}, ExpiresAt: time.Now().Add(time.Minute).UTC()}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid plan fixture")
	}
	in := domain.HostingDatabaseBackupDownloadStream{HostingAuthority: domain.HostingAuthority{OperationID: "session", RunID: "db_transfer_session", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: p.Hash}
	return p, in
}
func databaseDownloadContext(ctx context.Context) context.Context {
	return WithDatabaseTransport(ctx, DatabaseTransport{TrustedLoopbackHTTP: true})
}
func TestDatabaseBackupDownloadMultipartAndExactLength(t *testing.T) {
	for _, source := range []string{"archive", "short", "oversized"} {
		t.Run(source, func(t *testing.T) {
			p, in := databaseDownloadClientFixture(t)
			var valid atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing authorization")
				}
				mr, err := r.MultipartReader()
				if err != nil {
					if source == "archive" {
						t.Error(err)
					}
					return
				}
				meta, err := mr.NextPart()
				if err != nil {
					if source == "archive" {
						t.Error(err)
					}
					return
				}
				raw, _ := io.ReadAll(meta)
				var got domain.HostingDatabaseBackupDownloadStream
				if meta.FormName() != "metadata" || len(raw) > 4096 || json.Unmarshal(raw, &got) != nil || got != in {
					t.Error("metadata mismatch")
				}
				part, err := mr.NextPart()
				if err != nil {
					return
				}
				data, err := io.ReadAll(part)
				if err != nil {
					return
				}
				if part.FormName() != "archive" || part.FileName() != "archive.bin" || part.Header.Get("Content-Type") != "application/octet-stream" || string(data) != "archive" {
					return
				}
				if _, err = mr.NextPart(); err != io.EOF {
					return
				}
				valid.Store(true)
				_ = json.NewEncoder(w).Encode(map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
			}))
			defer server.Close()
			err := New(server.URL, "token").StreamDatabaseBackupDownload(databaseDownloadContext(t.Context()), in, p, strings.NewReader(source))
			if source == "archive" {
				if err != nil || !valid.Load() {
					t.Fatalf("valid stream failed %v", err)
				}
			} else if err == nil || valid.Load() {
				t.Fatal("wrong length accepted")
			}
		})
	}
}
func TestDatabaseBackupDownloadRejectsRedirectExpiryAndBadAcknowledgment(t *testing.T) {
	p, in := databaseDownloadClientFixture(t)
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer target.Close()
	for _, mode := range []string{"redirect", "bad_ack", "expiry", "strict_http"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"session_id": "wrong", "plan_hash": p.Hash})
			}))
			defer server.Close()
			plan := p
			request := in
			ctx := databaseDownloadContext(t.Context())
			if mode == "expiry" {
				plan.ExpiresAt = time.Now().Add(-time.Second)
				plan.Hash = ""
				plan.Hash = domain.HostingDocumentHash(plan)
				request.PlanHash = plan.Hash
			}
			if mode == "strict_http" {
				ctx = t.Context()
			}
			if New(server.URL, "token").StreamDatabaseBackupDownload(ctx, request, plan, bytes.NewReader([]byte("archive"))) == nil {
				t.Fatal("unsafe stream accepted")
			}
			if redirected.Load() {
				t.Fatal("redirect followed")
			}
		})
	}
}
func TestDatabaseBackupDownloadCancellationUnblocksBackpressure(t *testing.T) {
	p, in := databaseDownloadClientFixture(t)
	p.Blob.Bytes = 32 << 20
	p.LocalCopy.Bytes = p.Blob.Bytes
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	in.PlanHash = p.Hash
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(databaseDownloadContext(t.Context()))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- New(server.URL, "token").StreamDatabaseBackupDownload(ctx, in, p, io.LimitReader(zeroDatabaseDownloadReader{}, p.Blob.Bytes))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backpressured stream did not stop")
	}
}

type zeroDatabaseDownloadReader struct{}

func (zeroDatabaseDownloadReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestDatabaseBackupDownloadPlanRejectsChangedSessionAndExpiredPlan(t *testing.T) {
	for _, mode := range []string{"valid", "session", "expired", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			p, in := databaseDownloadClientFixture(t)
			if mode == "session" {
				p.SessionID = "other"
			}
			if mode == "expired" {
				p.ExpiresAt = time.Now().Add(-time.Second)
			}
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/hosting/database-backup-download/plan" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("plan transport mismatch")
				}
				raw, _ := json.Marshal(p)
				if mode == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			_, err := New(server.URL, "token").HostingDatabaseBackupDownloadPlan(databaseDownloadContext(t.Context()), in.HostingAuthority)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("plan acceptance %v", err)
			}
		})
	}
}

func TestDatabaseBackupDownloadUnavailableNoticeHasNoArchive(t *testing.T) {
	p, in := databaseDownloadClientFixture(t)
	in.FailureCode = "archive_unavailable"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		var metadata domain.HostingDatabaseBackupDownloadStream
		if part.FormName() != "metadata" || json.NewDecoder(part).Decode(&metadata) != nil || metadata != in {
			t.Error("invalid failure metadata")
		}
		if _, err = mr.NextPart(); err != io.EOF {
			t.Error("failure notification sent archive bytes")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"session_id": p.SessionID, "plan_hash": p.Hash})
	}))
	defer server.Close()
	c := New(server.URL, "token")
	if err := c.StreamDatabaseBackupDownload(databaseDownloadContext(t.Context()), in, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.StreamDatabaseBackupDownload(databaseDownloadContext(t.Context()), in, p, strings.NewReader("archive")); err == nil {
		t.Fatal("failure notice accepted source")
	}
	in.FailureCode = ""
	if err := c.StreamDatabaseBackupDownload(databaseDownloadContext(t.Context()), in, p, nil); err == nil {
		t.Fatal("normal stream accepted nil source")
	}
}
