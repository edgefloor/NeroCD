package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestDatabaseBackupReceiptVersionsAndPhases(t *testing.T) {
	for _, version := range []string{domain.HostingDatabaseBackupVersion, domain.HostingDatabaseMySQLBackupVersion, domain.HostingDatabaseBackupComponentVersion} {
		for _, phase := range []string{"intent", "complete", "failed", "uncertain"} {
			t.Run(version+"/"+phase, func(t *testing.T) {
				in := domain.HostingDatabaseBackupMutation{HostingAuthority: domain.HostingAuthority{OperationID: "backup", RunID: "db_backup_backup", LeaseID: "lease", Attempt: 1, Fence: "fence"}, ReceiptID: phase, PlanHash: "sha256:" + strings.Repeat("a", 64), Phase: phase}
				out := domain.HostingDatabaseBackupOperation{ID: "backup", Version: version, ProjectID: "project", SourceResourceID: "source", SourceGeneration: 1, BackupID: "backup", Mode: "backup", State: map[string]string{"intent": "applying", "complete": "succeeded", "failed": "failed", "uncertain": "uncertain"}[phase], Consistency: "logical_snapshot_source_writable", MaxBytes: 1024, TimeoutSeconds: 60, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
				if version == domain.HostingDatabaseBackupComponentVersion {
					in.ComponentVersion = version
					out.SourceComponent, out.SourceDeployedOperationID = "postgresql", "deployed"
				}
				if phase == "complete" {
					in.WritersResumed = version == domain.HostingDatabaseBackupComponentVersion
					in.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 10, Format: domain.HostingDatabaseBackupFormatFor(version)}
					out.Blob = in.Blob
				}
				if phase == "failed" || phase == "uncertain" {
					in.FailureCode, out.FailureCode = "execution_failed", "execution_failed"
				}
				out.OwnershipHeld = phase == "uncertain"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(out) }))
				defer server.Close()
				client := New(server.URL, "fixture")
				ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
				if _, err := client.HostingDatabaseBackupMutation(ctx, in); err != nil {
					t.Fatal("supported receipt rejected", err)
				}
				out.Version = "unsupported.v1"
				if _, err := client.HostingDatabaseBackupMutation(ctx, in); err == nil {
					t.Fatal("unsupported receipt version accepted")
				}
				out.Version = domain.HostingDatabaseBackupComponentVersion
				out.SourceComponent, out.SourceDeployedOperationID = "postgresql", "deployed"
				if version == domain.HostingDatabaseBackupComponentVersion {
					out.Version = domain.HostingDatabaseBackupVersion
					out.SourceComponent, out.SourceDeployedOperationID = "", ""
				}
				if _, err := client.HostingDatabaseBackupMutation(ctx, in); err == nil {
					t.Fatal("receipt switched the requested protocol version")
				}
				out.Version = version
				out.SourceComponent, out.SourceDeployedOperationID = "", ""
				if version == domain.HostingDatabaseBackupComponentVersion {
					out.SourceComponent, out.SourceDeployedOperationID = "postgresql", "deployed"
				}
				if phase == "complete" {
					changed := *out.Blob
					changed.Bytes++
					out.Blob = &changed
					if _, err := client.HostingDatabaseBackupMutation(ctx, in); err == nil {
						t.Fatal("changed completion blob accepted")
					}
				}
			})
		}
	}
}
