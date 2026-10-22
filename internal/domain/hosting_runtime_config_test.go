package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestHostingRuntimeCanonicalDocument(t *testing.T) {
	value := " visible literal "
	entries := []HostingRuntimeEntry{{Name: "Z", Kind: "managed_secret", Secret: &HostingRuntimeSecretReference{SecretID: "s", Version: 2}}, {Name: "A", Kind: "literal", Value: &value}, {Name: "B", Kind: "managed_secret", Secret: &HostingRuntimeSecretReference{SecretID: "s", Version: 2}}}
	doc, refs, err := CanonicalHostingRuntimeDocument(entries)
	if err != nil || doc.Entries[0].Name != "A" || len(refs) != 1 {
		t.Fatalf("canonical: %+v %+v %v", doc, refs, err)
	}
	*entries[1].Value = "changed"
	entries[0].Secret.Version = 3
	if *doc.Entries[0].Value != " visible literal " || doc.Entries[2].Secret.Version != 2 {
		t.Fatal("input aliases canonical document")
	}
	empty, _, err := CanonicalHostingRuntimeDocument([]HostingRuntimeEntry{})
	if err != nil || string(buildRuntimeJSON(t, empty)) != `{"entries":[]}` {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}
func buildRuntimeJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHostingRuntimeInputStrictDecode(t *testing.T) {
	base := `{"expected_resource_generation":1,"expected_runtime_generation":0,"request_id":"0123456789abcdef0123456789abcdef","entries":[{"name":"APP","kind":"literal","value":""}]}`
	var in HostingRuntimeConfigurationInput
	if err := json.Unmarshal([]byte(base), &in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		strings.Replace(base, `"value":""`, `"value":"","secret":null`, 1),
		strings.Replace(base, `"kind":"literal","value":""`, `"kind":"managed_secret","value":null,"secret":{"secret_id":"s","version":1}`, 1),
		strings.Replace(base, `"expected_runtime_generation":0,`, "", 1),
		strings.Replace(base, `"expected_runtime_generation":0`, `"expected_runtime_generation":null`, 1),
		strings.Replace(base, `"name":"APP"`, `"name":"APP","NAME":"OTHER"`, 1),
		strings.Replace(base, `"expected_runtime_generation":0`, `"expected_runtime_generation":0,"Expected_Runtime_Generation":1`, 1),
		strings.Replace(base, `"name"`, `"Name"`, 1),
		strings.Replace(base, `"name":"APP"`, `"name":"APP","na\u006de":"OTHER"`, 1),
		strings.Replace(base, `"value":""`, `"value":"\ud800"`, 1),
		strings.Replace(base, `"entries":`, `"extra":true,"entries":`, 1), base + ` {}`,
		strings.Repeat(" ", HostingRuntimeRequestLimit) + base,
	}
	for i, raw := range bad {
		if err := in.UnmarshalJSON([]byte(raw)); !errors.Is(err, ErrInvalidHostingRuntimeConfiguration) && err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}
func TestHostingRuntimeBounds(t *testing.T) {
	value := "ok"
	tests := []struct {
		name    string
		entries []HostingRuntimeEntry
	}{
		{"missing", nil}, {"reserved", []HostingRuntimeEntry{{Name: "NEROCD_TOKEN", Kind: "literal", Value: &value}}},
		{"duplicate", []HostingRuntimeEntry{{Name: "A", Kind: "literal", Value: &value}, {Name: "A", Kind: "literal", Value: &value}}},
		{"badname", []HostingRuntimeEntry{{Name: "1A", Kind: "literal", Value: &value}}},
		{"union", []HostingRuntimeEntry{{Name: "A", Kind: "literal", Value: &value, Secret: &HostingRuntimeSecretReference{SecretID: "s", Version: 1}}}},
	}
	for _, bad := range []string{strings.Repeat("x", 4097), "a\n", "a\r", "a\x00", string([]byte{255})} {
		v := bad
		tests = append(tests, struct {
			name    string
			entries []HostingRuntimeEntry
		}{"value", []HostingRuntimeEntry{{Name: "A", Kind: "literal", Value: &v}}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := CanonicalHostingRuntimeDocument(tt.entries); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	entries := make([]HostingRuntimeEntry, 65)
	for i := range entries {
		entries[i] = HostingRuntimeEntry{Name: strings.Repeat("a", i+1), Kind: "literal", Value: &value}
	}
	if _, _, err := CanonicalHostingRuntimeDocument(entries); err == nil {
		t.Fatal("65 entries accepted")
	}
	entries = entries[:17]
	for i := range entries {
		entries[i].Kind = "managed_secret"
		entries[i].Value = nil
		entries[i].Secret = &HostingRuntimeSecretReference{SecretID: entries[i].Name, Version: 1}
	}
	if _, _, err := CanonicalHostingRuntimeDocument(entries); err == nil {
		t.Fatal("17 unique refs accepted")
	}
	escaped := strings.Repeat("\t", 4096)
	entries = make([]HostingRuntimeEntry, 64)
	for i := range entries {
		entries[i] = HostingRuntimeEntry{Name: strings.Repeat("a", i+1), Kind: "literal", Value: &escaped}
	}
	if _, _, err := CanonicalHostingRuntimeDocument(entries); err == nil {
		t.Fatal("oversized canonical encoding accepted")
	}
}

func TestHostingRuntimeExactBoundariesAndCanonicalRetry(t *testing.T) {
	value := strings.Repeat("é", 2048)
	in := HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, RequestID: "0123456789abcdef0123456789abcdef", Entries: []HostingRuntimeEntry{{Name: strings.Repeat("A", 128), Kind: "literal", Value: &value}}}
	if _, _, err := in.Validate(); err != nil {
		t.Fatalf("4096 UTF-8 bytes %v", err)
	}
	value += "é"
	if _, _, err := in.Validate(); err == nil {
		t.Fatal("4098 UTF-8 bytes accepted")
	}
	empty := ""
	entries := make([]HostingRuntimeEntry, 64)
	for i := range entries {
		entries[i] = HostingRuntimeEntry{Name: strings.Repeat("a", i+1), Kind: "literal", Value: &empty}
	}
	first, _, err := CanonicalHostingRuntimeDocument(entries)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	second, _, err := CanonicalHostingRuntimeDocument(entries)
	if err != nil || HostingDocumentHash(first) != HostingDocumentHash(second) {
		t.Fatalf("reordering changed intent %v", err)
	}
	for i := range entries {
		entries[i].Value = nil
		entries[i].Kind = "managed_secret"
		entries[i].Secret = &HostingRuntimeSecretReference{SecretID: strings.Repeat("s", i%16+1), Version: 1}
	}
	if _, refs, err := CanonicalHostingRuntimeDocument(entries); err != nil || len(refs) != 16 {
		t.Fatalf("16 distinct refs over 64 entries %d %v", len(refs), err)
	}
	value = ""
	for _, request := range []string{"0123456789ABCDEF0123456789abcdef", "not-hex", strings.Repeat("0", 31)} {
		in.RequestID = request
		if _, _, err := in.Validate(); err == nil {
			t.Fatal("bad request identity accepted")
		}
	}
}
