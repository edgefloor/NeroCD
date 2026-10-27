package runnerruntime

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func archiveAttemptInSnapshot(snapshot runner.JournalSnapshot, a runner.AttemptIdentity) bool {
	for _, e := range snapshot.StorageArchive {
		if sameJournalAttempt(e.Attempt, a) {
			return true
		}
	}
	for _, e := range snapshot.Hosting {
		if sameJournalAttempt(e.Attempt, a) && hostingEntryArchiveConflictInSnapshot(snapshot, e) {
			return true
		}
	}
	return false
}

func storageArchiveConflict(snapshot runner.JournalSnapshot, ref domain.HostingStorageRef, binding, operation, root string) bool {
	for _, e := range snapshot.StorageArchive {
		if storageArchiveAttemptPlanOnly(snapshot, e.Authority()) {
			continue
		}
		p := e.Plan
		if p.OperationID == operation || root != "" && (p.OperationID == root || p.Mode == "recover" && p.RecoveryOfOperationID == root) {
			continue
		}
		if p.SourceStorage.Reference() == ref && ref != (domain.HostingStorageRef{}) || p.SourceBindingID() == binding && binding != "" {
			return true
		}
		if p.Target != nil && (p.Target.Storage.Reference() == ref && ref != (domain.HostingStorageRef{}) || p.Target.Storage.BindingID == binding && binding != "") {
			return true
		}
	}
	return false
}

// Plan is persisted before read-only preparation. Exhaustively checking the
// whole attempt proves no mutation intent exists; its evidence remains durable
// and startup never continues it. Prepared or unknown phases remain guarded.
func storageArchiveAttemptPlanOnly(snapshot runner.JournalSnapshot, a domain.HostingAuthority) bool {
	found := false
	for _, e := range snapshot.StorageArchive {
		if e.Authority() != a {
			continue
		}
		found = true
		if e.Phase != "plan" || e.Plan.Validate() != nil || e.Prepared != nil || e.ApplyBegin != nil || e.Progress != nil || e.Complete != nil || e.StorageCreate != nil || e.StorageMaterialized != nil || e.StorageMetadata != nil || e.UncertainHelperContainerID != "" || e.RecoverySettlement != nil || e.RetirementReason != "" {
			return false
		}
	}
	return found
}

func storageArchivePreparedTerminal(j *runner.AttemptJournal, a domain.HostingAuthority, p domain.HostingStorageArchivePlan, in domain.HostingStorageArchivePreparedRequest, op domain.HostingOperation) bool {
	if p.Mode == "recover" || p.RecoveryEvidence != nil || !archivePlanMatches(op, a, p) || op.State != "canceled" && op.State != "failed" || op.MutationHold || op.RestoreOwnershipHeld || op.StorageArchive.PreparedReceipt == nil {
		return false
	}
	r := op.StorageArchive.PreparedReceipt
	if r.ID != in.ReceiptID || r.BodyHash != domain.HostingDocumentHash(in) || r.CreatedAt.IsZero() || in.HostingAuthority != a || in.PlanHash != p.Hash {
		return false
	}
	found := false
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() != a {
			continue
		}
		if domain.HostingDocumentHash(e.Plan) != domain.HostingDocumentHash(p) || e.Phase != "plan" && e.Phase != "prepared" || e.ApplyBegin != nil || e.Progress != nil || e.Complete != nil || e.StorageCreate != nil || e.StorageMaterialized != nil || e.StorageMetadata != nil || e.UncertainHelperContainerID != "" || e.RecoverySettlement != nil || e.RetirementReason != "" {
			return false
		}
		if e.Prepared != nil && domain.HostingDocumentHash(*e.Prepared) == domain.HostingDocumentHash(in) {
			found = true
		}
	}
	return found
}

func hostingEntryArchiveConflict(j *runner.AttemptJournal, e runner.JournalHosting) bool {
	return hostingEntryArchiveConflictInSnapshot(j.Snapshot(), e)
}

func hostingEntryArchiveConflictInSnapshot(snapshot runner.JournalSnapshot, e runner.JournalHosting) bool {
	ref, binding := e.StorageRef, ""
	if e.Plan != nil {
		ref, binding = e.Plan.Snapshot.Storage, e.Plan.Snapshot.BindingID
	}
	if e.CleanupPlan != nil {
		ref, binding = e.CleanupPlan.Target.Snapshot.Storage, e.CleanupPlan.Target.Snapshot.BindingID
	}
	return storageArchiveConflict(snapshot, ref, binding, e.OperationID, "")
}

