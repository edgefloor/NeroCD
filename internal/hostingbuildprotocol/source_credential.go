package hostingbuildprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"nerocd/internal/domain"
)

// MaxSourceCredentialWireBytes bounds the entire secret response document.
const MaxSourceCredentialWireBytes = 16 << 10

// SourceCredential owns credential bytes. Only the explicit wire codec exposes them.
// Callers must Close after use and must not retain field copies.
type SourceCredential struct {
	Authority   domain.HostingBuildAuthority `json:"-"`
	DeliveryID  string                       `json:"-"`
	RequestHash string                       `json:"-"`
	Kind        string                       `json:"-"`
	Username    string                       `json:"-"`
	Bytes       []byte                       `json:"-"`
	UseUntil    time.Time                    `json:"-"`
}

// Format redacts material for every formatting verb.
func (SourceCredential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted source credential]")
}

// MarshalJSON excludes material outside the explicit wire codec.
func (SourceCredential) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Close clears owned bytes and releases material. Runtime string copies cannot be erased.
func (c *SourceCredential) Close() { clear(c.Bytes); *c = SourceCredential{} }

// SourceCredentialRequestHash binds the authenticated configured runner to intent.
func SourceCredentialRequestHash(runner string, in domain.HostingSourceCredentialRequest) string {
	return domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingSourceCredentialRequest
	}{runner, in})
}

// ValidBasicCredential matches the connected checkout's complete Basic payload budget.
func ValidBasicCredential(username string, password []byte) bool {
	return validCredentialField(username) && len(username) <= 256 && !strings.Contains(username, ":") &&
		utf8.Valid(password) && validCredentialField(string(password)) && len(username)+1+len(password) <= 4096
}
func validCredentialField(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

type sourceCredentialWire struct {
	Authority   domain.HostingBuildAuthority `json:"authority"`
	DeliveryID  string                       `json:"delivery_id"`
	RequestHash string                       `json:"request_hash"`
	Kind        string                       `json:"credential_kind"`
	Username    string                       `json:"username"`
	Credential  []byte                       `json:"credential"`
	UseUntil    time.Time                    `json:"use_until"`
}

func (c SourceCredential) valid() bool {
	return ValidateAuthority(c.Authority) == nil && domain.ValidHostingSourceDeliveryID(c.DeliveryID) && validDigest(c.RequestHash) && c.Kind == "https_basic" && ValidBasicCredential(c.Username, c.Bytes) && !c.UseUntil.IsZero()
}

// WriteSourceCredential is the sole secret-bearing encoder. It clears its output buffer.
func WriteSourceCredential(w io.Writer, c SourceCredential) error {
	if !c.valid() {
		return ErrInvalidMessage
	}
	raw, err := json.Marshal(sourceCredentialWire{c.Authority, c.DeliveryID, c.RequestHash, c.Kind, c.Username, c.Bytes, c.UseUntil})
	defer clear(raw)
	if err != nil || len(raw) > MaxSourceCredentialWireBytes {
		return ErrInvalidMessage
	}
	_, err = w.Write(raw)
	return err
}

// ReadSourceCredential rejects unknown, duplicate and trailing fields at every depth.
// All discarded byte buffers are cleared; errors never expose input or parser diagnostics.
func ReadSourceCredential(r io.Reader) (SourceCredential, error) {
	var zero SourceCredential
	raw, err := io.ReadAll(io.LimitReader(r, MaxSourceCredentialWireBytes+1))
	defer clear(raw)
	if err != nil || len(raw) > MaxSourceCredentialWireBytes || !validCredentialJSONUnicode(raw) {
		return zero, ErrInvalidMessage
	}
	if !uniqueJSON(raw) {
		return zero, ErrInvalidMessage
	}
	var fields map[string]json.RawMessage
	defer func() {
		for _, raw := range fields {
			clear(raw)
		}
	}()
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
		return zero, ErrInvalidMessage
	}
	for _, key := range []string{"authority", "delivery_id", "request_hash", "credential_kind", "username", "credential", "use_until"} {
		if _, ok := fields[key]; !ok {
			return zero, ErrInvalidMessage
		}
	}
	var authority map[string]json.RawMessage
	defer func() {
		for _, raw := range authority {
			clear(raw)
		}
	}()
	if json.Unmarshal(fields["authority"], &authority) != nil || len(authority) != 5 {
		return zero, ErrInvalidMessage
	}
	for _, key := range []string{"build_id", "run_id", "lease_id", "attempt", "fence"} {
		if _, ok := authority[key]; !ok {
			return zero, ErrInvalidMessage
		}
	}
	if b := bytes.TrimSpace(fields["credential"]); len(b) == 0 || b[0] != '"' {
		return zero, ErrInvalidMessage
	}
	var wire sourceCredentialWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil {
		clear(wire.Credential)
		return zero, ErrInvalidMessage
	}
	c := SourceCredential{wire.Authority, wire.DeliveryID, wire.RequestHash, wire.Kind, wire.Username, wire.Credential, wire.UseUntil}
	if !c.valid() {
		c.Close()
		return zero, ErrInvalidMessage
	}
	return c, nil
}

func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		t, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				key, ok := k.(string)
				if e != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !value() {
					return false
				}
			}
			t, err = d.Token()
			return err == nil && t == json.Delim('}')
		case '[':
			for d.More() {
				if !value() {
					return false
				}
			}
			t, err = d.Token()
			return err == nil && t == json.Delim(']')
		default:
			return false
		}
	}
	if !value() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func validCredentialJSONUnicode(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		code, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
