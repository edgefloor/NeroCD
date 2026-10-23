package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingStorageDeploymentOptionsAbsentValues(t *testing.T) {
	encoded, err := json.Marshal(HostingStorageDeploymentOptions{ResourceID: "res_uploads", Routes: []HostingRouteSwitchRoute{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"storage_ref", "previous_healthy_operation_id", "host_port", "container_port", "readiness", "storage_route_continuity"} {
		if !strings.Contains(string(encoded), `"`+field+`":null`) {
			t.Fatalf("missing explicit null %s: %s", field, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"routes":[]`) {
		t.Fatal(string(encoded))
	}
}

func TestHostingStorageDeploymentStatusHistoricalReceipt(t *testing.T) {
	for state, want := range map[string]string{"reserved": "pending", "proxy_pending": "pending", "loaded": "loaded_historical", "released": "released", "unresolved": "reconciliation_required", "proxy_failed": "reconciliation_required", "unexpected": "reconciliation_required"} {
		if got := StorageContinuityStatusReason(state); got != want {
			t.Fatalf("%s: %s", state, got)
		}
	}
	view := HostingStorageRouteContinuityStatus{ResourceID: "res_uploads", OperationID: "hop_next", State: "proxy_pending", Reason: "pending"}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "loaded_generation") || strings.Contains(string(encoded), "https_ready") || strings.Contains(string(encoded), "successor_proxy_operation_id") {
		t.Fatal(string(encoded))
	}
	generation := int64(9)
	view.LoadedGeneration = &generation
	view.State, view.Reason = "loaded", StorageContinuityStatusReason("loaded")
	encoded, err = json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingStorageRouteContinuityStatus
	if err = json.Unmarshal(encoded, &decoded); err != nil || decoded.LoadedGeneration == nil || *decoded.LoadedGeneration != generation || decoded.Reason != "loaded_historical" {
		t.Fatalf("round trip %+v %v", decoded, err)
	}
}
