package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// Lease rows and actor roles remain locked. This final database-clock query
// checks expiry only after all fallible reads/writes and their possible waits.
func archiveAuthorityClock(ctx context.Context, tx pgx.Tx, actor, lease string, active bool) error {
	valid, err := sqlcgen.New(tx).CheckStorageArchiveAuthorityClock(ctx, sqlcgen.CheckStorageArchiveAuthorityClockParams{ActorID: actor, LeaseID: lease, RequireActive: active})
	if err != nil {
		return err
	}
	if !valid.ActorValid {
		return auth.ErrForbidden
	}
	if !valid.LeaseValid {
		return ErrConflict
	}
	return nil
}

func archiveFinalizeLease(ctx context.Context, tx pgx.Tx, actor, lease string, active bool) error {
	if err := sqlcgen.New(tx).FlushStorageArchiveConstraints(ctx); err != nil {
		return err
	}
	return archiveAuthorityClock(ctx, tx, actor, lease, active)
}

func (s *PostgresStore) archiveAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if a.Attempt < 1 || a.Attempt > math.MaxInt32 {
		return zero, ErrConflict
	}
	q := sqlcgen.New(tx)
	if err := storageArchiveSchema(ctx, q); err != nil {
		return zero, err
	}
	if _, err := q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
		return zero, hostingError(err)
	}
	op, found, err := archiveOperation(ctx, tx, a.OperationID)
	if err != nil {
		return zero, err
	}
	if !found || op.RunID != a.RunID || op.StorageArchive.Plan.RunnerID != runner {
		return zero, ErrConflict
	}
	if err = s.archiveGate(ctx, tx, op); err != nil {
		return zero, err
	}
	if _, err = q.CheckStorageArchiveAuthority(ctx, sqlcgen.CheckStorageArchiveAuthorityParams{ID: a.LeaseID, RunID: a.RunID, RunnerID: runner, Attempt: int32(a.Attempt), Fence: a.Fence, OperationID: a.OperationID, RequireLive: live}); err != nil {
		return zero, hostingError(err)
	}
	if live {
		return op, archiveAuthorityClock(ctx, tx, op.CreatedBy, a.LeaseID, true)
	}
	return op, proxyCheckLegacyToken(ctx, tx, op.CreatedBy)
}

func archiveReceiptReplay(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, receipt string, body any) (bool, error) {
	prior, err := sqlcgen.New(tx).ReadStorageArchiveReceipt(ctx, sqlcgen.ReadStorageArchiveReceiptParams{OperationID: a.OperationID, Phase: phase})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var old domain.HostingAuthority
	if json.Unmarshal(prior.Authority, &old) != nil || old != a || prior.ReceiptID != receipt || prior.BodyHash != domain.HostingDocumentHash(body) {
		return false, ErrConflict
	}
	return true, nil
}

func archiveWriteReceipt(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, a domain.HostingAuthority, runner, phase, receipt string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err = archiveAudit(ctx, tx, runner, phase, op); err != nil {
		return err
	}
	_, err = sqlcgen.New(tx).InsertStorageArchiveReceipt(ctx, sqlcgen.InsertStorageArchiveReceiptParams{OperationID: op.ID, Phase: phase, ReceiptID: receipt, Authority: authority, Body: raw, BodyHash: domain.HostingDocumentHash(body), PlanHash: op.StorageArchive.Plan.Hash, AuditID: "aud_archive_" + phase + "_" + op.ID})
	return err
}

func (s *PostgresStore) archiveMutation(ctx context.Context, runner string, a domain.HostingAuthority, phase, receipt, planHash string, body any, mutate func(pgx.Tx, domain.HostingOperation) error) (domain.HostingOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		zero := domain.HostingOperation{}
		// Current actor/grant/helper/target authority is checked before historical
		// receipt replay. A historical ACK never grants fresh effect permission.
		op, err := s.archiveAuthority(ctx, tx, runner, a, false)
		if err != nil {
			return zero, err
		}
		if op.StorageArchive.Plan.Hash != planHash {
			return zero, ErrConflict
		}
		replay, err := archiveReceiptReplay(ctx, tx, a, phase, receipt, body)
		if err != nil {
			return zero, err
		}
		if replay {
			// A historical receipt is an ACK, not renewed effect permission.
			return op, archiveFinalizeActor(ctx, tx, op.CreatedBy)
		}
		if _, err = s.archiveAuthority(ctx, tx, runner, a, true); err != nil {
			return zero, err
		}
		if op.State == "cancel_requested" || op.State == "reconciliation_required" || op.State == "canceled" || op.State == "succeeded" || op.State == "failed" {
			return zero, ErrConflict
		}
		if err = mutate(tx, op); err != nil {
			return zero, err
		}
		if err = archiveWriteReceipt(ctx, tx, op, a, runner, phase, receipt, body); err != nil {
			return zero, err
		}
		// Recheck the fenced lease after all fallible audit/receipt writes and lock waits.
		if _, err = s.archiveAuthority(ctx, tx, runner, a, phase != "complete"); err != nil {
			return zero, err
		}
		updated, _, err := archiveOperation(ctx, tx, op.ID)
		if err != nil {
			return zero, err
		}
		return updated, archiveFinalizeLease(ctx, tx, op.CreatedBy, a.LeaseID, phase != "complete")
	})
}

