package runnerclient

import (
	"context"
	"errors"
	"io"
	"strings"

	"nerocd/internal/domain"
)

// StorageArchiveTransport permits literal loopback HTTP only in explicit local
// fixtures. Ordinary archive consumers require verified controller TLS.
type StorageArchiveTransport struct{ TrustedLoopbackHTTP bool }

// WithStorageArchiveTransport applies the existing protected request policy to
// the entire archive attempt, including shared lease and journal lifecycle calls.
// It cannot weaken a strict parent and grants no database execution capability.
func WithStorageArchiveTransport(parent context.Context, policy StorageArchiveTransport) context.Context {
	return WithDatabaseTransport(parent, DatabaseTransport(policy))
}

var errStorageArchiveResponse = errors.New("invalid storage archive response")

type storageArchiveResponse struct{ value domain.HostingOperation }

func (r *storageArchiveResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || domain.DecodeHostingStorageArchiveJSON(raw, &r.value) != nil {
		return errStorageArchiveResponse
	}
	return nil
}

func storageArchiveAuthorityValid(a domain.HostingAuthority) bool {
	return (domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "archive_plan", PlanHash: "sha256:" + strings.Repeat("0", 64)}).Validate() == nil
}

func validateStorageArchiveOperation(op domain.HostingOperation, a domain.HostingAuthority) error {
	sidecar := op.StorageArchive
	if sidecar == nil || sidecar.Plan.Validate() != nil || op.ID != a.OperationID || op.RunID != a.RunID || op.TaskMode != sidecar.Plan.Mode || op.ProjectID != sidecar.Plan.ProjectID || sidecar.Plan.OperationID != op.ID || sidecar.Mode != sidecar.Plan.Mode || sidecar.BackupID != sidecar.Plan.BackupID || sidecar.RecoveryOfOperationID != sidecar.Plan.RecoveryOfOperationID || op.Snapshot != (domain.HostingSnapshot{}) || op.Plan != nil || op.Release != nil || op.Result != nil || op.CleanupPlan != nil || op.ReconciliationHealthy != nil || op.ObservationLease != nil || op.PreparedReceipt != nil {
		return errStorageArchiveResponse
	}
	resource, binding := sidecar.Plan.SourceStorage.ResourceID, sidecar.Plan.SourceStorage.BindingID
	if sidecar.Plan.Target != nil {
		resource, binding = sidecar.Plan.Target.Resource.ID, sidecar.Plan.Target.Storage.BindingID
	}
	if op.ResourceID != resource || op.BindingID != binding || domain.ValidateStorageArchiveTask(domain.TaskRun{ID: a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: op.ID, HostingMode: op.TaskMode}}) != nil {
		return errStorageArchiveResponse
	}
	for _, proof := range sidecar.Progress {
		request := domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: proof.Receipt.ID, PlanHash: proof.PlanHash, Phase: proof.Phase, Writers: proof.Writers, HelperContainerID: proof.HelperContainerID, Blob: proof.Blob, RestoreVerified: proof.RestoreVerified}
		if request.Validate() != nil || proof.PlanHash != sidecar.Plan.Hash || proof.Receipt.BodyHash == "" || proof.Receipt.CreatedAt.IsZero() {
			return errStorageArchiveResponse
		}
	}
	if sidecar.Result != nil && (domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "archive_result", PlanHash: sidecar.Plan.Hash, Result: *sidecar.Result}).Validate() != nil {
		return errStorageArchiveResponse
	}
	if op.CompleteReceipt != nil && sidecar.Result == nil {
		return errStorageArchiveResponse
	}
	return nil
}

