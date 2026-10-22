package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

const maxBuildResponseBytes = 1 << 20

var errBuildResponse = errors.New("invalid hosting build response")

type buildResponse[T any] struct{ value T }

func (r *buildResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxBuildResponseBytes+1))
	if err != nil {
		return errBuildResponse
	}
	if len(raw) > maxBuildResponseBytes {
		return errBuildResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value T
	if decoder.Decode(&value) != nil {
		return errBuildResponse
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errBuildResponse
	}
	r.value = value
	return nil
}
func buildPost[T any](ctx context.Context, c Client, path string, in any) (T, error) {
	var response buildResponse[T]
	if err := c.post(ctx, "/api/v1/runners/builds/"+path, in, &response); err != nil {
		// The shared transport retains short HTTP bodies for older protocols. Build
		// messages need no such diagnostic; do not expose it even through APIError.
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			sanitized := *apiErr
			sanitized.Detail = ""
			return response.value, &sanitized
		}
		return response.value, err
	}
	return response.value, nil
}
func validateBuildResponse(b domain.HostingBuild, a domain.HostingBuildAuthority) error {
	if b.ID != a.BuildID || b.RunID != a.RunID || b.Revision <= 0 {
		return errBuildResponse
	}
	switch b.State {
	case "queued", "sourcing", "building", "succeeded", "failed", "canceled", "cleanup_required":
		return nil
	default:
		return errBuildResponse
	}
}

// BuildStatus reads exact attempt metadata. Receipts are historical proof only;
// they never imply MayExecute or acknowledgment of a caller-retained body.
func (c Client) BuildStatus(ctx context.Context, in hostingbuildprotocol.StatusRequest) (hostingbuildprotocol.StatusResponse, error) {
	var zero hostingbuildprotocol.StatusResponse
	if err := in.Validate(); err != nil {
		return zero, err
	}
	response, err := buildPost[hostingbuildprotocol.StatusResponse](ctx, c, "status", in)
	if err != nil {
		return zero, err
	}
	if validateBuildResponse(response.Build, in.Authority) != nil {
		return zero, errBuildResponse
	}
	if response.MayExecute && (response.CancelRequested || (response.Build.State != "queued" && response.Build.State != "sourcing" && response.Build.State != "building")) {
		return zero, errBuildResponse
	}
	phases := make(map[string]bool, 3)
	for _, ack := range response.Receipts {
		if phases[ack.Phase] || hostingbuildprotocol.ValidateAcknowledgment(ack, in.Authority, ack.Phase, ack.ReceiptID, ack.BodyHash) != nil {
			return zero, errBuildResponse
		}
		phases[ack.Phase] = true
	}
	return response, nil
}

// BuildSourceBegin requests sourcing at the exact retained revision. It returns
// no invented receipt acknowledgment and performs no automatic retry.
func (c Client) BuildSourceBegin(ctx context.Context, in hostingbuildprotocol.SourceBeginRequest) (hostingbuildprotocol.PhaseResponse, error) {
	var zero hostingbuildprotocol.PhaseResponse
	if err := in.Validate(); err != nil {
		return zero, err
	}
	response, err := buildPost[hostingbuildprotocol.PhaseResponse](ctx, c, "source-begin", in)
	if err != nil {
		return zero, err
	}
	if validateBuildResponse(response.Build, in.Authority) != nil || response.Acknowledgment != nil {
		return zero, errBuildResponse
	}
	return response, nil
}

// BuildSourceCommit submits an immutable source receipt and requires exact
// committed tuple, receipt ID, phase and source-body hash acknowledgment.
func (c Client) BuildSourceCommit(ctx context.Context, in hostingbuildprotocol.SourceCommitRequest) (hostingbuildprotocol.PhaseResponse, error) {
	var zero hostingbuildprotocol.PhaseResponse
	if err := in.Validate(); err != nil {
		return zero, err
	}
	response, err := buildPost[hostingbuildprotocol.PhaseResponse](ctx, c, "source-commit", in)
	if err != nil {
		return zero, err
	}
	if validateBuildResponse(response.Build, in.Authority) != nil || response.Acknowledgment == nil || hostingbuildprotocol.ValidateAcknowledgment(*response.Acknowledgment, in.Authority, "source", in.ReceiptID, hostingbuildprotocol.SourceReceiptHash(in.Receipt)) != nil {
		return zero, errBuildResponse
	}
	return response, nil
}

// BuildPrepare pins an observed daemon at the exact revision. This transport
// does not infer external effects or fabricate a receipt acknowledgment.
func (c Client) BuildPrepare(ctx context.Context, in hostingbuildprotocol.PrepareRequest) (hostingbuildprotocol.PhaseResponse, error) {
	var zero hostingbuildprotocol.PhaseResponse
	if err := in.Validate(); err != nil {
		return zero, err
	}
	response, err := buildPost[hostingbuildprotocol.PhaseResponse](ctx, c, "prepare", in)
	if err != nil {
		return zero, err
	}
	if validateBuildResponse(response.Build, in.Authority) != nil || response.Acknowledgment != nil {
		return zero, errBuildResponse
	}
	return response, nil
}

// BuildComplete submits the exact retained completion and revision. A valid
// acknowledgment proves that body committed, including the distinct cleanup phase.
func (c Client) BuildComplete(ctx context.Context, in hostingbuildprotocol.CompleteRequest) (hostingbuildprotocol.PhaseResponse, error) {
	var zero hostingbuildprotocol.PhaseResponse
	if err := in.Validate(); err != nil {
		return zero, err
	}
	phase, err := hostingbuildprotocol.CompletionPhase(in.Completion)
	if err != nil {
		return zero, err
	}
	response, err := buildPost[hostingbuildprotocol.PhaseResponse](ctx, c, "complete", in)
	if err != nil {
		return zero, err
	}
	if validateBuildResponse(response.Build, in.Authority) != nil || response.Acknowledgment == nil || hostingbuildprotocol.ValidateAcknowledgment(*response.Acknowledgment, in.Authority, phase, in.ReceiptID, hostingbuildprotocol.CompletionHash(in.Completion)) != nil {
		return zero, errBuildResponse
	}
	return response, nil
}
