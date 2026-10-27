package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errStorageArchiveHeld = errors.New("storage archive evidence remains held for authorized recovery")

func validateStorageArchiveExecutionConfig(c Config) error {
	if c.StorageArchive == nil {
		return nil
	}
	if !c.Execute || c.CompleteStatus != "" {
		return errors.New("storage archive requires configured execution and forbids generic completion")
	}
	if runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: c.StorageArchive.TrustedLoopbackHTTP}) != nil {
		return errors.New("storage archive requires protected controller transport")
	}
	return c.StorageArchive.validate()
}

// ValidateStorageArchiveExecution inspects the current native host and exact
// immutable helper before registration. It does not perform a qualification run.
func ValidateStorageArchiveExecution(ctx context.Context, c StorageArchiveExecutionConfig, endpoint string) error {
	e, err := openStorageArchiveEngine(ctx, c, endpoint)
	if err != nil {
		return err
	}
	return e.Close()
}

func archivePlanMatches(op domain.HostingOperation, a domain.HostingAuthority, p domain.HostingStorageArchivePlan) bool {
	if op.StorageArchive == nil || op.ID != a.OperationID || op.RunID != a.RunID || op.TaskMode != p.Mode || op.ProjectID != p.ProjectID || domain.HostingDocumentHash(op.StorageArchive.Plan) != domain.HostingDocumentHash(p) {
		return false
	}
	resource, binding := p.SourceResourceID(), p.SourceBindingID()
	if p.Target != nil {
		resource, binding = p.Target.Resource.ID, p.Target.Storage.BindingID
	}
	return op.ResourceID == resource && op.BindingID == binding && op.Snapshot == (domain.HostingSnapshot{}) && op.Plan == nil && op.Release == nil && op.Result == nil && op.CleanupPlan == nil && op.ReconciliationHealthy == nil && op.ObservationLease == nil && op.PreparedReceipt == nil && p.Validate() == nil
}

// Both the live lease and the current typed plan are checked after engine
// inspection. Historical receipts never extend a mutation's authority.
func archiveCurrentAuthority(c runnerclient.Client, a domain.HostingAuthority, p domain.HostingStorageArchivePlan, deadline time.Time, hold bool) func(context.Context) error {
	return func(parent context.Context) error {
		if parent.Err() != nil || !time.Now().Before(deadline) {
			return errLeaseAuthorityLost
		}
		ctx, cancel := context.WithDeadline(parent, deadline)
		defer cancel()
		lease, err := c.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
		if err != nil || lease.ID != a.LeaseID || lease.RunID != a.RunID || lease.RunnerID != p.RunnerID || lease.Attempt != a.Attempt || lease.Fence != a.Fence || lease.Status != domain.LeaseActive || !time.Now().Before(lease.ExpiresAt) {
			return errLeaseAuthorityLost
		}
		op, err := c.HostingStorageArchivePlan(ctx, a)
		if err != nil || !archivePlanMatches(op, a, p) || op.StorageArchive.Result != nil || hold && (!op.MutationHold || op.State != "applying") {
			return errLeaseAuthorityLost
		}
		if parent.Err() != nil || !time.Now().Before(deadline) {
			return errLeaseAuthorityLost
		}
		return nil
	}
}

func archiveRecord(j *runner.AttemptJournal, a domain.HostingAuthority, lease domain.RunLease, s *attemptSupervisor, p domain.HostingStorageArchivePlan, phase, suffix string) runner.JournalStorageArchive {
	id := attemptMutationKey("archive", a.LeaseID, a.Attempt, suffix)
	for _, old := range j.Snapshot().StorageArchive {
		if old.ID == id {
			return old
		}
	}
	return runner.JournalStorageArchive{ID: id, Attempt: journalAttemptIdentity(a.RunID, lease, s), Plan: p, Phase: phase, CreatedAt: time.Now().UTC()}
}

type archiveCoordinator struct {
	s     *attemptSupervisor
	j     *runner.AttemptJournal
	drain func(context.Context) error
	c     runnerclient.Client
	e     storageArchiveExecutionEngine
	a     domain.HostingAuthority
	lease domain.RunLease
	p     domain.HostingStorageArchivePlan
}

