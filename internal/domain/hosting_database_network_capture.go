package domain

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"time"
)

// These identifiers reserve an internal read-only family, not runtime support.
const (
	RunTypeDatabaseNetworkCapture           = "database_network_capture"
	HostingDatabaseNetworkCaptureCapability = "hosting_database_network_capture_v1"
	HostingDatabaseNetworkCaptureVersion    = "database-network.capture.v1"
)

// ErrInvalidHostingDatabaseNetworkCapture excludes private evidence details.
var ErrInvalidHostingDatabaseNetworkCapture = errors.New("invalid database network capture")

// HostingDatabaseNetworkCaptureCreation retains committed evidence without the
// historical fence. Receipt.BodyHash binds the original creation receipt body.
type HostingDatabaseNetworkCaptureCreation struct {
	Receipt    HostingReceipt    `json:"receipt"`
	VolumeName string            `json:"volume_name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Labels     map[string]string `json:"labels"`
	Options    map[string]string `json:"options"`
}

// HostingDatabaseNetworkCapturePlan permits only observing a settled DBv1
// source. It grants no process, storage or secret-delivery effects.
type HostingDatabaseNetworkCapturePlan struct {
	Version           string                                `json:"version"`
	OperationID       string                                `json:"operation_id"`
	SourceOperationID string                                `json:"source_operation_id"`
	ProjectID         string                                `json:"project_id"`
	ResourceID        string                                `json:"resource_id"`
	BindingID         string                                `json:"binding_id"`
	DestinationID     string                                `json:"destination_id"`
	ServerID          string                                `json:"server_id"`
	RunnerID          string                                `json:"runner_id"`
	SourcePlan        HostingApplyPlan                      `json:"source_plan"`
	SourceHealthy     HostingHealthy                        `json:"source_healthy"`
	Storage           HostingStorage                        `json:"storage"`
	Materialization   HostingStorageMaterialization         `json:"materialization"`
	Creation          HostingDatabaseNetworkCaptureCreation `json:"creation"`
	Hash              string                                `json:"hash"`
}

// HostingDatabaseNetworkCaptureID identifies one immutable source capture.
func HostingDatabaseNetworkCaptureID(source string) string {
	if !validRuntimeReferenceID(source) {
		return ""
	}
	return "hnc_" + HostingDocumentHash(struct{ Source string }{source})[7:39]
}

// DocumentHash binds the complete source and committed creation evidence.
func (p HostingDatabaseNetworkCapturePlan) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}

// Validate checks canonical shape and all known source/materialization links.
func (p HostingDatabaseNetworkCapturePlan) Validate() error {
	s, h, m, c := p.SourcePlan.Snapshot, p.SourceHealthy, p.Materialization, p.Creation
	if p.SourcePlan.Storage == nil || p.Storage.MountPath != p.SourcePlan.Storage.MountPath || p.Storage.VolumeName != p.SourcePlan.Storage.VolumeName {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	if p.Version != HostingDatabaseNetworkCaptureVersion || p.OperationID != HostingDatabaseNetworkCaptureID(p.SourceOperationID) || ValidateHostingDatabasePlan(p.SourcePlan) != nil || ValidateHostingHealthyStorage(h) != nil || h.OperationID != p.SourceOperationID || !databaseBindingDockerID(h.ContainerID) || h.PlanHash != p.SourcePlan.Hash || h.Snapshot != s || h.Release != p.SourcePlan.Release || h.Storage == nil || h.Storage.Plan != *p.SourcePlan.Storage || p.ProjectID != p.Storage.ProjectID || p.ResourceID != s.ResourceID || p.BindingID != s.BindingID || p.DestinationID != s.DestinationID || p.ServerID != s.ServerID || p.RunnerID != s.RunnerID || p.Storage.ResourceID != p.ResourceID || p.Storage.BindingID != p.BindingID || p.Storage.DestinationID != p.DestinationID || p.Storage.Reference() != s.Storage || m.Validate(p.Storage) != nil || m.State != "materialized" || m.MaterializedReceipt == nil || m.CreationOperationID != p.SourcePlan.Storage.CreationOperationID || m.CreationPlanHash != h.Storage.CreationPlanHash || m.CreationNonce != p.SourcePlan.Storage.CreationNonce || m.DaemonID != p.SourcePlan.Release.DaemonID || c.Receipt != *m.MaterializedReceipt || c.VolumeName != p.Storage.VolumeName || c.Driver != "local" || c.Scope != "local" || c.Options == nil || len(c.Options) != 0 || !maps.Equal(c.Labels, m.Labels) || !imageSHA256.MatchString(p.Hash) || p.Hash != p.DocumentHash() {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	return nil
}

// ValidateOrigin cross-checks known identity. CreatedAt, network ID, nonce and
// initialized state are new runner attestations, not controller-known facts.
func (p HostingDatabaseNetworkCapturePlan) ValidateOrigin(o HostingDatabaseNetworkOrigin) error {
	created, err := time.Parse(time.RFC3339Nano, o.VolumeCreatedAt)
	m := p.Materialization
	if p.Validate() != nil || o.Version != 1 || !o.InitReady || o.Binding != p.BindingID || o.Destination != p.DestinationID || o.Daemon != m.DaemonID || o.Configuration != p.SourcePlan.Snapshot.Database.ConfigurationHash || o.OriginOperation != m.CreationOperationID || o.OriginPlan != m.CreationPlanHash || o.Storage != m.StorageRef || o.VolumeNonce != m.CreationNonce || len(o.Nonce) != 32 || strings.Trim(o.Nonce, "0123456789abcdef") != "" || !databaseBindingDockerID(o.NetworkID) || err != nil || created.IsZero() || len(o.VolumeCreatedAt) > 64 {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	return nil
}

// HostingDatabaseNetworkOriginRequest needs the capture's new current lease.
// ObservedAt is sampling time; historical replay does not imply a fresh sample.
type HostingDatabaseNetworkOriginRequest struct {
	HostingAuthority
	ReceiptID  string                       `json:"receipt_id"`
	PlanHash   string                       `json:"plan_hash"`
	ObservedAt time.Time                    `json:"observed_at"`
	Origin     HostingDatabaseNetworkOrigin `json:"origin"`
}

// Validate checks request shape; the store checks live persisted authority.
func (r HostingDatabaseNetworkOriginRequest) Validate() error {
	if !validRuntimeReferenceID(r.OperationID) || !validRuntimeReferenceID(r.RunID) || !validRuntimeReferenceID(r.LeaseID) || !validRuntimeReferenceID(r.Fence) || r.Attempt < 1 || r.Attempt > 2147483647 || !validRuntimeReferenceID(r.ReceiptID) || !imageSHA256.MatchString(r.PlanHash) || r.ObservedAt.IsZero() {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	return nil
}

// HostingDatabaseNetworkOriginReceipt is historical runner attestation. Upgrade
// must independently inspect the actual volume/network under fresh authority.
type HostingDatabaseNetworkOriginReceipt struct {
	HostingReceipt
	OperationID       string                       `json:"operation_id"`
	SourceOperationID string                       `json:"source_operation_id"`
	PlanHash          string                       `json:"plan_hash"`
	OriginHash        string                       `json:"origin_hash"`
	Origin            HostingDatabaseNetworkOrigin `json:"origin"`
	ObservedAt        time.Time                    `json:"observed_at"`
	RunnerID          string                       `json:"runner_id"`
}

// HostingDatabaseNetworkCapture is independent from the source hosting operation.
type HostingDatabaseNetworkCapture struct {
	ID        string                               `json:"id"`
	RunID     string                               `json:"run_id"`
	State     string                               `json:"state"`
	CreatedBy string                               `json:"created_by"`
	Plan      HostingDatabaseNetworkCapturePlan    `json:"plan"`
	Receipt   *HostingDatabaseNetworkOriginReceipt `json:"receipt,omitempty"`
	CreatedAt time.Time                            `json:"created_at"`
	UpdatedAt time.Time                            `json:"updated_at"`
}

// HostingDatabaseNetworkCaptureClaim pairs one capture with its new task lease.
type HostingDatabaseNetworkCaptureClaim struct {
	Capture HostingDatabaseNetworkCapture `json:"capture"`
	Lease   RunLease                      `json:"lease"`
}

// ValidateHostingDatabaseNetworkCaptureTask rejects executable or nested payloads.
func ValidateHostingDatabaseNetworkCaptureTask(run TaskRun) error {
	want := RunSpec{Type: RunTypeDatabaseNetworkCapture, HostingOperationID: run.RunSpec.HostingOperationID, HostingMode: "capture", Inputs: map[string]any{}}
	if run.ID != "capture_"+want.HostingOperationID || !strings.HasPrefix(want.HostingOperationID, "hnc_") || !validRuntimeReferenceID(want.HostingOperationID) || !reflect.DeepEqual(run.RunSpec, want) || !reflect.DeepEqual(run.Workflow, Workflow{}) {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	return nil
}

// UnmarshalJSON rejects unknown, duplicate, null and oversized documents.
func (p *HostingDatabaseNetworkCapturePlan) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseNetworkCapturePlan
	var v wire
	if decodeDatabaseBinding(raw, &v) != nil || HostingDatabaseNetworkCapturePlan(v).Validate() != nil {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	*p = HostingDatabaseNetworkCapturePlan(v)
	return nil
}

// UnmarshalJSON rejects unknown, duplicate, null and oversized requests.
func (r *HostingDatabaseNetworkOriginRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseNetworkOriginRequest
	var v wire
	if decodeDatabaseBinding(raw, &v) != nil || HostingDatabaseNetworkOriginRequest(v).Validate() != nil {
		return ErrInvalidHostingDatabaseNetworkCapture
	}
	*r = HostingDatabaseNetworkOriginRequest(v)
	return nil
}