func retireStorageArchiveAttempt(j *runner.AttemptJournal, a domain.HostingAuthority) error {
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() == a && e.Complete == nil {
			if err := j.AckStorageArchive(e.ID); err != nil {
				return err
			}
		}
	}
	// The saved completion is removed last, preserving replay across a crash.
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() == a {
			if err := j.AckStorageArchive(e.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func retireStorageArchiveRecovery(j *runner.AttemptJournal, a domain.HostingAuthority, p domain.HostingStorageArchivePlan, receipt domain.HostingReceipt) error {
	return retireStorageArchiveRecoveryWithAck(j, a, p, receipt, j.AckStorageArchive)
}

func retireStorageArchiveRecoveryWithAck(j *runner.AttemptJournal, a domain.HostingAuthority, p domain.HostingStorageArchivePlan, receipt domain.HostingReceipt, ack func(string) error) error {
	if p.Mode != "recover" || p.Target != nil || p.RecoveryEvidence == nil || p.Validate() != nil {
		return errStorageArchiveHeld
	}
	var complete *runner.JournalStorageArchive
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() == a && e.Complete != nil {
			copy := e
			complete = &copy
		}
	}
	if complete == nil || complete.Complete.Result.Uncertain || complete.Complete.Result.ResumeOutcome == "uncertain" || complete.Complete.Result.ArchiveOutcome != "not_attempted" || len(p.RecoveryEvidence.ResumeRequiredContainerIDs) > 0 && complete.Complete.Result.ResumeOutcome != "resumed" || receipt.ID != complete.Complete.ReceiptID || receipt.BodyHash != domain.HostingDocumentHash(*complete.Complete) || receipt.CreatedAt.IsZero() {
		return errStorageArchiveHeld
	}
	settlement := *complete
	settlement.ID = attemptMutationKey("archive", a.LeaseID, a.Attempt, "recovery_settlement")
	settlement.Phase = "recovery_settlement"
	settlement.Complete = nil
	settlement.RecoverySettlement = &receipt
	settlement.RetirementReason = "fresh_recovery_settled"
	for _, old := range j.Snapshot().StorageArchive {
		if old.ID == settlement.ID {
			settlement = old
		}
	}
	if _, err := j.AppendStorageArchive(settlement); err != nil {
		return err
	}
	var historical, settlements []string
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() == a {
			continue
		}
		q := e.Plan
		root := q.OperationID == p.RecoveryOfOperationID && q.Hash == p.RecoveryEvidence.OriginalPlanHash
		prior := q.Mode == "recover" && q.RecoveryOfOperationID == p.RecoveryOfOperationID && q.RecoveryEvidence != nil && q.RecoveryEvidence.OriginalPlanHash == p.RecoveryEvidence.OriginalPlanHash
		if !root && !prior {
			continue
		}
		if q.SourceIdentityHash() != p.SourceIdentityHash() || q.DestinationID != p.DestinationID || q.ServerID != p.ServerID || q.ProjectID != p.ProjectID || q.RunnerID != p.RunnerID || q.Helper != p.Helper || domain.HostingDocumentHash(q.SourceMaterialization) != domain.HostingDocumentHash(p.SourceMaterialization) || domain.HostingDocumentHash(q.Target) != domain.HostingDocumentHash(p.Target) {
			return errStorageArchiveHeld
		}
		if prior && (q.RecoveryEvidence.SnapshotReceiptID != p.RecoveryEvidence.SnapshotReceiptID || domain.HostingDocumentHash(q.RecoveryEvidence.Writers) != domain.HostingDocumentHash(p.RecoveryEvidence.Writers)) {
			return errStorageArchiveHeld
		}
		if e.Progress != nil && e.Progress.Phase == "writer_snapshot" && (e.Progress.ReceiptID != p.RecoveryEvidence.SnapshotReceiptID || domain.HostingDocumentHash(e.Progress.Writers) != domain.HostingDocumentHash(p.RecoveryEvidence.Writers)) {
			return errStorageArchiveHeld
		}
		if e.RecoverySettlement != nil {
			settlements = append(settlements, e.ID)
		} else {
			historical = append(historical, e.ID)
		}
	}
	// Prior settlement metadata refers to its completion, which must remain
	// until that metadata is removed. The current exact completion stays last.
	for _, id := range append(settlements, historical...) {
		if err := ack(id); err != nil {
			return err
		}
	}
	for _, e := range j.Snapshot().StorageArchive {
		if e.Authority() == a && e.Complete == nil {
			if err := ack(e.ID); err != nil {
				return err
			}
		}
	}
	return ack(complete.ID)
}