// The boundary diagnostic deliberately omits the underlying error text: HTTP
// bodies, transport URLs and filesystem errors can contain private data. Keep
// the cause for classification without rendering it in runner output.
type storageArchiveProgressError struct {
	stage  string
	class  string
	status int
	cause  error
}

func (e *storageArchiveProgressError) Error() string {
	return fmt.Sprintf("storage archive progress acknowledgement failed: stage=%s class=%s status=%d", e.stage, e.class, e.status)
}
func (e *storageArchiveProgressError) Unwrap() error { return e.cause }
func archiveProgressDiagnostic(stage string, err error) error {
	if err == nil {
		return nil
	}
	out := &storageArchiveProgressError{stage: stage, class: "protocol_or_local", cause: err}
	var api *runnerclient.APIError
	switch {
	case errors.Is(err, context.Canceled):
		out.class = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		out.class = "deadline"
	case errors.As(err, &api):
		out.class = "transport_or_response"
		if api.StatusCode >= 100 && api.StatusCode <= 599 {
			out.class = "http"
			out.status = api.StatusCode
		}
	case errors.Is(err, domain.ErrInvalidStorageArchive):
		out.class = "invalid_request"
	}
	return out
}

func (x *archiveCoordinator) progress(ctx context.Context, phase, suffix string, writers []domain.HostingStorageArchiveWriterState, helper string, blob *domain.HostingStorageArchiveBlob, verified bool) error {
	entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "progress", suffix)
	request := domain.HostingStorageArchiveProgress{HostingAuthority: x.a, ReceiptID: entry.ID, PlanHash: x.p.Hash, Phase: phase, Writers: writers, HelperContainerID: helper, Blob: blob, RestoreVerified: verified}
	if phase == "archive_published" {
		if owned, ok := x.e.(interface {
			BackupCopyProof() *domain.BackupLocalCopyProof
		}); ok {
			request.LocalCopy = owned.BackupCopyProof()
		}
	}
	if entry.Progress != nil && domain.HostingDocumentHash(entry.Progress) != domain.HostingDocumentHash(request) {
		return errors.New("storage archive progress identity changed")
	}
	entry.Progress = &request
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		return archiveProgressDiagnostic("journal", err)
	}
	_, err := x.c.HostingStorageArchiveProgress(ctx, request)
	return archiveProgressDiagnostic("controller", err)
}

func executeStorageArchiveClaim(s *attemptSupervisor, j *runner.AttemptJournal, reporter *attemptReporter, server, token string, claim domain.ClaimedRun, endpoint string, config *StorageArchiveExecutionConfig) error {
	if config == nil || domain.ValidateStorageArchiveTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.Lease.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errors.New("invalid or unconfigured typed storage archive claim")
	}
	e, err := openStorageArchiveEngine(s.Context(), *config, endpoint)
	if err != nil {
		return err
	}
	return executeStorageArchiveClaimWithEngine(s, j, reporter, runnerclient.New(server, token), claim, config, e)
}

// The opened engine belongs to this claim until all execution and settlement
// handling completes, including failures before the first journal mutation.
func executeStorageArchiveClaimWithEngine(s *attemptSupervisor, j *runner.AttemptJournal, reporter *attemptReporter, c runnerclient.Client, claim domain.ClaimedRun, config *StorageArchiveExecutionConfig, e storageArchiveExecutionEngine) (retErr error) {
	defer func() { retErr = errors.Join(retErr, e.Close()) }()
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	op, err := c.HostingStorageArchivePlan(s.Context(), a)
	if err != nil {
		return err
	}
	if op.StorageArchive == nil || !archivePlanMatches(op, a, op.StorageArchive.Plan) || op.TaskMode != claim.Run.RunSpec.HostingMode || op.StorageArchive.Plan.RunnerID != claim.Lease.RunnerID || op.StorageArchive.Plan.Helper != config.Helper || op.StorageArchive.Plan.RemoteTarget != nil && op.StorageArchive.Plan.ComposeSource != nil && !config.RemoteBackups {
		return errors.New("storage archive task target changed")
	}
	x := archiveCoordinator{s: s, j: j, drain: reporter.WaitEmpty, c: c, e: e, a: a, lease: claim.Lease, p: op.StorageArchive.Plan}
	return x.execute(op)
}

