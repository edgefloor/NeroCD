package main

import (
	"errors"
	"nerocd/internal/domain"
)

func runnerRuntimeLogsCapabilities(enabled, execute, once bool, complete string, specified bool, capabilities []string) ([]string, error) {
	hasObservation := false
	hasStack := false
	result := make([]string, 0, len(capabilities)+2)
	for _, capability := range capabilities {
		if capability == domain.HostingRuntimeLogsCapability {
			hasObservation = true
		}
		hasStack = hasStack || capability == domain.HostingStackRuntimeLogsCapability
		if enabled && capability == "shell" {
			if specified {
				return nil, errors.New("hosting runtime logs cannot share explicit shell capability")
			}
			continue
		}
		result = append(result, capability)
	}
	if !enabled {
		if hasObservation || hasStack {
			return nil, errors.New("runtime log capabilities require --hosting-runtime-logs")
		}
		return capabilities, nil
	}
	if !execute || once || complete != "" {
		return nil, errors.New("hosting runtime logs requires --execute and forbids --once and --complete-status")
	}
	if !hasObservation {
		result = append(result, domain.HostingRuntimeLogsCapability)
	}
	if !hasStack {
		result = append(result, domain.HostingStackRuntimeLogsCapability)
	}
	return result, nil
}