func (c Client) storageArchivePost(ctx context.Context, path string, a domain.HostingAuthority, in any) (domain.HostingOperation, error) {
	var response storageArchiveResponse
	if _, protected := ctx.Value(databaseTransportKey{}).(DatabaseTransport); !protected {
		ctx = WithStorageArchiveTransport(ctx, StorageArchiveTransport{})
	}
	if err := c.post(ctx, "/api/v1/runners/hosting/storage-archive/"+path, in, &response); err != nil {
		var status *APIError
		if errors.As(err, &status) {
			copy := *status
			copy.Detail = ""
			return domain.HostingOperation{}, &copy
		}
		if ctx.Err() != nil {
			return domain.HostingOperation{}, ctx.Err()
		}
		return domain.HostingOperation{}, errors.New("storage archive controller request failed")
	}
	if ctx.Err() != nil {
		return domain.HostingOperation{}, ctx.Err()
	}
	if err := validateStorageArchiveOperation(response.value, a); err != nil {
		return domain.HostingOperation{}, err
	}
	return response.value, nil
}

// HostingStorageArchivePlan reads a currently authorized typed archive plan.
func (c Client) HostingStorageArchivePlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingOperation, error) {
	if !storageArchiveAuthorityValid(a) {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return c.storageArchivePost(ctx, "plan", a, a)
}

// HostingStorageArchivePrepared verifies the full-request preparation receipt.
func (c Client) HostingStorageArchivePrepared(ctx context.Context, in domain.HostingStorageArchivePreparedRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	op, err := c.storageArchivePost(ctx, "prepared", in.HostingAuthority, in)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	r := op.StorageArchive.PreparedReceipt
	if op.StorageArchive.Plan.Hash != in.PlanHash || op.StorageArchive.Plan.Helper != in.Helper || r == nil || r.ID != in.ReceiptID || r.BodyHash != domain.HostingDocumentHash(in) || r.CreatedAt.IsZero() {
		return domain.HostingOperation{}, errStorageArchiveResponse
	}
	return op, nil
}

// HostingStorageArchiveApplyBegin requires the exact plan and its mutation hold.
func (c Client) HostingStorageArchiveApplyBegin(ctx context.Context, in domain.HostingStorageArchiveApplyBeginRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	op, err := c.storageArchivePost(ctx, "apply-begin", in.HostingAuthority, in)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if op.StorageArchive.Plan.Hash != in.PlanHash || op.State != "applying" || !op.MutationHold || op.StorageArchive.Result != nil {
		return domain.HostingOperation{}, errStorageArchiveResponse
	}
	return op, nil
}

// HostingStorageArchiveProgress verifies acknowledgment of the immutable request.
func (c Client) HostingStorageArchiveProgress(ctx context.Context, in domain.HostingStorageArchiveProgress) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	op, err := c.storageArchivePost(ctx, "progress", in.HostingAuthority, in)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if op.StorageArchive.Plan.Hash != in.PlanHash {
		return domain.HostingOperation{}, errStorageArchiveResponse
	}
	count := 0
	for _, proof := range op.StorageArchive.Progress {
		if proof.Receipt.ID != in.ReceiptID {
			continue
		}
		count++
		if proof.Receipt.BodyHash != domain.HostingDocumentHash(in) || proof.Phase != in.Phase || proof.PlanHash != in.PlanHash || domain.HostingDocumentHash(proof.Writers) != domain.HostingDocumentHash(in.Writers) || proof.HelperContainerID != in.HelperContainerID || proof.RestoreVerified != in.RestoreVerified || domain.HostingDocumentHash(proof.Blob) != domain.HostingDocumentHash(in.Blob) {
			return domain.HostingOperation{}, errStorageArchiveResponse
		}
	}
	if count != 1 {
		return domain.HostingOperation{}, errStorageArchiveResponse
	}
	return op, nil
}

// HostingStorageArchiveComplete verifies the typed result without application readiness.
func (c Client) HostingStorageArchiveComplete(ctx context.Context, in domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	op, err := c.storageArchivePost(ctx, "complete", in.HostingAuthority, in)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	r := op.CompleteReceipt
	if op.StorageArchive.Plan.Hash != in.PlanHash || op.StorageArchive.Result == nil || domain.HostingDocumentHash(*op.StorageArchive.Result) != domain.HostingDocumentHash(in.Result) || r == nil || r.ID != in.ReceiptID || r.BodyHash != domain.HostingDocumentHash(in) || r.CreatedAt.IsZero() {
		return domain.HostingOperation{}, errStorageArchiveResponse
	}
	return op, nil
}