func (x *archiveCoordinator) execute(op domain.HostingOperation) error {
	if !archivePlanMatches(op, x.a, x.p) || op.StorageArchive.Result != nil || op.State != "dispatched" && op.State != "preparing" && op.State != "prepared" {
		return errStorageArchiveHeld
	}
	for _, old := range x.j.Snapshot().StorageArchive {
		if old.Authority() == x.a && old.Phase != "plan" && old.Phase != "prepared" {
			return errStorageArchiveHeld
		}
	}
	if storageArchiveConflict(x.j.Snapshot(), x.p.SourceStorage.Reference(), x.p.SourceBindingID(), x.p.OperationID, x.p.RecoveryOfOperationID) {
		return errStorageArchiveHeld
	}
	entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "plan", "plan")
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		return err
	}
	if err := x.e.Prepare(x.s.Context(), x.p); err != nil {
		return errStorageArchiveHeld
	}
	prepared := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "prepared", "prepared")
	req := domain.HostingStorageArchivePreparedRequest{HostingAuthority: x.a, ReceiptID: prepared.ID, PlanHash: x.p.Hash, Helper: x.p.Helper}
	prepared.Prepared = &req
	if _, err := x.j.AppendStorageArchive(prepared); err != nil {
		return err
	}
	op, err := x.c.HostingStorageArchivePrepared(x.s.Context(), req)
	if err != nil || !archivePlanMatches(op, x.a, x.p) || op.State != "prepared" {
		return errStorageArchiveHeld
	}
	if x.p.Mode == "restore" {
		if err = x.materialize(); err != nil {
			return err
		}
	}
	intent := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "apply_intent", "apply")
	apply := domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: x.a, ReceiptID: intent.ID, PlanHash: x.p.Hash}
	intent.ApplyBegin = &apply
	if _, err = x.j.AppendStorageArchive(intent); err != nil {
		return err
	}
	op, err = x.c.HostingStorageArchiveApplyBegin(x.s.Context(), apply)
	if err != nil || !archivePlanMatches(op, x.a, x.p) {
		return errStorageArchiveHeld
	}
	if x.p.Mode == "recover" {
		return x.recover()
	}
	return x.run()
}

func (x *archiveCoordinator) materialize() error {
	t := x.p.Target
	if t == nil {
		return errStorageArchiveHeld
	}
	entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "storage_create_intent", "create")
	request := domain.HostingStorageCreateRequest{HostingAuthority: x.a, PlanHash: x.p.Hash, StorageRef: t.Storage.Reference(), CreationNonce: t.Plan.CreationNonce}
	entry.StorageCreate = &request
	fresh := true
	for _, old := range x.j.Snapshot().StorageArchive {
		if old.ID == entry.ID {
			fresh = false
		}
	}
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		return err
	}
	ack, err := x.c.HostingStorageCreateBegin(x.s.Context(), request)
	if err != nil {
		return errStorageArchiveHeld
	}
	volume, err := archiveVolumeRequest(x.p, ack)
	if err != nil {
		return err
	}
	volume.CreatePermitted = volume.CreatePermitted && fresh
	before := func(ctx context.Context) error {
		if err := archiveCurrentAuthority(x.c, x.a, x.p, x.s.GuardDeadline(), false)(ctx); err != nil {
			return err
		}
		current, err := x.c.HostingStorageCreateBegin(ctx, request)
		if err != nil {
			return errLeaseAuthorityLost
		}
		observed, err := archiveVolumeRequest(x.p, current)
		if err != nil || observed.Storage != volume.Storage || observed.OriginOperationID != volume.OriginOperationID || observed.OriginPlanHash != volume.OriginPlanHash || observed.CreationNonce != volume.CreationNonce || observed.DaemonID != volume.DaemonID {
			return errLeaseAuthorityLost
		}
		return ctx.Err()
	}
	proof, err := x.e.EnsureRestoreVolume(x.s.Context(), volume, before)
	volume.CreatePermitted = false
	if errors.Is(err, errHostingOwnedVolumeUncertain) && x.s.Context().Err() == nil {
		proof, err = x.e.EnsureRestoreVolume(x.s.Context(), volume, before)
	}
	if err != nil {
		return errStorageArchiveHeld
	}
	materialized := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "storage_materialized", "materialized")
	r := domain.HostingStorageMaterializedRequest{HostingAuthority: x.a, ReceiptID: materialized.ID, PlanHash: x.p.Hash, StorageRef: proof.StorageRef, CreationOperationID: proof.CreationOperationID, CreationPlanHash: proof.CreationPlanHash, CreationNonce: proof.CreationNonce, DaemonID: proof.DaemonID, VolumeName: t.Storage.VolumeName, Driver: "local", Scope: "local", Labels: proof.Labels}
	if r.Matches(ack.Storage, ack.Materialization) != nil {
		return errStorageArchiveHeld
	}
	materialized.StorageMaterialized, materialized.StorageMetadata = &r, &ack.Storage
	if _, err = x.j.AppendStorageArchive(materialized); err != nil {
		return err
	}
	got, err := x.c.HostingStorageMaterialized(x.s.Context(), r)
	if err != nil || got.Receipt.ID != r.ReceiptID || got.Receipt.BodyHash != domain.HostingDocumentHash(r) || r.Matches(ack.Storage, got.Materialization) != nil {
		return errStorageArchiveHeld
	}
	return nil
}

