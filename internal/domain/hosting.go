package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ProjectEnvironment groups hosting resources within a project.
type ProjectEnvironment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Resource is desired application configuration. It does not imply deployment.
type Resource struct {
	ID                   string         `json:"id"`
	ProjectID            string         `json:"project_id"`
	ProjectEnvironmentID string         `json:"project_environment_id"`
	Name                 string         `json:"name"`
	Kind                 string         `json:"kind"`
	Source               ResourceSource `json:"source"`
	Generation           int64          `json:"generation"`
	CreatedBy            string         `json:"created_by"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

// ResourceSource selects an image, an immutable Git source, or a managed Compose source.
type ResourceSource struct {
	Kind       string                    `json:"kind"`
	Image      ImageSource               `json:"-"`
	Git        *GitResourceSource        `json:"-"`
	Compose    *HostingStackSource       `json:"-"`
	Dockerfile *DockerfileResourceSource `json:"-"`
}

// DockerfileResourceConfiguration stores exact source text and public build settings.
type DockerfileResourceConfiguration struct {
	Content  string               `json:"content"`
	Settings HostingBuildSettings `json:"settings"`
}

// DockerfileResourceSource identifies one immutable stored Dockerfile version.
type DockerfileResourceSource struct {
	SourceID    string `json:"source_id"`
	Generation  int64  `json:"generation"`
	ContentHash string `json:"content_hash"`
	DockerfileResourceConfiguration
}

// GitResourceConfiguration is public, nonsecret desired build configuration.
type GitResourceConfiguration struct {
	RepositoryURL  string               `json:"repository_url"`
	RequestedRef   string               `json:"requested_ref"`
	CredentialKind string               `json:"credential_kind"`
	Settings       HostingBuildSettings `json:"settings"`
}

// GitResourceSource expands one exact immutable persisted source version.
type GitResourceSource struct {
	SourceID   string `json:"source_id"`
	Generation int64  `json:"generation"`
	GitResourceConfiguration
}

// ResourceSourceInput excludes server-owned source identities and policy.
type ResourceSourceInput struct {
	Kind       string                           `json:"kind"`
	Image      ImageSource                      `json:"-"`
	Git        *GitResourceConfiguration        `json:"-"`
	Compose    *HostingStackSource              `json:"-"`
	Dockerfile *DockerfileResourceConfiguration `json:"-"`
}

func strictSourceJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("source must be one JSON document")
	}
	return nil
}

// MarshalJSON emits exactly the selected source variant.
func (s ResourceSource) MarshalJSON() ([]byte, error) {
	switch s.Kind {
	case "dockerfile":
		if s.Dockerfile == nil || s.Git != nil || s.Compose != nil || s.Image.Reference != "" {
			return nil, errors.New("missing or mixed Dockerfile source")
		}
		return json.Marshal(struct {
			Kind       string                    `json:"kind"`
			Dockerfile *DockerfileResourceSource `json:"dockerfile"`
		}{s.Kind, s.Dockerfile})
	case "compose":
		if s.Compose == nil || s.Git != nil || s.Dockerfile != nil || s.Image.Reference != "" {
			return nil, errors.New("missing or mixed Compose source")
		}
		if _, err := ParseHostingStack(*s.Compose); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Kind    string              `json:"kind"`
			Compose *HostingStackSource `json:"compose"`
		}{s.Kind, s.Compose})
	case "image":
		if s.Git != nil || s.Compose != nil || s.Dockerfile != nil {
			return nil, errors.New("mixed source variants")
		}
		return json.Marshal(struct {
			Kind  string      `json:"kind"`
			Image ImageSource `json:"image"`
		}{s.Kind, s.Image})
	case "git":
		if s.Git == nil || s.Compose != nil || s.Dockerfile != nil || s.Image.Reference != "" {
			return nil, errors.New("missing or mixed git source")
		}
		return json.Marshal(struct {
			Kind string             `json:"kind"`
			Git  *GitResourceSource `json:"git"`
		}{s.Kind, s.Git})
	}
	return nil, errors.New("unsupported source kind")
}

// UnmarshalJSON reads one exclusive persisted or expanded source variant.
func (s *ResourceSource) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind       string          `json:"kind"`
		Image      json.RawMessage `json:"image"`
		Git        json.RawMessage `json:"git"`
		Compose    json.RawMessage `json:"compose"`
		Dockerfile json.RawMessage `json:"dockerfile"`
	}
	if err := strictSourceJSON(data, &wire); err != nil {
		return err
	}
	*s = ResourceSource{Kind: wire.Kind}
	switch wire.Kind {
	case "dockerfile":
		if len(wire.Image) > 0 || len(wire.Git) > 0 || len(wire.Compose) > 0 || len(wire.Dockerfile) == 0 || bytes.Equal(wire.Dockerfile, []byte("null")) {
			return errors.New("dockerfile source requires only dockerfile")
		}
		s.Dockerfile = &DockerfileResourceSource{}
		return strictSourceJSON(wire.Dockerfile, s.Dockerfile)
	case "compose":
		if !uniqueStackSourceJSON(data) {
			return errors.New("duplicate Compose source field")
		}
		if len(wire.Dockerfile) > 0 || len(wire.Image) > 0 || len(wire.Git) > 0 || len(wire.Compose) == 0 || bytes.Equal(wire.Compose, []byte("null")) {
			return errors.New("compose source requires only compose")
		}
		s.Compose = &HostingStackSource{}
		if err := strictSourceJSON(wire.Compose, s.Compose); err != nil {
			return err
		}
		_, err := ParseHostingStack(*s.Compose)
		return err
	case "image":
		if len(wire.Dockerfile) > 0 || len(wire.Compose) > 0 || len(wire.Git) > 0 || len(wire.Image) == 0 || bytes.Equal(wire.Image, []byte("null")) {
			return errors.New("image source requires only image")
		}
		return strictSourceJSON(wire.Image, &s.Image)
	case "git":
		if len(wire.Dockerfile) > 0 || len(wire.Compose) > 0 || len(wire.Image) > 0 || len(wire.Git) == 0 || bytes.Equal(wire.Git, []byte("null")) {
			return errors.New("git source requires only git")
		}
		s.Git = &GitResourceSource{}
		return strictSourceJSON(wire.Git, s.Git)
	}
	return errors.New("unsupported source kind")
}

// UnmarshalJSON rejects server-owned fields and ambiguous public source variants.
func (s *ResourceSourceInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind       string          `json:"kind"`
		Image      json.RawMessage `json:"image"`
		Git        json.RawMessage `json:"git"`
		Compose    json.RawMessage `json:"compose"`
		Dockerfile json.RawMessage `json:"dockerfile"`
	}
	if err := strictSourceJSON(data, &wire); err != nil {
		return err
	}
	*s = ResourceSourceInput{Kind: wire.Kind}
	switch wire.Kind {
	case "dockerfile":
		if len(wire.Image) > 0 || len(wire.Git) > 0 || len(wire.Compose) > 0 || len(wire.Dockerfile) == 0 || bytes.Equal(wire.Dockerfile, []byte("null")) {
			return errors.New("dockerfile source requires only dockerfile")
		}
		s.Dockerfile = &DockerfileResourceConfiguration{}
		return strictSourceJSON(wire.Dockerfile, s.Dockerfile)
	case "compose":
		if !uniqueStackSourceJSON(data) {
			return errors.New("duplicate Compose source field")
		}
		if len(wire.Dockerfile) > 0 || len(wire.Image) > 0 || len(wire.Git) > 0 || len(wire.Compose) == 0 || bytes.Equal(wire.Compose, []byte("null")) {
			return errors.New("compose source requires only compose")
		}
		s.Compose = &HostingStackSource{}
		if err := strictSourceJSON(wire.Compose, s.Compose); err != nil {
			return err
		}
		return nil
	case "image":
		if len(wire.Dockerfile) > 0 || len(wire.Compose) > 0 || len(wire.Git) > 0 || len(wire.Image) == 0 || bytes.Equal(wire.Image, []byte("null")) {
			return errors.New("image source requires only image")
		}
		return strictSourceJSON(wire.Image, &s.Image)
	case "git":
		if len(wire.Dockerfile) > 0 || len(wire.Compose) > 0 || len(wire.Image) > 0 || len(wire.Git) == 0 || bytes.Equal(wire.Git, []byte("null")) {
			return errors.New("git source requires only git")
		}
		s.Git = &GitResourceConfiguration{}
		return strictSourceJSON(wire.Git, s.Git)
	}
	return errors.New("unsupported source kind")
}

// Source converts public configuration without manufacturing persisted identity.
func (s ResourceSourceInput) Source() ResourceSource {
	out := ResourceSource{Kind: s.Kind, Image: s.Image}
	if s.Dockerfile != nil {
		out.Dockerfile = &DockerfileResourceSource{DockerfileResourceConfiguration: *s.Dockerfile}
		out.Dockerfile.Settings = s.Dockerfile.Settings.Clone()
	}
	if s.Compose != nil {
		value := *s.Compose
		out.Compose = &value
	}
	if s.Git != nil {
		out.Git = &GitResourceSource{GitResourceConfiguration: *s.Git}
		out.Git.Settings = out.Git.Settings.Clone()
	}
	return out
}

// ImageSource holds an explicitly tagged image or repository SHA-256 digest.
type ImageSource struct {
	Reference string `json:"reference"`
}

var imagePathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*$`)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var imageSHA256 = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imagePort = regexp.MustCompile(`^[0-9]+$`)
var imageHostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// ValidateImageReference validates syntax only; it never contacts a registry.
// A registry port is distinct from a tag because only the final path component
// can contain a tag separator. A bare digest is never an image repository.
func ValidateImageReference(value string) error {
	invalid := errors.New("image reference must contain a valid repository and explicit tag or sha256 digest")
	if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\\?#") || strings.Contains(value, "://") || strings.ContainsFunc(value, unicode.IsSpace) {
		return invalid
	}
	repository := value
	if strings.Contains(value, "@") {
		var digest string
		repository, digest, _ = strings.Cut(value, "@")
		if !imageSHA256.MatchString(digest) {
			return invalid
		}
		// A tag alongside a digest is legal, but must also be syntactically valid.
		if strings.LastIndex(repository, ":") > strings.LastIndex(repository, "/") {
			var tag string
			pos := strings.LastIndex(repository, ":")
			repository, tag = repository[:pos], repository[pos+1:]
			if !imageTag.MatchString(tag) {
				return invalid
			}
		}
	} else {
		pos := strings.LastIndex(value, ":")
		if pos <= strings.LastIndex(value, "/") || pos < 1 || !imageTag.MatchString(value[pos+1:]) {
			return invalid
		}
		repository = value[:pos]
		if repository == "sha256" && len(value[pos+1:]) == 64 {
			return invalid
		}
	}
	if len(repository) == 0 || len(repository) > 255 {
		return invalid
	}
	parts := strings.Split(repository, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost" || strings.HasPrefix(parts[0], "[")) {
		if !validImageRegistry(parts[0]) {
			return invalid
		}
		parts = parts[1:]
	}
	for _, part := range parts {
		if !imagePathComponent.MatchString(part) {
			return invalid
		}
	}
	return nil
}

