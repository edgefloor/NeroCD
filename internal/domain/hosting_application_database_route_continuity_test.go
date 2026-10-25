package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestApplicationDatabaseRouteIntentCopiesAndPinsCompleteSelections(t *testing.T) {
	in := HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 3, ExpectedLoadedProxyOperationID: "proxy_loaded", Routes: []HostingRouteSwitchSelection{{RouteID: "route_b", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "previous"}, {RouteID: "route_a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "previous"}}}
	got, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(in)
	if err != nil || got.Routes[0].RouteID != "route_a" || in.Routes[0].RouteID != "route_b" {
		t.Fatal("canonicalization lost owned route order", err)
	}
	got.Routes[0].ExpectedVersion = 9
	if in.Routes[1].ExpectedVersion != 1 {
		t.Fatal("canonical intent aliases caller selections")
	}
	for name, change := range map[string]func(*HostingApplicationDatabaseRouteContinuityInput){"missing_loaded": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.ExpectedLoadedProxyOperationID = "" }, "zero_generation": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.ExpectedLoadedGeneration = 0 }, "no_routes": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.Routes = nil }, "duplicate": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.Routes[1] = v.Routes[0] }, "missing_predecessor": func(v *HostingApplicationDatabaseRouteContinuityInput) {
		v.Routes[0].ExpectedSelectedReleaseOperationID = ""
	}} {
		t.Run(name, func(t *testing.T) {
			bad := in
			bad.Routes = append([]HostingRouteSwitchSelection(nil), in.Routes...)
			change(&bad)
			if _, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(bad); err == nil {
				t.Fatal("incomplete intent admitted")
			}
		})
	}
}

func TestApplicationDatabaseYieldClosedAuthorityAndSafeProjection(t *testing.T) {
	h := "sha256:" + strings.Repeat("a", 64)
	y := HostingApplicationDatabaseYieldRequest{HostingAuthority: HostingAuthority{OperationID: "operation", RunID: "run_operation", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Version: HostingApplicationDatabaseRouteContinuityVersion, ReceiptID: "yield", PlanHash: h, StageHash: h, CandidateHash: h, ContinuationHash: h}
	raw, err := json.Marshal(y)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingApplicationDatabaseYieldRequest
	if json.Unmarshal(raw, &decoded) != nil || decoded != y {
		t.Fatal("yield tuple changed on round trip")
	}
	projection, err := json.Marshal(y.Receipt())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"lease_id", "attempt", "fence", "run_id"} {
		if bytes.Contains(projection, []byte(forbidden)) {
			t.Fatal("public yield disclosed authority", forbidden)
		}
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"version":`), []byte(`"extra":true,"version":`), 1), bytes.Replace(raw, []byte(`"attempt":1`), []byte(`"attempt":1,"attempt":1`), 1), bytes.Replace(raw, []byte(`"fence":"fence"`), []byte(`"fence":null`), 1), bytes.Replace(raw, []byte(`"receipt_id"`), []byte(`"Receipt_ID"`), 1)} {
		if json.Unmarshal(bad, &decoded) == nil {
			t.Fatal("ambiguous yield document admitted")
		}
	}
	y.Attempt = math.MaxInt32 + 1
	if y.Validate() == nil {
		t.Fatal("attempt overflow admitted")
	}
}
