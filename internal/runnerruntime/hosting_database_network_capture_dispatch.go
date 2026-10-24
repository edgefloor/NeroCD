package runnerruntime

import (
	"context"
	"errors"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

type captureDispatchOutcome int

const (
	captureDispatchEmpty captureDispatchOutcome = iota
	captureDispatchCompleted
	captureDispatchContended
)

type automaticDatabaseNetworkCaptureDispatcher interface {
	Reconcile(context.Context) error
	Poll(context.Context) (captureDispatchOutcome, error)
	BeforeOrdinary(context.Context) error
}

func validateAutomaticDatabaseNetworkCaptureConfig(c Config) error {
	if c.Database == nil || !c.Database.NetworkCapture {
		return nil
	}
	if c.DatabaseNetworkCapture != nil || !c.Execute || c.CompleteStatus != "" || strings.TrimSpace(c.JournalDir) == "" || c.JournalDir != strings.TrimSpace(c.JournalDir) || strings.TrimSpace(c.Token) == "" || !databaseNetworkCaptureRunnerID.MatchString(c.RunnerID) || c.Database.StateRoot == "" || c.Database.SecretRoot == "" {
		return errors.New("automatic database capture requires explicit database execution, identity, journal and protected roots")
	}
	if c.Database.TrustedLoopbackHTTP || runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{}) != nil {
		return errors.New("automatic database capture requires verified HTTPS")
	}
	return nil
}

// Selection and ordinary admission remain serial. Contention never falls through
// to an ordinary claim, and the existing host preflight precedes that claim.
func claimRunnerIteration(ctx context.Context, capture automaticDatabaseNetworkCaptureDispatcher, claim func(context.Context) (domain.ClaimedRun, error)) (domain.ClaimedRun, captureDispatchOutcome, error) {
	if capture != nil {
		outcome, err := capture.Poll(ctx)
		if err != nil || outcome != captureDispatchEmpty {
			return domain.ClaimedRun{}, outcome, err
		}
		if err := capture.BeforeOrdinary(ctx); err != nil {
			return domain.ClaimedRun{}, captureDispatchEmpty, err
		}
	}
	got, err := claim(ctx)
	if err == nil && got.Run.RunSpec.Type == domain.RunTypeDatabaseNetworkCapture {
		return domain.ClaimedRun{}, captureDispatchEmpty, errors.New("database network capture forbids generic execution and completion")
	}
	return got, captureDispatchEmpty, err
}

// Capture publication precedes every Docker preflight. Other families keep their
// established host prerequisites before their recovery or background work.
func reconcileAutomaticCaptureStartup(ctx context.Context, c Config, capture automaticDatabaseNetworkCaptureDispatcher, archivePreflight func(context.Context) error, beforeHost ...func(context.Context) error) error {
	if err := capture.Reconcile(ctx); err != nil {
		return err
	}
	for _, replay := range beforeHost {
		if err := replay(ctx); err != nil {
			return err
		}
	}
	if c.StorageArchive != nil {
		if err := archivePreflight(ctx); err != nil {
			return err
		}
	}
	if c.StorageArchive != nil || c.Proxy.Enabled() || c.Build != nil || c.LocalBuildDeploy || c.RuntimeEnvironment != nil || c.HostingObservation != nil || c.HostingRuntimeLogs != nil || c.HostingReadiness != nil {
		return capture.BeforeOrdinary(ctx)
	}
	return nil
}