// StorageArchivePlan returns only current live exact-target archive authority.
func (s *PostgresStore) StorageArchivePlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		op, err := s.archiveAuthority(ctx, tx, runner, a, true)
		if err != nil {
			return domain.HostingOperation{}, err
		}
		return op, archiveFinalizeLease(ctx, tx, op.CreatedBy, a.LeaseID, true)
	})
}

// StorageArchivePrepared confirms the independently configured helper identity.
func (s *PostgresStore) StorageArchivePrepared(ctx context.Context, runner string, in domain.HostingStorageArchivePreparedRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return s.archiveMutation(ctx, runner, in.HostingAuthority, "prepared", in.ReceiptID, in.PlanHash, in, func(tx pgx.Tx, op domain.HostingOperation) error {
		if in.Helper != op.StorageArchive.Plan.Helper || op.State != "dispatched" && op.State != "preparing" {
			return ErrConflict
		}
		_, err := sqlcgen.New(tx).SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: "prepared"})
		return err
	})
}

// StorageArchiveApplyBegin admits only archive effects, never application start.
func (s *PostgresStore) StorageArchiveApplyBegin(ctx context.Context, runner string, in domain.HostingStorageArchiveApplyBeginRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return s.archiveMutation(ctx, runner, in.HostingAuthority, "apply_begin", in.ReceiptID, in.PlanHash, in, func(tx pgx.Tx, op domain.HostingOperation) error {
		if op.State != "prepared" || op.StorageArchive.PreparedReceipt == nil {
			return ErrConflict
		}
		p := op.StorageArchive.Plan
		state, err := sqlcgen.New(tx).LockHostingPortReservation(ctx, op.BindingID)
		if err != nil {
			return err
		}
		if state.HoldOperationID == nil || *state.HoldOperationID != op.ID {
			return ErrConflict
		}
		if p.Mode == "restore" {
			m, err := readArchiveRestoreMaterialization(ctx, tx, p.Target.Storage)
			if err != nil || m.State != "materialized" {
				return ErrConflict
			}
		}
		_, err = sqlcgen.New(tx).SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: "applying"})
		return err
	})
}

func archiveProgressByPhase(op domain.HostingOperation, phase string) *domain.HostingStorageArchiveProgressEvidence {
	for i := range op.StorageArchive.Progress {
		if op.StorageArchive.Progress[i].Phase == phase {
			return &op.StorageArchive.Progress[i]
		}
	}
	return nil
}
func archiveWriterStatesMatch(writers []domain.HostingStorageArchiveWriterState, eligible []domain.HostingHealthy) bool {
	if len(writers) != len(eligible) {
		return false
	}
	for i, w := range writers {
		h := eligible[i]
		if w.ContainerID != h.ContainerID || w.OperationID != h.OperationID || w.ImageID != h.Release.ImageID || w.PlanHash != h.PlanHash || w.ReleaseHash != h.Release.Hash {
			return false
		}
	}
	return true
}
func archiveBlobMatchesPlan(blob *domain.HostingStorageArchiveBlob, p domain.HostingStorageArchivePlan) bool {
	return blob != nil && blob.Validate() == nil && blob.BackupID == p.BackupID && blob.DestinationID == p.DestinationID && blob.DaemonID == p.Helper.DaemonID && blob.SizeBytes <= p.Limits.ArchiveBytes
}