func archiveVolumeRequest(p domain.HostingStorageArchivePlan, ack domain.HostingStorageCreateAck) (hostingOwnedVolumeRequest, error) {
	t := p.Target
	m := ack.Materialization
	if t == nil || ack.Storage != t.Storage || m.Validate(ack.Storage) != nil || m.StorageRef != t.Storage.Reference() || m.CreationOperationID != t.Plan.CreationOperationID || m.CreationPlanHash != p.Hash || m.CreationNonce != t.Plan.CreationNonce || m.DaemonID != p.Helper.DaemonID {
		return hostingOwnedVolumeRequest{}, errors.New("storage archive target admission changed")
	}
	v := hostingOwnedVolumeRequest{Storage: ack.Storage, DaemonID: m.DaemonID, OriginOperationID: m.CreationOperationID, OriginPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, CreatePermitted: ack.CreatePermitted}
	return v, v.validate()
}

func (x *archiveCoordinator) run() error {
	var writers, owed []domain.HostingStorageArchiveWriterState
	before := archiveCurrentAuthority(x.c, x.a, x.p, x.s.GuardDeadline(), true)
	if x.p.Mode == "backup" {
		var err error
		writers, err = x.e.InspectWriters(x.s.Context(), x.p)
		if err != nil {
			return x.finish(domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "not_needed", FailureCode: "ownership_mismatch"})
		}
		if err = x.progress(x.s.Context(), "writer_snapshot", "writers", writers, "", nil, false); err != nil {
			return errStorageArchiveHeld
		}
		for _, w := range writers {
			if w.Running {
				owed = append(owed, w)
			}
		}
		// The controller accepts one full inventory per phase. An acknowledged
		// batch intent owes every originally-running writer, even when a stop
		// fails partway through or its response is lost.
		if err = x.progress(x.s.Context(), "stop_intent", "stop", writers, "", nil, false); err != nil {
			return x.fail(owed, "", "", "interrupted")
		}
		for _, w := range owed {
			if err = x.e.StopWriter(x.s.Context(), x.p, w, before); err != nil {
				return x.fail(owed, "", "", "interrupted")
			}
		}
		stopped := slices.Clone(writers)
		for i := range stopped {
			stopped[i].Running = false
		}
		if err = x.progress(x.s.Context(), "stopped", "stopped", stopped, "", nil, false); err != nil {
			return x.fail(owed, "", "", "interrupted")
		}
	}
	nonce := attemptMutationKey("archive", x.a.LeaseID, x.a.Attempt, "helper_intent")
	if err := x.progress(x.s.Context(), "helper_intent", "helper_intent", nil, "", nil, false); err != nil {
		return x.fail(owed, "", "", "interrupted")
	}
	id, err := x.e.CreateHelper(x.s.Context(), x.p, nonce, before)
	if err != nil || id == "" {
		// A parsed create response is uncertain identity evidence, not proof of
		// ownership. Preserve it privately without sending helper_identified.
		if id != "" {
			unknown := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "helper_unverified", "helper_unverified")
			unknown.UncertainHelperContainerID = id
			if _, saveErr := x.j.AppendStorageArchive(unknown); saveErr != nil {
				return errStorageArchiveHeld
			}
		}
		return x.fail(owed, id, nonce, "helper_failed")
	}
	// Exact returned identity is durable before ACK or helper execution.
	if err = x.progress(x.s.Context(), "helper_identified", "helper_identified", nil, id, nil, false); err != nil {
		return errors.Join(x.fail(owed, id, nonce, "interrupted"), err)
	}
	result := domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "not_needed"}
	if x.p.Mode == "backup" {
		blob, runErr := x.e.Backup(x.s.Context(), x.p, id, nonce, before)
		if runErr != nil {
			return x.fail(owed, id, nonce, "archive_invalid")
		}
		result.Blob = &blob
		err = x.progress(x.s.Context(), "archive_published", "published", nil, "", &blob, false)
	} else {
		if runErr := x.e.Restore(x.s.Context(), x.p, id, nonce, before); runErr != nil {
			return x.fail(owed, id, nonce, "archive_invalid")
		}
		result.RestoreVerified, result.Blob = true, x.p.Blob
		err = x.progress(x.s.Context(), "restore_verified", "verified", nil, "", x.p.Blob, true)
	}
	if err != nil {
		return x.fail(owed, id, nonce, "interrupted")
	}
	if err = x.e.CleanupHelper(x.s.Context(), x.p, id, nonce, before); err != nil {
		return x.fail(owed, id, nonce, "helper_failed")
	}
	if err = x.resume(x.s.Context(), owed, before); err != nil {
		result = domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "uncertain", Uncertain: true, FailureCode: "resume_failed"}
	} else if len(owed) > 0 {
		result.ResumeOutcome = "resumed"
	}
	return x.finish(result)
}

