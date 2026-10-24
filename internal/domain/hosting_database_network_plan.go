package domain

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
)

// These document versions do not advertise runtime support or grant authority.
const (
	HostingDatabaseNetworkCompilerVersion = "database-hosting.private.membership.v2"
	HostingDatabaseNetworkPlanVersion     = "database-network.private.membership.v2"
)

// ErrInvalidHostingDatabaseNetworkPlan excludes private proof and daemon details.
var ErrInvalidHostingDatabaseNetworkPlan = errors.New("invalid database network plan")

// HostingDatabaseNetworkPredecessor identifies the exact original plan version.
// V1 has no database alias; v2 always has the binding-derived alias.
type HostingDatabaseNetworkPredecessor struct {
	CompilerVersion string `json:"compiler_version"`
	PlanHash        string `json:"plan_hash"`
	Alias           string `json:"alias,omitempty"`
}

// HostingDatabaseNetworkMember pins a settled attached endpoint and its receipt.
// Hashes alone are not admission proof; compilation requires the full proofs below.
type HostingDatabaseNetworkMember struct {
	BindingID        string                         `json:"binding_id"`
	BindingHash      string                         `json:"binding_hash"`
	EndpointPlanHash string                         `json:"endpoint_plan_hash"`
	ReceiptID        string                         `json:"receipt_id"`
	ReceiptHash      string                         `json:"receipt_hash"`
	Endpoint         HostingDatabaseBindingEndpoint `json:"endpoint"`
}

// HostingDatabaseNetworkPlan preserves one owned network across DB replacements.
// The initial slice permits one app binding with at most two distinct endpoints.
type HostingDatabaseNetworkPlan struct {
	Version            string                            `json:"version"`
	NetworkID          string                            `json:"network_id"`
	OriginHash         string                            `json:"origin_hash"`
	Alias              string                            `json:"alias"`
	MembershipRevision int64                             `json:"membership_revision"`
	Members            []HostingDatabaseNetworkMember    `json:"members"`
	Predecessor        HostingDatabaseNetworkPredecessor `json:"predecessor"`
	Hash               string                            `json:"hash"`
}

// DocumentHash includes the canonical membership and exact predecessor identity.
func (n HostingDatabaseNetworkPlan) DocumentHash() string {
	n.Hash = ""
	return HostingDocumentHash(n)
}

// UnmarshalJSON rejects open, ambiguous and null network documents. Contextual
// descriptor and proof validation is still mandatory after decoding.
func (n *HostingDatabaseNetworkPlan) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseNetworkPlan
	var value wire
	if decodeDatabaseBinding(raw, &value) != nil {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	*n = HostingDatabaseNetworkPlan(value)
	return nil
}

// HostingDatabaseNetworkOrigin mirrors the retained initialized v1 engine record,
// including field order and JSON names, so DocumentHash hashes those exact bytes.
// Controllers must load authenticated retained evidence, not accept caller claims.
type HostingDatabaseNetworkOrigin struct {
	Version         int               `json:"version"`
	Binding         string            `json:"binding"`
	Destination     string            `json:"destination"`
	Daemon          string            `json:"daemon"`
	Configuration   string            `json:"configuration"`
	OriginOperation string            `json:"origin_operation"`
	OriginPlan      string            `json:"origin_plan"`
	Nonce           string            `json:"nonce"`
	Storage         HostingStorageRef `json:"storage"`
	VolumeNonce     string            `json:"volume_nonce"`
	VolumeCreatedAt string            `json:"volume_created_at"`
	NetworkID       string            `json:"network_id"`
	InitReady       bool              `json:"init_ready"`
}

// DocumentHash binds the complete original engine network record, without projection.
func (o HostingDatabaseNetworkOrigin) DocumentHash() string { return HostingDocumentHash(o) }

// HostingDatabaseNetworkMemberProof is transient compilation evidence, never a
// persisted lease or secret. DatabasePlan must be the original v1 plan against
// which Binding was saved. Creating new bindings against v2 is unsupported here.
type HostingDatabaseNetworkMemberProof struct {
	Binding              HostingDatabaseBinding
	EndpointPlan         HostingDatabaseBindingEndpointPlan
	Receipt              HostingDatabaseBindingEndpointReceipt
	ApplicationHealthy   HostingHealthy
	DatabasePlan         HostingApplyPlan
	ApplicationProjectID string
	DatabaseProjectID    string
}

