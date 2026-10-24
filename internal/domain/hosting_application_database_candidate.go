package domain

import (
	"math"
)

// HostingApplicationDatabaseCandidateVersion identifies actual stopped-container
// evidence. It is never a healthy release or permission to create another one.
const HostingApplicationDatabaseCandidateVersion = "application-database.candidate.v1"

// HostingApplicationDatabaseCandidate records an inspected create result. A
// missing container ID is uncertainty and cannot produce an attach plan.
type HostingApplicationDatabaseCandidate struct {
	Version          string `json:"version"`
	OperationID      string `json:"operation_id"`
	PlanHash         string `json:"plan_hash"`
	CreateNonce      string `json:"create_nonce"`
	ContainerID      string `json:"container_id"`
	ImageID          string `json:"image_id"`
	DaemonID         string `json:"daemon_id"`
	DefaultNetworkID string `json:"default_network_id"`
	Hash             string `json:"hash"`
}

// DocumentHash binds the complete stopped create receipt.
func (c HostingApplicationDatabaseCandidate) DocumentHash() string {
	c.Hash = ""
	return HostingDocumentHash(c)
}

// Validate links actual create evidence to the exact immutable plan and excludes
// every retained or database container. Live daemon inspection is still required.
func (c HostingApplicationDatabaseCandidate) Validate(p HostingApplyPlan, in HostingApplicationDatabasePlanInput) error {
	if ValidateHostingApplicationDatabasePlan(p, in) != nil || ValidateHostingDatabasePlanV2(in.DatabasePlan) != nil || c.Version != HostingApplicationDatabaseCandidateVersion || c.Hash != c.DocumentHash() || c.OperationID != in.OperationID || c.PlanHash != p.Hash || c.CreateNonce != in.CreateNonce || !databaseBindingDockerID(c.ContainerID) || c.ImageID != p.Release.ImageID || c.DaemonID != p.Release.DaemonID || !databaseBindingDockerID(c.DefaultNetworkID) || c.DefaultNetworkID == in.Origin.NetworkID || c.ContainerID == in.DatabaseHealthy.ContainerID || len(in.Members) > 1 || len(in.Members) == 1 && in.MembershipRevision > math.MaxInt64-2 {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	for _, m := range in.Members {
		if c.ContainerID == m.ApplicationHealthy.ContainerID {
			return ErrInvalidHostingApplicationDatabasePlan
		}
	}
	return nil
}

// CompileHostingApplicationDatabaseAttachPlan pins a real stopped candidate
// before health or endpoint observation. It never fabricates HostingHealthy.
func CompileHostingApplicationDatabaseAttachPlan(p HostingApplyPlan, in HostingApplicationDatabasePlanInput, c HostingApplicationDatabaseCandidate) (HostingDatabaseBindingEndpointPlan, error) {
	if c.Validate(p, in) != nil {
		return HostingDatabaseBindingEndpointPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	ep := HostingDatabaseBindingEndpointPlan{Version: HostingDatabaseBindingVersion, OperationID: in.OperationID, BindingHash: in.Binding.Hash, MembershipRevision: in.MembershipRevision + 1, Action: "attach", Target: HostingDatabaseBindingEndpointTarget{ContainerID: c.ContainerID, ContainerOperationID: c.OperationID, ImageID: c.ImageID, ReleaseHash: p.Release.Hash, ContainerPlanHash: p.Hash, NetworkID: in.Origin.NetworkID, NetworkName: in.Binding.NetworkName, DaemonID: c.DaemonID}}
	ep.Hash = ep.DocumentHash()
	if ep.Validate(in.Binding) != nil {
		return HostingDatabaseBindingEndpointPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	return ep, nil
}

// ValidateHostingApplicationDatabaseAttachPlan rejects any rewritten candidate
// target, revision or action, including a self-rehashed endpoint plan.
func ValidateHostingApplicationDatabaseAttachPlan(ep HostingDatabaseBindingEndpointPlan, p HostingApplyPlan, in HostingApplicationDatabasePlanInput, c HostingApplicationDatabaseCandidate) error {
	want, err := CompileHostingApplicationDatabaseAttachPlan(p, in, c)
	if err != nil || ep != want {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}

// UnmarshalJSON rejects partial, open, null and ambiguous create receipts.
func (c *HostingApplicationDatabaseCandidate) UnmarshalJSON(raw []byte) error {
	type wire HostingApplicationDatabaseCandidate
	var v wire
	if decodeApplicationDatabaseDocument(raw, &v, HostingApplicationDatabaseStageJSONLimit) != nil || v.Version != HostingApplicationDatabaseCandidateVersion || !hostingProxyID.MatchString(v.OperationID) || !imageSHA256.MatchString(v.PlanHash) || !applicationDatabaseNonce(v.CreateNonce) || !databaseBindingDockerID(v.ContainerID) || !imageSHA256.MatchString(v.ImageID) || !validRuntimeReferenceID(v.DaemonID) || !databaseBindingDockerID(v.DefaultNetworkID) || v.Hash != HostingApplicationDatabaseCandidate(v).DocumentHash() {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	*c = HostingApplicationDatabaseCandidate(v)
	return nil
}
