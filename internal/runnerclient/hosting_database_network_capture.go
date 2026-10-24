package runnerclient

import (
	"context"
	"errors"
	"io"
	"math"
	"regexp"
	"time"

	"nerocd/internal/domain"
)

var errDatabaseNetworkCapture = errors.New("invalid database network capture exchange")
var networkCaptureID = regexp.MustCompile(`^hnc_[0-9a-f]{32}$`)

type networkCaptureResponse[T any] struct{ value T }

func (r *networkCaptureResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, domain.HostingStorageProtocolJSONLimit+1))
	defer clear(raw)
	if err != nil || domain.DecodeHostingStorageProtocolJSON(raw, &r.value) != nil {
		return errDatabaseNetworkCapture
	}
	return nil
}

func networkCaptureAuthority(a domain.HostingAuthority) bool {
	return networkCaptureID.MatchString(a.OperationID) && readinessResponseID.MatchString(a.RunID) && readinessResponseID.MatchString(a.LeaseID) && readinessResponseID.MatchString(a.Fence) && a.Attempt > 0 && a.Attempt <= math.MaxInt32
}

func validateNetworkCapture(c domain.HostingDatabaseNetworkCapture, runner, id string) bool {
	if !readinessResponseID.MatchString(runner) || c.Plan.Validate() != nil || c.ID != id || c.ID != c.Plan.OperationID || c.RunID != "capture_"+id || c.Plan.RunnerID != runner || !readinessResponseID.MatchString(c.CreatedBy) || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return false
	}
	switch c.State {
	case "running":
		return c.Receipt == nil
	default:
		return false
	}
}

func (c Client) networkCapturePost(ctx context.Context, path string, in, out any) error {
	if _, protected := ctx.Value(databaseTransportKey{}).(DatabaseTransport); !protected {
		ctx = WithDatabaseTransport(ctx, DatabaseTransport{})
	}
	if err := c.post(ctx, "/api/v1/runners/database-network-captures/"+path, in, out); err != nil {
		var status *APIError
		if errors.As(err, &status) {
			copy := *status
			copy.Detail = ""
			return &copy
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDatabaseNetworkCapture
	}
	return ctx.Err()
}

// ClaimHostingDatabaseNetworkCapture claims one explicit capture for this runner.
// It does not select generic work or advertise a runtime capability.
func (c Client) ClaimHostingDatabaseNetworkCapture(ctx context.Context, runner, id string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	var response networkCaptureResponse[domain.HostingDatabaseNetworkCaptureClaim]
	if !networkCaptureID.MatchString(id) || !readinessResponseID.MatchString(runner) {
		return response.value, errDatabaseNetworkCapture
	}
	err := c.networkCapturePost(ctx, "claim", struct {
		CaptureID string `json:"capture_id"`
	}{id}, &response)
	if err == nil && !validNetworkCaptureClaim(response.value, runner, id) {
		err = errDatabaseNetworkCapture
	}
	if err != nil {
		return domain.HostingDatabaseNetworkCaptureClaim{}, err
	}
	return response.value, nil
}

func validNetworkCaptureClaim(v domain.HostingDatabaseNetworkCaptureClaim, runner, id string) bool {
	l := v.Lease
	now := time.Now()
	return networkCaptureID.MatchString(id) && validateNetworkCapture(v.Capture, runner, id) && l.RunnerID == runner && l.RunID == v.Capture.RunID && networkCaptureAuthority(domain.HostingAuthority{OperationID: id, RunID: l.RunID, LeaseID: l.ID, Attempt: l.Attempt, Fence: l.Fence}) && l.Status == domain.LeaseActive && l.CompletedAt == nil && !l.CreatedAt.IsZero() && !l.CreatedAt.After(now.Add(30*time.Second)) && l.ExpiresAt.After(l.CreatedAt) && l.ExpiresAt.Sub(l.CreatedAt) <= 10*time.Minute && l.ExpiresAt.After(now)
}

// ClaimNextHostingDatabaseNetworkCapture polls existing work for this runner.
// A typed 404 means no eligible capture; no capture identity is supplied by the caller.
func (c Client) ClaimNextHostingDatabaseNetworkCapture(ctx context.Context, runner string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	var response networkCaptureResponse[domain.HostingDatabaseNetworkCaptureClaim]
	if !readinessResponseID.MatchString(runner) {
		return response.value, errDatabaseNetworkCapture
	}
	err := c.networkCapturePost(ctx, "claim-next", struct{}{}, &response)
	if err == nil && !validNetworkCaptureClaim(response.value, runner, response.value.Capture.ID) {
		err = errDatabaseNetworkCapture
	}
	if err != nil {
		return domain.HostingDatabaseNetworkCaptureClaim{}, err
	}
	return response.value, nil
}

// HostingDatabaseNetworkCapturePlan loads the exact target under current authority.
func (c Client) HostingDatabaseNetworkCapturePlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error) {
	var response networkCaptureResponse[domain.HostingDatabaseNetworkCapture]
	if !networkCaptureAuthority(a) || !readinessResponseID.MatchString(runner) {
		return response.value, errDatabaseNetworkCapture
	}
	err := c.networkCapturePost(ctx, "plan", a, &response)
	if err == nil && (!validateNetworkCapture(response.value, runner, a.OperationID) || response.value.RunID != a.RunID) {
		err = errDatabaseNetworkCapture
	}
	if err != nil {
		return domain.HostingDatabaseNetworkCapture{}, err
	}
	return response.value, nil
}

// RecordHostingDatabaseNetworkOrigin checks the ACK against the entire original
// request, including historical authority and sampling time. Replay is not fresh observation.
func (c Client) RecordHostingDatabaseNetworkOrigin(ctx context.Context, runner string, in domain.HostingDatabaseNetworkOriginRequest, accepted domain.HostingDatabaseNetworkCapturePlan) (domain.HostingDatabaseNetworkOriginReceipt, error) {
	var response networkCaptureResponse[domain.HostingDatabaseNetworkOriginReceipt]
	if !networkCaptureAuthority(in.HostingAuthority) || in.Validate() != nil || accepted.ValidateOrigin(in.Origin) != nil || accepted.Hash != in.PlanHash || accepted.OperationID != in.OperationID || accepted.RunnerID != runner || in.RunID != "capture_"+accepted.OperationID {
		return response.value, errDatabaseNetworkCapture
	}
	err := c.networkCapturePost(ctx, "origin", in, &response)
	v := response.value
	if err == nil && (v.ID != in.ReceiptID || v.BodyHash != domain.HostingDocumentHash(in) || v.CreatedAt.IsZero() || v.OperationID != in.OperationID || v.SourceOperationID != accepted.SourceOperationID || v.PlanHash != accepted.Hash || v.OriginHash != in.Origin.DocumentHash() || v.Origin != in.Origin || !v.ObservedAt.Equal(in.ObservedAt) || v.RunnerID != runner) {
		err = errDatabaseNetworkCapture
	}
	if err != nil {
		return domain.HostingDatabaseNetworkOriginReceipt{}, err
	}
	return v, nil
}