// HostingDatabaseNetworkPlanInput supplies complete origin and member proofs.
// This validates historical documents, not a current lease or Docker observation.
type HostingDatabaseNetworkPlanInput struct {
	Origin             HostingDatabaseNetworkOrigin
	MembershipRevision int64
	Members            []HostingDatabaseNetworkMemberProof
	Retired            []HostingDatabaseNetworkRetirementProof
}

func databaseNetworkMemberLess(a, b HostingDatabaseNetworkMember) int {
	return strings.Compare(a.Endpoint.Target.ContainerID, b.Endpoint.Target.ContainerID)
}

func (n HostingDatabaseNetworkPlan) validate(s HostingSnapshot, r HostingRelease) error {
	if n.Version != HostingDatabaseNetworkPlanVersion || !databaseBindingDockerID(n.NetworkID) || !imageSHA256.MatchString(n.OriginHash) || n.Alias != HostingDatabaseBindingAlias(s.BindingID) || n.MembershipRevision < 1 || n.Members == nil || len(n.Members) > 2 || n.Hash != n.DocumentHash() || !imageSHA256.MatchString(n.Predecessor.PlanHash) {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	switch n.Predecessor.CompilerVersion {
	case HostingDatabaseCompilerVersion:
		if n.Predecessor.Alias != "" {
			return ErrInvalidHostingDatabaseNetworkPlan
		}
	case HostingDatabaseNetworkCompilerVersion:
		if n.Predecessor.Alias != n.Alias {
			return ErrInvalidHostingDatabaseNetworkPlan
		}
	default:
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	for i, m := range n.Members {
		t := m.Endpoint.Target
		if !hostingProxyID.MatchString(m.BindingID) || !imageSHA256.MatchString(m.BindingHash) || !imageSHA256.MatchString(m.EndpointPlanHash) || !validRuntimeReferenceID(m.ReceiptID) || !imageSHA256.MatchString(m.ReceiptHash) || !t.valid() || !databaseBindingDockerID(m.Endpoint.EndpointID) || t.NetworkID != n.NetworkID || t.NetworkName != HostingDatabaseNetworkName(s.BindingID) || t.DaemonID != r.DaemonID {
			return ErrInvalidHostingDatabaseNetworkPlan
		}
		if i > 0 {
			first := n.Members[0]
			if m.BindingID != first.BindingID || m.BindingHash != first.BindingHash || databaseNetworkMemberLess(n.Members[i-1], m) >= 0 || m.Endpoint.EndpointID == first.Endpoint.EndpointID || m.ReceiptID == first.ReceiptID || m.ReceiptHash == first.ReceiptHash || m.EndpointPlanHash == first.EndpointPlanHash {
				return ErrInvalidHostingDatabaseNetworkPlan
			}
		}
	}
	return nil
}

// ValidateHostingDatabasePlanV2 checks canonical v2 shape and its complete hash.
// Contextual proof and original predecessor checks require ValidateHostingDatabasePlanTransition.
// Existing generic validators deliberately continue to reject this family.
func ValidateHostingDatabasePlanV2(p HostingApplyPlan) error {
	if p.ApplicationDatabase != nil || p.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || p.CompilerVersion != HostingDatabaseNetworkCompilerVersion || p.Database == nil || p.Database.Network == nil || p.Storage == nil || p.PreviousHealthy == nil || p.RetainPrevious || len(p.RetainedReleases) != 0 || p.Database.Network.validate(p.Snapshot, p.Release) != nil || p.Database.Network.Predecessor.PlanHash != p.PreviousHealthy.PlanHash {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	db := *p.Database
	db.Network = nil
	// Reuse v1's recipe/config/storage checks, never reconstruct a v2 predecessor.
	base, err := CompileHostingDatabasePlan(p.Snapshot, p.Release, p.PreviousHealthy, *p.Storage, db)
	if err != nil || p.Release != p.PreviousHealthy.Release {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	base.Database.Network = p.Database.Network
	base.CompilerVersion = HostingDatabaseNetworkCompilerVersion
	base.Hash = ""
	if HostingDocumentHash(base) != p.Hash {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	return nil
}

func validateDatabaseOriginalPlan(p HostingApplyPlan) error {
	switch p.CompilerVersion {
	case HostingDatabaseCompilerVersion:
		return ValidateHostingDatabasePlan(p)
	case HostingDatabaseNetworkCompilerVersion:
		return ValidateHostingDatabasePlanV2(p)
	default:
		return ErrInvalidHostingDatabaseNetworkPlan
	}
}

func databaseNetworkSameTarget(a, b HostingApplyPlan) bool {
	x, y := a.Snapshot, b.Snapshot
	x.ResourceGeneration, y.ResourceGeneration = 0, 0
	return x == y && a.Release == b.Release && a.Storage != nil && b.Storage != nil && *a.Storage == *b.Storage && a.Database != nil && b.Database != nil && a.Database.Reference == b.Database.Reference && a.Database.NetworkName == b.Database.NetworkName && slices.Equal(a.Database.Credentials, b.Database.Credentials)
}

func databaseNetworkProofMembers(p, previousPlan HostingApplyPlan, in HostingDatabaseNetworkPlanInput) ([]HostingDatabaseNetworkMember, error) {
	o, h := in.Origin, p.PreviousHealthy
	created, err := time.Parse(time.RFC3339Nano, o.VolumeCreatedAt)
	if h == nil || h.Storage == nil || o.Version != 1 || !o.InitReady || !databaseBindingDockerID(o.NetworkID) || o.Binding != p.Snapshot.BindingID || o.Destination != p.Snapshot.DestinationID || o.Daemon != p.Release.DaemonID || o.Configuration != p.Snapshot.Database.ConfigurationHash || o.Storage != p.Snapshot.Storage || o.OriginOperation != p.Storage.CreationOperationID || o.OriginPlan != h.Storage.CreationPlanHash || !imageSHA256.MatchString(o.OriginPlan) || o.VolumeNonce != p.Storage.CreationNonce || len(o.Nonce) != 32 || strings.Trim(o.Nonce, "0123456789abcdef") != "" || err != nil || created.IsZero() || len(o.VolumeCreatedAt) > 64 || in.MembershipRevision < 1 || len(in.Members) > 2 {
		return nil, ErrInvalidHostingDatabaseNetworkPlan
	}
	if n := previousPlan.Database.Network; n != nil && len(in.Members) == 0 && len(n.Members) == 0 {
		// An attachment that never happened must not prevent DB maintenance.
		// An unchanged empty set keeps its exact expected revision.
		if in.MembershipRevision != n.MembershipRevision {
			return nil, ErrInvalidHostingDatabaseNetworkPlan
		}
	}
	if previousPlan.Database.Network != nil && (o.DocumentHash() != previousPlan.Database.Network.OriginHash || o.NetworkID != previousPlan.Database.Network.NetworkID || in.MembershipRevision < previousPlan.Database.Network.MembershipRevision) {
		return nil, ErrInvalidHostingDatabaseNetworkPlan
	}
	members := make([]HostingDatabaseNetworkMember, 0, len(in.Members))
	for _, proof := range in.Members {
		b, ep, receipt, app := proof.Binding, proof.EndpointPlan, proof.Receipt, proof.ApplicationHealthy
		if ValidateHostingDatabasePlan(proof.DatabasePlan) != nil || !databaseNetworkSameTarget(p, proof.DatabasePlan) || proof.DatabasePlan.Snapshot.ResourceGeneration > h.Snapshot.ResourceGeneration || b.ValidateTargets(proof.ApplicationProjectID, app.Snapshot, proof.DatabaseProjectID, proof.DatabasePlan) != nil || ep.ValidateApplication(b, app) != nil || ep.Action != "attach" || ep.MembershipRevision > in.MembershipRevision || ep.Target.NetworkID != o.NetworkID || ep.Target.DaemonID != o.Daemon {
			return nil, ErrInvalidHostingDatabaseNetworkPlan
		}
		endpoint, ok := receipt.ObservedMembership(ep, b)
		if !ok {
			return nil, ErrInvalidHostingDatabaseNetworkPlan
		}
		members = append(members, HostingDatabaseNetworkMember{BindingID: b.ID, BindingHash: b.Hash, EndpointPlanHash: ep.Hash, ReceiptID: receipt.ReceiptID, ReceiptHash: receipt.Hash, Endpoint: endpoint})
	}
	slices.SortFunc(members, databaseNetworkMemberLess)
	if previousPlan.Database.Network != nil && !slices.Equal(members, previousPlan.Database.Network.Members) && in.MembershipRevision == previousPlan.Database.Network.MembershipRevision {
		return nil, ErrInvalidHostingDatabaseNetworkPlan
	}
	var prior []HostingDatabaseNetworkMember
	var priorRevision int64
	if n := previousPlan.Database.Network; n != nil {
		prior, priorRevision = n.Members, n.MembershipRevision
	}
	if validateDatabaseNetworkRetirements(prior, members, priorRevision, in.MembershipRevision, in.Retired) != nil {
		return nil, ErrInvalidHostingDatabaseNetworkPlan
	}
	return members, nil
}

// ValidateHostingDatabasePlanTransition requires the exact original predecessor
// and complete historical proofs. It does not authorize current network effects.
func ValidateHostingDatabasePlanTransition(p, previousPlan HostingApplyPlan, in HostingDatabaseNetworkPlanInput) error {
	if ValidateHostingDatabasePlanV2(p) != nil || validateDatabaseOriginalPlan(previousPlan) != nil || !databaseNetworkSameTarget(p, previousPlan) || !databaseBindingDockerID(p.PreviousHealthy.ContainerID) || p.PreviousHealthy.PlanHash != previousPlan.Hash || p.PreviousHealthy.Snapshot != previousPlan.Snapshot || p.PreviousHealthy.Release != previousPlan.Release || p.PreviousHealthy.Storage == nil || p.PreviousHealthy.Storage.Plan != *previousPlan.Storage || p.Snapshot.ResourceGeneration < previousPlan.Snapshot.ResourceGeneration {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	originPlan := previousPlan.Hash
	if previousPlan.PreviousHealthy != nil {
		originPlan = previousPlan.PreviousHealthy.Storage.CreationPlanHash
	} else if p.PreviousHealthy.OperationID != p.Storage.CreationOperationID {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	if p.PreviousHealthy.Storage.CreationPlanHash != originPlan {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	n := p.Database.Network
	want := HostingDatabaseNetworkPredecessor{CompilerVersion: previousPlan.CompilerVersion, PlanHash: previousPlan.Hash}
	if previousPlan.Database.Network != nil {
		want.Alias = previousPlan.Database.Network.Alias
	}
	if n.Predecessor != want {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	members, err := databaseNetworkProofMembers(p, previousPlan, in)
	if err != nil || !reflect.DeepEqual(n.Members, members) || n.OriginHash != in.Origin.DocumentHash() || n.NetworkID != in.Origin.NetworkID || n.MembershipRevision != in.MembershipRevision {
		return ErrInvalidHostingDatabaseNetworkPlan
	}
	return nil
}

// CompileHostingDatabasePlanV2 upgrades an existing healthy DB without changing
// its recipe, release, placement, config, volume or network origin. It creates no
// fresh DB and returns owned copies of all caller-supplied persisted slices.
// Database.Network must be nil: this compiler derives the new descriptor from
// the original predecessor and explicit proofs rather than trusting a caller one.
func CompileHostingDatabasePlanV2(s HostingSnapshot, r HostingRelease, previous HostingHealthy, previousPlan HostingApplyPlan, storage HostingStoragePlan, database HostingDatabasePlan, in HostingDatabaseNetworkPlanInput) (HostingApplyPlan, error) {
	if database.Network != nil || validateDatabaseOriginalPlan(previousPlan) != nil {
		return HostingApplyPlan{}, ErrInvalidHostingDatabaseNetworkPlan
	}
	p, err := CompileHostingDatabasePlan(s, r, &previous, storage, database)
	if err != nil {
		return HostingApplyPlan{}, ErrInvalidHostingDatabaseNetworkPlan
	}
	members, err := databaseNetworkProofMembers(p, previousPlan, in)
	if err != nil {
		return HostingApplyPlan{}, err
	}
	n := &HostingDatabaseNetworkPlan{Version: HostingDatabaseNetworkPlanVersion, NetworkID: in.Origin.NetworkID, OriginHash: in.Origin.DocumentHash(), Alias: HostingDatabaseBindingAlias(s.BindingID), MembershipRevision: in.MembershipRevision, Members: members, Predecessor: HostingDatabaseNetworkPredecessor{CompilerVersion: previousPlan.CompilerVersion, PlanHash: previousPlan.Hash}}
	if previousPlan.Database.Network != nil {
		n.Predecessor.Alias = previousPlan.Database.Network.Alias
	}
	n.Hash = n.DocumentHash()
	p.Database.Network, p.CompilerVersion, p.Hash = n, HostingDatabaseNetworkCompilerVersion, ""
	p.Hash = HostingDocumentHash(p)
	if ValidateHostingDatabasePlanTransition(p, previousPlan, in) != nil {
		return HostingApplyPlan{}, ErrInvalidHostingDatabaseNetworkPlan
	}
	return p, nil
}
