package main

import (
	"testing"

	"nerocd/internal/domain"
)

func TestRunnerLocalBuildCapabilitiesRequireExplicitMode(t *testing.T) {
	for _, tc := range []struct {
		enabled, build, proxy, specified bool
		caps                             []string
		fail                             bool
	}{
		{true, false, false, false, []string{"shell"}, false},
		{true, true, true, false, []string{domain.HostingBuildTaskType, domain.RunTypeProxyApply}, false},
		{true, false, false, true, []string{"shell"}, true},
		{false, false, false, true, []string{domain.RunTypeLocalBuildDeploy}, true},
		{true, false, false, true, []string{"opaque"}, false},
	} {
		got, err := runnerLocalBuildCapabilities(tc.enabled, tc.build, tc.proxy, tc.specified, tc.caps)
		if (err != nil) != tc.fail {
			t.Fatal("local capability guard mismatch")
		}
		if !tc.fail && tc.enabled {
			found := false
			for _, c := range got {
				found = found || c == domain.RunTypeLocalBuildDeploy
				if c == "shell" {
					t.Fatal("shell capability retained")
				}
			}
			if !found {
				t.Fatal("local capability absent")
			}
		}
	}
}
