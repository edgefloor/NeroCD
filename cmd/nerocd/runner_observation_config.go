package main

import (
	"errors"
	"nerocd/internal/domain"
)

func runnerObservationCapabilities(enabled, execute, once bool, complete string, specified bool, capabilities []string) ([]string, error) {
	hasObservation := false
	result := make([]string, 0, len(capabilities)+1)
	for _, capability := range capabilities {
		if capability == domain.HostingObservationCapability {
			hasObservation = true
		}
		if enabled && capability == "shell" {
			if specified {
				return nil, errors.New("hosting observation cannot share explicit shell capability")
			}
			continue
		}
		result = append(result, capability)
	}
	if !enabled {
		if hasObservation {
			return nil, errors.New("hosting_observation_v1 requires --hosting-observation")
		}
		return capabilities, nil
	}
	if !execute || once || complete != "" {
		return nil, errors.New("hosting observation requires --execute and forbids --once and --complete-status")
	}
	if !hasObservation {
		result = append(result, domain.HostingObservationCapability)
	}
	return result, nil
}
