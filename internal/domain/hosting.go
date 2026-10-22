package domain

import (
	"errors"
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

// ResourceSource identifies the desired container image without resolving it.
type ResourceSource struct {
	Kind  string      `json:"kind"`
	Image ImageSource `json:"image"`
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
		registry := parts[0]
		host := registry
		if strings.HasPrefix(registry, "[") || strings.Contains(registry, ":") {
			var port string
			var err error
			host, port, err = net.SplitHostPort(registry)
			if err != nil || !imagePort.MatchString(port) {
				return invalid
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return invalid
			}
		}
		if net.ParseIP(host) == nil {
			for _, label := range strings.Split(host, ".") {
				if len(label) > 63 || !imageHostname.MatchString(label) {
					return invalid
				}
			}
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

// ValidateResourceSource rejects unsupported sources before persistence.
func ValidateResourceSource(source ResourceSource) error {
	if source.Kind != "image" {
		return errors.New("source.kind must be image")
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
