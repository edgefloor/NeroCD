package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
)

var errDatabaseConnection = errors.New("invalid database connection exchange")

type connectionResponse[T any] struct{ value T }

func (r *connectionResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, domain.HostingApplicationDatabaseJournalJSONLimit+1))
	defer clear(raw)
	if err != nil {
		return errDatabaseConnection
	}
	return decodeConnectionResponse(raw, &r.value)
}

func decodeConnectionResponse(raw []byte, out any) error {
	if len(raw) > domain.HostingApplicationDatabaseJournalJSONLimit || !connectionUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return errDatabaseConnection
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errDatabaseConnection
	}
	return nil
}

func connectionUniqueJSON(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || key != strings.ToLower(key) || seen[key] || !connectionUniqueJSON(d) {
				return false
			}
			seen[key] = true
		}
	case '[':
		for d.More() {
			if !connectionUniqueJSON(d) {
				return false
			}
		}
	default:
		return false
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

func connectionOperationValid(op domain.HostingOperation) bool {
	if op.ApplicationDatabase != nil || op.Snapshot.ApplicationDatabase != (domain.HostingApplicationDatabaseRef{}) || op.Plan != nil && op.Plan.ApplicationDatabase != nil {
		return op.ApplicationDatabase != nil && domain.ValidateHostingApplicationDatabaseOperation(op) == nil
	}
	if op.DatabaseMaintenance != nil {
		return domain.ValidateHostingDatabaseMaintenanceOperation(op) == nil
	}
	return domain.ValidateHostingDatabaseUpgradeOperation(op) == nil
}

func connectionOperationMatches(actual, expected domain.HostingOperation) bool {
	return connectionOperationValid(actual) && connectionOperationValid(expected) && actual.ID == expected.ID && actual.ProjectID == expected.ProjectID && actual.ResourceID == expected.ResourceID && actual.BindingID == expected.BindingID && domain.HostingDocumentHash(actual.ApplicationDatabaseRouteContinuity) == domain.HostingDocumentHash(expected.ApplicationDatabaseRouteContinuity) && domain.HostingDocumentHash(actual.ApplicationDatabaseStorage) == domain.HostingDocumentHash(expected.ApplicationDatabaseStorage) && actual.RunID == expected.RunID && actual.TaskMode == expected.TaskMode && actual.ReconciliationRunID == expected.ReconciliationRunID && actual.Snapshot == expected.Snapshot && domain.HostingDocumentHash(actual.ApplicationDatabase) == domain.HostingDocumentHash(expected.ApplicationDatabase) && domain.HostingDocumentHash(actual.DatabaseMaintenance) == domain.HostingDocumentHash(expected.DatabaseMaintenance) && domain.HostingDocumentHash(actual.Plan) == domain.HostingDocumentHash(expected.Plan) && domain.HostingDocumentHash(actual.Release) == domain.HostingDocumentHash(expected.Release)
}

func applicationConnectionInput(op domain.HostingOperation, a domain.HostingAuthority) (domain.HostingApplicationDatabasePlanInput, error) {
	if a.Attempt < 1 || a.Attempt > math.MaxInt32 || !readinessResponseID.MatchString(a.LeaseID) || !readinessResponseID.MatchString(a.Fence) || op.ApplicationDatabase == nil || op.Plan == nil || !connectionOperationValid(op) || op.ID != a.OperationID || op.TaskMode != "deploy" && op.TaskMode != "observe" && op.TaskMode != "finalize" && op.TaskMode != "storage_recover" || op.TaskMode == "storage_recover" && (op.ApplicationDatabaseStorage == nil || a.RunID != "storage_recover_"+op.ID) || op.TaskMode == "deploy" && op.RunID != a.RunID || op.TaskMode == "observe" && op.ReconciliationRunID != a.RunID || op.TaskMode == "finalize" && (op.ApplicationDatabaseRouteContinuity == nil || op.ApplicationDatabaseYield == nil || op.ApplicationDatabaseRouteContinuity.FinalizeRunID != a.RunID) {
		return domain.HostingApplicationDatabasePlanInput{}, errDatabaseConnection
	}
	return domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
}

