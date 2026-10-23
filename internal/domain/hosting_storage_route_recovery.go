package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// HostingStorageRouteRecoveryInput pins the immutable evidence reviewed by the
// operator. Recovery queues proxy continuity without admitting storage work.
type HostingStorageRouteRecoveryInput struct {
	ExpectedDescriptorHash      string `json:"expected_descriptor_hash"`
	ExpectedCompleteReceiptID   string `json:"expected_complete_receipt_id"`
	ExpectedCompleteReceiptHash string `json:"expected_complete_receipt_hash"`
}

// Validate requires exact descriptor and completion receipt identities.
func (in HostingStorageRouteRecoveryInput) Validate() error {
	if !imageSHA256.MatchString(in.ExpectedDescriptorHash) || !imageSHA256.MatchString(in.ExpectedCompleteReceiptHash) || !hostingProxyID.MatchString(in.ExpectedCompleteReceiptID) {
		return errors.New("invalid storage route recovery evidence")
	}
	return nil
}

// UnmarshalJSON accepts only the three explicit evidence fields, once each.
func (in *HostingStorageRouteRecoveryInput) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) {
		return errors.New("invalid storage route recovery input")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid storage route recovery fields")
	}
	seen := make(map[string]bool, 3)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return errors.New("invalid storage route recovery fields")
		}
		switch key {
		case "expected_descriptor_hash", "expected_complete_receipt_id", "expected_complete_receipt_hash":
		default:
			return errors.New("invalid storage route recovery field")
		}
		seen[key] = true
		var value string
		if d.Decode(&value) != nil || value == "" {
			return errors.New("invalid storage route recovery value")
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid storage route recovery document")
	}
	type wire HostingStorageRouteRecoveryInput
	var value wire
	if strictSourceJSON(raw, &value) != nil {
		return errors.New("invalid storage route recovery values")
	}
	*in = HostingStorageRouteRecoveryInput(value)
	return in.Validate()
}
