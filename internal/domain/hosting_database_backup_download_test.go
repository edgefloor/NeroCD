package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDatabaseDownloadPinsFourFormatsAndReadAuthority(t *testing.T) {
	for _, version := range []string{HostingDatabaseBackupVersion, HostingDatabaseMySQLBackupVersion, HostingDatabaseBackupComponentVersion, HostingDatabaseMySQLBackupComponentVersion} {
		t.Run(version, func(t *testing.T) {
			p := HostingDatabaseBackupDownloadPlan{Version: HostingDatabaseBackupDownloadVersion, BackupVersion: version, ServerID: "server", SessionID: "session", ActorID: "actor", ProjectID: "project", ResourceID: "resource", BindingID: "binding", BackupID: "backup", CopyID: "copy", RunnerID: "runner", DestinationID: "destination", DaemonID: "daemon", Blob: HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 7, Format: HostingDatabaseBackupFormatFor(version)}, LocalCopy: BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("b", 64), Device: 1, Inode: 2, Bytes: 7, Created: true}, ExpiresAt: time.Now().Add(time.Minute)}
			sign := func(p HostingDatabaseBackupDownloadPlan) HostingDatabaseBackupDownloadPlan {
				p.Hash = ""
				p.Hash = HostingDocumentHash(p)
				return p
			}
			p = sign(p)
			if p.Validate() != nil {
				t.Fatal("qualified historical plan rejected")
			}
			for _, mutate := range []func(*HostingDatabaseBackupDownloadPlan){func(p *HostingDatabaseBackupDownloadPlan) { p.BackupVersion = "unknown" }, func(p *HostingDatabaseBackupDownloadPlan) {
				if p.Blob.Format == "mysql_sql" {
					p.Blob.Format = "postgresql_custom"
				} else {
					p.Blob.Format = "mysql_sql"
				}
			}, func(p *HostingDatabaseBackupDownloadPlan) { p.Blob.Bytes = HostingDatabaseBackupMaxBytes + 1 }, func(p *HostingDatabaseBackupDownloadPlan) { p.LocalCopy.Inode = 0 }, func(p *HostingDatabaseBackupDownloadPlan) { p.Version = "unknown" }} {
				bad := p
				mutate(&bad)
				bad = sign(bad)
				if bad.Validate() == nil {
					t.Fatal("invalid copy/format admitted")
				}
			}
			run := TaskRun{ID: "db_transfer_session", RunSpec: RunSpec{Type: RunTypeDatabaseBackupTransfer, HostingOperationID: "session", HostingMode: HostingDatabaseBackupDownloadMode, Inputs: map[string]any{}}}
			if ValidateDatabaseBackupTransferTask(run) != nil {
				t.Fatal("read task rejected")
			}
			run.RunSpec.Process = &ProcessSpec{}
			if ValidateDatabaseBackupTransferTask(run) == nil {
				t.Fatal("generic process granted read authority")
			}
		})
	}
}
