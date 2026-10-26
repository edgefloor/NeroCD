package runnerclient

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

// HostingApplicationDatabaseStorageWriter submits a writer receipt and validates the returned state.
func (c Client) HostingApplicationDatabaseStorageWriter(ctx context.Context, request domain.HostingApplicationDatabaseStorageWriterRequest, expected domain.HostingOperation) (domain.HostingOperation, error) {
	var reply connectionResponse[domain.HostingOperation]
	if _, err := applicationConnectionInput(expected, request.HostingAuthority); err != nil || request.Validate() != nil || expected.ApplicationDatabaseStorage == nil || request.Receipt.PlanHash != expected.Plan.Hash || request.Receipt.StorageRef != expected.Snapshot.Storage {
		return reply.value, errDatabaseConnection
	}
	if err := c.connectionPost(ctx, "storage/writer", request, &reply); err != nil {
		return domain.HostingOperation{}, err
	}
	out := reply.value
	if !connectionOperationMatches(out, expected) || out.ApplicationDatabaseStorageState == nil || out.ApplicationDatabaseStorageState.StateHash != request.Receipt.Hash || out.ApplicationDatabaseStorageState.ReceiptID != request.Receipt.ReceiptID || out.ApplicationDatabaseStorageState.Phase != request.Receipt.Phase {
		return domain.HostingOperation{}, errDatabaseConnection
	}
	return out, nil
}
// HostingApplicationDatabaseStorageAuthorize requests effect permission bounded by the lease expiry.
func (c Client) HostingApplicationDatabaseStorageAuthorize(ctx context.Context, request domain.HostingApplicationDatabaseStorageEffectRequest, expected domain.HostingOperation, expiry time.Time) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error) {
	var reply connectionResponse[domain.HostingApplicationDatabaseStorageEffectAuthorization]
	if _, err := applicationConnectionInput(expected, request.HostingAuthority); err != nil || request.Validate() != nil || expected.ApplicationDatabaseStorage == nil || !expected.MutationHold || expected.Result != nil || !time.Now().Before(expiry) {
		return reply.value, errDatabaseConnection
	}
	if err := c.connectionPost(ctx, "storage/authorize", request, &reply); err != nil {
		return domain.HostingApplicationDatabaseStorageEffectAuthorization{}, err
	}
	out := reply.value
	now := time.Now()
	if out.Validate() != nil || out.StateHash != request.StateHash || out.AuthorizationID != request.AuthorizationID || out.OperationID != expected.ID || out.PlanHash != expected.Plan.Hash || out.Action != request.Action || out.StageHash != request.StageHash || out.LeaseID != request.LeaseID || out.Attempt != request.Attempt || out.Fence != request.Fence || out.MembershipRevision != expected.ApplicationDatabase.MembershipRevision || !now.Before(out.UseUntil) || out.UseUntil.After(expiry) || out.UseUntil.After(now.Add(30*time.Second)) {
		return domain.HostingApplicationDatabaseStorageEffectAuthorization{}, errDatabaseConnection
	}
	return out, nil
}
// HostingApplicationDatabaseStorageProxyPlan fetches and validates a stopped-writer proxy plan.
func (c Client) HostingApplicationDatabaseStorageProxyPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingApplicationDatabaseStorageProxyPlan, error) {
	var reply connectionResponse[domain.HostingApplicationDatabaseStorageProxyPlan]
	if err := c.connectionPost(ctx, "storage/proxy-plan", a, &reply); err != nil {
		return reply.value, err
	}
	out := reply.value
	op := out.Operation
	if op.ApplicationDatabaseStorage == nil || op.ApplicationDatabaseYield == nil || op.ApplicationDatabaseRouteContinuity == nil || op.Plan == nil || domain.ValidateHostingApplicationDatabaseOperation(op) != nil || op.ApplicationDatabaseStorageState == nil || op.ApplicationDatabaseStorageState.Phase != "stopped" || !op.MutationHold || out.Stage.Hash != op.ApplicationDatabaseYield.StageHash || out.Stage.Candidate.Hash != op.ApplicationDatabaseYield.CandidateHash || out.Stage.Phase != "ready" || out.Storage.CreatePermitted || out.Storage.Storage.Reference() != op.Snapshot.Storage || out.Storage.Materialization.Validate(out.Storage.Storage) != nil {
		return domain.HostingApplicationDatabaseStorageProxyPlan{}, errDatabaseConnection
	}
	return out, nil
}

func effectiveApplicationMemberEndpoint(m domain.HostingDatabaseNetworkMemberProof) string {
	endpoint, _, _, ok := domain.HostingDatabaseMemberEndpoint(m)
	if !ok {
		return ""
	}
	return endpoint.EndpointID
}
