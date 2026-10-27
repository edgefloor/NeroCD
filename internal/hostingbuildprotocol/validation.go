package hostingbuildprotocol

import (
	"errors"
	"math"
	"strings"
	"unicode"

	"nerocd/internal/domain"
)

// ErrInvalidMessage reports inconsistent wire identity or shape without echoing input.
var ErrInvalidMessage = errors.New("invalid hosting build protocol message")

// ValidateAuthority matches the existing store's build ID, canonical run binding,
// positive bounded attempt, and nonempty lease/fence requirements. It checks no
// persisted authority and never normalizes identifiers.
func ValidateAuthority(a domain.HostingBuildAuthority) error {
	if len(a.BuildID) < 1 || len(a.BuildID) > 63 || a.RunID != "build_run_"+a.BuildID || a.LeaseID == "" || a.Fence == "" || a.Attempt < 1 || a.Attempt > math.MaxInt32 {
		return ErrInvalidMessage
	}
	for i, c := range a.BuildID {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (c != '-' || i == 0) {
			return ErrInvalidMessage
		}
	}
	return nil
}
func validReceiptID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsFunc(id, unicode.IsControl)
}
func validDigest(hash string) bool {
	if len(hash) != 71 || !strings.HasPrefix(hash, "sha256:") {
		return false
	}
	for _, c := range hash[7:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validPhase(phase string) bool {
	return phase == "source" || phase == "complete" || phase == "cleanup"
}

// Validate checks structural authority only; the store must authorize the request.
func (r StatusRequest) Validate() error { return ValidateAuthority(r.Authority) }

// Validate checks authority and a positive int64 revision. CAS and overflow
// handling remain persistence concerns.
func (r SourceBeginRequest) Validate() error {
	if ValidateAuthority(r.Authority) != nil || r.ExpectedRevision <= 0 {
		return ErrInvalidMessage
	}
	return nil
}

// Validate checks the receipt's build binding. The store separately verifies
// source/input identity, commit, policy and tree against the immutable snapshot.
func (r SourceCommitRequest) Validate() error {
	if ValidateAuthority(r.Authority) != nil || !validReceiptID(r.ReceiptID) || r.Receipt.BuildID != r.Authority.BuildID {
		return ErrInvalidMessage
	}
	return nil
}

// Validate matches the store's structural daemon and revision requirements.
func (r PrepareRequest) Validate() error {
	if ValidateAuthority(r.Authority) != nil || r.ExpectedRevision <= 0 || r.DaemonID == "" || len(r.DaemonID) > 256 || strings.TrimSpace(r.DaemonID) != r.DaemonID || strings.ContainsFunc(r.DaemonID, unicode.IsControl) {
		return ErrInvalidMessage
	}
	return nil
}

// Validate admits only the store's state/failure/artifact combinations. It does
// not validate an artifact's provenance, digests, or binding to a build snapshot.
func (c Completion) Validate() error {
	switch c.State {
	case "succeeded":
		if (c.Artifact == nil) == (c.ComposeResult == nil) || c.FailureCode != "" {
			return ErrInvalidMessage
		}
	case "failed", "canceled", "cleanup_required":
		if c.Artifact != nil || c.ComposeResult != nil || !domain.ValidHostingBuildFailure(c.FailureCode) {
			return ErrInvalidMessage
		}
	default:
		return ErrInvalidMessage
	}
	return nil
}

// Validate checks request identity and completion shape without claiming effects.
func (r CompleteRequest) Validate() error {
	if ValidateAuthority(r.Authority) != nil || !validReceiptID(r.ReceiptID) || r.ExpectedRevision <= 0 || r.Completion.Validate() != nil {
		return ErrInvalidMessage
	}
	if r.Completion.Artifact != nil && r.Completion.Artifact.BuildID != r.Authority.BuildID {
		return ErrInvalidMessage
	}
	return nil
}

// CompletionPhase uses the persisted receipt phase, which is distinct from any
// future agent journal kind. Invalid completion shapes have no receipt phase.
func CompletionPhase(c Completion) (string, error) {
	if c.Validate() != nil {
		return "", ErrInvalidMessage
	}
	if c.State == "cleanup_required" {
		return "cleanup", nil
	}
	return "complete", nil
}

// SourceReceiptHash hashes only the source receipt, matching persisted replay.
func SourceReceiptHash(receipt domain.HostingBuildSourceReceipt) string {
	return domain.HostingDocumentHash(receipt)
}

// CompletionHash hashes only the completion body, matching persisted replay.
func CompletionHash(completion Completion) string { return domain.HostingDocumentHash(completion) }

// ValidateAcknowledgment checks exact tuple, phase, receipt ID, and body hash
// equality against caller-retained intent. Passing is historical proof only;
// it never establishes live authority or makes MayExecute true.
func ValidateAcknowledgment(ack ReceiptAcknowledgment, authority domain.HostingBuildAuthority, phase, receiptID, bodyHash string) error {
	if ValidateAuthority(authority) != nil || ValidateAuthority(ack.Authority) != nil || ack.Authority != authority || !validPhase(phase) || ack.Phase != phase || !validReceiptID(receiptID) || ack.ReceiptID != receiptID || !validDigest(bodyHash) || !validDigest(ack.BodyHash) || ack.BodyHash != bodyHash {
		return ErrInvalidMessage
	}
	return nil
}
