package domain

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

func validHostingHash(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// HostingProxyRecoveryPlanLifetime is deliberately short. Replays never renew it.
const HostingProxyRecoveryPlanLifetime = 30 * time.Second

// HostingProxyRecoveryInput binds review to the operator's observed receipt and generations.
type HostingProxyRecoveryInput struct {
	IdempotencyKey              string `json:"idempotency_key"`
	ExpectedLoadedGeneration    int64  `json:"expected_loaded_generation"`
	ExpectedDesiredGeneration   int64  `json:"expected_desired_generation"`
	ExpectedCompleteReceiptID   string `json:"expected_complete_receipt_id"`
	ExpectedCompleteReceiptHash string `json:"expected_complete_receipt_hash"`
}

// HostingProxyRecoveryApplyInput accepts only an exact reviewed plan.
type HostingProxyRecoveryApplyInput struct {
	IdempotencyKey string `json:"idempotency_key"`
	PlanHash       string `json:"plan_hash"`
}

// HostingProxyRecoveryRoute records a metadata repair without changing delegation.
type HostingProxyRecoveryRoute struct {
	RouteID                    string                  `json:"route_id"`
	Version                    int64                   `json:"version"`
	SelectedReleaseOperationID string                  `json:"selected_release_operation_id"`
	Enabled                    bool                    `json:"enabled"`
	Authorized                 bool                    `json:"authorized"`
	PriorTarget                HostingProxyRouteTarget `json:"prior_target"`
}

// HostingProxyRecoveryReview contains no credentials or certificate material.
// FactsHash also binds unselected routes, grants and completed attempt evidence.
type HostingProxyRecoveryReview struct {
	Version             int                         `json:"version"`
	OperationID         string                      `json:"operation_id"`
	ProxyID             string                      `json:"proxy_id"`
	ActorID             string                      `json:"actor_id"`
	LoadedGeneration    int64                       `json:"loaded_generation"`
	DesiredGeneration   int64                       `json:"desired_generation"`
	PriorOperationID    string                      `json:"prior_operation_id"`
	PriorSnapshotHash   string                      `json:"prior_snapshot_hash"`
	CompleteReceiptID   string                      `json:"complete_receipt_id"`
	CompleteReceiptHash string                      `json:"complete_receipt_hash"`
	EngineOutcome       string                      `json:"engine_outcome"`
	FactsHash           string                      `json:"facts_hash"`
	Routes              []HostingProxyRecoveryRoute `json:"routes"`
	ConvergenceRoutes   []HostingProxyRouteTarget   `json:"convergence_routes"`
}

// HostingProxyRecoveryPlan is immutable review evidence, not execution authority.
type HostingProxyRecoveryPlan struct {
	ID        string                     `json:"id"`
	Review    HostingProxyRecoveryReview `json:"review"`
	PlanHash  string                     `json:"plan_hash"`
	CreatedAt time.Time                  `json:"created_at"`
	ExpiresAt time.Time                  `json:"expires_at"`
}

// HostingProxyRecoveryResolution records metadata settlement separately from the engine result.
type HostingProxyRecoveryResolution struct {
	ID                     string                       `json:"id"`
	OperationID            string                       `json:"operation_id"`
	PlanID                 string                       `json:"plan_id"`
	PlanHash               string                       `json:"plan_hash"`
	ActorID                string                       `json:"actor_id"`
	EngineOutcome          string                       `json:"engine_outcome"`
	CompleteReceiptID      string                       `json:"complete_receipt_id"`
	CompleteReceiptHash    string                       `json:"complete_receipt_hash"`
	ConvergenceOperationID string                       `json:"convergence_operation_id"`
	Routes                 []HostingProxyRecoveryChange `json:"routes"`
	CreatedAt              time.Time                    `json:"created_at"`
}

// HostingProxyRecoveryChange preserves the exact metadata transition.
type HostingProxyRecoveryChange struct {
	RouteID                  string `json:"route_id"`
	BeforeVersion            int64  `json:"before_version"`
	AfterVersion             int64  `json:"after_version"`
	BeforeReleaseOperationID string `json:"before_release_operation_id"`
	AfterReleaseOperationID  string `json:"after_release_operation_id"`
	Enabled                  bool   `json:"enabled"`
}

// HostingProxyRecoveryStatus explains eligibility without asserting current health.
type HostingProxyRecoveryStatus struct {
	OperationID string                          `json:"operation_id"`
	Eligible    bool                            `json:"eligible"`
	Reason      string                          `json:"reason"`
	Review      *HostingProxyRecoveryReview     `json:"review,omitempty"`
	Resolution  *HostingProxyRecoveryResolution `json:"resolution,omitempty"`
}

func recoveryKey(key string) bool {
	return key != "" && len(key) <= 128 && !strings.ContainsFunc(key, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

// ValidateHostingProxyRecoveryInput rejects unspecified predecessor observations.
func ValidateHostingProxyRecoveryInput(in HostingProxyRecoveryInput) error {
	if !recoveryKey(in.IdempotencyKey) || in.ExpectedLoadedGeneration < 1 || in.ExpectedDesiredGeneration < in.ExpectedLoadedGeneration || !hostingProxyID.MatchString(in.ExpectedCompleteReceiptID) || !validHostingHash(in.ExpectedCompleteReceiptHash) {
		return errors.New("exact recovery receipt and generations are required")
	}
	return nil
}

// ValidateHostingProxyRecoveryApplyInput rejects generic unlock requests.
func ValidateHostingProxyRecoveryApplyInput(in HostingProxyRecoveryApplyInput) error {
	if !recoveryKey(in.IdempotencyKey) || !validHostingHash(in.PlanHash) {
		return errors.New("exact recovery plan hash and idempotency key are required")
	}
	return nil
}

// HostingProxyRecoveryPlanHash bounds and hashes a typed review and its fixed expiry.
func HostingProxyRecoveryPlanHash(plan HostingProxyRecoveryPlan) (string, error) {
	if plan.Review.Version != 1 || len(plan.Review.Routes) < 1 || len(plan.Review.Routes) > HostingProxyMaxRoutes || len(plan.Review.ConvergenceRoutes) > HostingProxyMaxRoutes || !plan.ExpiresAt.Equal(plan.CreatedAt.Add(HostingProxyRecoveryPlanLifetime)) {
		return "", errors.New("invalid recovery review")
	}
	plan.PlanHash = ""
	body, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	if len(body) > 128<<10 {
		return "", errors.New("recovery review exceeds limit")
	}
	return HostingDocumentHash(plan), nil
}
