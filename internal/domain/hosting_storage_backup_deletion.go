package domain

import (
	"regexp"
	"slices"
)

var storageDeletionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

const HostingStorageBackupDeletionVersion = "owned-upload.backup-deletion.v1"
const HostingStorageBackupDeletionCapability = "hosting_storage_backup_deletion_v1"
const StorageBackupManualSelection = "manual_selection"

type HostingStorageBackupDeletionPreviewInput struct {
	RequestID                  string   `json:"request_id"`
	ExpectedResourceGeneration int64    `json:"expected_resource_generation"`
	CopyIDs                    []string `json:"copy_ids"`
}

func (in HostingStorageBackupDeletionPreviewInput) Validate() error {
	if !storageDeletionID.MatchString(in.RequestID) || in.ExpectedResourceGeneration < 1 || len(in.CopyIDs) == 0 || len(in.CopyIDs) > BackupRetentionBatchLimit {
		return ErrInvalidStorageBackupRetention
	}
	seen := map[string]bool{}
	for _, id := range in.CopyIDs {
		if !storageDeletionID.MatchString(id) || seen[id] {
			return ErrInvalidStorageBackupRetention
		}
		seen[id] = true
	}
	return nil
}

type HostingStorageBackupDeletionInput struct {
	RequestID        string `json:"request_id"`
	PreviewID        string `json:"preview_id"`
	ExpectedPlanHash string `json:"expected_plan_hash"`
	ConfirmFinalCopy bool   `json:"confirm_final_copy"`
}

func (in HostingStorageBackupDeletionInput) Validate() error {
	if !storageDeletionID.MatchString(in.RequestID) || !storageDeletionID.MatchString(in.PreviewID) || !imageSHA256.MatchString(in.ExpectedPlanHash) {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}
func ValidStorageBackupDeletionOperation(o HostingStorageBackupRetentionExecution) bool {
	if o.Version == HostingStorageBackupRetentionVersion {
		return o.Origin == "" && o.BackupID == "" && o.ResourceGeneration == 0 && !o.FinalCopy
	}
	if o.Version != HostingStorageBackupDeletionVersion || o.Origin != StorageBackupManualSelection || !validRuntimeReferenceID(o.BackupID) || o.ResourceGeneration < 1 || o.PolicyRevision != 0 {
		return false
	}
	for _, item := range o.Items {
		if item.BackupID != o.BackupID {
			return false
		}
	}
	return len(o.Items) > 0
}

// SelectStorageBackupDeletion resolves only explicit copy IDs from the complete
// server inventory. It never evaluates retention limits or silently drops a copy.
func SelectStorageBackupDeletion(backup string, ids []string, inventory []StorageBackupRetentionCopy) ([]StorageBackupRetentionCopy, bool, error) {
	if len(ids) == 0 || len(ids) > BackupRetentionBatchLimit {
		return nil, false, ErrInvalidStorageBackupRetention
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, false, ErrInvalidStorageBackupRetention
		}
		seen[id] = true
	}
	chosen := make([]StorageBackupRetentionCopy, 0, len(ids))
	remaining := 0
	for _, c := range inventory {
		if c.Item.BackupID != backup {
			continue
		}
		if slices.Contains(ids, c.Item.CopyID) {
			if c.Item.Availability != "available" || len(c.Item.ProtectedReasons) > 0 {
				return nil, false, ErrInvalidStorageBackupRetention
			}
			c.Item.Selected = true
			chosen = append(chosen, c)
		} else if c.Item.Availability == "available" {
			remaining++
		}
	}
	if len(chosen) != len(ids) || len(chosen) == 0 {
		return nil, false, ErrInvalidStorageBackupRetention
	}
	return chosen, remaining == 0, nil
}
