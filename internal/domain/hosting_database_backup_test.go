package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDatabaseBackupClosedInputAndTaskAuthority(t *testing.T) {
	valid := `{"request_id":"backup_request","expected_resource_generation":1}`
	var in HostingDatabaseBackupInput
	if DecodeHostingDatabaseBackupJSON([]byte(valid), &in) != nil || in.Validate() != nil {
		t.Fatal("valid immediate backup rejected")
	}
	for _, raw := range []string{`null`, `[]`, valid + `{}`, strings.Replace(valid, `}`, `,"path":"/private"}`, 1), strings.Replace(valid, `"request_id":"backup_request"`, `"request_id":"backup_request","request_id":"changed"`, 1), strings.Replace(valid, `"request_id"`, `"Request_ID"`, 1)} {
		var got HostingDatabaseBackupInput
		if DecodeHostingDatabaseBackupJSON([]byte(raw), &got) == nil && got.Validate() == nil {
			t.Fatal("ambiguous request accepted", raw)
		}
	}
	for _, name := range []string{"", " bad", "bad ", "bad\x01name", strings.Repeat("a", 129)} {
		if (HostingDatabaseBackupRestoreInput{RequestID: "restore", Name: name}).Validate() == nil {
			t.Fatal("invalid target name accepted")
		}
	}
	run := TaskRun{ID: "db_backup_backup", RunSpec: RunSpec{Type: RunTypeDatabaseBackup, HostingOperationID: "backup", HostingMode: "backup", Inputs: map[string]any{}}}
	if ValidateDatabaseBackupTask(run) != nil || !ContainsHostingExecution(run.RunSpec, Workflow{}) {
		t.Fatal("internal task not protected")
	}
	run.RunSpec.Inputs["path"] = "/private"
	if ValidateDatabaseBackupTask(run) == nil {
		t.Fatal("caller execution payload accepted")
	}
}
func TestDatabaseBackupBlobAndMutationBounds(t *testing.T) {
	blob := HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 5, Format: "postgresql_custom"}
	if blob.Validate() != nil {
		t.Fatal("valid custom format rejected")
	}
	for _, mutate := range []func(*HostingDatabaseBackupBlob){func(b *HostingDatabaseBackupBlob) { b.Bytes = HostingDatabaseBackupMaxBytes + 1 }, func(b *HostingDatabaseBackupBlob) { b.Digest = "password" }, func(b *HostingDatabaseBackupBlob) { b.Format = "sql" }, func(b *HostingDatabaseBackupBlob) { b.Bytes = 4 }} {
		copy := blob
		mutate(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid artifact accepted")
		}
	}
	in := HostingDatabaseBackupMutation{HostingAuthority: HostingAuthority{OperationID: "backup", RunID: "db_backup_backup", LeaseID: "lease", Attempt: 1, Fence: "fence"}, ReceiptID: "intent", PlanHash: blob.Digest, Phase: "intent"}
	if in.Validate() != nil {
		t.Fatal("valid intent rejected")
	}
	in.Blob = &blob
	if in.Validate() == nil {
		t.Fatal("intent smuggled completion")
	}
	in.Phase = "complete"
	if in.Validate() != nil {
		t.Fatal("valid result rejected")
	}
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "path") {
		t.Fatal("mutation contains private values")
	}
}
