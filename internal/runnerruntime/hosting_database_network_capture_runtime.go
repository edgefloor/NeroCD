//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// DatabaseNetworkCaptureExecutionConfig enables one explicit read-only capture.
// Engine retains the protected roots on the qualified native daemon host.
type DatabaseNetworkCaptureExecutionConfig struct {
	CaptureID string
	Engine    DatabaseEngineConfig
}

// Format keeps protected host configuration out of diagnostics.
func (DatabaseNetworkCaptureExecutionConfig) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[database network capture configuration]")
}

// MarshalJSON refuses exporting protected host configuration.
func (DatabaseNetworkCaptureExecutionConfig) MarshalJSON() ([]byte, error) {
	return nil, errors.New("database network capture configuration is not exportable")
}

func validateDatabaseNetworkCaptureRuntime(c Config) error {
	if c.DatabaseNetworkCapture == nil || !databaseNetworkCaptureID.MatchString(c.DatabaseNetworkCapture.CaptureID) || !databaseNetworkCaptureRunnerID.MatchString(c.RunnerID) || strings.TrimSpace(c.Token) == "" || strings.TrimSpace(c.JournalDir) == "" || c.JournalDir != strings.TrimSpace(c.JournalDir) {
		return errDatabaseNetworkCaptureExecution
	}
	if !c.Execute || c.CompleteStatus != "" || c.Database != nil || c.StorageArchive != nil || c.Proxy.Enabled() || c.Build != nil || c.LocalBuildDeploy || c.RuntimeEnvironment != nil || c.HostingObservation != nil || c.HostingRuntimeLogs != nil || c.HostingReadiness != nil {
		return errors.New("database network capture requires a separate exact-ID execution mode")
	}
	if runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{}) != nil {
		return errors.New("database network capture requires verified HTTPS")
	}
	return nil
}

func captureRuntimeJournalCompatible(s runner.JournalSnapshot, id string) bool {
	if len(s.StorageArchive)+len(s.BuildLogs)+len(s.BuildLogCursors)+len(s.Build)+len(s.Proxy)+len(s.Hosting)+len(s.Events)+len(s.Completions)+len(s.Provenance) != 0 {
		return false
	}
	for _, e := range s.DatabaseNetworkCapture {
		if e.Attempt.RunID != "capture_"+id {
			return false
		}
	}
	return true
}

type captureRuntimeObserverFactory func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error)

func openCaptureRuntimeObserver(ctx context.Context, config DatabaseEngineConfig, endpoint string) (databaseNetworkCaptureObserver, func() error, error) {
	// Revalidate the roots instead of accepting a zero or stale configuration.
	current, err := NewDatabaseEngineConfig(config.stateRoot, config.secretRoot)
	if err != nil {
		return nil, nil, errDatabaseNetworkCaptureExecution
	}
	e, err := newImageEngine(endpoint)
	if err != nil {
		return nil, nil, errDatabaseNetworkCaptureExecution
	}
	fail := func() (databaseNetworkCaptureObserver, func() error, error) {
		_ = e.Close()
		return nil, nil, errDatabaseNetworkCaptureExecution
	}
	if validateDatabaseDaemon(ctx, e, current) != nil {
		return fail()
	}
	o, err := newDatabaseNetworkOriginObserver(e, current)
	if err != nil {
		return fail()
	}
	return o, e.Close, nil
}

func runDatabaseNetworkCaptureRuntime(ctx context.Context, c Config) error {
	client := runnerclient.New(c.Server, c.Token)
	return runDatabaseNetworkCaptureRuntimeWith(ctx, c, client, func(ctx context.Context) (string, error) {
		return client.RuntimeExecutionIdentityWithOptions(ctx, runnerclient.RuntimeExecutionOptions{DatabaseNetworkCapture: true})
	}, openCaptureRuntimeObserver)
}

// The factory is intentionally lazy: immutable origin/ACK replay does not open
// a daemon connection or resample. The accepted coordinator owns all authority
// and original-window checks; this entry point owns journal/observer lifetimes.
func runDatabaseNetworkCaptureRuntimeWith(parent context.Context, c Config, client databaseNetworkCaptureClient, identity func(context.Context) (string, error), open captureRuntimeObserverFactory) (err error) {
	if err = validateDatabaseNetworkCaptureRuntime(c); err != nil {
		return err
	}
	ctx := runnerclient.WithDatabaseTransport(parent, runnerclient.DatabaseTransport{})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	auth, cancel := context.WithTimeout(ctx, 5*time.Second)
	id, identityErr := identity(auth)
	if identityErr == nil {
		identityErr = auth.Err()
	}
	cancel()
	if identityErr != nil || id != c.RunnerID {
		return captureExecutionFailure(ctx)
	}
	j, err := runner.OpenAttemptJournal(c.JournalDir)
	if err != nil {
		return errDatabaseNetworkCaptureExecution
	}
	defer func() {
		if j.Close() != nil {
			err = errors.Join(err, errDatabaseNetworkCaptureExecution)
		}
	}()
	target := c.DatabaseNetworkCapture.CaptureID
	if !captureRuntimeJournalCompatible(j.Snapshot(), target) {
		return errDatabaseNetworkCaptureExecution
	}
	chain, err := captureExecutionChain(j, c.RunnerID, target)
	if err != nil {
		return errDatabaseNetworkCaptureExecution
	}
	// Preserve the original deadline and avoid opening the host for expired work.
	if len(chain) > 0 && len(chain) < 3 && !chain[0].Attempt.ExpiresAt.After(time.Now()) {
		return errDatabaseNetworkCaptureExecution
	}
	observer := &lazyCaptureRuntimeObserver{config: c.DatabaseNetworkCapture.Engine, endpoint: c.ImageDockerEndpoint, open: open}
	defer func() {
		if observer.Close() != nil {
			err = errors.Join(err, errDatabaseNetworkCaptureExecution)
		}
	}()
	return executeDatabaseNetworkCapture(ctx, j, client, observer, c.RunnerID, target)
}