func (x *archiveCoordinator) resume(ctx context.Context, owed []domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	if len(owed) == 0 {
		return nil
	}
	var writers []domain.HostingStorageArchiveWriterState
	if x.p.Mode == "recover" {
		writers = x.p.RecoveryEvidence.Writers
	} else {
		for _, e := range x.j.Snapshot().StorageArchive {
			if e.Authority() == x.a && e.Progress != nil && e.Progress.Phase == "writer_snapshot" {
				writers = e.Progress.Writers
			}
		}
	}
	for _, w := range owed {
		if !w.Running || !slices.Contains(writers, w) {
			return errStorageArchiveHeld
		}
	}
	// A durable completed batch is replayed as metadata only. Partial loops
	// retain the batch obligation; fresh recovery inspects already-running
	// writers rather than interpreting intent as proof they were restarted.
	for _, e := range x.j.Snapshot().StorageArchive {
		if e.Authority() == x.a && e.Progress != nil && e.Progress.Phase == "resumed" {
			return x.progress(ctx, "resumed", "resumed", writers, "", nil, false)
		}
	}
	if err := x.progress(ctx, "resume_intent", "resume", writers, "", nil, false); err != nil {
		return err
	}
	for _, w := range owed {
		if err := x.e.ResumeWriter(ctx, x.p, w, before); err != nil {
			return err
		}
	}
	return x.progress(ctx, "resumed", "resumed", writers, "", nil, false)
}

