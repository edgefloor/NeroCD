package main

import (
	"nerocd/internal/domain"
	"reflect"
	"testing"
)

func TestRunnerVerifiedSwitchCapabilityRequiresExplicitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		enabled, proxy bool
		caps           []string
		valid          bool
	}{
		{false, false, nil, true},
		{true, false, nil, false},
		{false, true, []string{domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}, false},
		{true, true, []string{domain.RunTypeProxyApply}, true},
	} {
		before := append([]string(nil), tc.caps...)
		caps, err := runnerProxyVerifiedSwitchCapabilities(tc.enabled, tc.proxy, tc.caps)
		if (err == nil) != tc.valid {
			t.Fatalf("enabled=%v proxy=%v: %v", tc.enabled, tc.proxy, err)
		}
		if !reflect.DeepEqual(before, tc.caps) {
			t.Fatal("capability selection aliases caller")
		}
		if tc.valid && tc.enabled && len(caps) != 2 {
			t.Fatal("verified switch capability not selected")
		}
	}
	if err := runRunner([]string{"--proxy-verified-switch"}); err == nil {
		t.Fatal("flag without proxy configuration accepted")
	}
}
