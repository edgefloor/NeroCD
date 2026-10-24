package main

import (
	"errors"
	"slices"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func runnerApplicationDatabaseConfig(application, maintenance, runtimeEnvironment bool, config *runnerruntime.DatabaseExecutionConfig) error {
	if application || maintenance {
		if config == nil || !config.NetworkCapture || !config.NetworkUpgrade || !runtimeEnvironment {
			return errors.New("connected database execution requires --hosting-database, --hosting-database-network-capture, --hosting-database-network-upgrade and --hosting-runtime-env")
		}
	}
	if config != nil {
		config.ApplicationDatabase, config.MembershipMaintenance = application, maintenance
	}
	return nil
}

func runnerApplicationDatabaseCapabilities(application, maintenance bool, capabilities []string) ([]string, error) {
	for _, option := range []struct {
		enabled    bool
		capability string
	}{{application, domain.HostingApplicationDatabaseCapability}, {maintenance, domain.HostingDatabaseMaintenanceCapability}} {
		if !option.enabled && slices.Contains(capabilities, option.capability) {
			return nil, errors.New("connected database capabilities require explicit execution opt-in")
		}
		if option.enabled && !slices.Contains(capabilities, option.capability) {
			capabilities = append(capabilities, option.capability)
		}
	}
	if application || maintenance {
		for _, cap := range []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability} {
			if !slices.Contains(capabilities, cap) {
				return nil, errors.New("connected database capability prerequisites unavailable")
			}
		}
		if slices.Contains(capabilities, "shell") {
			return nil, errors.New("connected database execution forbids shell capability")
		}
	}
	return capabilities, nil
}
