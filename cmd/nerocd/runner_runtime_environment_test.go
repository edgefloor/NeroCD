package main

import (
	"nerocd/internal/domain"
	"testing"
)

func TestRunnerRuntimeEnvironmentCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		enabled, execute, specified bool
		complete                    string
		caps                        []string
		ok                          bool
	}{
		{name: "default image only", enabled: true, execute: true, caps: []string{"shell"}, ok: true},
		{name: "explicit image", enabled: true, execute: true, specified: true, caps: []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}, ok: true},
		{name: "local artifact", enabled: true, execute: true, specified: true, caps: []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeLocalBuildDeploy}, ok: true},
		{name: "no opt in", execute: true, caps: []string{domain.HostingRuntimeEnvironmentCapability}},
		{name: "shell", enabled: true, execute: true, specified: true, caps: []string{"shell", domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}},
		{name: "missing release", enabled: true, execute: true, specified: true, caps: []string{domain.RunTypeImageDeploy}},
		{name: "no execute", enabled: true},
		{name: "generic completion", enabled: true, execute: true, complete: "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps, err := runnerRuntimeEnvironmentCapabilities(tc.enabled, tc.execute, tc.complete, tc.specified, tc.caps)
			if (err == nil) != tc.ok {
				t.Fatalf("outcome %v", err)
			}
			if tc.ok {
				seen := map[string]bool{}
				for _, c := range caps {
					seen[c] = true
				}
				if !seen[domain.HostingRuntimeEnvironmentCapability] || !seen[domain.RunTypeImageDeploy] || !seen[domain.HostingReleasePortsCapability] || seen["shell"] {
					t.Fatal("wrong capability contract")
				}
			}
		})
	}
}
