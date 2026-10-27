package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Observation protocol constants bound collection and freshness independently.
const (
	HostingObservationCapability        = "hosting_observation_v1"
	HostingStorageObservationCapability = "hosting_storage_observation_v1"
	HostingObservationJSONLimit         = 8 << 10
	HostingObservationStaleSeconds      = 30
	HostingObservationIntervalSeconds   = 10
	HostingObservationTimeoutSeconds    = 5
)

// HostingObservationTargetsInput selects a bounded server-derived sweep.
type HostingObservationTargetsInput struct {
	AfterBindingID string `json:"after_binding_id"`
	Limit          int    `json:"limit"`
}

// Validate checks the bounded sweep cursor and page size.
func (in HostingObservationTargetsInput) Validate() error {
	if !observationID(in.AfterBindingID, true) || in.Limit < 1 || in.Limit > 50 {
		return errors.New("invalid observation cursor or limit")
	}
	return nil
}

// HostingObservationTarget authorizes one read of an exact settled container.
type HostingObservationTarget struct {
	SampleID             string    `json:"sample_id"`
	IssuedAt             time.Time `json:"issued_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	ResourceID           string    `json:"resource_id"`
	BindingID            string    `json:"binding_id"`
	DestinationID        string    `json:"destination_id"`
	ServerID             string    `json:"server_id"`
	RunnerID             string    `json:"runner_id"`
	OperationID          string    `json:"operation_id"`
	DaemonID             string    `json:"daemon_id"`
	ContainerID          string    `json:"container_id"`
	ImageID              string    `json:"image_id"`
	ReleaseHash          string    `json:"release_hash"`
	PlanHash             string    `json:"plan_hash"`
	RuntimeNamespace     string    `json:"runtime_namespace"`
	ContainerPort        int       `json:"container_port"`
	HostPort             int       `json:"host_port"`
	ReadinessPath        string    `json:"readiness_path"`
	SampleTimeoutSeconds int       `json:"sample_timeout_seconds"`

	Storage *HostingObservationStorage `json:"storage,omitempty"`
}

// HostingObservationStorage is the exact mount authorized by the controller's
// validated immutable healthy receipt. Its absence authorizes no mounts.
type HostingObservationStorage struct {
	VolumeName string `json:"volume_name"`
	MountPath  string `json:"mount_path"`
}

// Validate rejects mount syntax outside the managed local-volume contract.
func (s HostingObservationStorage) Validate() error {
	if len(s.VolumeName) != 37 || !strings.HasPrefix(s.VolumeName, "ncdv_") || !hostingStorageRequest.MatchString(s.VolumeName[5:]) || ValidateHostingStorageMountPath(s.MountPath) != nil {
		return ErrInvalidHostingStorage
	}
	return nil
}

// HostingObservationTargets is one bounded server-selected sweep page.
type HostingObservationTargets struct {
	Targets    []HostingObservationTarget `json:"targets"`
	NextCursor string                     `json:"next_cursor"`
	EndOfSweep bool                       `json:"end_of_sweep"`
}

// HostingRuntimeObservation contains only closed, nonsecret evidence.
type HostingRuntimeObservation struct {
	BindingID        string `json:"binding_id"`
	SampleID         string `json:"sample_id"`
	SampleDurationMS int    `json:"sample_duration_ms"`
	ContainerState   string `json:"container_state"`
	Readiness        string `json:"readiness"`
	CollectionError  string `json:"collection_error"`
	ExitCode         *int   `json:"exit_code,omitempty"`
}

func observationID(v string, empty bool) bool {
	if empty && v == "" {
		return true
	}
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	return !strings.ContainsFunc(v, func(r rune) bool { return r < 33 || r > 126 })
}

// Validate rejects inconsistent or unverified evidence combinations.
func (in HostingRuntimeObservation) Validate() error {
	if !observationID(in.BindingID, false) || !observationID(in.SampleID, false) || in.SampleDurationMS < 0 || in.SampleDurationMS > 5000 {
		return errors.New("invalid observation identity or duration")
	}
	switch in.ContainerState {
	case "running", "exited", "missing", "unknown":
	default:
		return errors.New("invalid container state")
	}
	switch in.Readiness {
	case "ready", "not_ready", "not_checked", "unknown":
	default:
		return errors.New("invalid readiness")
	}
	switch in.CollectionError {
	case "", "daemon_unreachable", "daemon_mismatch", "target_mismatch", "inspect_failed", "readiness_timeout":
	default:
		return errors.New("invalid collection error")
	}
	if in.ExitCode != nil && (in.ContainerState != "exited" || in.CollectionError != "" || *in.ExitCode < 0 || *in.ExitCode > 255) {
		return errors.New("exit code requires verified exit")
	}
	if in.Readiness == "ready" && (in.ContainerState != "running" || in.CollectionError != "") {
		return errors.New("ready requires verified running container")
	}
	if in.CollectionError != "" && in.CollectionError != "readiness_timeout" && (in.ContainerState != "unknown" || in.Readiness != "unknown") {
		return errors.New("failed collection requires unknown evidence")
	}
	if in.CollectionError == "readiness_timeout" && (in.ContainerState != "running" || in.Readiness != "unknown") {
		return errors.New("readiness timeout requires verified running container")
	}
	if in.ContainerState != "running" && in.Readiness != "unknown" && in.Readiness != "not_checked" {
		return errors.New("readiness requires running container")
	}
	return nil
}

// HostingObservationAck records the original acceptance time of canonical evidence.
type HostingObservationAck struct {
	SampleID   string    `json:"sample_id"`
	BodyHash   string    `json:"body_hash"`
	ReceivedAt time.Time `json:"received_at"`
}

// HostingRuntimeStatus keeps independent observation and heartbeat clocks.
type HostingRuntimeStatus struct {
	ResourceID            string                  `json:"resource_id"`
	BindingID             string                  `json:"binding_id"`
	DestinationID         string                  `json:"destination_id"`
	ServerID              string                  `json:"server_id"`
	RunnerID              string                  `json:"runner_id"`
	CurrentOperationID    string                  `json:"current_operation_id"`
	CurrentContainerID    string                  `json:"current_container_id"`
	ObservedOperationID   string                  `json:"observed_operation_id"`
	ObservedContainerID   string                  `json:"observed_container_id"`
	ObservedAt            *time.Time              `json:"observed_at"`
	ServerTime            time.Time               `json:"server_time"`
	StaleAfterSeconds     int                     `json:"stale_after_seconds"`
	TargetIntervalSeconds int                     `json:"target_interval_seconds"`
	SampleTimeoutSeconds  int                     `json:"sample_timeout_seconds"`
	Freshness             string                  `json:"freshness"`
	Reason                string                  `json:"reason"`
	AgentReachability     string                  `json:"agent_reachability"`
	LastHeartbeatAt       *time.Time              `json:"last_heartbeat_at"`
	ContainerState        string                  `json:"container_state"`
	Readiness             string                  `json:"readiness"`
	CollectionError       string                  `json:"collection_error"`
	ExitCode              *int                    `json:"exit_code,omitempty"`
	MutationHold          bool                    `json:"mutation_hold"`
	RestoreOwnershipHeld  bool                    `json:"restore_ownership_held"`
	LastObservation       *HostingLastObservation `json:"last_observation"`
}

// HostingLastObservation retains historical evidence without asserting current health.
type HostingLastObservation struct {
	OperationID string                    `json:"operation_id"`
	ContainerID string                    `json:"container_id"`
	ObservedAt  time.Time                 `json:"observed_at"`
	Observation HostingRuntimeObservation `json:"observation"`
}

// DecodeHostingObservationJSON rejects unknown, duplicate and case-folded keys.
func DecodeHostingObservationJSON(raw []byte, out any) error {
	if len(raw) > HostingObservationJSONLimit || !validRuntimeJSONUnicode(raw) || !uniqueObservationJSON(raw) {
		return errors.New("invalid observation JSON")
	}
	allowed := map[string]bool{}
	switch out.(type) {
	case *HostingObservationTargetsInput:
		for _, k := range []string{"after_binding_id", "limit"} {
			allowed[k] = true
		}
	case *HostingRuntimeObservation:
		for _, k := range []string{"binding_id", "sample_id", "sample_duration_ms", "container_state", "readiness", "collection_error", "exit_code"} {
			allowed[k] = true
		}
	default:
		return errors.New("invalid observation DTO")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("invalid observation object")
	}
	for k, v := range fields {
		if !allowed[k] || bytes.Equal(v, []byte("null")) {
			return errors.New("invalid observation field")
		}
	}
	// Required fields have meaningful zero values, so presence is explicit.
	for k := range allowed {
		if k != "exit_code" && k != "after_binding_id" && fields[k] == nil {
			return errors.New("missing observation field")
		}
	}
	return json.Unmarshal(raw, out)
}

func uniqueObservationJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}')
}
