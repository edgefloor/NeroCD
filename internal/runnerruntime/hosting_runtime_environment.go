package runnerruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// RuntimeEnvironmentConfig explicitly enables protected runtime value delivery.
// TrustedLoopbackHTTP permits literal loopback fixtures only, never remote HTTP.
type RuntimeEnvironmentConfig struct{ TrustedLoopbackHTTP bool }

func validateRuntimeEnvironmentConfig(c Config) error {
	if c.RuntimeEnvironment == nil {
		return nil
	}
	if !c.Execute || c.CompleteStatus != "" {
		return errors.New("runtime environment requires execution and forbids generic completion")
	}
	return runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: c.RuntimeEnvironment.TrustedLoopbackHTTP})
}
func runtimeEnvironmentIdentity(ctx context.Context, c Config) (string, error) {
	return runnerclient.New(c.Server, c.Token).RuntimeExecutionIdentityWithOptions(ctx, runnerclient.RuntimeExecutionOptions{StorageBackupDownloads: c.StorageArchive != nil && c.StorageArchive.BackupDownloads, StorageBackupRetention: c.StorageArchive != nil && c.StorageArchive.BackupRetention, DatabaseBackupRetention: c.Database != nil && c.Database.BackupRetention, RemoteDatabaseBackups: c.Database != nil && c.Database.RemoteBackups, LogicalDatabaseBackups: c.Database != nil && c.Database.LogicalBackups, ApplicationDatabaseStorage: c.Database != nil && c.Database.ApplicationStorage, ApplicationDatabaseRouteContinuity: c.Database != nil && c.Database.RouteContinuity, ApplicationDatabase: c.Database != nil && c.Database.ApplicationDatabase, DatabaseMaintenance: c.Database != nil && c.Database.MembershipMaintenance, StorageArchive: c.StorageArchive != nil, RemoteStorageBackups: c.StorageArchive != nil && c.StorageArchive.RemoteBackups, StorageArchiveTrustedLoopbackHTTP: c.StorageArchive != nil && c.StorageArchive.TrustedLoopbackHTTP, Database: c.Database != nil, DatabaseNetworkCaptureAutomatic: c.Database != nil && c.Database.NetworkCapture, DatabaseUpgrade: c.Database != nil && c.Database.NetworkUpgrade, DatabaseTrustedLoopbackHTTP: c.Database != nil && c.Database.TrustedLoopbackHTTP, Proxy: c.Proxy.Enabled(), ProxyVerifiedSwitch: c.Proxy.VerifiedSwitch, Build: c.Build != nil, LocalBuild: c.LocalBuildDeploy, PrivateGit: c.Build != nil && c.Build.PrivateGit != nil, RegistryPull: c.RegistryPull != nil, RuntimeEnvironment: c.RuntimeEnvironment != nil, Observation: c.HostingObservation != nil, RuntimeLogs: c.HostingRuntimeLogs != nil, Readiness: c.HostingReadiness != nil, ReadinessTrustedLoopbackHTTP: c.HostingReadiness != nil && c.HostingReadiness.TrustedLoopbackHTTP})
}
func markHostingRuntimeEntry(entry *runner.JournalHosting, ref domain.HostingRuntimeConfigurationRef) {
	entry.RuntimeConfiguration = ref
	entry.RuntimeEnvironment = ref != (domain.HostingRuntimeConfigurationRef{})
}
func newRuntimeDeliveryID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("runtime delivery identity unavailable")
	}
	return hex.EncodeToString(value[:]), nil
}
func fetchHostingRuntimeEnvironment(s *attemptSupervisor, c runnerclient.Client, runnerID string, in domain.HostingRuntimeEnvironmentRequest, options RuntimeEnvironmentConfig, deadline time.Time) (*hostingruntimeprotocol.Environment, error) {
	// One fixed request window survives response loss. Requests and retry delays
	// share it; neither lease renewal nor another response extends this window.
	ctx, cancel := context.WithDeadline(s.Context(), deadline)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		environment, err := c.HostingRuntimeEnvironment(ctx, runnerID, in, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: options.TrustedLoopbackHTTP})
		if err == nil {
			_, metadataErr := environment.Metadata()
			if metadataErr != nil || ctx.Err() != nil {
				environment.Close()
				return nil, runnerclient.ErrRuntimeEnvironment
			}
			return environment, nil
		}
		if runnerclient.ClassifyFailure(err) != runnerclient.FailureTransient || attempt == 2 || !waitWorkerInterval(ctx, 100*time.Millisecond) {
			return nil, runnerclient.ErrRuntimeEnvironment
		}
	}
	return nil, runnerclient.ErrRuntimeEnvironment
}

func runtimeDeliveryDeadline(s *attemptSupervisor) time.Time {
	deadline := time.Now().Add(60 * time.Second)
	if guard := s.GuardDeadline(); guard.Before(deadline) {
		deadline = guard
	}
	return deadline
}

// mutateBeforeRuntimeDeadline bounds read-only daemon verification, then checks
// the original window immediately before mutation. The command itself remains
// bound to the attempt context once mutation starts.
func (e *imageEngine) mutateBeforeRuntimeDeadline(ctx context.Context, daemon string, deadline time.Time, args ...string) ([]byte, error) {
	check, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := e.verifyDaemon(check, daemon); err != nil {
		return nil, err
	}
	if check.Err() != nil || !time.Now().Before(deadline) {
		return nil, runnerclient.ErrRuntimeEnvironment
	}
	return e.docker(ctx, args...)
}
