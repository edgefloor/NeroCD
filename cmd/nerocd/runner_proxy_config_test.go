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

func TestVerifiedProxyOnlyUsesSignalContext(t *testing.T) {
	if !runnerNeedsSignalContext(false, false, false, true) {
		t.Fatal("strict proxy-only mode bypasses signal cancellation")
	}
	if runnerNeedsSignalContext(false, false, false, false) {
		t.Fatal("legacy runner signal behavior changed")
	}
	for n := range 3 {
		modes := [3]bool{}
		modes[n] = true
		if !runnerNeedsSignalContext(modes[0], modes[1], modes[2], false) {
			t.Fatal("existing signal mode changed")
		}
	}
}

func TestRunnerReadinessAndVerifiedSwitchCapabilitiesCompose(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, readiness := range []bool{false, true} {
			base := []string{domain.RunTypeProxyApply}
			caps, err := runnerProxyVerifiedSwitchCapabilities(strict, true, base)
			if err != nil {
				t.Fatal(err)
			}
			caps, err = runnerReadinessCapabilities(readiness, true, false, "", true, caps)
			if err != nil {
				t.Fatal(err)
			}
			haveStrict, haveReadiness := false, false
			for _, capability := range caps {
				haveStrict = haveStrict || capability == domain.HostingProxyVerifiedSwitchCapability
				haveReadiness = haveReadiness || capability == domain.ServerReadinessCapability
			}
			if haveStrict != strict || haveReadiness != readiness {
				t.Fatalf("strict=%v readiness=%v capabilities=%v", strict, readiness, caps)
			}
			if !reflect.DeepEqual(base, []string{domain.RunTypeProxyApply}) {
				t.Fatal("combined opt-ins mutated caller capabilities")
			}
			if strict && !runnerNeedsSignalContext(false, false, false, strict) {
				t.Fatal("strict proxy signal context lost")
			}
		}
	}
}
