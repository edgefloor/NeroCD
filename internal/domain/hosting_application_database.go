package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

// These versions describe a domain contract only. They advertise no runner capability.
const (
	HostingApplicationDatabasePolicyVersion   = "public-image.runtime-env.database.release-ports.v1"
	HostingApplicationDatabaseCompilerVersion = "image-hosting.runtime-env.database.v1"
	HostingApplicationDatabasePlanVersion     = "application-database.private.v1"
)

// ErrInvalidHostingApplicationDatabasePlan omits credentials and daemon details.
var ErrInvalidHostingApplicationDatabasePlan = errors.New("invalid application database plan")

// HostingApplicationDatabaseRef pins one exact immutable binding across replacement.
type HostingApplicationDatabaseRef struct {
	BindingID   string `json:"binding_id"`
	BindingHash string `json:"binding_hash"`
}

// HostingApplicationDatabasePlan pins desired application creation and settled
// database network evidence. No field proves a candidate container is healthy.
type HostingApplicationDatabasePlan struct {
	Version            string                         `json:"version"`
	OperationID        string                         `json:"operation_id"`
	CreateNonce        string                         `json:"create_nonce"`
	Binding            HostingDatabaseBinding         `json:"binding"`
	DatabasePlanHash   string                         `json:"database_plan_hash"`
	Origin             HostingDatabaseNetworkOrigin   `json:"origin"`
	MembershipRevision int64                          `json:"membership_revision"`
	PreviousMembers    []HostingDatabaseNetworkMember `json:"previous_members"`
	Hash               string                         `json:"hash"`
}

// DocumentHash binds the complete descriptor with its own hash field empty.
func (p HostingApplicationDatabasePlan) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}

// HostingApplicationDatabasePlanInput is transient, authenticated retained
// evidence. BindingApplicationSnapshot and BindingDatabasePlan are the original
// ordinary runtime/v1 database capture, supplied explicitly even for v2 databases.
// A controller must load these records and authorize live effects separately.
type HostingApplicationDatabasePlanInput struct {
	OperationID                string
	CreateNonce                string
	Binding                    HostingDatabaseBinding
	ApplicationProjectID       string
	DatabaseProjectID          string
	BindingApplicationSnapshot HostingSnapshot
	BindingDatabasePlan        HostingApplyPlan
	DatabasePlan               HostingApplyPlan
	DatabaseHealthy            HostingHealthy
	Origin                     HostingDatabaseNetworkOrigin
	MembershipRevision         int64
	Members                    []HostingDatabaseNetworkMemberProof
	Retired                    []HostingDatabaseNetworkRetirementProof
}

func applicationDatabaseNonce(value string) bool {
	return len(value) == 32 && strings.Trim(value, "0123456789abcdef") == ""
}

func applicationDatabaseOrigin(in HostingApplicationDatabasePlanInput) bool {
	p, h, o := in.DatabasePlan, in.DatabaseHealthy, in.Origin
	if validateDatabaseOriginalPlan(p) != nil || !databaseNetworkSameTarget(p, in.BindingDatabasePlan) || p.Snapshot.ResourceGeneration < in.BindingDatabasePlan.Snapshot.ResourceGeneration || h.Snapshot != p.Snapshot || h.Release != p.Release || h.PlanHash != p.Hash || !hostingProxyID.MatchString(h.OperationID) || !databaseBindingDockerID(h.ContainerID) || ValidateHostingHealthyStorage(h) != nil || h.Storage == nil || h.Storage.Plan != *p.Storage {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, o.VolumeCreatedAt)
	if o.Version != 1 || !o.InitReady || !databaseBindingDockerID(o.NetworkID) || o.Binding != p.Snapshot.BindingID || o.Destination != p.Snapshot.DestinationID || o.Daemon != p.Release.DaemonID || o.Configuration != p.Snapshot.Database.ConfigurationHash || o.Storage != p.Snapshot.Storage || o.OriginOperation != p.Storage.CreationOperationID || o.OriginPlan != h.Storage.CreationPlanHash || !imageSHA256.MatchString(o.OriginPlan) || o.VolumeNonce != p.Storage.CreationNonce || !applicationDatabaseNonce(o.Nonce) || err != nil || created.IsZero() || len(o.VolumeCreatedAt) > 64 {
		return false
	}
	if p.PreviousHealthy == nil {
		if h.Storage.CreationPlanHash != p.Hash || h.OperationID != p.Storage.CreationOperationID {
			return false
		}
	} else if p.PreviousHealthy.Storage == nil || h.Storage.CreationPlanHash != p.PreviousHealthy.Storage.CreationPlanHash {
		return false
	}
	if n := p.Database.Network; n != nil {
		return o.DocumentHash() == n.OriginHash && o.NetworkID == n.NetworkID && in.MembershipRevision >= n.MembershipRevision
	}
	return true
}

