package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingStorageRouteRecoveryInput(t *testing.T) {
	in := HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: "sha256:" + strings.Repeat("a", 64), ExpectedCompleteReceiptID: "complete_hop_next", ExpectedCompleteReceiptHash: "sha256:" + strings.Repeat("b", 64)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingStorageRouteRecoveryInput
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded != in {
		t.Fatalf("valid recovery input: %+v %v", decoded, err)
	}
	for _, bad := range []string{
		`null`, `[]`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"expected_descriptor_hash":`, `"expected_descriptor_hash":"sha256:`+strings.Repeat("c", 64)+`","expected_descriptor_hash":`, 1),
		strings.Replace(string(raw), `"expected_descriptor_hash"`, `"Expected_Descriptor_Hash"`, 1),
		strings.Replace(string(raw), `"expected_descriptor_hash"`, `"unknown"`, 1),
		strings.Replace(string(raw), `"complete_hop_next"`, `null`, 1),
		strings.Replace(string(raw), `"complete_hop_next"`, `"bad/id"`, 1),
		strings.Replace(string(raw), `"complete_hop_next"`, `12`, 1),
		strings.Replace(string(raw), strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
	} {
		if err = json.Unmarshal([]byte(bad), &decoded); err == nil {
			t.Errorf("ambiguous recovery input accepted: %s", bad)
		}
	}
	for _, bad := range []HostingStorageRouteRecoveryInput{{}, {ExpectedDescriptorHash: in.ExpectedDescriptorHash, ExpectedCompleteReceiptID: in.ExpectedCompleteReceiptID}, {ExpectedCompleteReceiptHash: in.ExpectedCompleteReceiptHash}} {
		if bad.Validate() == nil {
			t.Fatalf("missing evidence accepted: %+v", bad)
		}
	}
}
