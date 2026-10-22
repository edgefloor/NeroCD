package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrHostingSourceCredentialUnavailable is fixed and contains no key, envelope,
// parser, or transport diagnostics.
var ErrHostingSourceCredentialUnavailable = errors.New("hosting_source_credential_unavailable")

// HostingSourceRunnerIdentity verifies the persisted dedicated-runner principal.
type HostingSourceRunnerIdentity interface {
	CurrentRunnerIdentity(context.Context) (RunnerIdentity, error)
}

// HostingBuildCredentialDelivery is constructed separately from Service. No
// endpoint or controller is registered by constructing this component.
type HostingBuildCredentialDelivery struct {
	auth       auth.Provider
	runners    HostingSourceRunnerIdentity
	repository store.HostingSourceCredentialRepository
	keys       *secretcrypto.Keyring
	clock      func() time.Time
}

// NewHostingBuildCredentialDelivery accepts immutable keys and a trusted clock.
// Missing keys remain a fixed unavailable delivery result.
func NewHostingBuildCredentialDelivery(provider auth.Provider, runners HostingSourceRunnerIdentity, repository store.HostingSourceCredentialRepository, keys *secretcrypto.Keyring, clock func() time.Time) (*HostingBuildCredentialDelivery, error) {
	if provider == nil || runners == nil || repository == nil || clock == nil {
		return nil, ErrHostingSourceCredentialUnavailable
	}
	return &HostingBuildCredentialDelivery{provider, runners, repository, keys, clock}, nil
}

// HostingSourceCredentialMaterial owns its Bytes. The eventual transport caller
// must Close it after use; it must never serialize these fields or retain copies.
type HostingSourceCredentialMaterial struct {
	Kind     string    `json:"-"`
	Username string    `json:"-"`
	Bytes    []byte    `json:"-"`
	UseUntil time.Time `json:"-"`
}

// Format redacts material for every formatting verb.
func (HostingSourceCredentialMaterial) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted source credential]")
}

// Close clears owned byte storage. Go does not guarantee erasure of string or runtime copies.
func (c *HostingSourceCredentialMaterial) Close() {
	clear(c.Bytes)
	*c = HostingSourceCredentialMaterial{}
}

// HostingSourceCredentialDelivery owns material until Take transfers ownership.
type HostingSourceCredentialDelivery struct {
	material HostingSourceCredentialMaterial
}

// Format redacts delivery contents for every formatting verb.
func (HostingSourceCredentialDelivery) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted source credential delivery]")
}

// MarshalJSON deliberately exposes no credential material.
func (HostingSourceCredentialDelivery) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Take transfers the material exactly once. Later wiring maps it to the source adapter.
func (d *HostingSourceCredentialDelivery) Take() HostingSourceCredentialMaterial {
	m := d.material
	d.material = HostingSourceCredentialMaterial{}
	return m
}

// Close clears material that has not been transferred.
func (d *HostingSourceCredentialDelivery) Close() { d.material.Close() }

// Deliver authorizes and commits the audit before decrypting. An audit records
// authorized access and may remain when decryption or decoding subsequently fails.
func (s *HostingBuildCredentialDelivery) Deliver(ctx context.Context, in domain.HostingSourceCredentialRequest) (HostingSourceCredentialDelivery, error) {
	var zero HostingSourceCredentialDelivery
	principal, err := s.auth.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	if principal.Provider != domain.PrincipalRunner || principal.ID == "" {
		return zero, auth.ErrForbidden
	}
	runner, err := s.runners.CurrentRunnerIdentity(ctx)
	if err != nil {
		return zero, auth.ErrUnauthenticated
	}
	if runner.RunnerID != principal.ID {
		return zero, auth.ErrForbidden
	}
	authorized, err := s.repository.AuthorizeHostingSourceCredential(ctx, runner.RunnerID, in)
	if err != nil {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	defer clear(authorized.Record.Envelope.Data)
	if !domain.ValidHostingSourceDeliveryID(in.DeliveryID) || authorized.Window.DeliveryID != in.DeliveryID || authorized.Window.RequestHash != domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingSourceCredentialRequest
	}{runner.RunnerID, in}) || !s.clock().Before(authorized.Window.UseUntil) || authorized.Record.Metadata.SecretID != authorized.SecretID || authorized.Record.Metadata.Version != authorized.SecretVersion {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	plaintext, err := s.keys.Open(secretcrypto.Binding{ProjectID: authorized.ProjectID, SecretID: authorized.SecretID, Version: authorized.SecretVersion}, authorized.Record.Envelope)
	if err != nil {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	defer clear(plaintext)
	material, err := decodeHostingSourceCredential(authorized.Kind, plaintext)
	if err != nil {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	if ctx.Err() != nil || !s.clock().Before(authorized.Window.UseUntil) {
		material.Close()
		return zero, ErrHostingSourceCredentialUnavailable
	}
	material.UseUntil = authorized.Window.UseUntil
	return HostingSourceCredentialDelivery{material: material}, nil
}

func decodeHostingSourceCredential(kind string, plaintext []byte) (HostingSourceCredentialMaterial, error) {
	var zero HostingSourceCredentialMaterial
	if len(plaintext) == 0 || len(plaintext) > 65536 || !utf8.Valid(plaintext) {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	if kind == "ssh_private_key" {
		// The established SSH parser rejects encrypted keys without an interactive
		// passphrase. No algorithm or cryptographic primitive is implemented here.
		if _, err := ssh.ParseRawPrivateKey(plaintext); err != nil {
			return zero, ErrHostingSourceCredentialUnavailable
		}
		return HostingSourceCredentialMaterial{Kind: kind, Bytes: bytes.Clone(plaintext)}, nil
	}
	if kind != "https_basic" {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	fields := map[string]string{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return zero, ErrHostingSourceCredentialUnavailable
		}
		key, ok := token.(string)
		if !ok || (key != "username" && key != "password") {
			return zero, ErrHostingSourceCredentialUnavailable
		}
		if _, ok = fields[key]; ok {
			return zero, ErrHostingSourceCredentialUnavailable
		}
		var value string
		if decoder.Decode(&value) != nil {
			return zero, ErrHostingSourceCredentialUnavailable
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	if _, err = decoder.Token(); err != io.EOF {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	username, password := fields["username"], fields["password"]
	if !sourceCredentialField(username, 256) || !sourceCredentialField(password, 65536) {
		return zero, ErrHostingSourceCredentialUnavailable
	}
	return HostingSourceCredentialMaterial{Kind: kind, Username: username, Bytes: []byte(password)}, nil
}
func sourceCredentialField(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !strings.ContainsFunc(value, unicode.IsControl)
}
