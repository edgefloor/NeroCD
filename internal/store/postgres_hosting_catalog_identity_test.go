package store

import (
	"encoding/json"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestCatalogBindingIdentityPreservesAllFieldsAcrossJSON(t *testing.T) {
	live := domain.ResourceBinding{ID: "binding_catalog", ResourceID: "resource_catalog", DestinationID: "destination", RuntimeNamespace: "ncd_catalog", CreatedBy: "owner", CreatedAt: time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.FixedZone("UTC", 0))}
	raw, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	var saved domain.ResourceBinding
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if live == saved || !live.CreatedAt.Equal(saved.CreatedAt) || domain.HostingDocumentHash(live) != domain.HostingDocumentHash(saved) {
		t.Fatal("fixture did not cross the persisted UTC representation boundary")
	}
	if !catalogBindingEqual(live, saved) || !catalogBindingEqual(saved, live) {
		t.Fatal("equal persisted binding rejected after JSON round trip")
	}
	for _, change := range []struct {
		name   string
		mutate func(*domain.ResourceBinding)
	}{
		{"binding_id", func(v *domain.ResourceBinding) { v.ID += "_other" }},
		{"resource_id", func(v *domain.ResourceBinding) { v.ResourceID += "_other" }},
		{"destination_id", func(v *domain.ResourceBinding) { v.DestinationID += "_other" }},
		{"runtime_namespace", func(v *domain.ResourceBinding) { v.RuntimeNamespace += "_other" }},
		{"created_by", func(v *domain.ResourceBinding) { v.CreatedBy += "_other" }},
		{"timestamp_drift", func(v *domain.ResourceBinding) { v.CreatedAt = v.CreatedAt.Add(time.Microsecond) }},
		{"missing_timestamp", func(v *domain.ResourceBinding) { v.CreatedAt = time.Time{} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := saved
			change.mutate(&changed)
			if catalogBindingEqual(live, changed) || catalogBindingEqual(changed, live) {
				t.Fatal("changed binding identity accepted")
			}
		})
	}
}