// Startup replays immutable evidence only. Even an active prior lease does not
// cause stop, helper start, extraction or writer restart here. Unsettled storage
// stays locally guarded while unrelated claims remain available.
func reconcileStorageArchiveJournal(parent context.Context, config Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().StorageArchive) == 0 {
		return nil
	}
	if config.StorageArchive == nil {
		return errors.New("storage archive journal requires explicit configuration")
	}
	parent = runnerclient.WithStorageArchiveTransport(parent, runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: config.StorageArchive.TrustedLoopbackHTTP})
	c := runnerclient.New(config.Server, config.Token)
	seen := map[domain.HostingAuthority]bool{}
	ordered := j.Snapshot().StorageArchive
	settledRecovery := map[domain.HostingAuthority]bool{}
	for _, e := range ordered {
		if e.Plan.Mode == "recover" && e.Complete != nil && !e.Complete.Result.Uncertain {
			settledRecovery[e.Authority()] = true
		}
	}
	slices.SortStableFunc(ordered, func(left, right runner.JournalStorageArchive) int {
		if settledRecovery[left.Authority()] == settledRecovery[right.Authority()] {
			return 0
		}
		if settledRecovery[left.Authority()] {
			return -1
		}
		return 1
	})
	for _, first := range ordered {
		a := first.Authority()
		if seen[a] {
			continue
		}
		seen[a] = true
		if first.Plan.RunnerID != config.RunnerID || first.Plan.Helper != config.StorageArchive.Helper {
			return errors.New("storage archive journal local identity changed")
		}
		var entries []runner.JournalStorageArchive
		var complete *runner.JournalStorageArchive
		for _, e := range j.Snapshot().StorageArchive {
			if e.Authority() == a {
				entries = append(entries, e)
				if e.Complete != nil {
					copy := e
					complete = &copy
				}
			}
		}
		if len(entries) == 0 {
			continue
		}
		// Saved completion wins over every earlier intent. A lost ACK never
		// executes the helper or restores a second time.
		if complete != nil {
			if err := replayHostingEvents(config.Server, config.Token, j, first.Attempt, parent); err != nil {
				if runnerHTTPStatus(err, http.StatusConflict) || runnerHTTPStatus(err, http.StatusNotFound) {
					continue
				}
				return err
			}
			ctx, cancel := context.WithTimeout(parent, 5*time.Second)
			op, err := c.HostingStorageArchiveComplete(ctx, *complete.Complete)
			cancel()
			if runnerHTTPStatus(err, http.StatusConflict) || runnerHTTPStatus(err, http.StatusNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if !archivePlanMatches(op, a, first.Plan) {
				return errors.New("storage archive replay acknowledgement changed")
			}
			if !complete.Complete.Result.Uncertain && !op.MutationHold && !op.RestoreOwnershipHeld {
				if first.Plan.Mode == "recover" {
					err = retireStorageArchiveRecovery(j, a, first.Plan, *op.CompleteReceipt)
				} else {
					err = retireStorageArchiveAttempt(j, a)
				}
				if err != nil {
					return err
				}
			}
			continue
		}
		for _, e := range entries {
			ctx, cancel := context.WithTimeout(parent, 5*time.Second)
			var err error
			retired := false
			switch {
			case e.Prepared != nil:
				var op domain.HostingOperation
				op, err = c.HostingStorageArchivePrepared(ctx, *e.Prepared)
				if err == nil && storageArchivePreparedTerminal(j, a, first.Plan, *e.Prepared, op) {
					if err = retireStorageArchiveAttempt(j, a); err != nil {
						cancel()
						return err
					}
					retired = true
				}
			case e.Progress != nil:
				_, err = c.HostingStorageArchiveProgress(ctx, *e.Progress)
			case e.StorageMaterialized != nil:
				var ack domain.HostingStorageMaterializedAck
				ack, err = c.HostingStorageMaterialized(ctx, *e.StorageMaterialized)
				if err == nil && (ack.Receipt.ID != e.StorageMaterialized.ReceiptID || ack.Receipt.BodyHash != domain.HostingDocumentHash(*e.StorageMaterialized) || e.StorageMaterialized.Matches(*e.StorageMetadata, ack.Materialization) != nil) {
					err = errors.New("storage archive materialization replay changed")
				}
			}
			cancel()
			if retired {
				break
			}
			if runnerHTTPStatus(err, http.StatusConflict) || runnerHTTPStatus(err, http.StatusNotFound) {
				break
			}
			if err != nil {
				return err
			}
		}
	}
	return parent.Err()
}

