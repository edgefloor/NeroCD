package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStorageRetentionPinnedUpstreamUnion(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	base := []BackupRetentionItem{{CopyID: "new", Location: "remote", Bytes: 128, CreatedAt: now.Add(-48 * time.Hour), Availability: "available"}, {CopyID: "middle", Location: "remote", Bytes: 64, CreatedAt: now.Add(-72 * time.Hour), Availability: "available"}, {CopyID: "old", Location: "remote", Bytes: 64, CreatedAt: now.Add(-96 * time.Hour), Availability: "available"}}
	for _, tc := range []struct {
		name   string
		limits BackupRetentionLimits
		want   []string
	}{{"zero", BackupRetentionLimits{}, nil}, {"count", BackupRetentionLimits{KeepCount: 1}, []string{"old", "middle"}}, {"size crossing included", BackupRetentionLimits{MaxBytes: 160}, []string{"old", "middle"}}, {"size equality survives", BackupRetentionLimits{MaxBytes: 192}, []string{"old"}}, {"size newest alone survives", BackupRetentionLimits{MaxBytes: 1}, []string{"old", "middle"}}, {"strict age", BackupRetentionLimits{KeepDays: 3}, []string{"old"}}, {"age selects all", BackupRetentionLimits{KeepDays: 1}, []string{"old", "middle", "new"}}, {"union", BackupRetentionLimits{KeepCount: 2, KeepDays: 1}, []string{"old", "middle", "new"}}} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, _ := SelectStorageBackupRetention(HostingStorageBackupRetentionPolicy{Enabled: true, Remote: tc.limits}, base, now)
			var selected []string
			for _, c := range got {
				if c.Selected {
					selected = append(selected, c.CopyID)
				}
			}
			if !reflect.DeepEqual(selected, tc.want) {
				t.Fatalf("selected %v want %v", selected, tc.want)
			}
		})
	}
	if base[0].Selected || base[0].ProtectedReasons != nil {
		t.Fatal("selection mutated inventory")
	}
}
func TestStorageRetentionIndependentLimitsAndProtections(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	items := []BackupRetentionItem{{CopyID: "b", Location: "local", Bytes: 128, CreatedAt: now, Availability: "available"}, {CopyID: "a", Location: "local", Bytes: 64, CreatedAt: now, Availability: "available"}, {CopyID: "r", Location: "remote", Bytes: 64, CreatedAt: now.Add(-72 * time.Hour), Availability: "available"}, {CopyID: "p", Location: "remote", Bytes: 64, CreatedAt: now.Add(-96 * time.Hour), Availability: "unproven", ProtectedReasons: []string{"unproven_ownership"}}}
	got, _, _, _ := SelectStorageBackupRetention(HostingStorageBackupRetentionPolicy{Enabled: true, Local: BackupRetentionLimits{KeepCount: 1}, Remote: BackupRetentionLimits{KeepDays: 1}}, items, now)
	selected := map[string]bool{}
	for _, c := range got {
		selected[c.CopyID] = c.Selected
	}
	if !selected["a"] || selected["b"] || !selected["r"] || selected["p"] {
		t.Fatalf("selection %v", selected)
	}
	for _, c := range got {
		for _, reason := range c.ProtectedReasons {
			if reason == "newest" {
				t.Fatal("blanket newest protection forbidden")
			}
		}
	}
}

func TestStorageRetentionCredentialPurposeCannotBeSubstituted(t *testing.T) {
	request := BackupRemoteCredentialRequest{RetentionCopyID: "copy", Authority: HostingAuthority{OperationID: "execution", RunID: "storage_retention_execution", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon"}
	metadata := BackupRemoteCredentialMetadata{Purpose: HostingStorageBackupRetentionCredentialPurpose, Request: request, RunnerID: "runner", UseUntil: time.Now().Add(time.Minute)}
	metadata.RequestHash = HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{"runner", request})
	if request.Validate() != nil || metadata.Validate() != nil {
		t.Fatal("storage retention credential family rejected")
	}
	for _, purpose := range []string{HostingDatabaseBackupRetentionCredentialPurpose, HostingDatabaseBackupRemoteCredentialPurpose, HostingStorageBackupRemoteCredentialPurpose} {
		bad := metadata
		bad.Purpose = purpose
		if bad.Validate() == nil {
			t.Fatal("credential purpose substituted", purpose)
		}
	}
	request.RetentionCopyID = ""
	if request.Validate() == nil {
		t.Fatal("retention identity acquired transfer credentials")
	}
}