func (x *archiveCoordinator) fail(owed []domain.HostingStorageArchiveWriterState, id, nonce, code string) error {
	// Only a durable stop intent creates an obligation. A failed journal write
	// cannot grant authority to start a container.
	kept := owed[:0]
	for _, w := range owed {
		for _, e := range x.j.Snapshot().StorageArchive {
			if e.Authority() == x.a && e.Progress != nil && e.Progress.Phase == "stop_intent" && slices.Contains(e.Progress.Writers, w) {
				kept = append(kept, w)
				break
			}
		}
	}
	owed = kept
	result := domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "not_needed", FailureCode: code, Uncertain: nonce != ""}
	// Cancellation does not itself authorize restart. A short cleanup window
	// retains the parent's transport policy and ends at the original lease guard.
	deadline := x.s.GuardDeadline()
	if cap := time.Now().Add(15 * time.Second); cap.Before(deadline) {
		deadline = cap
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(x.s.Context()), deadline)
	defer cancel()
	before := archiveCurrentAuthority(x.c, x.a, x.p, deadline, true)
	quiescent := nonce == ""
	if id != "" && before(ctx) == nil && x.e.CleanupHelper(ctx, x.p, id, nonce, before) == nil {
		quiescent = true
	}
	if len(owed) > 0 {
		result.ResumeOutcome = "uncertain"
		result.Uncertain = true
	}
	if quiescent && len(owed) > 0 && x.resume(ctx, owed, before) == nil {
		result.ResumeOutcome = "resumed"
	}
	if quiescent && (len(owed) == 0 || result.ResumeOutcome == "resumed") && x.p.Mode == "backup" {
		result.Uncertain = false
	}
	if x.s.Context().Err() != nil {
		result.FailureCode = "canceled"
	}
	return x.finish(result)
}

func (x *archiveCoordinator) finish(result domain.HostingStorageArchiveResult) error {
	entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "complete", "complete")
	req := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: x.a, ReceiptID: entry.ID, PlanHash: x.p.Hash, Result: result}
	entry.Complete = &req
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		return err
	}
	if x.s.Context().Err() != nil {
		return errStorageArchiveHeld
	}
	if x.drain != nil {
		if err := x.drain(x.s.Context()); err != nil {
			return err
		}
	}
	op, err := x.c.HostingStorageArchiveComplete(x.s.Context(), req)
	if err != nil || !archivePlanMatches(op, x.a, x.p) || result.Uncertain || op.MutationHold || op.RestoreOwnershipHeld {
		return errStorageArchiveHeld
	}
	if x.p.Mode == "recover" {
		return retireStorageArchiveRecovery(x.j, x.a, x.p, *op.CompleteReceipt)
	}
	return retireStorageArchiveAttempt(x.j, x.a)
}

func (x *archiveCoordinator) recover() error {
	original, entries, err := storageArchiveRecoverySource(x.j.Snapshot(), x.p)
	if err != nil {
		return err
	}
	before := archiveCurrentAuthority(x.c, x.a, x.p, x.s.GuardDeadline(), true)
	id, nonce := "", ""
	for _, e := range entries {
		if e.UncertainHelperContainerID != "" {
			id = e.UncertainHelperContainerID
		}
		if e.Progress == nil {
			continue
		}
		if e.Progress.Phase == "helper_intent" {
			nonce = e.Progress.ReceiptID
		}
		if e.Progress.Phase == "helper_identified" {
			id = e.Progress.HelperContainerID
		}
	}
	if nonce != "" {
		if id == "" || x.e.CleanupHelper(x.s.Context(), original, id, nonce, before) != nil {
			return errStorageArchiveHeld
		}
	}
	// The fresh controller-derived subset is authoritative, including after an
	// interrupted recovery. Do not recreate obligations from old running bits.
	var owed []domain.HostingStorageArchiveWriterState
	for _, w := range x.p.RecoveryEvidence.Writers {
		if slices.Contains(x.p.RecoveryEvidence.ResumeRequiredContainerIDs, w.ContainerID) {
			owed = append(owed, w)
		}
	}
	if len(owed) > 0 {
		if _, err = x.e.InspectWriters(x.s.Context(), x.p); err != nil {
			return errStorageArchiveHeld
		}
		if err = x.resume(x.s.Context(), owed, before); err != nil {
			return errStorageArchiveHeld
		}
	}
	result := domain.HostingStorageArchiveResult{ArchiveOutcome: "not_attempted", ResumeOutcome: "not_needed"}
	if len(owed) > 0 {
		result.ResumeOutcome = "resumed"
	}
	if x.p.Target != nil {
		result.Uncertain = true
		result.FailureCode = "interrupted"
	}
	return x.finish(result)
}
