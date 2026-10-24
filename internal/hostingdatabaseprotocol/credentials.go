// Package hostingdatabaseprotocol carries protected database credentials through an explicit codec.
package hostingdatabaseprotocol

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// Purpose and limits bound the credential delivery protocol.
const (
	Purpose      = "hosting_database_credentials"
	MaxWireBytes = 16 << 10
)

// ErrInvalidMessage excludes parser diagnostics and protected values.
var ErrInvalidMessage = errors.New("invalid hosting database credentials")

// Metadata binds credentials to their authenticated recipient and original deadline.
type Metadata struct {
	RunnerID    string                                `json:"runner_id"`
	Authority   domain.HostingAuthority               `json:"authority"`
	DeliveryID  string                                `json:"delivery_id"`
	Mode        string                                `json:"mode"`
	PlanHash    string                                `json:"plan_hash"`
	Database    domain.HostingDatabaseExecutionRef    `json:"database"`
	RequestHash string                                `json:"request_hash"`
	Purpose     string                                `json:"purpose"`
	UseUntil    time.Time                             `json:"use_until"`
	References  []domain.HostingDatabaseCredentialRef `json:"references"`
}

// Credential contains borrowed or owned bytes. Ordinary output suppresses them.
type Credential struct {
	Reference domain.HostingDatabaseCredentialRef `json:"-"`
	Value     []byte                              `json:"-"`
}

// Format suppresses all credential contents.
func (Credential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted database credential]")
}

// MarshalJSON suppresses values outside the explicit codec.
func (Credential) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Credentials owns its bytes and must not be copied or used concurrently.
// Close cannot erase copies retained by Go or the recipient.
type Credentials struct {
	metadata Metadata
	entries  []Credential
	closed   bool
}

// Format suppresses all contents for every formatting verb.
func (Credentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted database credentials]")
}

// MarshalJSON suppresses values outside the explicit codec.
func (Credentials) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// RequestHash binds public intent to the authenticated runner.
func RequestHash(runner string, in domain.HostingDatabaseCredentialRequest) string {
	return domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingDatabaseCredentialRequest
	}{runner, in})
}

func validMetadata(m Metadata) bool {
	in := domain.HostingDatabaseCredentialRequest{Authority: m.Authority, DeliveryID: m.DeliveryID, Mode: m.Mode, PlanHash: m.PlanHash}
	if in.Validate() != nil || m.RunnerID == "" || len(m.RunnerID) > 256 || m.RequestHash != RequestHash(m.RunnerID, in) || m.Purpose != Purpose || m.UseUntil.IsZero() || len(m.References) != 2 {
		return false
	}
	recipe, err := domain.HostingDatabaseRecipeFor(m.Database.Engine, m.Database.RecipeVersion)
	if err != nil || len(m.Database.ConfigurationHash) != 71 || m.Database.ConfigurationHash[:7] != "sha256:" || strings.ToLower(m.Database.ConfigurationHash) != m.Database.ConfigurationHash {
		return false
	}
	if _, err = hex.DecodeString(m.Database.ConfigurationHash[7:]); err != nil {
		return false
	}
	for i, ref := range m.References {
		if ref.Role != recipe.Credentials[i].Role || ref.Username != recipe.Credentials[i].Username || len(ref.SecretID) != 40 || !strings.HasPrefix(ref.SecretID, "msec_db_") || strings.ToLower(ref.SecretID) != ref.SecretID || ref.Version < 1 || i > 0 && ref.SecretID == m.References[0].SecretID {
			return false
		}
		if _, err := hex.DecodeString(ref.SecretID[8:]); err != nil {
			return false
		}
	}
	return true
}

// ValidateMetadata checks closed public bindings before decryption or codec use.
func ValidateMetadata(m Metadata) error {
	if !validMetadata(m) {
		return ErrInvalidMessage
	}
	return nil
}

func validEntries(m Metadata, entries []Credential) bool {
	if len(entries) != len(m.References) {
		return false
	}
	for i, e := range entries {
		if e.Reference != m.References[i] || len(e.Value) != 43 {
			return false
		}
		// Creation generates exactly 32 random bytes encoded as canonical base64url.
		var decoded [32]byte
		n, err := base64.RawURLEncoding.Strict().Decode(decoded[:], e.Value)
		clear(decoded[:])
		if err != nil || n != 32 {
			return false
		}
	}
	return true
}

// NewCredentials copies values into storage owned by the returned object.
func NewCredentials(m Metadata, entries []Credential) (*Credentials, error) {
	if !validMetadata(m) || !validEntries(m, entries) {
		return nil, ErrInvalidMessage
	}
	m.References = slices.Clone(m.References)
	c := &Credentials{metadata: m, entries: make([]Credential, len(entries))}
	for i, e := range entries {
		c.entries[i] = Credential{Reference: e.Reference, Value: bytes.Clone(e.Value)}
	}
	return c, nil
}

// Metadata returns an independent copy of public bindings.
func (c *Credentials) Metadata() (Metadata, error) {
	if c == nil || c.closed {
		return Metadata{}, ErrInvalidMessage
	}
	m := c.metadata
	m.References = slices.Clone(m.References)
	return m, nil
}

