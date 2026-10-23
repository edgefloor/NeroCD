package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidHostingStorage reports a closed metadata validation failure.
var ErrInvalidHostingStorage = errors.New("invalid hosting storage")

// HostingStorageInput creates desired metadata only. RequestID contains exactly
// 32 lowercase hexadecimal characters generated from 128 random bits and is
// retained unchanged across retries. It is not a hyphenated UUID.
type HostingStorageInput struct {
	RequestID                  string `json:"request_id"`
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
	MountPath                  string `json:"mount_path"`
}

// HostingStorage is one immutable, retained local volume for a resource binding.
// Its binding-derived identifiers are nonsecret and independent of display names.
// Saving this document neither creates a Docker volume nor schedules execution.
type HostingStorage struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ResourceID    string    `json:"resource_id"`
	BindingID     string    `json:"binding_id"`
	DestinationID string    `json:"destination_id"`
	Kind          string    `json:"kind"`
	Driver        string    `json:"driver"`
	VolumeName    string    `json:"volume_name"`
	MountPath     string    `json:"mount_path"`
	AccessMode    string    `json:"access_mode"`
	Retention     string    `json:"retention"`
	Generation    int64     `json:"generation"`
	Hash          string    `json:"hash"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// HostingStorageRef pins immutable metadata. Generation is initially always one;
// a later metadata version must never imply relocation or a different data volume.
type HostingStorageRef struct {
	StorageID  string `json:"storage_id"`
	Generation int64  `json:"generation"`
	Hash       string `json:"hash"`
}

var hostingStoragePath = regexp.MustCompile(`\A/[A-Za-z0-9_./-]+\z`)
var hostingStorageRequest = regexp.MustCompile(`\A[0-9a-f]{32}\z`)

// ValidateHostingStorageMountPath accepts canonical absolute container paths up
// to 512 bytes. The restricted alphabet excludes Docker CSV and bind syntax.
func ValidateHostingStorageMountPath(value string) error {
	if len(value) > 512 || value == "/" || !hostingStoragePath.MatchString(value) || path.Clean(value) != value {
		return ErrInvalidHostingStorage
	}
	for _, reserved := range []string{"/proc", "/sys", "/dev"} {
		if value == reserved || strings.HasPrefix(value, reserved+"/") {
			return ErrInvalidHostingStorage
		}
	}
	return nil
}

// UnmarshalJSON rejects server-owned fields, duplicate keys and ambiguous input.
func (in *HostingStorageInput) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) {
		return ErrInvalidHostingStorage
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidHostingStorage
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return ErrInvalidHostingStorage
		}
		switch key {
		case "request_id", "expected_resource_generation", "mount_path":
		default:
			return ErrInvalidHostingStorage
		}
		seen[key] = true
		var field json.RawMessage
		if decoder.Decode(&field) != nil || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return ErrInvalidHostingStorage
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 {
		return ErrInvalidHostingStorage
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalidHostingStorage
	}
	type wire HostingStorageInput
	var value wire
	if strictSourceJSON(raw, &value) != nil {
		return ErrInvalidHostingStorage
	}
	*in = HostingStorageInput(value)
	return nil
}

// Validate rejects unsupported request identities, generations and mount paths.
func (in HostingStorageInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || in.ExpectedResourceGeneration < 1 {
		return ErrInvalidHostingStorage
	}
	return ValidateHostingStorageMountPath(in.MountPath)
}

// HostingStorageIdentity derives a stable nonsecret identity from one immutable
// binding. The 128-bit identifier is never derived from a display or operation name.
func HostingStorageIdentity(binding string) (id, volume string) {
	suffix := HostingDocumentHash(struct {
		BindingID string `json:"binding_id"`
	}{binding})[7:39]
	return "hsv_" + suffix, "ncdv_" + suffix
}

// DocumentHash covers every immutable identity and mount policy field. Audit
// attribution and timestamps do not change the runtime metadata reference.
func (s HostingStorage) DocumentHash() string {
	return HostingDocumentHash(struct {
		ID            string `json:"id"`
		ProjectID     string `json:"project_id"`
		ResourceID    string `json:"resource_id"`
		BindingID     string `json:"binding_id"`
		DestinationID string `json:"destination_id"`
		Kind          string `json:"kind"`
		Driver        string `json:"driver"`
		VolumeName    string `json:"volume_name"`
		MountPath     string `json:"mount_path"`
		AccessMode    string `json:"access_mode"`
		Retention     string `json:"retention"`
		Generation    int64  `json:"generation"`
	}{s.ID, s.ProjectID, s.ResourceID, s.BindingID, s.DestinationID, s.Kind, s.Driver, s.VolumeName, s.MountPath, s.AccessMode, s.Retention, s.Generation})
}

// Validate checks the fixed initial contract and verifies its complete identity hash.
func (s HostingStorage) Validate() error {
	for _, value := range []string{s.ProjectID, s.ResourceID, s.BindingID, s.DestinationID, s.CreatedBy} {
		if !validRuntimeReferenceID(value) {
			return ErrInvalidHostingStorage
		}
	}
	id, volume := HostingStorageIdentity(s.BindingID)
	if s.ID != id || s.VolumeName != volume || s.Kind != "platform_managed" || s.Driver != "local" || s.AccessMode != "read_write" || s.Retention != "retain" || s.Generation != 1 || s.CreatedAt.IsZero() || s.Hash != s.DocumentHash() {
		return ErrInvalidHostingStorage
	}
	return ValidateHostingStorageMountPath(s.MountPath)
}

// Reference returns the exact immutable document reference for future admission.
func (s HostingStorage) Reference() HostingStorageRef {
	return HostingStorageRef{StorageID: s.ID, Generation: s.Generation, Hash: s.Hash}
}

// HostingStorageAuditID binds a retained creation audit to one resource request.
func HostingStorageAuditID(resource, request string) string {
	return "aud_hosting_storage_" + HostingDocumentHash(struct{ Resource, Request string }{resource, request})[7:]
}
