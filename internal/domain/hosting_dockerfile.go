package domain

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// HostingStoredDockerfileCapability identifies source materialization without Git transport.
const HostingStoredDockerfileCapability = "hosting_stored_dockerfile_v1"

// HostingDockerfileContentLimit bounds exact decoded UTF-8 source bytes.
const HostingDockerfileContentLimit = 1 << 20

// HostingDockerfileResourceJSONLimit accommodates worst-case JSON escaping and bounded settings.
const HostingDockerfileResourceJSONLimit = 6*HostingDockerfileContentLimit + (256 << 10)

// HostingDockerfileContentHash binds exact stored bytes before build transformations.
func HostingDockerfileContentHash(content string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}

// ValidateDockerfileResourceConfiguration rejects extra context and invalid source bytes.
func ValidateDockerfileResourceConfiguration(c DockerfileResourceConfiguration) error {
	if len(c.Content) > HostingDockerfileContentLimit || !utf8.ValidString(c.Content) || strings.ContainsRune(c.Content, 0) || strings.TrimSpace(c.Content) == "" {
		return errors.New("Dockerfile content must contain 1 to 1048576 UTF-8 bytes without NUL")
	}
	s := c.Settings
	if s.Strategy != "dockerfile" || s.ContextDir != "" || s.DockerfilePath != "" && s.DockerfilePath != "Dockerfile" {
		return errors.New("stored Dockerfile requires dockerfile strategy and root Dockerfile context")
	}
	return ValidateHostingBuildSettings(s)
}

// MarshalJSON preserves legacy Git documents and omits unrelated transport fields for stored sources.
func (s HostingGitSource) MarshalJSON() ([]byte, error) {
	type legacy HostingGitSource
	if s.Kind != "dockerfile" {
		return json.Marshal(legacy(s))
	}
	return json.Marshal(struct {
		Kind        string               `json:"kind"`
		ID          string               `json:"id"`
		ResourceID  string               `json:"resource_id"`
		ProjectID   string               `json:"project_id"`
		Generation  int64                `json:"generation"`
		Content     string               `json:"content"`
		ContentHash string               `json:"content_hash"`
		Settings    HostingBuildSettings `json:"settings"`
	}{s.Kind, s.ID, s.ResourceID, s.ProjectID, s.Generation, s.Content, s.ContentHash, s.Settings})
}

// MarshalJSON exposes stored source identity without content or Git transport metadata.
func (s PublicHostingBuildSource) MarshalJSON() ([]byte, error) {
	type legacy PublicHostingBuildSource
	if s.Kind != "dockerfile" {
		return json.Marshal(legacy(s))
	}
	return json.Marshal(struct {
		Kind        string               `json:"kind"`
		SourceID    string               `json:"source_id"`
		Generation  int64                `json:"generation"`
		ContentHash string               `json:"content_hash"`
		Settings    HostingBuildSettings `json:"settings"`
	}{s.Kind, s.SourceID, s.Generation, s.ContentHash, s.Settings})
}