func (c Client) connectionPost(ctx context.Context, path string, request, response any) error {
	if _, protected := ctx.Value(databaseTransportKey{}).(DatabaseTransport); !protected {
		ctx = WithDatabaseTransport(ctx, DatabaseTransport{})
	}
	if err := c.post(ctx, "/api/v1/runners/hosting/database-binding/"+path, request, response); err != nil {
		var status *APIError
		if errors.As(err, &status) {
			copy := *status
			copy.Detail = ""
			return &copy
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDatabaseConnection
	}
	return ctx.Err()
}

// HostingApplicationDatabaseStage publishes an already-durable exact stage.
// Its acknowledgement is historical evidence, never effect permission.
func (c Client) HostingApplicationDatabaseStage(ctx context.Context, request domain.HostingApplicationDatabaseStageRequest, expected domain.HostingOperation) (domain.HostingOperation, error) {
	var reply connectionResponse[domain.HostingOperation]
	in, err := applicationConnectionInput(expected, request.HostingAuthority)
	if err != nil || request.Validate() != nil || request.Stage.Validate(*expected.Plan, in) != nil {
		return reply.value, errDatabaseConnection
	}
	if err = c.connectionPost(ctx, "stage", request, &reply); err != nil {
		return domain.HostingOperation{}, err
	}
	if !connectionOperationMatches(reply.value, expected) {
		return domain.HostingOperation{}, errDatabaseConnection
	}
	return reply.value, nil
}

// HostingApplicationDatabaseRetirementPlan loads the exact prior member target.
func (c Client) HostingApplicationDatabaseRetirementPlan(ctx context.Context, a domain.HostingAuthority, expected domain.HostingOperation) (domain.HostingDatabaseBindingEndpointPlan, error) {
	var reply connectionResponse[domain.HostingDatabaseBindingEndpointPlan]
	in, err := applicationConnectionInput(expected, a)
	if err != nil || len(in.Members) != 1 {
		return reply.value, errDatabaseConnection
	}
	if err = c.connectionPost(ctx, "retirement-plan", a, &reply); err != nil {
		return domain.HostingDatabaseBindingEndpointPlan{}, err
	}
	if !connectionRetirementMatches(reply.value, expected, in) {
		return domain.HostingDatabaseBindingEndpointPlan{}, errDatabaseConnection
	}
	return reply.value, nil
}

func connectionRetirementMatches(p domain.HostingDatabaseBindingEndpointPlan, op domain.HostingOperation, in domain.HostingApplicationDatabasePlanInput) bool {
	return len(in.Members) == 1 && p.Validate(in.Binding) == nil && p.OperationID == op.ID && p.Action == "retire" && p.Target == in.Members[0].EndpointPlan.Target && p.ExpectedEndpointID == effectiveApplicationMemberEndpoint(in.Members[0]) && p.MembershipRevision == in.MembershipRevision+2
}

// HostingApplicationDatabaseRetirement publishes an immutable retirement body.
func (c Client) HostingApplicationDatabaseRetirement(ctx context.Context, request domain.HostingApplicationDatabaseRetirementRequest, plan domain.HostingDatabaseBindingEndpointPlan, expected domain.HostingOperation) (domain.HostingOperation, error) {
	var reply connectionResponse[domain.HostingOperation]
	in, err := applicationConnectionInput(expected, request.HostingAuthority)
	if err != nil || request.Validate() != nil || request.PlanHash != expected.Plan.Hash || !connectionRetirementMatches(plan, expected, in) || request.Receipt.Validate(plan, in.Binding) != nil {
		return reply.value, errDatabaseConnection
	}
	if err = c.connectionPost(ctx, "retirement", request, &reply); err != nil {
		return domain.HostingOperation{}, err
	}
	if !connectionOperationMatches(reply.value, expected) {
		return domain.HostingOperation{}, errDatabaseConnection
	}
	return reply.value, nil
}

// HostingApplicationDatabaseAuthorize requests fresh permission for one effect.
// The caller checks this fixed expiry immediately before invoking the effect.
func (c Client) HostingApplicationDatabaseAuthorize(ctx context.Context, request domain.HostingApplicationDatabaseEffectRequest, expected domain.HostingOperation, revision int64, leaseExpiry time.Time) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
	var reply connectionResponse[domain.HostingApplicationDatabaseEffectAuthorization]
	in, err := applicationConnectionInput(expected, request.HostingAuthority)
	if err != nil || revision != in.MembershipRevision || request.Validate() != nil || expected.TaskMode != "deploy" && (expected.TaskMode != "finalize" || request.Action != "retire") || expected.Result != nil || expected.RestoreOwnershipHeld || !expected.MutationHold || expected.State != "applying" && expected.State != "verifying" || !time.Now().Before(leaseExpiry) {
		return reply.value, errDatabaseConnection
	}
	if err = c.connectionPost(ctx, "authorize", request, &reply); err != nil {
		return domain.HostingApplicationDatabaseEffectAuthorization{}, err
	}
	v := reply.value
	now := time.Now()
	if v.Validate() != nil || v.AuthorizationID != request.AuthorizationID || v.OperationID != request.OperationID || v.PlanHash != expected.Plan.Hash || v.Action != request.Action || v.StageHash != request.ExpectedStageHash || v.LeaseID != request.LeaseID || v.Attempt != request.Attempt || v.Fence != request.Fence || v.MembershipRevision != revision || !now.Before(v.UseUntil) || v.UseUntil.After(now.Add(30*time.Second)) || v.UseUntil.After(leaseExpiry) {
		return domain.HostingApplicationDatabaseEffectAuthorization{}, errDatabaseConnection
	}
	return v, nil
}

