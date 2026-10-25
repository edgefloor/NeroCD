package runner

import (
	"bytes"
	"encoding/json"
	"errors"

	"nerocd/internal/domain"
)

func decodeStorageJournal(raw []byte, state *journalState) error {
	if state.Version == 11 || state.Version == 14 || state.Version == 15 || state.Version == 16 {
		return decodeDatabaseJournal(raw, state)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(state)
}

// HostingJournalUsesStorage finds storage evidence even when a marker is omitted
// or the current snapshot is stateless. It grants no creation permission.
func HostingJournalUsesStorage(entry JournalHosting) bool {
	if entry.StorageRef != (domain.HostingStorageRef{}) || entry.StorageCreate != nil || entry.StorageMaterialized != nil || entry.StorageMetadata != nil || entry.Phase == "storage_create_intent" || entry.Phase == "storage_materialized" {
		return true
	}
	if entry.Plan != nil {
		if entry.Plan.Storage != nil || entry.Plan.CompilerVersion == domain.HostingOwnedStorageCompilerVersion || entry.Plan.PreviousHealthy != nil && entry.Plan.PreviousHealthy.Storage != nil {
			return true
		}
		for _, healthy := range entry.Plan.RetainedReleases {
			if healthy.Storage != nil {
				return true
			}
		}
	}
	if entry.CleanupPlan != nil && entry.CleanupPlan.Target.Storage != nil {
		return true
	}
	for _, snapshot := range hostingJournalSnapshots(entry) {
		if snapshot.Storage != (domain.HostingStorageRef{}) || snapshot.PolicyVersion == domain.HostingOwnedStoragePolicyVersion {
			return true
		}
	}
	return false
}

func journalStateUsesStorage(state journalState) bool {
	for _, entry := range state.Hosting {
		if HostingJournalUsesStorage(entry) {
			return true
		}
	}
	return false
}

func validateHostingJournalStorage(entry JournalHosting) error {
	for _, snapshot := range hostingJournalSnapshots(entry) {
		if domain.ValidateHostingStorageSnapshot(snapshot) != nil {
			return errors.New("invalid journal storage snapshot")
		}
	}
	if !HostingJournalUsesStorage(entry) {
		return nil
	}
	ref := entry.StorageRef
	if domain.ValidateHostingStorageSnapshot(domain.HostingSnapshot{Storage: ref, PolicyVersion: domain.HostingOwnedStoragePolicyVersion}) != nil {
		return errors.New("journal storage reference missing or invalid")
	}
	if entry.RuntimeEnvironment || entry.LocalBuild || HostingJournalUsesRuntimeEnvironment(entry) || HostingJournalUsesLocalBuild(entry) {
		return errors.New("journal storage cannot mix unsupported execution families")
	}
	if entry.Plan != nil {
		plan := *entry.Plan
		hash := plan.Hash
		plan.Hash = ""
		familyMatches := domain.HostingPlanFamilyMatches(entry.Plan.Snapshot, *entry.Plan)
		if entry.DatabaseUpgrade != nil || entry.DatabaseMaintenance != nil {
			familyMatches = validateHostingJournalDatabase(entry) == nil
		}
		if hash != domain.HostingDocumentHash(plan) || !familyMatches || plan.Snapshot.Storage != ref || plan.Storage == nil {
			return errors.New("journal storage plan identity mismatch")
		}
	}
	if entry.CleanupPlan != nil && (entry.CleanupPlan.Target.Snapshot.Storage != ref || domain.ValidateHostingHealthyStorage(entry.CleanupPlan.Target) != nil) {
		return errors.New("journal storage cleanup identity mismatch")
	}
	if entry.Phase != "storage_create_intent" && entry.StorageCreate != nil || entry.Phase != "storage_materialized" && (entry.StorageMaterialized != nil || entry.StorageMetadata != nil) {
		return errors.New("journal storage document in wrong phase")
	}
	if entry.Phase != "storage_create_intent" && entry.Phase != "storage_materialized" {
		return nil
	}
	if entry.Plan == nil || entry.Prepared != nil || entry.Result != nil || entry.CleanupPlan != nil {
		return errors.New("journal storage phase requires only its immutable plan and request")
	}
	plan := entry.Plan
	authority := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
	if entry.Phase == "storage_create_intent" {
		in := entry.StorageCreate
		if in == nil || in.Validate() != nil || in.HostingAuthority != authority || in.StorageRef != ref || in.PlanHash != plan.Hash || in.CreationNonce != plan.Storage.CreationNonce {
			return errors.New("invalid journal storage creation intent")
		}
		return nil
	}
	in := entry.StorageMaterialized
	if in == nil || in.Validate() != nil || in.HostingAuthority != authority || in.StorageRef != ref || in.PlanHash != plan.Hash || in.CreationNonce != plan.Storage.CreationNonce || in.CreationOperationID != plan.Storage.CreationOperationID || in.DaemonID != plan.Storage.DaemonID || in.VolumeName != plan.Storage.VolumeName || in.CreationOperationID == entry.OperationID && in.CreationPlanHash != plan.Hash {
		return errors.New("invalid journal storage materialization evidence")
	}
	storage := entry.StorageMetadata
	origin := domain.HostingStorageMaterialization{StorageRef: ref, State: "create_admitted", CreationOperationID: in.CreationOperationID, CreationPlanHash: in.CreationPlanHash, CreationNonce: in.CreationNonce, DaemonID: in.DaemonID, Labels: in.Labels}
	if storage == nil || storage.ResourceID != plan.Snapshot.ResourceID || storage.BindingID != plan.Snapshot.BindingID || storage.DestinationID != plan.Snapshot.DestinationID || storage.MountPath != plan.Storage.MountPath || in.Matches(*storage, origin) != nil {
		return errors.New("journal storage metadata differs from frozen identity")
	}
	return nil
}
