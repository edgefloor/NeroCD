package runner

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"nerocd/internal/domain"
)

// HostingJournalUsesDatabase detects database evidence at every retained level.
// The marker and reference also protect receipts without embedded snapshots.
func HostingJournalUsesDatabase(entry JournalHosting) bool {
	if entry.DatabaseMaintenance != nil || entry.DatabaseUpgrade != nil || entry.Database || entry.DatabaseRef != (domain.HostingDatabaseExecutionRef{}) || entry.Plan != nil && (entry.Plan.Database != nil || entry.Plan.CompilerVersion == domain.HostingDatabaseCompilerVersion) {
		return true
	}
	for _, snapshot := range hostingJournalSnapshots(entry) {
		if snapshot.Database != (domain.HostingDatabaseExecutionRef{}) || snapshot.PolicyVersion == domain.HostingDatabasePolicyVersion {
			return true
		}
	}
	return false
}

func journalStateUsesDatabase(state journalState) bool {
	for _, entry := range state.Hosting {
		if HostingJournalUsesDatabase(entry) {
			return true
		}
	}
	return false
}

func validateHostingJournalDatabase(entry JournalHosting) error {
	if !HostingJournalUsesDatabase(entry) {
		return nil
	}
	ref := entry.DatabaseRef
	if !entry.Database || entry.LocalBuild || entry.RuntimeEnvironment || HostingJournalUsesLocalBuild(entry) || HostingJournalUsesRuntimeEnvironment(entry) || entry.CleanupPlan != nil || entry.StorageRef == (domain.HostingStorageRef{}) {
		return errors.New("journal database requires an unmixed marked storage family")
	}
	if _, err := domain.HostingDatabaseRecipeFor(ref.Engine, ref.RecipeVersion); err != nil || !hostingVolumeHashForJournal(ref.ConfigurationHash) {
		return errors.New("journal database requires exact configuration reference")
	}
	for _, snapshot := range hostingJournalSnapshots(entry) {
		if snapshot.Database != ref || domain.ValidateHostingDatabaseSnapshot(snapshot) != nil || snapshot.Storage != entry.StorageRef {
			return errors.New("journal database snapshot family mismatch")
		}
	}
	if entry.DatabaseMaintenance != nil {
		if err := validateDatabaseMaintenanceJournal(entry); err != nil {
			return err
		}
	} else if entry.DatabaseUpgrade != nil {
		proof := entry.DatabaseUpgrade
		if proof.Validate() != nil || proof.PreviousPlan.Snapshot.Database != ref || proof.PreviousPlan.Snapshot.Storage != entry.StorageRef || entry.OperationID == proof.PreviousHealthy.OperationID {
			return errors.New("journal database upgrade proof mismatch")
		}
		op := domain.HostingOperation{ID: entry.OperationID, ProjectID: proof.CapturePlan.ProjectID, ResourceID: proof.CapturePlan.ResourceID, BindingID: proof.CapturePlan.BindingID, Snapshot: proof.PreviousPlan.Snapshot, DatabaseUpgrade: proof, Plan: entry.Plan}
		if entry.Plan != nil {
			op.Release = &entry.Plan.Release
		}
		if domain.ValidateHostingDatabaseUpgradeOperation(op) != nil || entry.Prepared != nil && entry.Prepared.Release != proof.PreviousPlan.Release {
			return errors.New("journal database upgrade document mismatch")
		}
	} else if entry.Plan != nil && domain.ValidateHostingDatabasePlan(*entry.Plan) != nil {
		return errors.New("journal database plan mismatch")
	}
	if entry.Prepared != nil {
		recipe, _ := domain.HostingDatabaseRecipeFor(ref.Engine, ref.RecipeVersion)
		r := entry.Prepared.Release
		match := false
		for _, platform := range recipe.Platforms {
			match = match || r.OS == platform.OS && r.Architecture == platform.Architecture && r.Variant == platform.Variant && r.CanonicalImage == strings.Split(recipe.ImageReference, "@")[0]+"@"+platform.Digest
		}
		if !match || r.LocalBuild != (domain.HostingBuildArtifact{}) {
			return errors.New("journal database preparation recipe mismatch")
		}
	}
	if entry.Phase == "apply_intent" {
		decoded, err := hex.DecodeString(entry.DeliveryID)
		if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != entry.DeliveryID {
			return errors.New("journal database apply intent requires original delivery id")
		}
	} else if entry.DeliveryID != "" {
		return errors.New("journal database delivery requires apply intent")
	}
	return nil
}

func hostingVolumeHashForJournal(hash string) bool {
	if len(hash) != 71 || hash[:7] != "sha256:" {
		return false
	}
	raw, err := hex.DecodeString(hash[7:])
	return err == nil && hex.EncodeToString(raw) == hash[7:]
}

// Strict decoding also rejects duplicate and case-aliased members before Go's
// struct decoder can collapse contradictory evidence.
func decodeDatabaseJournal(raw []byte, state *journalState) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if !databaseJournalUnique(d) {
		return errors.New("invalid database journal document")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid database journal document")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(state) != nil {
		return errors.New("invalid database journal document")
	}
	return nil
}
func databaseJournalUnique(d *json.Decoder) bool { return databaseJournalUniqueValue(d, false) }

// Saved Compose environment keys are case-sensitive; protocol members stay canonical.
func databaseJournalUniqueValue(d *json.Decoder, environment bool) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || !environment && key != strings.ToLower(key) || seen[key] {
				return false
			}
			seen[key] = true
			if !databaseJournalUniqueValue(d, !environment && key == "environment") {
				return false
			}
		}
	case '[':
		for d.More() {
			if !databaseJournalUniqueValue(d, false) {
				return false
			}
		}
	default:
		return false
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

func journalStateUsesDatabaseUpgrade(state journalState) bool {
	for _, entry := range state.Hosting {
		if entry.DatabaseUpgrade != nil {
			return true
		}
	}
	return false
}

func validateDatabaseUpgradeJournalChain(entries []JournalHosting) error {
	type identity struct {
		operation, run, lease, fence string
		attempt                      int
	}
	proofs := map[identity]string{}
	for _, entry := range entries {
		key := identity{entry.OperationID, entry.Attempt.RunID, entry.Attempt.LeaseID, entry.Attempt.Fence, entry.Attempt.Attempt}
		hash := ""
		if entry.DatabaseUpgrade != nil {
			hash = domain.HostingDocumentHash(entry.DatabaseUpgrade)
		}
		if prior, ok := proofs[key]; ok && prior != hash {
			return errors.New("database journal upgrade proof changed between phases")
		}
		proofs[key] = hash
	}
	return nil
}
