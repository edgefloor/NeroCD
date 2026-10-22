package domain

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// HostingRuntimeDocumentLimit bounds the canonical public configuration document.
const HostingRuntimeDocumentLimit = 256 << 10

// HostingRuntimeRequestLimit bounds the complete encoded HTTP request.
const HostingRuntimeRequestLimit = 512 << 10

// ErrInvalidHostingRuntimeConfiguration never contains literal or secret values.
var ErrInvalidHostingRuntimeConfiguration = errors.New("invalid hosting runtime configuration")

// HostingRuntimeSecretReference pins metadata only, never the selected secret value.
type HostingRuntimeSecretReference struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
}

// HostingRuntimeEntry explicitly selects a viewer-readable nonsecret literal or
// one managed-secret reference. Value permits an intentionally empty literal.
type HostingRuntimeEntry struct {
	Name   string                         `json:"name"`
	Kind   string                         `json:"kind"`
	Value  *string                        `json:"value,omitempty"`
	Secret *HostingRuntimeSecretReference `json:"secret,omitempty"`
}

// UnmarshalJSON preserves union field presence, including forbidden null fields.
func (entry *HostingRuntimeEntry) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return ErrInvalidHostingRuntimeConfiguration
	}
	var kind string
	if json.Unmarshal(fields["kind"], &kind) != nil {
		return ErrInvalidHostingRuntimeConfiguration
	}
	switch kind {
	case "literal":
		if _, ok := fields["value"]; !ok {
			return ErrInvalidHostingRuntimeConfiguration
		}
	case "managed_secret":
		if _, ok := fields["secret"]; !ok {
			return ErrInvalidHostingRuntimeConfiguration
		}
	default:
		return ErrInvalidHostingRuntimeConfiguration
	}
	type wire HostingRuntimeEntry
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return ErrInvalidHostingRuntimeConfiguration
	}
	*entry = HostingRuntimeEntry(value)
	return nil
}

// HostingRuntimeDocument is the immutable, sorted public reference document.
type HostingRuntimeDocument struct {
	Entries []HostingRuntimeEntry `json:"entries"`
}

// HostingRuntimeConfiguration is saved desired input, not execution support.
// Generation zero represents an untouched resource without a persisted version.
type HostingRuntimeConfiguration struct {
	ResourceID         string                `json:"resource_id"`
	ProjectID          string                `json:"project_id"`
	ResourceGeneration int64                 `json:"resource_generation"`
	RuntimeGeneration  int64                 `json:"runtime_generation"`
	Entries            []HostingRuntimeEntry `json:"entries"`
	CreatedBy          string                `json:"created_by,omitempty"`
	CreatedAt          *time.Time            `json:"created_at,omitempty"`
}

// HostingRuntimeConfigurationInput replaces the full document under explicit CAS.
// RequestID must be generated from 128 random bits and retained across retries.
type HostingRuntimeConfigurationInput struct {
	ExpectedResourceGeneration int64                 `json:"expected_resource_generation"`
	ExpectedRuntimeGeneration  int64                 `json:"expected_runtime_generation"`
	RequestID                  string                `json:"request_id"`
	Entries                    []HostingRuntimeEntry `json:"entries"`
}

var runtimeEnvironmentName = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)