// HostingApplicationDatabaseState loads read-only evidence without new authority.
func (c Client) HostingApplicationDatabaseState(ctx context.Context, a domain.HostingAuthority, expected domain.HostingOperation) (domain.HostingApplicationDatabaseExecutionState, error) {
	var reply connectionResponse[domain.HostingApplicationDatabaseExecutionState]
	in, err := applicationConnectionInput(expected, a)
	if err != nil {
		return reply.value, errDatabaseConnection
	}
	if err = c.connectionPost(ctx, "state", a, &reply); err != nil {
		return domain.HostingApplicationDatabaseExecutionState{}, err
	}
	v := reply.value
	revision := in.MembershipRevision
	settled := expected.State == "succeeded" && expected.Result != nil && expected.Result.Ready && !expected.Result.Uncertain && !expected.MutationHold
	if settled {
		revision++
		if len(in.Members) == 1 {
			revision++
		}
	}
	if v.Validate() != nil || v.OperationID != a.OperationID || v.PlanHash != expected.Plan.Hash || v.MembershipRevision != revision || expected.MutationHold && v.HoldOperationID != expected.ID || settled && v.HoldOperationID != "" || v.Stage != nil && v.Stage.Validate(*expected.Plan, in) != nil || v.RetirementPlan != nil && (v.Stage == nil || v.Stage.Phase != "ready" || !connectionRetirementMatches(*v.RetirementPlan, expected, in)) || v.RetirementReceipt != nil && v.RetirementReceipt.Validate(*v.RetirementPlan, in.Binding) != nil {
		return domain.HostingApplicationDatabaseExecutionState{}, errDatabaseConnection
	}
	return v, nil
}

// HostingDatabaseMaintenanceCredentials retains the existing protected wire and
// prepared-only deploy window under the complete maintenance sidecar.
func (c Client) HostingDatabaseMaintenanceCredentials(ctx context.Context, runnerID string, request domain.HostingDatabaseCredentialRequest, op domain.HostingOperation, options RuntimeEnvironmentTransport) (*hostingdatabaseprotocol.Credentials, error) {
	if op.DatabaseMaintenance == nil || op.Plan == nil || domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil || op.ID != request.Authority.OperationID || op.Snapshot.RunnerID != runnerID || op.TaskMode != request.Mode || op.RestoreOwnershipHeld || request.Mode == "deploy" && (op.RunID != request.Authority.RunID || op.State != "prepared" || op.Result != nil) || request.Mode == "observe" && (op.ReconciliationRunID != request.Authority.RunID || !op.ReconcileRequested || op.Result == nil || op.Result.ContainerID == "") {
		return nil, ErrDatabaseCredentials
	}
	return c.hostingDatabaseCredentials(ctx, runnerID, request, *op.Plan, options, true)
}

// HostingApplicationDatabaseYield acknowledges immutable preparation history.
func (c Client) HostingApplicationDatabaseYield(ctx context.Context, in domain.HostingApplicationDatabaseYieldRequest, expected domain.HostingOperation) (domain.HostingOperation, error) {
	var reply connectionResponse[domain.HostingOperation]
	if in.Validate() != nil || expected.ApplicationDatabaseRouteContinuity == nil || expected.TaskMode != "deploy" || in.RunID != expected.RunID || expected.Plan == nil || in.PlanHash != expected.Plan.Hash || in.ContinuationHash != expected.ApplicationDatabaseRouteContinuity.Hash {
		return reply.value, errDatabaseConnection
	}
	if err := c.connectionPost(ctx, "yield", in, &reply); err != nil {
		return reply.value, err
	}
	receipt := in.Receipt()
	if !connectionOperationMatches(reply.value, expected) || reply.value.ApplicationDatabaseYield == nil || *reply.value.ApplicationDatabaseYield != receipt {
		return domain.HostingOperation{}, errDatabaseConnection
	}
	return reply.value, nil
}
