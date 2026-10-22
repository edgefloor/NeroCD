// Package hostingruntimeprotocol carries protected environment bytes through an explicit codec.
package hostingruntimeprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"nerocd/internal/domain"
)

// Purpose and budgets define the protected hosting environment wire contract.
const (
	Purpose                 = "hosting_container_environment"
	MaxEnvironmentBytes     = 256 << 10
	MaxEnvironmentWireBytes = 512 << 10
)

// ErrInvalidMessage excludes input and parser diagnostics.
var ErrInvalidMessage = errors.New("invalid hosting runtime environment")
var environmentName = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)

// Entry contains borrowed or owned bytes. Ordinary output always suppresses them.
type Entry struct {
	Name  string `json:"-"`
	Value []byte `json:"-"`
}

// Format suppresses entry values for every formatting verb.
func (Entry) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[redacted environment entry]") }

// MarshalJSON suppresses values outside the explicit codec.
func (Entry) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Metadata binds bytes to one current prepared operation and original deadline.
type Metadata struct {
	Authority            domain.HostingAuthority               `json:"authority"`
	DeliveryID           string                                `json:"delivery_id"`
	PlanHash             string                                `json:"plan_hash"`
	RuntimeConfiguration domain.HostingRuntimeConfigurationRef `json:"runtime_configuration"`
	RequestHash          string                                `json:"request_hash"`
	Purpose              string                                `json:"purpose"`
	UseUntil             time.Time                             `json:"use_until"`
}

// Environment owns its bytes. It must not be copied or used concurrently.
// Close clears owned buffers, but cannot erase copies retained by Go or Docker.
type Environment struct {
	metadata Metadata
	entries  []Entry
	closed   bool
}

// Format suppresses environment values for every formatting verb.
func (Environment) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted runtime environment]")
}

// MarshalJSON suppresses values outside the explicit codec.
func (Environment) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// ValidateEntries uses the helper's complete NAME=value plus newline budget.
func ValidateEntries(entries []Entry) error {
	if entries == nil || len(entries) > 64 {
		return ErrInvalidMessage
	}
	seen := map[string]bool{}
	size := 0
	for _, entry := range entries {
		if !environmentName.MatchString(entry.Name) || strings.HasPrefix(entry.Name, "NEROCD_") || seen[entry.Name] || len(entry.Value) > 4096 || !utf8.Valid(entry.Value) || bytes.ContainsAny(entry.Value, "\x00\r\n") {
			return ErrInvalidMessage
		}
		seen[entry.Name] = true
		size += len(entry.Name) + len(entry.Value) + 2
		if size > MaxEnvironmentBytes {
			return ErrInvalidMessage
		}
	}
	return nil
}

func digest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil
}

// ValidateRequest bounds public authority fields before repository access.
func ValidateRequest(in domain.HostingRuntimeEnvironmentRequest) error {
	a := in.Authority
	if a.Attempt < 1 || a.RunID != "run_"+a.OperationID || !domain.ValidHostingRuntimeDeliveryID(in.DeliveryID) || !digest(in.PlanHash) || in.RuntimeConfiguration.Generation < 1 || !digest(in.RuntimeConfiguration.Hash) {
		return ErrInvalidMessage
	}
	for _, s := range []string{a.OperationID, a.RunID, a.LeaseID, a.Fence} {
		if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
			return ErrInvalidMessage
		}
	}
	return nil
}

// RuntimeEnvironmentRequestHash includes only the authenticated runner and public intent.
func RuntimeEnvironmentRequestHash(runner string, in domain.HostingRuntimeEnvironmentRequest) string {
	return domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingRuntimeEnvironmentRequest
	}{runner, in})
}

func validMetadata(m Metadata) bool {
	return ValidateRequest(domain.HostingRuntimeEnvironmentRequest{Authority: m.Authority, PlanHash: m.PlanHash, RuntimeConfiguration: m.RuntimeConfiguration, DeliveryID: m.DeliveryID}) == nil && digest(m.RequestHash) && m.Purpose == Purpose && !m.UseUntil.IsZero()
}

// NewEnvironment copies values into storage owned by the returned object.
func NewEnvironment(metadata Metadata, entries []Entry) (*Environment, error) {
	if !validMetadata(metadata) || ValidateEntries(entries) != nil {
		return nil, ErrInvalidMessage
	}
	e := &Environment{metadata: metadata, entries: make([]Entry, len(entries))}
	for i, v := range entries {
		e.entries[i] = Entry{Name: v.Name, Value: append([]byte{}, v.Value...)}
	}
	return e, nil
}

