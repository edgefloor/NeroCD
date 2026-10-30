package main

import (
	"errors"
	"nerocd/internal/domain"
	"slices"
)

// runnerDatabaseDownloadCapabilities enrolls the exact read adapter only by opt-in.
func runnerDatabaseDownloadCapabilities(enabled bool, capabilities []string) ([]string, error) {
	if !enabled {
		if slices.Contains(capabilities, domain.HostingDatabaseBackupDownloadCapability) {
			return nil, errors.New("database download capability requires --hosting-database-backup-download")
		}
		return capabilities, nil
	}
	out := slices.Clone(capabilities)
	for _, capability := range []string{domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupDownloadCapability} {
		if !slices.Contains(out, capability) {
			out = append(out, capability)
		}
	}
	return out, nil
}
