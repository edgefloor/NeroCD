package domain

import "testing"

func TestStorageBackupDeletionExplicitSelection(t *testing.T) {
	inventory := []StorageBackupRetentionCopy{{Item: BackupRetentionItem{CopyID: "local", BackupID: "backup", Location: "local", Availability: "available"}}, {Item: BackupRetentionItem{CopyID: "remote", BackupID: "backup", Location: "remote", Availability: "available"}}, {Item: BackupRetentionItem{CopyID: "foreign", BackupID: "other", Availability: "available"}}}
	got, final, e := SelectStorageBackupDeletion("backup", []string{"local"}, inventory)
	if e != nil || final || len(got) != 1 || !got[0].Item.Selected || inventory[0].Item.Selected {
		t.Fatal(got, final, e)
	}
	_, final, e = SelectStorageBackupDeletion("backup", []string{"local", "remote"}, inventory)
	if e != nil || !final {
		t.Fatal(final, e)
	}
	for _, ids := range [][]string{nil, {"local", "local"}, {"foreign"}, {"missing"}} {
		if _, _, e := SelectStorageBackupDeletion("backup", ids, inventory); e == nil {
			t.Fatal("invalid selection", ids)
		}
	}
	for _, reason := range []string{"shared", "active_or_recovery", "reserved", "unproven_ownership"} {
		bad := append([]StorageBackupRetentionCopy{}, inventory...)
		bad[0].Item.ProtectedReasons = []string{reason}
		if _, _, e := SelectStorageBackupDeletion("backup", []string{"local"}, bad); e == nil {
			t.Fatal(reason)
		}
	}
	for _, state := range []string{"deleted", "reserved", "uncertain", "unproven"} {
		bad := append([]StorageBackupRetentionCopy{}, inventory...)
		bad[0].Item.Availability = state
		if _, _, e := SelectStorageBackupDeletion("backup", []string{"local"}, bad); e == nil {
			t.Fatal(state)
		}
	}
}
func TestStorageBackupDeletionInputClosedSelection(t *testing.T) {
	valid := HostingStorageBackupDeletionPreviewInput{RequestID: "request", ExpectedResourceGeneration: 1, CopyIDs: []string{"copy"}}
	if valid.Validate() != nil {
		t.Fatal("valid rejected")
	}
	for _, ids := range [][]string{nil, {"copy", "copy"}, {"../copy"}} {
		bad := valid
		bad.CopyIDs = ids
		if bad.Validate() == nil {
			t.Fatal(ids)
		}
	}
}
