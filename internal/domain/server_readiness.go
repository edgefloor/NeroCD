package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Server readiness is a bounded diagnostic, independent of deployment admission.
const (
	ServerReadinessCapability             = "hosting_readiness_v1"
	ServerReadinessProfile                = "basic_image_hosting.v1"
	ServerReadinessJSONLimit              = 8 << 10
	ServerReadinessMinimumFreeBytes int64 = 1 << 30
)

// ServerReadinessChallenge selects current placement without accepting client IDs.
type ServerReadinessChallenge struct {
	SampleID      string    `json:"sample_id"`
	Profile       string    `json:"profile"`
	ServerID      string    `json:"server_id"`
	DestinationID string    `json:"destination_id"`
	RunnerID      string    `json:"runner_id"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// ServerReadinessReport contains only source-free, bounded evidence.
type ServerReadinessReport struct {
	SampleID           string `json:"sample_id"`
	Profile            string `json:"profile"`
	DurationMS         int    `json:"duration_ms"`
	OS                 string `json:"os"`
	Architecture       string `json:"architecture"`
	Execution          string `json:"execution"`
	Configuration      string `json:"configuration"`
	Docker             string `json:"docker"`
	DockerAPIVersion   string `json:"docker_api_version"`
	DaemonID           string `json:"daemon_id"`
	DaemonOS           string `json:"daemon_os"`
	DaemonArchitecture string `json:"daemon_architecture"`
	CLI                string `json:"cli"`
	WorkRoot           string `json:"work_root"`
	JournalRoot        string `json:"journal_root"`
	WorkFreeBytes      int64  `json:"work_free_bytes"`
	JournalFreeBytes   int64  `json:"journal_free_bytes"`
	Error              string `json:"error"`
}

// ServerReadinessAck records receipt only. GET alone qualifies current readiness.
type ServerReadinessAck struct {
	SampleID   string    `json:"sample_id"`
	ObservedAt time.Time `json:"observed_at"`
	Accepted   bool      `json:"accepted"`
}

// ServerReadiness is qualified against one current database snapshot.
type ServerReadiness struct {
	ServerID   string                 `json:"server_id"`
	Profile    string                 `json:"profile"`
	Status     string                 `json:"status"`
	Reason     string                 `json:"reason"`
	SampleID   string                 `json:"sample_id,omitempty"`
	ObservedAt *time.Time             `json:"observed_at,omitempty"`
	Report     *ServerReadinessReport `json:"report,omitempty"`
}

var readinessVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)
var readinessSample = regexp.MustCompile(`^srd_[0-9a-f]{32}$`)

// Validate rejects evidence outside the frozen report vocabulary.
func (in ServerReadinessReport) Validate() error {
	valid := readinessSample.MatchString(in.SampleID) && in.Profile == ServerReadinessProfile && in.DurationMS >= 0 && in.DurationMS <= 5000 && in.WorkFreeBytes >= 0 && in.JournalFreeBytes >= 0
	valid = valid && slices.Contains([]string{"linux", "unsupported", "unknown"}, in.OS) && slices.Contains([]string{"linux", "unsupported", "unknown"}, in.DaemonOS)
	valid = valid && slices.Contains([]string{"amd64", "arm64", "unsupported", "unknown"}, in.Architecture) && slices.Contains([]string{"amd64", "arm64", "unsupported", "unknown"}, in.DaemonArchitecture)
	valid = valid && slices.Contains([]string{"enabled", "disabled"}, in.Execution) && slices.Contains([]string{"matched", "mismatch"}, in.Configuration)
	valid = valid && slices.Contains([]string{"available", "unreachable", "unsupported_api", "invalid_response"}, in.Docker)
	valid = valid && (in.DockerAPIVersion == "" || readinessVersion.MatchString(in.DockerAPIVersion)) && len(in.DaemonID) <= 128 && !strings.ContainsFunc(in.DaemonID, func(r rune) bool { return r < 32 || r > 126 })
	valid = valid && slices.Contains([]string{"available", "missing", "unsafe", "unknown"}, in.CLI)
	for _, root := range []string{in.WorkRoot, in.JournalRoot} {
		valid = valid && slices.Contains([]string{"accessible", "missing", "unsafe", "inaccessible", "unknown"}, root)
	}
	valid = valid && slices.Contains([]string{"", "timeout", "daemon_changed", "local_check_failed"}, in.Error)
	if !valid {
		return errors.New("invalid server readiness evidence")
	}
	return nil
}

// Qualifies implements only the fixture-supported basic image hosting profile.
func (in ServerReadinessReport) Qualifies() bool {
	return in.Validate() == nil && in.OS == "linux" && (in.Architecture == "amd64" || in.Architecture == "arm64") && in.DaemonOS == "linux" && in.DaemonArchitecture == in.Architecture && in.Execution == "enabled" && in.Configuration == "matched" && in.Docker == "available" && in.DockerAPIVersion == "1.54" && strings.TrimSpace(in.DaemonID) != "" && in.CLI == "available" && in.WorkRoot == "accessible" && in.JournalRoot == "accessible" && in.WorkFreeBytes >= ServerReadinessMinimumFreeBytes && in.JournalFreeBytes >= ServerReadinessMinimumFreeBytes && in.Error == ""
}

// DecodeServerReadinessJSON rejects missing, unknown, duplicate, folded and null fields.
func DecodeServerReadinessJSON(raw []byte, out any) error {
	if len(raw) > ServerReadinessJSONLimit || !validRuntimeJSONUnicode(raw) || !uniqueObservationJSON(raw) {
		return errors.New("invalid readiness JSON")
	}
	allowed := map[string]bool{}
	switch out.(type) {
	case *struct{}:
	case *ServerReadinessReport:
		for _, k := range []string{"sample_id", "profile", "duration_ms", "os", "architecture", "execution", "configuration", "docker", "docker_api_version", "daemon_id", "daemon_os", "daemon_architecture", "cli", "work_root", "journal_root", "work_free_bytes", "journal_free_bytes", "error"} {
			allowed[k] = true
		}
	default:
		return errors.New("invalid readiness DTO")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(allowed) {
		return errors.New("invalid readiness fields")
	}
	for k, v := range fields {
		if !allowed[k] || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errors.New("invalid readiness field")
		}
	}
	return json.Unmarshal(raw, out)
}
