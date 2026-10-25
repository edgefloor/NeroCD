package domain

import (
	"errors"
	"math"
	"time"
)

// Connected execution requires explicit opt-in in addition to the existing
// image/runtime/database/storage capabilities.
const (
	HostingApplicationDatabaseCapability = "hosting_application_database_v1"
	HostingDatabaseMaintenanceCapability = "hosting_database_membership_maintenance_v1"
	HostingDatabaseConnectionJSONLimit   = 32 << 10
)

var (
	// ErrInvalidHostingDatabaseConnection omits saved target and credential details.
	ErrInvalidHostingDatabaseConnection = errors.New("invalid database connection request")
	// ErrHostingDatabaseRecoveryObserveOnly reports the supported recovery limit.
	ErrHostingDatabaseRecoveryObserveOnly = errors.New("database_connection_recovery_observe_only")
)

// HostingDatabaseConnectionInput captures both parts of one atomic owner request.
type HostingDatabaseConnectionInput struct {
	Binding    HostingDatabaseBindingInput `json:"binding"`
	Deployment HostingDeploymentInput      `json:"deployment"`
}

// HostingDatabaseConnection reports durable progress without credential values.
// Waiting for capture does not imply that automatic capture is enabled on a runner.
type HostingDatabaseConnection struct {
	ID                     string    `json:"id"`
	ProjectID              string    `json:"project_id"`
	ApplicationResourceID  string    `json:"application_resource_id"`
	DatabaseResourceID     string    `json:"database_resource_id"`
	BindingID              string    `json:"binding_id"`
	BindingHash            string    `json:"binding_hash"`
	State                  string    `json:"state"`
	ReasonCode             string    `json:"reason_code"`
	CaptureID              string    `json:"capture_id"`
	UpgradeOperationID     string    `json:"upgrade_operation_id"`
	ApplicationOperationID string    `json:"application_operation_id"`
	MembershipRevision     int64     `json:"membership_revision"`
	HoldOperationID        string    `json:"hold_operation_id,omitempty"`
	RecoveryLimit          string    `json:"recovery_limit,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// HostingDatabaseConnectionOption contains only bounded selection metadata.
type HostingDatabaseConnectionOption struct {
	ResourceID    string `json:"resource_id"`
	Name          string `json:"name"`
	Engine        string `json:"engine"`
	Generation    int64  `json:"generation"`
	DestinationID string `json:"destination_id"`
	Status        string `json:"status"`
	ReasonCode    string `json:"reason_code"`
}

// HostingDatabaseMaintenanceInput selects current metadata, never caller proof.
type HostingDatabaseMaintenanceInput struct {
	ExpectedGeneration         int64  `json:"expected_generation"`
	ExpectedMembershipRevision int64  `json:"expected_membership_revision"`
	IdempotencyKey             string `json:"idempotency_key"`
}

// HostingDatabaseConnectionRecoveryInput admits observation only. Effectful
// recovery needs a future descriptor; it cannot relabel the original candidate.
type HostingDatabaseConnectionRecoveryInput struct {
	ApplicationDatabaseStorage *HostingApplicationDatabaseStorageInput `json:"application_database_storage,omitempty"`
	StorageStateHash           string                                  `json:"storage_state_hash,omitempty"`
	Action                     string                                  `json:"action"`
	OperationID                string                                  `json:"operation_id"`
	StageHash                  string                                  `json:"stage_hash"`
	CandidateHash              string                                  `json:"candidate_hash"`
	ExpectedMembershipRevision int64                                   `json:"expected_membership_revision"`
	IdempotencyKey             string                                  `json:"idempotency_key"`
}

// HostingApplicationDatabaseStageRequest binds one immutable stage to its lease.
type HostingApplicationDatabaseStageRequest struct {
	HostingAuthority
	Stage HostingApplicationDatabaseStage `json:"stage"`
}

// HostingApplicationDatabaseRetirementRequest records ordered predecessor
// retirement evidence under the same operation and fenced lease.
type HostingApplicationDatabaseRetirementRequest struct {
	HostingAuthority
	PlanHash string                                `json:"plan_hash"`
	Receipt  HostingDatabaseBindingEndpointReceipt `json:"receipt"`
}

// HostingApplicationDatabaseEffectRequest requests fresh effect authority. A
// stage acknowledgement is immutable history and cannot replace this request.
type HostingApplicationDatabaseEffectRequest struct {
	HostingAuthority
	AuthorizationID   string `json:"authorization_id"`
	ExpectedStageHash string `json:"expected_stage_hash"`
	Action            string `json:"action"`
}

// HostingApplicationDatabaseEffectAuthorization has a fixed, lease-bounded
// expiry. Replaying its identifier never extends that expiry or the lease.
type HostingApplicationDatabaseEffectAuthorization struct {
	AuthorizationID    string    `json:"authorization_id"`
	OperationID        string    `json:"operation_id"`
	PlanHash           string    `json:"plan_hash"`
	Action             string    `json:"action"`
	StageHash          string    `json:"stage_hash"`
	LeaseID            string    `json:"lease_id"`
	Attempt            int       `json:"attempt"`
	Fence              string    `json:"fence"`
	MembershipRevision int64     `json:"membership_revision"`
	UseUntil           time.Time `json:"use_until"`
}

// HostingApplicationDatabaseExecutionState is authenticated read-only retained
// evidence. It does not authorize any create, configuration or retirement effect.
type HostingApplicationDatabaseExecutionState struct {
	Storage            *HostingStorageCreateAck                        `json:"storage,omitempty"`
	StorageWriter      *HostingApplicationDatabaseStorageWriterRequest `json:"storage_writer,omitempty"`
	StorageRecovery    *HostingApplicationDatabaseStorageRecoveryPlan  `json:"storage_recovery,omitempty"`
	OperationID        string                                          `json:"operation_id"`
	PlanHash           string                                          `json:"plan_hash"`
	Stage              *HostingApplicationDatabaseStage                `json:"stage,omitempty"`
	RetirementPlan     *HostingDatabaseBindingEndpointPlan             `json:"retirement_plan,omitempty"`
	RetirementReceipt  *HostingDatabaseBindingEndpointReceipt          `json:"retirement_receipt,omitempty"`
	MembershipRevision int64                                           `json:"membership_revision"`
	HoldOperationID    string                                          `json:"hold_operation_id"`
}

// Validate checks state framing; callers must compare it with the operation proof.
func (out HostingApplicationDatabaseExecutionState) Validate() error {
	if out.Storage != nil && (out.Storage.CreatePermitted || out.Storage.Materialization.Validate(out.Storage.Storage) != nil) {
		return ErrInvalidHostingStorage
	}
	if out.StorageWriter != nil && (out.Storage == nil || out.StorageWriter.Validate() != nil || out.StorageWriter.OperationID != out.OperationID || out.StorageWriter.Receipt.PlanHash != out.PlanHash || out.StorageWriter.Receipt.StorageRef != out.Storage.Storage.Reference()) {
		return ErrInvalidHostingStorage
	}
	if out.StorageRecovery != nil && (out.Storage == nil || out.StorageRecovery.Validate() != nil || out.StorageRecovery.OperationID != out.OperationID || out.StorageRecovery.StorageRef != out.Storage.Storage.Reference()) {
		return ErrInvalidHostingStorage
	}

	if !hostingProxyID.MatchString(out.OperationID) || !imageSHA256.MatchString(out.PlanHash) || out.MembershipRevision < 1 || out.HoldOperationID != "" && out.HoldOperationID != out.OperationID {
		return ErrInvalidHostingDatabaseConnection
	}
	if out.Stage != nil && (out.Stage.OperationID != out.OperationID || out.Stage.PlanHash != out.PlanHash || out.Stage.Hash != out.Stage.DocumentHash()) {
		return ErrInvalidHostingDatabaseConnection
	}
	if out.RetirementPlan != nil && (out.RetirementPlan.OperationID != out.OperationID || out.RetirementPlan.Action != "retire" || out.RetirementPlan.Hash != out.RetirementPlan.DocumentHash()) {
		return ErrInvalidHostingDatabaseConnection
	}
	if out.RetirementReceipt != nil && (out.RetirementPlan == nil || !out.RetirementReceipt.validShape() || out.RetirementReceipt.OperationID != out.OperationID || out.RetirementReceipt.PlanHash != out.RetirementPlan.Hash) {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate checks the closed initial request without authorizing saved targets.
// Both CAS inputs refer to the runtime generation before binding injection.
func (in HostingDatabaseConnectionInput) Validate() error {
	if in.Binding.Validate() != nil || ValidateHostingDeployment(in.Deployment) != nil || in.Deployment.ApplicationDatabaseStorage != nil || in.Deployment.StorageRouteContinuity != nil || in.Deployment.ApplicationDatabaseRouteContinuity != nil || in.Deployment.ExpectedGeneration != in.Binding.ExpectedResourceGeneration || in.Deployment.ExpectedRuntimeConfigurationGeneration != in.Binding.ExpectedRuntimeGeneration {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate checks generation and idempotency selection without loading members.
func (in HostingDatabaseMaintenanceInput) Validate() error {
	if in.ExpectedGeneration < 1 || in.ExpectedMembershipRevision < 1 || !validRuntimeReferenceID(in.IdempotencyKey) {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate admits observation shape only, including an unresolved unknown candidate.
func (in HostingDatabaseConnectionRecoveryInput) Validate() error {
	if in.Action != "observe" && in.Action != "resume_predecessor" && in.Action != "complete_replacement" {
		return ErrHostingDatabaseRecoveryObserveOnly
	}
	if in.Action == "observe" && (in.ApplicationDatabaseStorage != nil || in.StorageStateHash != "") || in.Action != "observe" && (in.ApplicationDatabaseStorage == nil || in.ApplicationDatabaseStorage.Validate() != nil || !imageSHA256.MatchString(in.StorageStateHash)) {
		return ErrInvalidHostingDatabaseConnection
	}
	known := imageSHA256.MatchString(in.StageHash) && imageSHA256.MatchString(in.CandidateHash)
	unknown := in.StageHash == "" && in.CandidateHash == ""
	if !hostingProxyID.MatchString(in.OperationID) || !validRuntimeReferenceID(in.IdempotencyKey) || in.ExpectedMembershipRevision < 1 || !known && !unknown {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

func connectionRequestAuthority(a HostingAuthority) bool {
	return hostingProxyID.MatchString(a.OperationID) && hostingProxyID.MatchString(a.RunID) && validRuntimeReferenceID(a.LeaseID) && validRuntimeReferenceID(a.Fence) && a.Attempt > 0 && a.Attempt <= math.MaxInt32
}

// Validate checks authority framing; stage ordering requires the saved proof.
func (in HostingApplicationDatabaseStageRequest) Validate() error {
	if !connectionRequestAuthority(in.HostingAuthority) || in.OperationID != in.Stage.OperationID || in.Stage.Hash != in.Stage.DocumentHash() {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate checks retirement receipt framing without granting live authority.
func (in HostingApplicationDatabaseRetirementRequest) Validate() error {
	if !connectionRequestAuthority(in.HostingAuthority) || !imageSHA256.MatchString(in.PlanHash) || in.Receipt.OperationID != in.OperationID || !in.Receipt.validShape() {
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate requires an explicit action and its expected durable stage.
func (in HostingApplicationDatabaseEffectRequest) Validate() error {
	if !connectionRequestAuthority(in.HostingAuthority) || !hostingStorageRequest.MatchString(in.AuthorizationID) {
		return ErrInvalidHostingDatabaseConnection
	}
	switch in.Action {
	case "create":
		if in.ExpectedStageHash != "" {
			return ErrInvalidHostingDatabaseConnection
		}
	case "configure", "start", "retire":
		if !imageSHA256.MatchString(in.ExpectedStageHash) {
			return ErrInvalidHostingDatabaseConnection
		}
	default:
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// Validate checks response shape; freshness and correlation require live context.
func (out HostingApplicationDatabaseEffectAuthorization) Validate() error {
	if !hostingProxyID.MatchString(out.OperationID) || !validRuntimeReferenceID(out.LeaseID) || !validRuntimeReferenceID(out.Fence) || out.Attempt < 1 || out.Attempt > math.MaxInt32 || !hostingStorageRequest.MatchString(out.AuthorizationID) || !imageSHA256.MatchString(out.PlanHash) || out.MembershipRevision < 1 || out.UseUntil.IsZero() {
		return ErrInvalidHostingDatabaseConnection
	}
	switch out.Action {
	case "create":
		if out.StageHash != "" {
			return ErrInvalidHostingDatabaseConnection
		}
	case "configure", "start", "retire":
		if !imageSHA256.MatchString(out.StageHash) {
			return ErrInvalidHostingDatabaseConnection
		}
	default:
		return ErrInvalidHostingDatabaseConnection
	}
	return nil
}

// UnmarshalJSON closes and bounds the complete initial connection request.
func (in *HostingDatabaseConnectionInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseConnectionInput
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingDatabaseConnectionInput(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*in = HostingDatabaseConnectionInput(v)
	return nil
}

// UnmarshalJSON rejects ambiguous, open and oversized maintenance selection.
func (in *HostingDatabaseMaintenanceInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseMaintenanceInput
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingDatabaseMaintenanceInput(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*in = HostingDatabaseMaintenanceInput(v)
	return nil
}

// UnmarshalJSON closes recovery selection and preserves the observe-only error.
func (in *HostingDatabaseConnectionRecoveryInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseConnectionRecoveryInput
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	if err := HostingDatabaseConnectionRecoveryInput(v).Validate(); err != nil {
		return err
	}
	*in = HostingDatabaseConnectionRecoveryInput(v)
	return nil
}

// UnmarshalJSON bounds stage and authority framing before contextual validation.
func (in *HostingApplicationDatabaseStageRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseStageRequest
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingApplicationDatabaseStageRequest(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*in = HostingApplicationDatabaseStageRequest(v)
	return nil
}

// UnmarshalJSON closes retirement evidence and rejects supplied credential data.
func (in *HostingApplicationDatabaseRetirementRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseRetirementRequest
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingApplicationDatabaseRetirementRequest(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*in = HostingApplicationDatabaseRetirementRequest(v)
	return nil
}

// UnmarshalJSON closes effect selection; decoding never grants permission.
func (in *HostingApplicationDatabaseEffectRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseEffectRequest
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingApplicationDatabaseEffectRequest(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*in = HostingApplicationDatabaseEffectRequest(v)
	return nil
}

// UnmarshalJSON closes and bounds retained read-only execution state.
func (out *HostingApplicationDatabaseExecutionState) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseExecutionState
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingApplicationDatabaseExecutionState(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*out = HostingApplicationDatabaseExecutionState(v)
	return nil
}

// UnmarshalJSON rejects malformed or ambiguous effect authorization metadata.
func (out *HostingApplicationDatabaseEffectAuthorization) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseEffectAuthorization
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingDatabaseConnectionJSONLimit) != nil || HostingApplicationDatabaseEffectAuthorization(v).Validate() != nil {
		return ErrInvalidHostingDatabaseConnection
	}
	*out = HostingApplicationDatabaseEffectAuthorization(v)
	return nil
}