func applicationDatabaseMembers(in HostingApplicationDatabasePlanInput) ([]HostingDatabaseNetworkMember, error) {
	if in.MembershipRevision < 1 || in.MembershipRevision == math.MaxInt64 || len(in.Members) > 2 {
		return nil, ErrInvalidHostingApplicationDatabasePlan
	}
	members := make([]HostingDatabaseNetworkMember, 0, len(in.Members))
	for _, proof := range in.Members {
		b, ep, app := proof.Binding, proof.EndpointPlan, proof.ApplicationHealthy
		if b.ID != in.Binding.ID || b.Hash != in.Binding.Hash || ValidateHostingDatabasePlan(proof.DatabasePlan) != nil || !databaseNetworkSameTarget(proof.DatabasePlan, in.BindingDatabasePlan) || proof.DatabasePlan.Hash != in.BindingDatabasePlan.Hash || b.ValidateTargets(proof.ApplicationProjectID, app.Snapshot, proof.DatabaseProjectID, proof.DatabasePlan) != nil || ep.ValidateApplication(b, app) != nil || ep.Action != "attach" || ep.MembershipRevision > in.MembershipRevision || ep.Target.NetworkID != in.Origin.NetworkID || ep.Target.DaemonID != in.Origin.Daemon || ep.Target.ContainerOperationID == in.OperationID {
			return nil, ErrInvalidHostingApplicationDatabasePlan
		}
		endpoint, ok := proof.Receipt.ObservedMembership(ep, b)
		if !ok {
			return nil, ErrInvalidHostingApplicationDatabasePlan
		}
		members = append(members, HostingDatabaseNetworkMember{BindingID: b.ID, BindingHash: b.Hash, EndpointPlanHash: ep.Hash, ReceiptID: proof.Receipt.ReceiptID, ReceiptHash: proof.Receipt.Hash, Endpoint: endpoint})
	}
	slices.SortFunc(members, databaseNetworkMemberLess)
	for i := 1; i < len(members); i++ {
		a, b := members[i-1], members[i]
		if databaseNetworkMemberLess(a, b) >= 0 || a.Endpoint.EndpointID == b.Endpoint.EndpointID || a.ReceiptID == b.ReceiptID || a.ReceiptHash == b.ReceiptHash || a.EndpointPlanHash == b.EndpointPlanHash {
			return nil, ErrInvalidHostingApplicationDatabasePlan
		}
	}
	if n := in.DatabasePlan.Database.Network; n != nil {
		if slices.Equal(members, n.Members) {
			if in.MembershipRevision != n.MembershipRevision {
				return nil, ErrInvalidHostingApplicationDatabasePlan
			}
		} else {
			if in.MembershipRevision <= n.MembershipRevision {
				return nil, ErrInvalidHostingApplicationDatabasePlan
			}
		}
	}
	var prior []HostingDatabaseNetworkMember
	var priorRevision int64
	if n := in.DatabasePlan.Database.Network; n != nil {
		prior, priorRevision = n.Members, n.MembershipRevision
	}
	if validateDatabaseNetworkRetirements(in.DatabasePlan, in.Origin, prior, members, priorRevision, in.MembershipRevision, in.Retired) != nil {
		return nil, ErrInvalidHostingApplicationDatabasePlan
	}
	return members, nil
}

