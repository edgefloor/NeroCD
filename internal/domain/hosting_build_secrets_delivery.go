package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// HostingBuildSecretsWireLimit bounds the worst-case JSON escaped response for
// 64 managed values of at most 65536 bytes each, plus the closed envelope.
const HostingBuildSecretsWireLimit = 26 << 20

// HostingBuildSecretValue is ephemeral protected material, never build metadata.
type HostingBuildSecretValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UnmarshalJSON requires an explicit value, including the valid empty string.
func (v *HostingBuildSecretValue) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 6*65536+1024 || !utf8.Valid(raw) || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) || decoder.Decode(&wire) != nil || wire.Value == nil || !validHostingBuildArgName(wire.Name) || !ValidHostingBuildSecretValue(*wire.Value) {
		return errors.New("invalid build secret value")
	}
	*v = HostingBuildSecretValue{Name: wire.Name, Value: *wire.Value}
	return nil
}

// Format redacts protected material for every formatting verb.
func (HostingBuildSecretValue) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted build secret]")
}

// MarshalJSON excludes material from accidental serialization.
func (HostingBuildSecretValue) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// HostingBuildSecretsDelivery carries only live snapshot-selected values.
type HostingBuildSecretsDelivery struct {
	Secrets  []HostingBuildSecretValue `json:"secrets"`
	UseUntil time.Time                 `json:"use_until"`
}

// Format redacts the complete delivery.
func (HostingBuildSecretsDelivery) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted build secrets delivery]")
}

// MarshalJSON excludes protected values outside the deliberate HTTP codec.
func (HostingBuildSecretsDelivery) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Close releases references to protected strings. Go cannot guarantee string erasure.
func (d *HostingBuildSecretsDelivery) Close() { clear(d.Secrets); *d = HostingBuildSecretsDelivery{} }

// ValidHostingBuildSecretValue admits exact managed UTF-8 values usable in RUN environments.
func ValidHostingBuildSecretValue(value string) bool {
	return len(value) <= 65536 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validBuildSecretsDelivery(d *HostingBuildSecretsDelivery) bool {
	if d == nil || d.UseUntil.IsZero() || len(d.Secrets) == 0 || len(d.Secrets) > 64 {
		return false
	}
	names := map[string]bool{}
	for _, secret := range d.Secrets {
		if !validHostingBuildArgName(secret.Name) || names[secret.Name] || !ValidHostingBuildSecretValue(secret.Value) {
			return false
		}
		names[secret.Name] = true
	}
	return true
}

// WriteHostingBuildSecretsDelivery is the sole deliberate protected serialization path.
func WriteHostingBuildSecretsDelivery(w io.Writer, d *HostingBuildSecretsDelivery) error {
	if !validBuildSecretsDelivery(d) {
		return errors.New("invalid build secrets delivery")
	}
	type value HostingBuildSecretValue
	values := make([]value, len(d.Secrets))
	defer clear(values)
	for i, v := range d.Secrets {
		values[i] = value(v)
	}
	return json.NewEncoder(w).Encode(struct {
		Secrets  []value   `json:"secrets"`
		UseUntil time.Time `json:"use_until"`
	}{values, d.UseUntil})
}

// ReadHostingBuildSecretsDelivery validates a bounded closed protected response.
func ReadHostingBuildSecretsDelivery(r io.Reader) (*HostingBuildSecretsDelivery, error) {
	raw, err := io.ReadAll(io.LimitReader(r, HostingBuildSecretsWireLimit+1))
	defer clear(raw)
	if err != nil || len(raw) > HostingBuildSecretsWireLimit || !utf8.Valid(raw) || !storageProtocolUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return nil, errors.New("invalid build secrets delivery")
	}
	var out HostingBuildSecretsDelivery
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || !validBuildSecretsDelivery(&out) {
		out.Close()
		return nil, errors.New("invalid build secrets delivery")
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		out.Close()
		return nil, errors.New("invalid build secrets delivery")
	}
	return &out, nil
}
