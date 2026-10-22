package main

import (
	"errors"

	"nerocd/internal/domain"
)

func runnerLocalBuildCapabilities(enabled, build, proxy, specified bool, capabilities []string) ([]string, error) {
	if enabled && !specified {
		capabilities = []string{domain.RunTypeLocalBuildDeploy}
		if build {
			capabilities = append(capabilities, domain.HostingBuildTaskType)
		}
		if proxy {
			capabilities = append(capabilities, domain.RunTypeProxyApply)
		}
	}
	found := false
	for _, capability := range capabilities {
		if capability == domain.RunTypeLocalBuildDeploy {
			found = true
		}
		if enabled && capability == "shell" {
			return nil, errors.New("shell and local build deployment cannot share this runner")
		}
	}
	if found && !enabled {
		return nil, errors.New("local_build_deploy_v1 requires --local-build-deploy")
	}
	if enabled && !found {
		capabilities = append(capabilities, domain.RunTypeLocalBuildDeploy)
	}
	return capabilities, nil
}