// Metadata returns public bindings and rejects a closed object.
func (e *Environment) Metadata() (Metadata, error) {
	if e == nil || e.closed {
		return Metadata{}, ErrInvalidMessage
	}
	return e.metadata, nil
}

// WithValues borrows entries only for fn. The callback must not retain or mutate them.
func (e *Environment) WithValues(fn func([]Entry) error) error {
	if e == nil || e.closed || fn == nil {
		return ErrInvalidMessage
	}
	return fn(e.entries)
}

// Close clears owned buffers and prevents subsequent access.
func (e *Environment) Close() {
	if e == nil {
		return
	}
	for i := range e.entries {
		clear(e.entries[i].Value)
		e.entries[i] = Entry{}
	}
	e.entries = nil
	e.metadata = Metadata{}
	e.closed = true
}

type wireEntry struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}
type wireEnvironment struct {
	Metadata
	Entries []wireEntry `json:"entries"`
}

// WriteEnvironment is the sole secret-bearing encoder and clears its output buffer.
func WriteEnvironment(w io.Writer, e *Environment) error {
	if e == nil || e.closed || !validMetadata(e.metadata) || ValidateEntries(e.entries) != nil {
		return ErrInvalidMessage
	}
	wire := wireEnvironment{Metadata: e.metadata, Entries: make([]wireEntry, len(e.entries))}
	for i, v := range e.entries {
		wire.Entries[i] = wireEntry(v)
	}
	raw, err := json.Marshal(wire)
	defer clear(raw)
	if err != nil || len(raw) > MaxEnvironmentWireBytes {
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
		for _, value := range fields {
			clear(value)
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

// ReadEnvironment rejects duplicate, unknown, case-aliased, malformed or trailing data.
// Parser diagnostics and discarded buffers never leave this boundary.
func ReadEnvironment(r io.Reader) (*Environment, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxEnvironmentWireBytes+1))
	defer clear(raw)
	if err != nil || len(raw) > MaxEnvironmentWireBytes || !validJSONUnicode(raw) || !uniqueJSON(raw) || !exactObject(raw, "authority", "delivery_id", "plan_hash", "runtime_configuration", "request_hash", "purpose", "use_until", "entries") {
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
	if !exactObject(fields["authority"], "operation_id", "run_id", "lease_id", "attempt", "fence") || !exactObject(fields["runtime_configuration"], "generation", "hash") {
		return nil, ErrInvalidMessage
	}
	var entryFields []json.RawMessage
	if json.Unmarshal(fields["entries"], &entryFields) != nil || len(entryFields) > 64 {
		return nil, ErrInvalidMessage
	}
	defer func() {
		for _, v := range entryFields {
			clear(v)
		}
	}()
	for _, v := range entryFields {
		if !exactObject(v, "name", "value") {
			return nil, ErrInvalidMessage
		}
		var pair map[string]json.RawMessage
		if json.Unmarshal(v, &pair) != nil {
			return nil, ErrInvalidMessage
		}
		value := bytes.TrimSpace(pair["value"])
		ok := len(value) > 0 && value[0] == '"'
		for _, b := range pair {
			clear(b)
		}
		if !ok {
			return nil, ErrInvalidMessage
		}
	}
	var wire wireEnvironment
	defer func() {
		for _, v := range wire.Entries {
			clear(v.Value)
		}
	}()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil {
		return nil, ErrInvalidMessage
	}
	entries := make([]Entry, len(wire.Entries))
	for i, v := range wire.Entries {
		entries[i] = Entry(v)
	}
	return NewEnvironment(wire.Metadata, entries)
}

// ReadRequest rejects ambiguous public intent before authorization or auditing.
func ReadRequest(r io.Reader) (domain.HostingRuntimeEnvironmentRequest, error) {
	var in domain.HostingRuntimeEnvironmentRequest
	raw, err := io.ReadAll(io.LimitReader(r, 8193))
	defer clear(raw)
	if err != nil || len(raw) > 8192 || !validJSONUnicode(raw) || !uniqueJSON(raw) || !exactObject(raw, "authority", "plan_hash", "runtime_configuration", "delivery_id") {
		return in, ErrInvalidMessage
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !exactObject(fields["authority"], "operation_id", "run_id", "lease_id", "attempt", "fence") || !exactObject(fields["runtime_configuration"], "generation", "hash") {
		return in, ErrInvalidMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || ValidateRequest(in) != nil {
		return domain.HostingRuntimeEnvironmentRequest{}, ErrInvalidMessage
	}
	return in, nil
}
