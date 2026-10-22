// Package hostingbuildprotocol defines nonsecret build-agent phase messages.
// It implements no transport, authentication, authorization, or execution.
package hostingbuildprotocol

import "nerocd/internal/domain"

// StatusRequest identifies an attempt whose live status or committed proof is requested.
type StatusRequest struct {
	Authority domain.HostingBuildAuthority `json:"authority"`
}

// SourceBeginRequest requests the queued-to-sourcing transition at a revision.
type SourceBeginRequest struct {
	Authority        domain.HostingBuildAuthority `json:"authority"`
	ExpectedRevision int64                        `json:"expected_revision"`
}

// SourceCommitRequest records source identity. It deliberately has no revision:
// the receipt body alone is hashed and the store owns the revision transition.
type SourceCommitRequest struct {
	Authority domain.HostingBuildAuthority     `json:"authority"`
	ReceiptID string                           `json:"receipt_id"`
	Receipt   domain.HostingBuildSourceReceipt `json:"receipt"`
}

// PrepareRequest pins an observed daemon at the expected build revision.
type PrepareRequest struct {
	Authority        domain.HostingBuildAuthority `json:"authority"`
	ExpectedRevision int64                        `json:"expected_revision"`
	DaemonID         string                       `json:"daemon_id"`
}

// Completion preserves the exact field order and JSON shape of the persisted
// completion body. Request envelopes and revisions never enter its receipt hash.
type Completion struct {
	State       string                       `json:"state"`
	FailureCode string                       `json:"failure_code,omitempty"`
	Artifact    *domain.HostingBuildArtifact `json:"artifact,omitempty"`
}

// CompleteRequest records a completion receipt at the expected revision.
type CompleteRequest struct {
	Authority        domain.HostingBuildAuthority `json:"authority"`
	ReceiptID        string                       `json:"receipt_id"`
	ExpectedRevision int64                        `json:"expected_revision"`
	Completion       Completion                   `json:"completion"`
}

// SourceCredentialRequest reuses the existing authorization request. No plaintext
// credential response is defined in this nonsecret protocol package.
type SourceCredentialRequest = domain.HostingSourceCredentialRequest

// ReceiptAcknowledgment proves a committed body and exact historical tuple.
// It never grants permission to execute another phase.
type ReceiptAcknowledgment struct {
	Authority domain.HostingBuildAuthority `json:"authority"`
	Phase     string                       `json:"phase"`
	ReceiptID string                       `json:"receipt_id"`
	BodyHash  string                       `json:"body_hash"`
}

// PhaseResponse carries persisted build metadata and optional committed proof.
type PhaseResponse struct {
	Build          domain.HostingBuild    `json:"build"`
	Acknowledgment *ReceiptAcknowledgment `json:"acknowledgment,omitempty"`
}

// StatusResponse separates historical proof from current permission. Only a
// future reviewed live authorization path may set MayExecute true.
type StatusResponse struct {
	Build           domain.HostingBuild     `json:"build"`
	MayExecute      bool                    `json:"may_execute"`
	CancelRequested bool                    `json:"cancel_requested"`
	Receipts        []ReceiptAcknowledgment `json:"receipts"`
}
