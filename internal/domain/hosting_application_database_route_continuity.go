package domain

import "errors"

const HostingApplicationDatabaseRouteContinuityVersion = "application-database.route-continuity.v1"
const HostingApplicationDatabaseRouteContinuityCapability = "hosting_application_database_route_continuity_v1"

// HostingApplicationDatabaseRouteContinuityInput pins the loaded proxy and all application routes.
type HostingApplicationDatabaseRouteContinuityInput struct {
	ExpectedLoadedGeneration       int64                         `json:"expected_loaded_generation"`
	ExpectedLoadedProxyOperationID string                        `json:"expected_loaded_proxy_operation_id"`
	Routes                         []HostingRouteSwitchSelection `json:"routes"`
}

func CanonicalHostingApplicationDatabaseRouteContinuityInput(in HostingApplicationDatabaseRouteContinuityInput) (HostingApplicationDatabaseRouteContinuityInput, error) {
	c, err := CanonicalHostingStorageRouteContinuityInput(HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: in.ExpectedLoadedGeneration, ExpectedLoadedProxyOperationID: in.ExpectedLoadedProxyOperationID, Routes: in.Routes})
	if err != nil {
		return HostingApplicationDatabaseRouteContinuityInput{}, err
	}
	in.Routes = c.Routes
	return in, nil
}

// HostingApplicationDatabaseRouteContinuity reserves immutable child identities.
// It grants no lease authority and never publishes a healthy candidate.
type HostingApplicationDatabaseRouteContinuity struct {
	Version            string                                         `json:"version"`
	OperationID        string                                         `json:"operation_id"`
	ProxyOperationID   string                                         `json:"proxy_operation_id"`
	FinalizeRunID      string                                         `json:"finalize_run_id"`
	Request            HostingApplicationDatabaseRouteContinuityInput `json:"request"`
	ProxyID            string                                         `json:"proxy_id"`
	LoadedSnapshotHash string                                         `json:"loaded_snapshot_hash"`
	PreviousHealthy    HostingHealthy                                 `json:"previous_healthy"`
	Hash               string                                         `json:"hash"`
}

func (c HostingApplicationDatabaseRouteContinuity) DocumentHash() string {
	c.Hash = ""
	return HostingDocumentHash(c)
}
func (c HostingApplicationDatabaseRouteContinuity) Validate(op HostingOperation) error {
	// Binding state and membership evidence are decoded independently. Compare
	// the complete persisted document, including nested storage provenance.
	if c.Version != HostingApplicationDatabaseRouteContinuityVersion || c.OperationID != op.ID || c.ProxyOperationID != "appdb_proxy_"+op.ID || c.FinalizeRunID != "finalize_"+op.ID || !hostingProxyID.MatchString(c.ProxyOperationID) || !hostingProxyID.MatchString(c.FinalizeRunID) || !hostingProxyID.MatchString(c.ProxyID) || op.RunID != "" && op.RunID != "run_"+op.ID || !imageSHA256.MatchString(c.LoadedSnapshotHash) || c.Hash != c.DocumentHash() || op.ApplicationDatabase == nil || len(op.ApplicationDatabase.Members) != 1 || HostingDocumentHash(c.PreviousHealthy) != HostingDocumentHash(op.ApplicationDatabase.Members[0].ApplicationHealthy) || c.PreviousHealthy.Snapshot.HostPort == op.Snapshot.HostPort {
		return errors.New("invalid application database route continuation")
	}
	canonical, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(c.Request)
	if err != nil || HostingDocumentHash(canonical) != HostingDocumentHash(c.Request) {
		return errors.New("invalid application database route selection")
	}
	for _, pick := range c.Request.Routes {
		if pick.ExpectedSelectedReleaseOperationID != c.PreviousHealthy.OperationID {
			return errors.New("route predecessor mismatch")
		}
	}
	return nil
}

// HostingApplicationDatabaseYieldRequest durably closes only the prepare task.
// Replaying this receipt is historical acknowledgement and grants no effects.
type HostingApplicationDatabaseYieldRequest struct {
	HostingAuthority
	Version          string `json:"version"`
	ReceiptID        string `json:"receipt_id"`
	PlanHash         string `json:"plan_hash"`
	StageHash        string `json:"stage_hash"`
	CandidateHash    string `json:"candidate_hash"`
	ContinuationHash string `json:"continuation_hash"`
}

func (y HostingApplicationDatabaseYieldRequest) Validate() error {
	if y.Version != HostingApplicationDatabaseRouteContinuityVersion || !hostingProxyID.MatchString(y.ReceiptID) || !connectionRequestAuthority(y.HostingAuthority) || y.RunID != "run_"+y.OperationID || !imageSHA256.MatchString(y.PlanHash) || !imageSHA256.MatchString(y.StageHash) || !imageSHA256.MatchString(y.CandidateHash) || !imageSHA256.MatchString(y.ContinuationHash) {
		return errors.New("invalid application database yield")
	}
	return nil
}
func (y HostingApplicationDatabaseYieldRequest) ValidateStage(op HostingOperation, stage HostingApplicationDatabaseStage) error {
	if y.Validate() != nil || op.ApplicationDatabaseRouteContinuity == nil || op.ApplicationDatabaseRouteContinuity.Validate(op) != nil || op.Plan == nil || y.OperationID != op.ID || y.RunID != op.RunID || y.PlanHash != op.Plan.Hash || y.StageHash != stage.Hash || stage.Phase != "ready" || y.CandidateHash != stage.Candidate.DocumentHash() || y.ContinuationHash != op.ApplicationDatabaseRouteContinuity.Hash {
		return errors.New("yield ready candidate mismatch")
	}
	in, err := HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	if err != nil {
		return err
	}
	return stage.Validate(*op.Plan, in)
}
func (y *HostingApplicationDatabaseYieldRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseYieldRequest
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, 16<<10) != nil || HostingApplicationDatabaseYieldRequest(v).Validate() != nil {
		return errors.New("invalid application database yield")
	}
	*y = HostingApplicationDatabaseYieldRequest(v)
	return nil
}

// HostingApplicationDatabaseYieldReceipt exposes correlation without lease authority.
type HostingApplicationDatabaseYieldReceipt struct {
	Version          string `json:"version"`
	ReceiptID        string `json:"receipt_id"`
	PlanHash         string `json:"plan_hash"`
	StageHash        string `json:"stage_hash"`
	CandidateHash    string `json:"candidate_hash"`
	ContinuationHash string `json:"continuation_hash"`
}

func (y HostingApplicationDatabaseYieldRequest) Receipt() HostingApplicationDatabaseYieldReceipt {
	return HostingApplicationDatabaseYieldReceipt{
		Version:          y.Version,
		ReceiptID:        y.ReceiptID,
		PlanHash:         y.PlanHash,
		StageHash:        y.StageHash,
		CandidateHash:    y.CandidateHash,
		ContinuationHash: y.ContinuationHash,
	}
}

// Validate checks only public correlation framing, never live effect authority.
func (y HostingApplicationDatabaseYieldReceipt) Validate() error {
	if y.Version != HostingApplicationDatabaseRouteContinuityVersion || !hostingProxyID.MatchString(y.ReceiptID) || !imageSHA256.MatchString(y.PlanHash) || !imageSHA256.MatchString(y.StageHash) || !imageSHA256.MatchString(y.CandidateHash) || !imageSHA256.MatchString(y.ContinuationHash) {
		return errors.New("invalid application database yield receipt")
	}
	return nil
}
