package domain

import (
	"strings"
	"testing"
	"time"
)

func TestHostingProxyRecoveryPlan(t *testing.T) {
	now := time.Unix(123, 0).UTC()
	p := HostingProxyRecoveryPlan{ID: "prp_fixture", Review: HostingProxyRecoveryReview{Version: 1, Routes: []HostingProxyRecoveryRoute{{RouteID: "route_fixture", Version: 2, PriorTarget: HostingProxyRouteTarget{ReleaseOperationID: "release_a"}}}}, CreatedAt: now, ExpiresAt: now.Add(HostingProxyRecoveryPlanLifetime)}
	hash, err := HostingProxyRecoveryPlanHash(p)
	if err != nil {
		t.Fatal(err)
	}
	p.PlanHash = hash
	if got, err := HostingProxyRecoveryPlanHash(p); err != nil || got != hash {
		t.Fatalf("self hash %s %v", got, err)
	}
	p.Review.Routes[0].Enabled = true
	if got, _ := HostingProxyRecoveryPlanHash(p); got == hash {
		t.Fatal("review change did not change hash")
	}
	p.ExpiresAt = p.ExpiresAt.Add(time.Second)
	if _, err := HostingProxyRecoveryPlanHash(p); err == nil {
		t.Fatal("extended expiry accepted")
	}
	for _, key := range []string{"", "has space", strings.Repeat("x", 129)} {
		if ValidateHostingProxyRecoveryApplyInput(HostingProxyRecoveryApplyInput{IdempotencyKey: key, PlanHash: hash}) == nil {
			t.Fatalf("key %q", key)
		}
	}
}
