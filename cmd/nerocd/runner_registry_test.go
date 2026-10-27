package main

import (
	"slices"
	"testing"

	"nerocd/internal/domain"
)

func TestRunnerRegistryPullCapabilityConfiguration(t *testing.T) {
	caps, err := runnerRegistryPullCapabilities(true, true, "", false, []string{"shell"})
	if err != nil || slices.Contains(caps, "shell") || !slices.Contains(caps, domain.HostingRegistryPullCapability) || !slices.Contains(caps, domain.HostingReleasePortsCapability) {
		t.Fatal("configured capability", err)
	}
	for _, c := range []struct {
		enabled, execute, specified bool
		complete                    string
		caps                        []string
	}{{false, true, true, "", []string{domain.HostingRegistryPullCapability}}, {true, false, false, "", nil}, {true, true, false, "succeeded", nil}, {true, true, true, "", []string{"shell"}}, {true, true, true, "", []string{domain.RunTypeImageDeploy}}} {
		if _, err := runnerRegistryPullCapabilities(c.enabled, c.execute, c.complete, c.specified, c.caps); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
