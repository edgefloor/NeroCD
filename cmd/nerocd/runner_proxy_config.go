package main

import (
	"errors"
	"nerocd/internal/domain"
	"slices"
)

func runnerProxyVerifiedSwitchCapabilities(enabled, proxyConfigured bool, capabilities []string) ([]string, error) {
	capable := slices.Contains(capabilities, domain.HostingProxyVerifiedSwitchCapability)
	if enabled && !proxyConfigured || capable && !enabled {
		return nil, errors.New("verified proxy switching requires --proxy-verified-switch and explicit proxy configuration")
	}
	if enabled && !capable {
		capabilities = append(append([]string(nil), capabilities...), domain.HostingProxyVerifiedSwitchCapability)
	}
	return capabilities, nil
}

func runnerNeedsSignalContext(buildConfigured, localBuild, runtimeEnvironment, verifiedSwitch bool) bool {
	return buildConfigured || localBuild || runtimeEnvironment || verifiedSwitch
}