func applicationDatabasePrevious(h HostingHealthy, proofs []HostingDatabaseNetworkMemberProof) bool {
	// Compare the complete settled record, including ports and namespace. Endpoint
	// target hashes alone must not allow a rewritten predecessor snapshot.
	for _, proof := range proofs {
		if h == proof.ApplicationHealthy {
			return true
		}
	}
	return false
}

// CompileHostingApplicationDatabasePlan freezes an ordinary image release and
// exact binding before application creation. It does not admit a runtime task,
// fetch credentials, create an endpoint, or invent a healthy candidate record.
func CompileHostingApplicationDatabasePlan(s HostingSnapshot, r HostingRelease, previous *HostingHealthy, retained []HostingHealthy, in HostingApplicationDatabasePlanInput) (HostingApplyPlan, error) {
	b := in.Binding
	if in.OperationID == in.DatabaseHealthy.OperationID || in.OperationID == in.Origin.OriginOperation {
		return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	for _, retired := range in.Retired {
		if in.OperationID == retired.RetirePlan.OperationID {
			return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
		}
	}
	if !hostingProxyID.MatchString(in.OperationID) || !applicationDatabaseNonce(in.CreateNonce) || in.BindingApplicationSnapshot.PolicyVersion != HostingRuntimePolicyVersion || in.BindingApplicationSnapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || b.ValidateTargets(in.ApplicationProjectID, in.BindingApplicationSnapshot, in.DatabaseProjectID, in.BindingDatabasePlan) != nil || s.PolicyVersion != HostingApplicationDatabasePolicyVersion || b.ValidateTargets(in.ApplicationProjectID, s, in.DatabaseProjectID, in.BindingDatabasePlan) != nil || r.LocalBuild != (HostingBuildArtifact{}) || ValidateHostingSnapshotRelease(s, r) != nil || r.DaemonID != in.DatabasePlan.Release.DaemonID || !applicationDatabaseOrigin(in) {
		return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	members, err := applicationDatabaseMembers(in)
	if err != nil {
		return HostingApplyPlan{}, err
	}
	if previous != nil && !applicationDatabasePrevious(*previous, in.Members) || len(retained) > 2 {
		return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	for _, h := range retained {
		if !applicationDatabasePrevious(h, in.Members) {
			return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
		}
	}
	p, err := compileRetainedHostingPlan(s, r, previous, retained, HostingApplicationDatabasePolicyVersion, HostingApplicationDatabaseCompilerVersion)
	if err != nil {
		return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	b.EnvironmentNames = slices.Clone(b.EnvironmentNames)
	descriptor := &HostingApplicationDatabasePlan{Version: HostingApplicationDatabasePlanVersion, OperationID: in.OperationID, CreateNonce: in.CreateNonce, Binding: b, DatabasePlanHash: in.DatabasePlan.Hash, Origin: in.Origin, MembershipRevision: in.MembershipRevision, PreviousMembers: members}
	descriptor.Hash = descriptor.DocumentHash()
	p.ApplicationDatabase, p.Hash = descriptor, ""
	p.Hash = HostingDocumentHash(p)
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 48<<10 {
		return HostingApplyPlan{}, ErrInvalidHostingApplicationDatabasePlan
	}
	return p, nil
}

// ValidateHostingApplicationDatabasePlan requires complete original capture and
// retained proofs again; a self-rehashed descriptor alone cannot authorize effects.
func ValidateHostingApplicationDatabasePlan(p HostingApplyPlan, in HostingApplicationDatabasePlanInput) error {
	if p.ApplicationDatabase == nil || p.ApplicationDatabase.Version != HostingApplicationDatabasePlanVersion || p.ApplicationDatabase.Hash != p.ApplicationDatabase.DocumentHash() || p.Database != nil || p.Storage != nil || p.CompilerVersion != HostingApplicationDatabaseCompilerVersion {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	want, err := CompileHostingApplicationDatabasePlan(p.Snapshot, p.Release, p.PreviousHealthy, p.RetainedReleases, in)
	if err != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	expected, err := json.Marshal(want)
	if err != nil || !bytes.Equal(raw, expected) {
		return ErrInvalidHostingApplicationDatabasePlan
	}
	return nil
}
