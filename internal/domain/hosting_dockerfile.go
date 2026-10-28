package domain

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"nerocd/internal/source"
	"strings"
	"unicode/utf8"
)

// HostingStoredDockerfileCapability identifies source materialization without Git transport.
const HostingStoredDockerfileCapability = "hosting_stored_dockerfile_v1"

// HostingDockerfileContentLimit bounds exact decoded UTF-8 source bytes.
const HostingDockerfileContentLimit = 1 << 20

// HostingDockerfileResourceJSONLimit accommodates worst-case JSON escaping and bounded settings.
const HostingDockerfileResourceJSONLimit = 8 << 20

// HostingDockerfileContentHash binds exact stored bytes before build transformations.
func HostingDockerfileContentHash(content string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}

// ValidateDockerfileResourceConfiguration rejects extra context and invalid source bytes.
func ValidateDockerfileResourceConfiguration(c DockerfileResourceConfiguration) error {
	if len(c.Content) > HostingDockerfileContentLimit || !utf8.ValidString(c.Content) || strings.ContainsRune(c.Content, 0) || strings.TrimSpace(c.Content) == "" {
		return errors.New("dockerfile content must contain 1 to 1048576 UTF-8 bytes without NUL")
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
	if s.RepositoryURL != "" || s.RequestedRef != "" || s.CredentialKind != "" || HostingDocumentHash(s.Policy) != HostingDocumentHash(source.RepositoryPolicy{}) {
		return nil, errors.New("mixed stored Dockerfile source")
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

// GitIdentity returns the immutable build source reference for either build source kind.
func (s ResourceSource) GitIdentity() (string, int64) {
	if s.Kind == "dockerfile" && s.Dockerfile != nil {
		return s.Dockerfile.SourceID, s.Dockerfile.Generation
	}
	if s.Kind == "git" && s.Git != nil {
		return s.Git.SourceID, s.Git.Generation
	}
	return "", 0
}

// MarshalJSON keeps legacy receipt bytes and selects the stored-source receipt fields.
func (r HostingBuildSourceReceipt) MarshalJSON() ([]byte, error) {
	type legacy HostingBuildSourceReceipt
	if r.Kind != "dockerfile" {
		return json.Marshal(legacy(r))
	}
	if r.Commit != "" || r.PolicyHash != "" {
		return nil, errors.New("mixed stored Dockerfile receipt")
	}
	return json.Marshal(struct {
		Kind        string `json:"kind"`
		ContentHash string `json:"content_hash"`
		BuildID     string `json:"build_id"`
		SourceID    string `json:"source_id"`
		InputID     string `json:"input_id"`
		TreeDigest  string `json:"tree_digest"`
	}{r.Kind, r.ContentHash, r.BuildID, r.SourceID, r.InputID, r.TreeDigest})
}

// MarshalJSON preserves legacy public receipts without exposing empty Git fields for stored sources.
func (r PublicHostingBuildSourceReceipt) MarshalJSON() ([]byte, error) {
	type legacy PublicHostingBuildSourceReceipt
	if r.Kind != "dockerfile" {
		return json.Marshal(legacy(r))
	}
	return json.Marshal(struct {
		Kind        string `json:"kind"`
		ContentHash string `json:"content_hash"`
		TreeDigest  string `json:"tree_digest"`
	}{r.Kind, r.ContentHash, r.TreeDigest})
}