// CanonicalHostingRuntimeDocument validates and copies entries and unique secret
// references. Only public literals and references participate in document hashes.
func CanonicalHostingRuntimeDocument(entries []HostingRuntimeEntry) (HostingRuntimeDocument, []HostingRuntimeSecretReference, error) {
	var zero HostingRuntimeDocument
	if entries == nil || len(entries) > 64 {
		return zero, nil, ErrInvalidHostingRuntimeConfiguration
	}
	doc := HostingRuntimeDocument{Entries: make([]HostingRuntimeEntry, 0, len(entries))}
	names := map[string]bool{}
	refs := map[HostingRuntimeSecretReference]bool{}
	for _, entry := range entries {
		if !runtimeEnvironmentName.MatchString(entry.Name) || strings.HasPrefix(entry.Name, "NEROCD_") || names[entry.Name] {
			return zero, nil, ErrInvalidHostingRuntimeConfiguration
		}
		names[entry.Name] = true
		switch entry.Kind {
		case "literal":
			if entry.Value == nil || entry.Secret != nil || len(*entry.Value) > 4096 || !utf8.ValidString(*entry.Value) || strings.ContainsAny(*entry.Value, "\x00\r\n") {
				return zero, nil, ErrInvalidHostingRuntimeConfiguration
			}
			value := *entry.Value
			entry.Value = &value
		case "managed_secret":
			if entry.Value != nil || entry.Secret == nil || entry.Secret.Version < 1 || !validRuntimeReferenceID(entry.Secret.SecretID) {
				return zero, nil, ErrInvalidHostingRuntimeConfiguration
			}
			ref := *entry.Secret
			entry.Secret = &ref
			refs[ref] = true
		default:
			return zero, nil, ErrInvalidHostingRuntimeConfiguration
		}
		doc.Entries = append(doc.Entries, entry)
	}
	if len(refs) > 16 {
		return zero, nil, ErrInvalidHostingRuntimeConfiguration
	}
	slices.SortFunc(doc.Entries, func(a, b HostingRuntimeEntry) int { return cmp.Compare(a.Name, b.Name) })
	ordered := make([]HostingRuntimeSecretReference, 0, len(refs))
	for ref := range refs {
		ordered = append(ordered, ref)
	}
	slices.SortFunc(ordered, func(a, b HostingRuntimeSecretReference) int {
		if n := cmp.Compare(a.SecretID, b.SecretID); n != 0 {
			return n
		}
		return cmp.Compare(a.Version, b.Version)
	})
	raw, err := json.Marshal(doc)
	if err != nil || len(raw) > HostingRuntimeDocumentLimit {
		return zero, nil, ErrInvalidHostingRuntimeConfiguration
	}
	return doc, ordered, nil
}

func validRuntimeReferenceID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && strings.TrimSpace(id) == id && !strings.ContainsFunc(id, unicode.IsControl)
}

// Validate checks CAS and retry shape and canonicalizes the complete document.
func (in HostingRuntimeConfigurationInput) Validate() (HostingRuntimeDocument, []HostingRuntimeSecretReference, error) {
	if in.ExpectedResourceGeneration < 1 || in.ExpectedRuntimeGeneration < 0 || in.ExpectedRuntimeGeneration == math.MaxInt64 || len(in.RequestID) != 32 || strings.ToLower(in.RequestID) != in.RequestID {
		return HostingRuntimeDocument{}, nil, ErrInvalidHostingRuntimeConfiguration
	}
	if _, err := hex.DecodeString(in.RequestID); err != nil {
		return HostingRuntimeDocument{}, nil, ErrInvalidHostingRuntimeConfiguration
	}
	return CanonicalHostingRuntimeDocument(in.Entries)
}

// UnmarshalJSON rejects duplicate, unknown, malformed Unicode and trailing fields.
func (in *HostingRuntimeConfigurationInput) UnmarshalJSON(raw []byte) error {
	if len(raw) > HostingRuntimeRequestLimit || !validRuntimeJSONUnicode(raw) || !uniqueRuntimeJSON(raw) {
		return ErrInvalidHostingRuntimeConfiguration
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrInvalidHostingRuntimeConfiguration
	}
	for _, key := range []string{"expected_resource_generation", "expected_runtime_generation", "request_id", "entries"} {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return ErrInvalidHostingRuntimeConfiguration
		}
	}
	type wire HostingRuntimeConfigurationInput
	var value wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil {
		return ErrInvalidHostingRuntimeConfiguration
	}
	*in = HostingRuntimeConfigurationInput(value)
	return nil
}

// HostingRuntimeAuditID binds the retained audit to one resource-scoped request.
func HostingRuntimeAuditID(resource, request string) string {
	return "aud_runtime_config_" + HostingDocumentHash(struct{ Resource, Request string }{resource, request})[7:]
}

func uniqueRuntimeJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, e := d.Token()
				key, ok := keyToken.(string)
				if e != nil || !ok || !runtimeJSONKey(key) || seen[key] {
					return false
				}
				seen[key] = true
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func validRuntimeJSONUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// Exact spelling also rejects encoding/json's case-insensitive field aliases.
func runtimeJSONKey(key string) bool {
	switch key {
	case "expected_resource_generation", "expected_runtime_generation", "request_id", "entries", "name", "kind", "value", "secret", "secret_id", "version":
		return true
	default:
		return false
	}
}