// validImageRegistry keeps IPv6 brackets distinct from DNS names. Image
// references permit IPv6 literals with optional ports, but no zones or embedded
// dotted IPv4 addresses. DNS names and IPv4 addresses must be unbracketed.
func validImageRegistry(registry string) bool {
	host := registry
	port := ""
	if strings.HasPrefix(registry, "[") {
		end := strings.IndexByte(registry, ']')
		if end < 0 {
			return false
		}
		host = registry[1:end]
		if net.ParseIP(host) == nil || !strings.Contains(host, ":") || strings.Contains(host, ".") {
			return false
		}
		suffix := registry[end+1:]
		if suffix == "" {
			return true
		}
		if !strings.HasPrefix(suffix, ":") {
			return false
		}
		port = suffix[1:]
	} else {
		if strings.Contains(registry, ":") {
			var err error
			host, port, err = net.SplitHostPort(registry)
			if err != nil {
				return false
			}
		}
		if net.ParseIP(host) == nil {
			for _, label := range strings.Split(host, ".") {
				if len(label) > 63 || !imageHostname.MatchString(label) {
					return false
				}
			}
		}
		if !strings.Contains(registry, ":") {
			return true
		}
	}
	if !imagePort.MatchString(port) {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535
}

// ValidateResourceSource rejects unsupported sources before persistence.
func ValidateResourceSource(source ResourceSource) error {
	if source.Kind == "dockerfile" {
		if source.Dockerfile == nil || source.Git != nil || source.Compose != nil || source.Image.Reference != "" {
			return errors.New("invalid Dockerfile source")
		}
		return ValidateDockerfileResourceConfiguration(source.Dockerfile.DockerfileResourceConfiguration)
	}
	if source.Dockerfile != nil {
		return errors.New("mixed resource source")
	}
	if source.Kind == "compose" {
		if source.Compose == nil || source.Git != nil || source.Image.Reference != "" {
			return errors.New("invalid Compose source")
		}
		_, err := ParseHostingStack(*source.Compose)
		return err
	}
	if source.Compose != nil {
		return errors.New("mixed resource source")
	}

	if source.Kind == "git" {
		if source.Git == nil || source.Image.Reference != "" || source.Git.SourceID == "" || source.Git.Generation < 1 {
			return errors.New("invalid git source reference")
		}
		return nil
	}
	if source.Kind != "image" || source.Git != nil {
		return errors.New("source.kind must be image, git, or compose")
	}
	return ValidateImageReference(source.Image.Reference)
}

// ValidateHostingName bounds names consistently in both repository adapters.
func ValidateHostingName(name string) error {
	if name == "" || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("name must contain 1 to 128 bytes without surrounding whitespace or control characters")
	}
	return nil
}
