package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/distribution/reference"
)

// HostingRegistryPullCapability requires explicit private-image pull configuration.
const HostingRegistryPullCapability = "hosting_registry_pull_v1"

// ErrInvalidRegistryCredential never contains protected values.
var ErrInvalidRegistryCredential = errors.New("invalid registry credential")

// HostingRegistryCredentialRef pins a project-owned immutable secret version.
type HostingRegistryCredentialRef struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
}

// Validate checks the complete immutable secret reference.
func (r HostingRegistryCredentialRef) Validate() error {
	if !validRuntimeReferenceID(r.SecretID) || r.Version < 1 {
		return ErrInvalidRegistryCredential
	}
	return nil
}

// ValidateHostingRegistrySnapshot limits selected credentials to ordinary image paths.
func ValidateHostingRegistrySnapshot(s HostingSnapshot) error {
	if s.RegistryCredential == (HostingRegistryCredentialRef{}) {
		return nil
	}
	if s.RegistryCredential.Validate() != nil || s.Stack != (HostingStackRef{}) || s.LocalBuild != (HostingLocalBuildSource{}) || s.Database != (HostingDatabaseExecutionRef{}) || s.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || s.StorageRouteContinuity != (HostingStorageRouteContinuityRef{}) {
		return ErrInvalidRegistryCredential
	}
	_, err := HostingImageRegistry(s.ImageReference)
	return err
}

// HostingImageRegistry derives the authority from the maintained image grammar.
func HostingImageRegistry(image string) (string, error) {
	n, err := NormalizeHostingImage(image)
	if err != nil {
		return "", ErrInvalidRegistryCredential
	}
	named, err := reference.ParseNormalizedNamed(n)
	if err != nil {
		return "", ErrInvalidRegistryCredential
	}
	return reference.Domain(named), nil
}

// HostingRegistryCredential contains protected bytes, never metadata or journal evidence.
type HostingRegistryCredential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Format redacts credentials for all formatting verbs.
func (HostingRegistryCredential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted registry credential]")
}

// MarshalJSON prevents accidental credential serialization.
func (HostingRegistryCredential) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// Close releases references to protected strings; Go cannot guarantee string erasure.
func (c *HostingRegistryCredential) Close() { *c = HostingRegistryCredential{} }

// DecodeHostingRegistryCredential accepts only the closed bounded secret payload.
func DecodeHostingRegistryCredential(raw []byte) (HostingRegistryCredential, error) {
	var c HostingRegistryCredential
	if len(raw) > 32768 || !utf8.Valid(raw) || DecodeHostingStorageProtocolJSON(raw, &c) != nil {
		return c, ErrInvalidRegistryCredential
	}
	switch c.Registry {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		c.Registry = "docker.io"
	}
	if c.Registry == "" || len(c.Registry) > 255 || c.Registry != strings.ToLower(c.Registry) || strings.ContainsAny(c.Registry, "/@?#\\") || c.Username == "" || len(c.Username) > 255 || strings.Contains(c.Username, ":") || c.Password == "" || len(c.Password) > 20000 {
		c.Close()
		return c, ErrInvalidRegistryCredential
	}
	for _, v := range []string{c.Registry, c.Username, c.Password} {
		if strings.ContainsFunc(v, unicode.IsControl) {
			c.Close()
			return c, ErrInvalidRegistryCredential
		}
	}
	host, err := HostingImageRegistry(c.Registry + "/nerocd/probe:v1")
	if err != nil || host != c.Registry {
		c.Close()
		return c, ErrInvalidRegistryCredential
	}
	return c, nil
}

// HostingRegistryCredentialRequest cannot select a secret or registry.
type HostingRegistryCredentialRequest struct {
	Authority  HostingAuthority `json:"authority"`
	DeliveryID string           `json:"delivery_id"`
}

// Validate bounds the full attempt authority and retained delivery identifier.
func (r HostingRegistryCredentialRequest) Validate() error {
	if r.Authority.RunID != "run_"+r.Authority.OperationID || !validStorageArchiveAuthority(r.Authority, "registry", "sha256:"+strings.Repeat("0", 64)) || !ValidHostingSourceDeliveryID(r.DeliveryID) {
		return ErrInvalidRegistryCredential
	}
	return nil
}

// HostingRegistryCredentialDelivery is used only by the protected endpoint codec.
type HostingRegistryCredentialDelivery struct {
	Credential HostingRegistryCredential `json:"credential"`
	UseUntil   time.Time                 `json:"use_until"`
}

// Format redacts delivery material for all formatting verbs.
func (HostingRegistryCredentialDelivery) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted registry credential delivery]")
}

// MarshalJSON blocks accidental serialization outside the protected codec.
func (HostingRegistryCredentialDelivery) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// Close releases protected response material.
func (d *HostingRegistryCredentialDelivery) Close() {
	d.Credential.Close()
	*d = HostingRegistryCredentialDelivery{}
}

// WriteHostingRegistryDelivery is the sole deliberate protected serialization path.
func WriteHostingRegistryDelivery(w io.Writer, d *HostingRegistryCredentialDelivery) error {
	type credential HostingRegistryCredential
	return json.NewEncoder(w).Encode(struct {
		Credential credential `json:"credential"`
		UseUntil   time.Time  `json:"use_until"`
	}{credential(d.Credential), d.UseUntil})
}

// ReadHostingRegistryDelivery bounds and validates protected HTTP response bytes.
func ReadHostingRegistryDelivery(r io.Reader) (*HostingRegistryCredentialDelivery, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 32769))
	defer clear(raw)
	if err != nil || len(raw) > 32768 {
		return nil, ErrInvalidRegistryCredential
	}
	var out HostingRegistryCredentialDelivery
	if DecodeHostingStorageProtocolJSON(raw, &out) != nil || out.UseUntil.IsZero() {
		return nil, ErrInvalidRegistryCredential
	}
	type credential HostingRegistryCredential
	b, err := json.Marshal(credential(out.Credential))
	if err != nil {
		return nil, ErrInvalidRegistryCredential
	}
	defer clear(b)
	c, err := DecodeHostingRegistryCredential(b)
	if err != nil {
		return nil, err
	}
	out.Credential = c
	return &out, nil
}

// UnmarshalJSON rejects aliases, duplicate fields and unknown secret selectors.
func (r *HostingRegistryCredentialRef) UnmarshalJSON(raw []byte) error {
	type wire HostingRegistryCredentialRef
	var v wire
	if DecodeHostingStorageProtocolJSON(raw, &v) != nil || HostingRegistryCredentialRef(v).Validate() != nil {
		return ErrInvalidRegistryCredential
	}
	*r = HostingRegistryCredentialRef(v)
	return nil
}

// UnmarshalJSON preserves the closed deployment document and rejects an explicit null credential selection.
func (in *HostingDeploymentInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDeploymentInput
	var value wire
	if DecodeHostingStorageProtocolJSON(raw, &value) != nil {
		return ErrInvalidRegistryCredential
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrInvalidRegistryCredential
	}
	if selection, ok := fields["registry_credential"]; ok && (strings.TrimSpace(string(selection)) == "null" || value.RegistryCredential == nil) {
		return ErrInvalidRegistryCredential
	}
	*in = HostingDeploymentInput(value)
	return nil
}
