package main

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

func runnerDatabaseConfig(enabled, execute bool, complete, stateRoot, secretRoot string) (*runnerruntime.DatabaseExecutionConfig, error) {
	if !enabled {
		if stateRoot != "" || secretRoot != "" {
			return nil, errors.New("database roots require --hosting-database")
		}
		return nil, nil
	}
	if !execute || complete != "" {
		return nil, errors.New("database execution requires --execute and forbids --complete-status")
	}
	if stateRoot == "" || secretRoot == "" {
		return nil, errors.New("database execution requires explicit persistent state and secret roots")
	}
	return &runnerruntime.DatabaseExecutionConfig{StateRoot: stateRoot, SecretRoot: secretRoot}, nil
}
func runnerDatabaseCapabilities(enabled, specified bool, capabilities []string) ([]string, error) {
	if enabled && !specified {
		capabilities = []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	}
	has := func(name string) bool {
		for _, capability := range capabilities {
			if capability == name {
				return true
			}
		}
		return false
	}
	if !enabled && (has(domain.HostingDatabaseCapability) || has(domain.RunTypeDatabaseDeploy)) {
		return nil, errors.New("database capabilities require --hosting-database")
	}
	if enabled {
		if has("shell") {
			return nil, errors.New("shell and database execution cannot share this runner")
		}
		for _, capability := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
			if !has(capability) {
				capabilities = append(capabilities, capability)
			}
		}
	}
	return capabilities, nil
}

// postRunnerBootstrap keeps database and readiness enrollment on the protected
// transport before any runner credential is available for ordinary requests.
func postRunnerBootstrap(ctx context.Context, database, readiness bool, server, path string, in any, token string, out any) error {
	if database || readiness {
		return postRunnerReadinessBootstrap(ctx, server, path, in, token, out)
	}
	return postAPIInto(server+path, in, token, out)
}