// WithValues borrows entries only for fn. The callback must not retain or mutate them.
func (c *Credentials) WithValues(fn func([]Credential) error) error {
	if c == nil || c.closed || fn == nil {
		return ErrInvalidMessage
	}
	return fn(c.entries)
}

// Close clears owned buffers and prevents subsequent access.
func (c *Credentials) Close() {
	if c == nil {
		return
	}
	for i := range c.entries {
		clear(c.entries[i].Value)
		c.entries[i] = Credential{}
	}
	c.entries = nil
	c.metadata = Metadata{}
	c.closed = true
}

type wireCredential struct {
	Reference domain.HostingDatabaseCredentialRef `json:"reference"`
	Value     []byte                              `json:"value"`
}
type wireCredentials struct {
	Metadata
	Entries []wireCredential `json:"entries"`
}

// WriteCredentials is the sole value-bearing encoder and clears its output buffer.
func WriteCredentials(w io.Writer, c *Credentials) error {
	if c == nil || c.closed || !validMetadata(c.metadata) || !validEntries(c.metadata, c.entries) {
		return ErrInvalidMessage
	}
	wire := wireCredentials{Metadata: c.metadata, Entries: make([]wireCredential, len(c.entries))}
	for i, e := range c.entries {
		wire.Entries[i] = wireCredential(e)
	}
	raw, err := json.Marshal(wire)
	defer clear(raw)
	if err != nil || len(raw) > MaxWireBytes {
		return ErrInvalidMessage
	}
	n, err := w.Write(raw)
	if err == nil && n != len(raw) {
		return io.ErrShortWrite
	}
	return err
}

func exactObject(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	defer func() {
		for _, v := range fields {
			clear(v)
		}
	}()
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, k := range keys {
		v, ok := fields[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	return true
}

func readBounded(r io.Reader, limit int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(raw) > limit || !validJSONUnicode(raw) || !uniqueJSON(raw) {
		clear(raw)
		return nil, ErrInvalidMessage
	}
	return raw, nil
}

// ReadRequest rejects duplicate, missing, unknown, aliased, malformed or trailing input.
func ReadRequest(r io.Reader) (domain.HostingDatabaseCredentialRequest, error) {
	var in domain.HostingDatabaseCredentialRequest
	raw, err := readBounded(r, 4096)
	defer clear(raw)
	if err != nil || !exactObject(raw, "authority", "delivery_id", "plan_hash", "mode") {
		return in, ErrInvalidMessage
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !exactObject(fields["authority"], "operation_id", "run_id", "lease_id", "attempt", "fence") {
		return in, ErrInvalidMessage
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || in.Validate() != nil {
		return domain.HostingDatabaseCredentialRequest{}, ErrInvalidMessage
	}
	return in, nil
}

// ReadCredentials rejects ambiguous metadata and clears discarded value buffers.
func ReadCredentials(r io.Reader) (*Credentials, error) {
	raw, err := readBounded(r, MaxWireBytes)
	defer clear(raw)
	if err != nil || !exactObject(raw, "runner_id", "authority", "delivery_id", "mode", "plan_hash", "database", "request_hash", "purpose", "use_until", "references", "entries") {
		return nil, ErrInvalidMessage
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrInvalidMessage
	}
	defer func() {
		for _, v := range fields {
			clear(v)
		}
	}()
	if !exactObject(fields["authority"], "operation_id", "run_id", "lease_id", "attempt", "fence") || !exactObject(fields["database"], "engine", "recipe_version", "configuration_hash") {
		return nil, ErrInvalidMessage
	}
	var refs, entries []json.RawMessage
	defer func() {
		for _, v := range refs {
			clear(v)
		}
		for _, v := range entries {
			clear(v)
		}
	}()
	if json.Unmarshal(fields["references"], &refs) != nil || len(refs) != 2 || json.Unmarshal(fields["entries"], &entries) != nil || len(entries) != 2 {
		return nil, ErrInvalidMessage
	}
	for _, ref := range refs {
		if !exactObject(ref, "role", "username", "secret_id", "version") {
			return nil, ErrInvalidMessage
		}
	}
	for _, entry := range entries {
		if !exactObject(entry, "reference", "value") {
			return nil, ErrInvalidMessage
		}
		var pair map[string]json.RawMessage
		if json.Unmarshal(entry, &pair) != nil {
			return nil, ErrInvalidMessage
		}
		ok := exactObject(pair["reference"], "role", "username", "secret_id", "version") && len(pair["value"]) > 0 && pair["value"][0] == '"'
		for _, v := range pair {
			clear(v)
		}
		if !ok {
			return nil, ErrInvalidMessage
		}
	}
	var wire wireCredentials
	defer func() {
		for _, e := range wire.Entries {
			clear(e.Value)
		}
	}()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil {
		return nil, ErrInvalidMessage
	}
	values := make([]Credential, len(wire.Entries))
	for i, e := range wire.Entries {
		values[i] = Credential(e)
	}
	return NewCredentials(wire.Metadata, values)
}