func storageArchiveRecoverySource(snapshot runner.JournalSnapshot, p domain.HostingStorageArchivePlan) (domain.HostingStorageArchivePlan, []runner.JournalStorageArchive, error) {
	if p.Mode != "recover" || p.RecoveryEvidence == nil || p.Validate() != nil {
		return domain.HostingStorageArchivePlan{}, nil, errStorageArchiveHeld
	}
	var original domain.HostingStorageArchivePlan
	var entries []runner.JournalStorageArchive
	for _, e := range snapshot.StorageArchive {
		if e.Plan.OperationID == p.RecoveryOfOperationID && e.Plan.Hash == p.RecoveryEvidence.OriginalPlanHash {
			original = e.Plan
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 || original.Mode == "recover" || original.SourceIdentityHash() != p.SourceIdentityHash() || domain.HostingDocumentHash(original.SourceMaterialization) != domain.HostingDocumentHash(p.SourceMaterialization) || original.Helper != p.Helper || domain.HostingDocumentHash(original.Target) != domain.HostingDocumentHash(p.Target) || domain.HostingDocumentHash(original.Blob) != domain.HostingDocumentHash(p.Blob) || original.BackupID != p.BackupID || original.DestinationID != p.DestinationID || original.RunnerID != p.RunnerID {
		return original, nil, errStorageArchiveHeld
	}
	stops := map[string]bool{}
	snapshotMatched := len(p.RecoveryEvidence.Writers) == 0
	intent, identified := false, false
	helperID := ""
	for _, e := range entries {
		if e.UncertainHelperContainerID != "" {
			identified = true
			if helperID != "" && helperID != e.UncertainHelperContainerID {
				return original, nil, errStorageArchiveHeld
			}
			helperID = e.UncertainHelperContainerID
		}
		if e.Progress == nil {
			continue
		}
		r := e.Progress
		switch r.Phase {
		case "writer_snapshot":
			snapshotMatched = r.ReceiptID == p.RecoveryEvidence.SnapshotReceiptID && domain.HostingDocumentHash(r.Writers) == domain.HostingDocumentHash(p.RecoveryEvidence.Writers)
		case "stop_intent":
			for _, w := range r.Writers {
				if w.Running {
					stops[w.ContainerID] = true
				}
			}
		case "helper_intent":
			intent = true
		case "helper_identified":
			identified = true
			if helperID != "" && helperID != r.HelperContainerID {
				return original, nil, errStorageArchiveHeld
			}
			helperID = r.HelperContainerID
		}
	}
	if !snapshotMatched || identified && !intent {
		return original, nil, errStorageArchiveHeld
	}
	for _, id := range p.RecoveryEvidence.ResumeRequiredContainerIDs {
		if !stops[id] {
			return original, nil, errStorageArchiveHeld
		}
		// Durable resumed proof from the root or a prior recovery cannot be
		// widened back into a later controller-derived obligation.
		for _, e := range snapshot.StorageArchive {
			if e.Plan.OperationID != original.OperationID && (e.Plan.Mode != "recover" || e.Plan.RecoveryOfOperationID != original.OperationID || e.Plan.RecoveryEvidence == nil || e.Plan.RecoveryEvidence.OriginalPlanHash != original.Hash) {
				continue
			}
			if e.Progress != nil && e.Progress.Phase == "resumed" {
				// Recovery receipts carry the full original snapshot, but prove
				// restart only for that recovery's narrowed authorized subset.
				if e.Plan.Mode == "recover" && !slices.Contains(e.Plan.RecoveryEvidence.ResumeRequiredContainerIDs, id) {
					continue
				}
				for _, w := range e.Progress.Writers {
					if w.ContainerID == id {
						return original, nil, errStorageArchiveHeld
					}
				}
			}
		}
	}
	// Compare all original running observations, including originally stopped
	// writers, instead of accepting only the requested restart subset.
	if len(original.Writers) != len(p.Writers) || !slices.EqualFunc(original.Writers, p.Writers, func(a, b domain.HostingHealthy) bool {
		return domain.HostingDocumentHash(a) == domain.HostingDocumentHash(b)
	}) {
		return original, nil, errStorageArchiveHeld
	}
	return original, entries, nil
}