func validateArchiveProgress(op domain.HostingOperation, in domain.HostingStorageArchiveProgress) error {
	p := op.StorageArchive.Plan
	if op.State != "applying" || in.PlanHash != p.Hash {
		return ErrConflict
	}
	previous := ""
	if n := len(op.StorageArchive.Progress); n > 0 {
		previous = op.StorageArchive.Progress[n-1].Phase
	}
	snapshot := archiveProgressByPhase(op, "writer_snapshot")
	if p.Mode == "recover" {
		if in.HelperContainerID != "" {
			return ErrConflict
		}
		switch in.Phase {
		case "resume_intent":
			if previous != "" || p.Target != nil || p.RecoveryEvidence == nil || len(p.RecoveryEvidence.ResumeRequiredContainerIDs) == 0 || !reflect.DeepEqual(in.Writers, p.RecoveryEvidence.Writers) || in.Blob != nil || in.RestoreVerified {
				return ErrConflict
			}
		case "resumed":
			if previous != "resume_intent" || !reflect.DeepEqual(in.Writers, p.RecoveryEvidence.Writers) || in.Blob != nil || in.RestoreVerified {
				return ErrConflict
			}
		case "restore_verified":
			if previous != "" || p.Target == nil || len(in.Writers) != 0 || !in.RestoreVerified || !reflect.DeepEqual(in.Blob, p.Blob) {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
		return nil
	}
	switch in.Phase {
	case "writer_snapshot":
		if previous != "" || p.Mode != "backup" || !archiveWriterStatesMatch(in.Writers, p.Writers) {
			return ErrConflict
		}
	case "stop_intent":
		if previous != "writer_snapshot" || snapshot == nil || !reflect.DeepEqual(in.Writers, snapshot.Writers) {
			return ErrConflict
		}
	case "stopped":
		if previous != "stop_intent" || !archiveWriterStatesMatch(in.Writers, p.Writers) {
			return ErrConflict
		}
		for _, w := range in.Writers {
			if w.Running {
				return ErrConflict
			}
		}
	case "helper_intent":
		if len(in.Writers) != 0 || p.Mode == "backup" && previous != "stopped" || p.Mode == "restore" && previous != "" {
			return ErrConflict
		}
	case "helper_identified":
		if previous != "helper_intent" || len(in.Writers) != 0 || len(in.HelperContainerID) != 64 || strings.Trim(in.HelperContainerID, "0123456789abcdef") != "" {
			return ErrConflict
		}
	case "archive_published":
		if p.Mode != "backup" || previous != "helper_identified" || len(in.Writers) != 0 || !archiveBlobMatchesPlan(in.Blob, p) {
			return ErrConflict
		}
	case "restore_verified":
		if p.Mode != "restore" || previous != "helper_identified" || len(in.Writers) != 0 || !in.RestoreVerified || !reflect.DeepEqual(in.Blob, p.Blob) {
			return ErrConflict
		}
	case "resume_intent":
		if p.Mode != "backup" || snapshot == nil || !slices.Contains([]string{"stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published"}, previous) || !reflect.DeepEqual(in.Writers, snapshot.Writers) {
			return ErrConflict
		}
	case "resumed":
		if p.Mode != "backup" || previous != "resume_intent" || snapshot == nil || !reflect.DeepEqual(in.Writers, snapshot.Writers) {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	if in.Phase != "helper_identified" && in.HelperContainerID != "" {
		return ErrConflict
	}
	if in.Phase != "archive_published" && in.Phase != "restore_verified" && in.Blob != nil {
		return ErrConflict
	}
	if in.Phase != "restore_verified" && in.RestoreVerified {
		return ErrConflict
	}
	return nil
}

// StorageArchiveProgress commits irreversible phase history before the next effect.
func (s *PostgresStore) StorageArchiveProgress(ctx context.Context, runner string, in domain.HostingStorageArchiveProgress) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return s.archiveMutation(ctx, runner, in.HostingAuthority, in.Phase, in.ReceiptID, in.PlanHash, in, func(tx pgx.Tx, op domain.HostingOperation) error {
		if err := validateArchiveProgress(op, in); err != nil {
			return err
		}
		if in.Phase == "archive_published" {
			raw, err := json.Marshal(in.Blob)
			if err != nil {
				return err
			}
			return sqlcgen.New(tx).InsertStorageArchiveBlob(ctx, sqlcgen.InsertStorageArchiveBlobParams{BackupID: op.StorageArchive.BackupID, OperationID: op.ID, Body: raw})
		}
		return nil
	})
}

func archiveResumeProven(op domain.HostingOperation, result domain.HostingStorageArchiveResult) bool {
	p := op.StorageArchive.Plan
	if p.Target != nil {
		return result.ResumeOutcome == "not_needed"
	}
	if p.Mode == "recover" {
		if len(p.RecoveryEvidence.ResumeRequiredContainerIDs) == 0 {
			return result.ResumeOutcome == "not_needed"
		}
		return result.ResumeOutcome == "resumed" && archiveProgressByPhase(op, "resumed") != nil
	}
	snapshot := archiveProgressByPhase(op, "writer_snapshot")
	if archiveProgressByPhase(op, "stop_intent") == nil {
		return result.ResumeOutcome == "not_needed"
	}
	if snapshot == nil {
		return false
	}
	running := false
	for _, w := range snapshot.Writers {
		running = running || w.Running
	}
	if !running {
		return result.ResumeOutcome == "not_needed"
	}
	return result.ResumeOutcome == "resumed" && archiveProgressByPhase(op, "resumed") != nil
}

// StorageArchiveComplete settles only the independent archive outcome. Mutation
// holds survive any missing resumption or uncertain new-target verification.
func (s *PostgresStore) StorageArchiveComplete(ctx context.Context, runner string, in domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return s.archiveMutation(ctx, runner, in.HostingAuthority, "complete", in.ReceiptID, in.PlanHash, in, func(tx pgx.Tx, op domain.HostingOperation) error {
		p := op.StorageArchive.Plan
		r := in.Result
		if op.State != "applying" && op.State != "prepared" && op.State != "dispatched" {
			return ErrConflict
		}
		resume := archiveResumeProven(op, r)
		if !resume && (!r.Uncertain || r.ResumeOutcome != "uncertain") {
			return ErrConflict
		}
		if r.Uncertain && r.ArchiveOutcome == "succeeded" {
			return ErrConflict
		}
		if p.Mode == "recover" {
			if r.ArchiveOutcome != "not_attempted" || r.Blob != nil {
				return ErrConflict
			}
			if p.Target != nil && r.RestoreVerified && archiveProgressByPhase(op, "restore_verified") == nil {
				return ErrConflict
			}
		} else if r.ArchiveOutcome == "succeeded" {
			if r.FailureCode != "" || !resume {
				return ErrConflict
			}
			phase := "archive_published"
			if p.Target != nil {
				phase = "restore_verified"
			}
			evidence := archiveProgressByPhase(op, phase)
			if evidence == nil || !reflect.DeepEqual(r.Blob, evidence.Blob) || r.RestoreVerified != (p.Target != nil) {
				return ErrConflict
			}
		} else if r.ArchiveOutcome != "failed" || r.FailureCode == "" || r.Blob != nil || r.RestoreVerified {
			return ErrConflict
		}
		clearHold := resume && !r.Uncertain && (p.Target == nil || r.RestoreVerified)
		q := sqlcgen.New(tx)
		if clearHold {
			if p.Target != nil {
				if err := q.ResolveStorageArchiveTargetHold(ctx, sqlcgen.ResolveStorageArchiveTargetHoldParams{BindingID: op.BindingID, OperationID: op.ID}); err != nil {
					return err
				}
			}
			if _, err := q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); err != nil {
				return err
			}
		}
		state := "failed"
		leaseState := domain.RunFailed
		if r.Uncertain || !resume {
			state = "reconciliation_required"
		}
		if p.Mode == "recover" && clearHold || r.ArchiveOutcome == "succeeded" {
			state = "succeeded"
			leaseState = domain.RunSucceeded
		}
		if _, err := q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
			return err
		}
		if p.Mode == "recover" && clearHold {
			originalState := "failed"
			published, err := q.StorageArchiveHasPublishedBlob(ctx, p.RecoveryOfOperationID)
			if err != nil {
				return err
			}
			if published || p.Target != nil && r.RestoreVerified {
				originalState = "succeeded"
			}
			if _, err := q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: p.RecoveryOfOperationID, State: originalState}); err != nil {
				return err
			}
			if published && p.Target == nil {
				original, found, e := archiveOperation(ctx, tx, p.RecoveryOfOperationID)
				if e != nil {
					return e
				}
				if !found {
					return ErrConflict
				}
				raw, e := q.ReadStorageArchiveBlob(ctx, p.BackupID)
				if e != nil {
					return e
				}
				var blob domain.HostingStorageArchiveBlob
				if json.Unmarshal(raw, &blob) != nil {
					return ErrConflict
				}
				if e = s.storageRemoteLocalComplete(ctx, tx, original.StorageArchive.Plan, original.CreatedBy, blob); e != nil {
					return e
				}
			}
		}
		if p.Mode == "backup" && r.ArchiveOutcome == "succeeded" && clearHold {
			if err := s.storageRemoteLocalComplete(ctx, tx, p, op.CreatedBy, *r.Blob); err != nil {
				return err
			}
		}
		if _, err := q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: leaseState, CompletionKey: &in.ReceiptID}); err != nil {
			return err
		}
		_, err := q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: in.RunID, Status: leaseState})
		return err
	})
}
