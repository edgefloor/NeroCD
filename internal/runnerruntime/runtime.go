package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errLeaseAuthorityLost = errors.New("lease authority lost")

// runnerOperationalCounters are intentionally process-lifetime, aggregate
// counters. They never retain an operation, lease, runner, URL, or error. The
// service bounds the same values, and saturation keeps a compromised or
// persistently failing runner from publishing an unbounded integer.
type runnerOperationalCounters struct {
	retries       atomic.Uint64
	renewFailures atomic.Uint64
}

const maxRunnerOperationalCounter = uint64(100000)

func (c *runnerOperationalCounters) increment(value *atomic.Uint64) {
	for {
		current := value.Load()
		if current >= maxRunnerOperationalCounter || value.CompareAndSwap(current, current+1) {
			return
		}
	}
}

func (c *runnerOperationalCounters) Retry()        { c.increment(&c.retries) }
func (c *runnerOperationalCounters) RenewFailure() { c.increment(&c.renewFailures) }
func (c *runnerOperationalCounters) Snapshot() (retryCount, renewFailures int) {
	return int(c.retries.Load()), int(c.renewFailures.Load())
}

// attemptSupervisor owns the cancellation boundary for a single fenced attempt.
// Its watchdog is independent of request goroutines, so a blocked request cannot
// let a child continue after the locally known authority deadline.
type attemptSupervisor struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	expiry  time.Time
	margin  time.Duration
	done    chan struct{}
	metrics *runnerOperationalCounters
}
type leaseWatcher struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}
type leaseRenewer struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func (r *leaseRenewer) Stop() { r.once.Do(func() { r.cancel(); <-r.done }) }

func (w *leaseWatcher) Stop() { w.once.Do(func() { w.cancel(); <-w.done }) }

func newAttemptSupervisor(lease domain.RunLease, counters ...*runnerOperationalCounters) *attemptSupervisor {
	return newAttemptSupervisorFrom(context.Background(), lease, counters...)
}
func newAttemptSupervisorFrom(parent context.Context, lease domain.RunLease, counters ...*runnerOperationalCounters) *attemptSupervisor {
	ctx, cancel := context.WithCancel(parent)
	// created_at identifies the attempt, not the latest renewal period. After a
	// long-running lease is read during startup replay it can be many minutes old
	// while still carrying a fresh short TTL. Base the local guard on authority
	// remaining now so cumulative attempt age cannot cancel a valid renewal.
	remaining := time.Until(lease.ExpiresAt)
	margin := remaining / 5
	if margin < time.Second {
		margin = time.Second
	}
	if margin > 30*time.Second {
		margin = 30 * time.Second
	}
	metrics := &runnerOperationalCounters{}
	if len(counters) != 0 && counters[0] != nil {
		metrics = counters[0]
	}
	s := &attemptSupervisor{ctx: ctx, cancel: cancel, expiry: lease.ExpiresAt, margin: margin, done: make(chan struct{}), metrics: metrics}
	go func() {
		defer close(s.done)
		for {
			s.mu.RLock()
			until := time.Until(s.expiry.Add(-s.margin))
			s.mu.RUnlock()
			if until <= 0 {
				s.cancel()
				return
			}
			t := time.NewTimer(until)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	return s
}
func (s *attemptSupervisor) Context() context.Context { return s.ctx }
func (s *attemptSupervisor) RequestContext() (context.Context, context.CancelFunc, error) {
	return s.RequestContextFrom(s.ctx)
}
func (s *attemptSupervisor) RequestContextFrom(parent context.Context) (context.Context, context.CancelFunc, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errLeaseAuthorityLost, err)
	}
	s.mu.RLock()
	deadline := s.expiry.Add(-s.margin)
	s.mu.RUnlock()
	if !deadline.After(time.Now()) {
		s.cancel()
		return nil, nil, fmt.Errorf("%w: %v", errLeaseAuthorityLost, context.DeadlineExceeded)
	}
	max := time.Now().Add(5 * time.Second)
	if deadline.After(max) {
		deadline = max
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, nil
}
func (s *attemptSupervisor) Update(lease domain.RunLease) {
	s.mu.Lock()
	if lease.ExpiresAt.After(s.expiry) {
		s.expiry = lease.ExpiresAt
	}
	s.mu.Unlock()
}
func (s *attemptSupervisor) Expiry() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.expiry
}
func (s *attemptSupervisor) GuardDeadline() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.expiry.Add(-s.margin)
}
func (s *attemptSupervisor) Close() { s.cancel(); <-s.done }

type runnerCompleteRequest = runnerclient.CompleteRequest
type runnerEventRequest = runnerclient.EventRequest
type runnerEventBatchRequest = runnerclient.EventBatchRequest
type runnerEventBatchAck = runnerclient.EventBatchAck

// Config supplies the narrow command-owned settings needed by the runner runtime.
type Config struct {
	StorageArchive         *StorageArchiveExecutionConfig
	Database               *DatabaseExecutionConfig
	DatabaseNetworkCapture *DatabaseNetworkCaptureExecutionConfig
	HostingReadiness       *HostingReadinessConfig
	HostingObservation     *HostingObservationConfig
	HostingRuntimeLogs     *HostingRuntimeLogsConfig
	RegistryPull           *RegistryPullConfig
	RuntimeEnvironment     *RuntimeEnvironmentConfig
	LocalBuildDeploy       bool
	Build                  *BuildConfig
	Proxy                  ProxyConfig
	ImageDockerEndpoint    string
	Server                 string
	Token                  string
	RunnerID               string
	WorkDir                string
	JournalDir             string
	SecretRoot             string
	ImagePolicy            string
	Execute                bool
	Once                   bool
	PollInterval           time.Duration
	CancelPollInterval     time.Duration
	CompleteStatus         string
}

// Run owns the runner loop, durable replay, lease lifecycle, and execution.
func Run(config Config) error { return RunContext(context.Background(), config) }

// RunContext joins attempt cleanup before returning after parent cancellation.
// It retains unresolved journal evidence and never treats cancellation as success.
func RunContext(ctx context.Context, config Config) error {
	if config.DatabaseNetworkCapture != nil {
		return runDatabaseNetworkCaptureRuntime(ctx, config)
	}
	if config.StorageArchive != nil {
		ctx = runnerclient.WithStorageArchiveTransport(ctx, runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: config.StorageArchive.TrustedLoopbackHTTP})
	}
	if config.Database != nil {
		ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: config.Database.TrustedLoopbackHTTP})
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := validateDatabaseExecutionConfig(config); err != nil {
		return err
	}
	if err := validateStorageArchiveExecutionConfig(config); err != nil {
		return err
	}
	if err := validateHostingReadinessConfig(config); err != nil {
		return err
	}
	if err := validateHostingRuntimeLogsConfig(config); err != nil {
		return err
	}
	if err := validateHostingObservationConfig(config); err != nil {
		return err
	}
	if err := validateRegistryPullConfig(config); err != nil {
		return err
	}
	if err := validateRuntimeEnvironmentConfig(config); err != nil {
		return err
	}
	if config.LocalBuildDeploy && (!config.Execute || config.CompleteStatus != "") {
		return errors.New("local build deployment requires execution and forbids generic completion")
	}
	if err := config.Proxy.Validate(); err != nil {
		return err
	}
	if err := validateBuildRuntimeConfig(config); err != nil {
		return err
	}
	if config.Proxy.Enabled() && !config.Execute {
		return errors.New("configured managed proxy requires execution")
	}
	if config.Database != nil && !config.Database.NetworkCapture {
		if err := ValidateDatabaseExecution(ctx, *config.Database, config.ImageDockerEndpoint); err != nil {
			return err
		}
	}
	if config.StorageArchive != nil && (config.Database == nil || !config.Database.NetworkCapture) {
		if err := ValidateStorageArchiveExecution(ctx, *config.StorageArchive, config.ImageDockerEndpoint); err != nil {
			return err
		}
	}
	if config.StorageArchive != nil || config.Database != nil || config.Proxy.Enabled() || config.Build != nil || config.LocalBuildDeploy || config.RegistryPull != nil || config.RuntimeEnvironment != nil || config.HostingObservation != nil || config.HostingRuntimeLogs != nil || config.HostingReadiness != nil {
		if err := validateLocalBuildRuntimeIdentity(ctx, config); err != nil {
			return err
		}
	}
	imagePolicy, err := parseComposeImagePolicy(config.ImagePolicy)
	if err != nil {
		return err
	}
	resolvedJournalDir := strings.TrimSpace(config.JournalDir)
	if resolvedJournalDir == "" {
		resolvedJournalDir = filepath.Join(config.WorkDir, ".nerocd-journal-"+attemptMutationKey("runner", config.RunnerID, 1, "journal"))
	}
	journal, err := runner.OpenAttemptJournal(resolvedJournalDir)
	if err != nil {
		return fmt.Errorf("open runner journal: %w", err)
	}
	defer func() { _ = journal.Close() }()
	// A saved connected completion needs no daemon. Capture chains retain their
	// dedicated first dispatch and mixed-evidence hold before any other replay.
	if err := reconcileConnectedBeforeCapture(ctx, journal.Snapshot(), func(ctx context.Context) error {
		if err := replaySavedDatabaseMaintenanceCompletions(ctx, config, journal); err != nil {
			return err
		}
		return reconcileApplicationDatabaseJournal(ctx, config, journal)
	}); err != nil {
		return err
	}
	captureDispatcher, err := newAutomaticDatabaseNetworkCaptureDispatcher(config, journal)
	if err != nil {
		return err
	}
	if captureDispatcher != nil {
		if err := reconcileAutomaticCaptureStartup(ctx, config, captureDispatcher, func(ctx context.Context) error {
			return ValidateStorageArchiveExecution(ctx, *config.StorageArchive, config.ImageDockerEndpoint)
		}, func(ctx context.Context) error { return reconcileApplicationDatabaseJournal(ctx, config, journal) }); err != nil {
			return err
		}
	} else if len(journal.Snapshot().DatabaseNetworkCapture) != 0 {
		return errors.New("retained database network capture requires explicit exact-ID capture mode")
	} else if err := reconcileApplicationDatabaseJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := validateLocalBuildJournalConfiguration(journal, config.LocalBuildDeploy); err != nil {
		return err
	}
	buildReplayCtx, buildReplayCancel := context.WithTimeout(ctx, 30*time.Second)
	buildExecutor, buildReplayErr := reconcileRuntimeBuilds(buildReplayCtx, config, journal)
	buildReplayCancel()
	if buildReplayErr != nil {
		return buildReplayErr
	}
	operational := &runnerOperationalCounters{}
	proxyReplayConfig := config.Proxy
	proxyReplayConfig.storageDatabase = config.Database
	proxyReplayConfig.storageEndpoint = config.ImageDockerEndpoint
	if err := reconcileProxyJournal(config.Server, config.Token, config.RunnerID, journal, proxyReplayConfig, ctx); err != nil {
		return fmt.Errorf("reconcile proxy journal: %w", err)
	}
	if err := reconcileStorageBackupRetentionJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileDatabaseBackupRetentionJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileStorageBackupTransferJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileDatabaseBackupTransferJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileDatabaseBackupJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileStorageArchiveJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileHostingDatabaseJournal(ctx, config, journal); err != nil {
		return err
	}
	if err := reconcileHostingJournalWithLocalBuild(config.Server, config.Token, journal, config.ImageDockerEndpoint, config.LocalBuildDeploy, ctx); err != nil {
		return fmt.Errorf("reconcile hosting journal: %w", err)
	}
	if err := reconcileRunnerJournalWithCounters(config.Server, config.Token, journal, operational, ctx); err != nil {
		return fmt.Errorf("reconcile runner journal: %w", err)
	}
	// Preserve legacy uncertain-journal reconciliation before ordinary lifecycle
	// reads. Configured build/proxy runners authenticate before their replay.
	if config.StorageArchive == nil && config.Database == nil && !config.Proxy.Enabled() && config.Build == nil && !config.LocalBuildDeploy && config.RegistryPull == nil && config.RuntimeEnvironment == nil && config.HostingObservation == nil && config.HostingRuntimeLogs == nil && config.HostingReadiness == nil {
		if err := validateLocalBuildRuntimeIdentity(ctx, config); err != nil {
			return err
		}
	}
	if config.HostingReadiness != nil {
		stop, err := startHostingReadiness(ctx, config, resolvedJournalDir)
		if err != nil {
			return err
		}
		defer stop()
	}
	if config.HostingObservation != nil {
		stop, err := startHostingObservation(ctx, config)
		if err != nil {
			return err
		}
		defer stop()
	}
	if config.HostingRuntimeLogs != nil {
		stop, err := startHostingRuntimeLogs(ctx, config)
		if err != nil {
			return err
		}
		defer stop()
	}
	for {
		if _, err := runnerclient.New(config.Server, config.Token).Heartbeat(ctx); err != nil {
			return err
		}
		retryCount, renewFailures := operational.Snapshot()
		if err := runnerclient.New(config.Server, config.Token).ReportTelemetry(ctx, runnerclient.OperationalTelemetry{JournalDepth: journal.Depth(), RetryCount: retryCount, RenewFailures: renewFailures}); err != nil {
			return fmt.Errorf("report runner telemetry: %w", err)
		}
		claim, capture, err := claimRunnerIteration(ctx, captureDispatcher, runnerclient.New(config.Server, config.Token).Claim)
		if err == nil && capture != captureDispatchEmpty {
			if capture == captureDispatchCompleted && config.Once {
				return nil
			}
			if !waitWorkerInterval(ctx, config.PollInterval) {
				return ctx.Err()
			}
			continue
		}
		if err != nil {
			if runnerHTTPStatus(err, http.StatusNotFound) {
				if config.Once {
					fmt.Println("runner registered; no matching queued run")
					return nil
				}
				if !waitWorkerInterval(ctx, config.PollInterval) {
					return ctx.Err()
				}
				continue
			}
			return err
		}
		if isBuildClaim(claim) {
			if !config.Execute {
				return errors.New("hosting_build_v1 requires explicit execution; generic completion is forbidden")
			}
			if err := executeBuildClaim(ctx, config.Server, config.Token, claim, config.RunnerID, config.CancelPollInterval, buildExecutor, operational); err != nil && !settledBuildMayContinue(ctx, config.Once, err) {
				return err
			}
			if config.Once {
				return nil
			}
			continue
		}
		if claim.Run.RunSpec.Type == domain.RunTypeDatabaseDeploy && (config.Database == nil || !config.Execute || config.CompleteStatus != "") {
			return errors.New("database_deploy requires configured execution; generic completion is forbidden")
		}
		if claim.Run.RunSpec.Type == domain.RunTypeStorageArchive && (config.StorageArchive == nil || !config.Execute || config.CompleteStatus != "") {
			return errors.New("storage_archive requires configured execution; generic completion is forbidden")
		}
		if claim.Run.RunSpec.Type == domain.RunTypeLocalBuildDeploy && (!config.LocalBuildDeploy || !config.Execute || config.CompleteStatus != "") {
			return errors.New("local build deployment requires explicit execution; generic completion is forbidden")
		}
		if err := printRunnerAuthorityEvent("claimed_run", claim.Run.ID, claim.Lease); err != nil {
			return err
		}
		if config.Execute {
			if err := executeClaimWithOptionsContext(ctx, config.Server, config.Token, claim, config.WorkDir, config.CancelPollInterval, journal, config.SecretRoot, imagePolicy, operational, claimExecutionOptions{storageArchive: config.StorageArchive, database: config.Database, imageEndpoint: config.ImageDockerEndpoint, proxy: config.Proxy, localBuild: config.LocalBuildDeploy, registryPull: config.RegistryPull, runtimeEnvironment: config.RuntimeEnvironment}); err != nil && !settledImagePreparationMayContinue(ctx, config.Once, err) {
				return err
			}
		} else if config.CompleteStatus != "" {
			if claim.Run.RunSpec.Type == domain.RunTypeProxyApply {
				return errors.New("proxy_apply requires explicit managed proxy execution; generic completion is forbidden")
			}
			if claim.Run.RunSpec.Type == domain.RunTypeImageDeploy {
				return errors.New("image_deploy requires --execute; generic completion is forbidden")
			}
			if strings.TrimSpace(claim.Lease.ID) == "" {
				return errors.New("claim response lease did not include id")
			}
			supervisor := newAttemptSupervisorFrom(ctx, claim.Lease)
			var completed domain.RunLease
			if err := journaledCompletion(supervisor, journal, config.Server, config.Token, claim.Run.ID, claim.Lease, config.CompleteStatus, &completed); err != nil {
				supervisor.Close()
				return err
			}
			supervisor.Close()
			if err := printRunnerAuthorityEvent("completed_run", claim.Run.ID, completed); err != nil {
				return err
			}
		}
		if config.Once {
			return nil
		}
	}
}

func printRunnerAuthorityEvent(event, runID string, lease domain.RunLease) error {
	return json.NewEncoder(os.Stdout).Encode(struct {
		Event   string `json:"event"`
		RunID   string `json:"run_id"`
		LeaseID string `json:"lease_id"`
		Attempt int    `json:"attempt"`
		Status  string `json:"status,omitempty"`
	}{Event: event, RunID: runID, LeaseID: lease.ID, Attempt: lease.Attempt, Status: lease.Status})
}

func executeClaim(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration) error {
	journalDir := filepath.Join(workDir, ".nerocd-execute-journal-"+attemptMutationKey("lease", claim.Lease.ID, claim.Lease.Attempt, "journal"))
	journal, err := runner.OpenAttemptJournal(journalDir)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	return executeClaimWithJournalAndSecretRoot(server, token, claim, workDir, cancelPollInterval, journal, os.Getenv("NEROCD_RUNNER_SECRET_ROOT"))
}

func executeClaimWithJournalAndSecretRoot(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string) error {
	return executeClaimWithJournalAndSecretRootAndCounters(server, token, claim, workDir, cancelPollInterval, journal, secretRoot, composeImagePolicyPreloaded, &runnerOperationalCounters{})
}

func executeClaimWithJournalAndSecretRootAndCounters(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string, imagePolicy composeImagePolicy, operational *runnerOperationalCounters, imageEndpoints ...string) error {
	options := claimExecutionOptions{}
	if len(imageEndpoints) > 0 {
		options.imageEndpoint = imageEndpoints[0]
	}
	return executeClaimWithOptions(server, token, claim, workDir, cancelPollInterval, journal, secretRoot, imagePolicy, operational, options)
}

type claimExecutionOptions struct {
	storageArchive     *StorageArchiveExecutionConfig
	database           *DatabaseExecutionConfig
	registryPull       *RegistryPullConfig
	runtimeEnvironment *RuntimeEnvironmentConfig
	localBuild         bool
	imageEndpoint      string
	proxy              ProxyConfig
	build              *BuildExecutor
}

func executeClaimWithOptions(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string, imagePolicy composeImagePolicy, operational *runnerOperationalCounters, options claimExecutionOptions) error {
	return executeClaimWithOptionsContext(context.Background(), server, token, claim, workDir, cancelPollInterval, journal, secretRoot, imagePolicy, operational, options)
}
func executeClaimWithOptionsContext(parent context.Context, server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string, imagePolicy composeImagePolicy, operational *runnerOperationalCounters, options claimExecutionOptions) error {
	if options.storageArchive != nil {
		parent = runnerclient.WithStorageArchiveTransport(parent, runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: options.storageArchive.TrustedLoopbackHTTP})
	}
	if options.database != nil {
		parent = runnerclient.WithDatabaseTransport(parent, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: options.database.TrustedLoopbackHTTP})
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if err := validateTypedExecutionClaim(claim); err != nil {
		return err
	}
	if isBuildClaim(claim) {
		return executeBuildClaim(parent, server, token, claim, claim.Lease.RunnerID, cancelPollInterval, options.build, operational)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeComposeDeploy {
		if err := validateComposeExecutionClaim(claim); err != nil {
			return err
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeStorageBackupRetention {
		if options.storageArchive == nil || !options.storageArchive.BackupRetention || domain.ValidateStorageBackupRetentionTask(claim.Run) != nil {
			return errors.New("storage retention is unconfigured")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupRetention {
		if options.database == nil || !options.database.BackupRetention || domain.ValidateDatabaseBackupRetentionTask(claim.Run) != nil {
			return errors.New("backup retention execution not configured")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeStorageBackupTransfer {
		if options.storageArchive == nil || (claim.Run.RunSpec.HostingMode == domain.HostingStorageBackupDownloadMode && !options.storageArchive.BackupDownloads) || (claim.Run.RunSpec.HostingMode != domain.HostingStorageBackupDownloadMode && !options.storageArchive.RemoteBackups) || domain.ValidateStorageBackupTransferTask(claim.Run) != nil {
			return errors.New("storage remote backup execution not configured")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupTransfer {
		if options.database == nil || !options.database.RemoteBackups || domain.ValidateDatabaseBackupTransferTask(claim.Run) != nil {
			return errors.New("invalid or unconfigured remote backup task")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackup {
		if options.database == nil || !options.database.LogicalBackups || domain.ValidateDatabaseBackupTask(claim.Run) != nil {
			return errors.New("invalid or unconfigured database backup task")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeStorageArchive {
		if options.storageArchive == nil || domain.ValidateStorageArchiveTask(claim.Run) != nil {
			return errors.New("invalid or unconfigured storage archive task")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeDatabaseDeploy {
		if options.database == nil || domain.ValidateDatabaseDeploymentTask(claim.Run) != nil {
			return errors.New("invalid or unconfigured database task")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeLocalBuildDeploy {
		if !options.localBuild || validateLocalBuildClaim(claim) != nil {
			return errors.New("invalid or unconfigured local build deployment task")
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeImageDeploy {
		if err := domain.ValidateImageDeploymentTask(claim.Run); err != nil {
			return err
		}
	} else if claim.Run.RunSpec.Type == domain.RunTypeProxyApply {
		if err := domain.ValidateProxyApplyTask(claim.Run); err != nil {
			return err
		}
		if !options.proxy.Enabled() {
			return errors.New("managed proxy execution is not configured")
		}
	} else if domain.ContainsHostingExecution(claim.Run.RunSpec, claim.Run.Workflow) {
		return errors.New("hosting execution cannot enter the generic runtime")
	}
	sequence := 4
	var sequenceMu sync.Mutex
	var supervisor *attemptSupervisor
	var reporter *attemptReporter
	var redactor *runner.Redactor
	emitPersisted := func(stream string, message string) {
		if message == "" {
			return
		}
		sequenceMu.Lock()
		currentSequence := sequence
		sequence++
		sequenceMu.Unlock()
		if supervisor == nil || reporter == nil {
			return
		}
		if err := reporter.Emit(stream, message, currentSequence); err != nil {
			supervisor.cancel()
		}
	}
	emit := func(stream string, message string) {
		if redactor != nil {
			message = redactor.RedactChunk(stream, message)
		}
		emitPersisted(stream, message)
	}
	flushRedactor := func() {
		if redactor == nil {
			return
		}
		for _, event := range redactor.Flush() {
			emitPersisted(event.Stream, event.Message)
		}
	}
	// One attempt-scoped context also governs preparation and checkout.
	supervisor = newAttemptSupervisorFrom(parent, claim.Lease, operational)
	defer supervisor.Close()
	processCtx, stopProcessPolling := context.WithCancel(supervisor.Context())
	defer stopProcessPolling()
	// Confirm authority before any secret resolution or checkout side effect.
	preflightCtx, preflightCancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer preflightCancel()
	initialRenew, err := runnerclient.New(server, token).Renew(preflightCtx, runnerclient.Authority{LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence})
	if err != nil {
		supervisor.metrics.RenewFailure()
		return fmt.Errorf("confirm lease authority: %w", err)
	}
	var validators []func(domain.RunLease) error
	if claim.Run.RunSpec.Type == domain.RunTypeComposeDeploy || claim.Run.RunSpec.Type == domain.RunTypeStorageBackupRetention || claim.Run.RunSpec.Type == domain.RunTypeStorageBackupTransfer || claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupRetention || claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupTransfer || claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackup || claim.Run.RunSpec.Type == domain.RunTypeStorageArchive || claim.Run.RunSpec.Type == domain.RunTypeDatabaseDeploy || claim.Run.RunSpec.Type == domain.RunTypeLocalBuildDeploy || claim.Run.RunSpec.Type == domain.RunTypeImageDeploy && options.database != nil && options.database.ApplicationDatabase {
		validate := exactBuildLease(claim.Lease)
		if err := validate(initialRenew); err != nil {
			return err
		}
		validators = append(validators, validate)
	}
	claim.Lease.ExpiresAt = initialRenew.ExpiresAt
	supervisor.Update(initialRenew)
	renewEvery := time.Until(claim.Lease.ExpiresAt) / 3
	if renewEvery <= 0 || renewEvery > 30*time.Second {
		renewEvery = 5 * time.Second
	}
	renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, claim.Lease, renewEvery, validators...)
	defer renewer.Stop()
	watcher := startLeaseWatcher(supervisor.Context(), server, token, claim.Lease, cancelPollInterval, supervisor.cancel, validators...)
	defer watcher.Stop()
	reporter = startAttemptReporter(supervisor.Context(), supervisor, journal, server, token, claim.Run.ID, claim.Lease)
	defer reporter.Stop()
	if claim.Run.RunSpec.Type == domain.RunTypeStorageBackupRetention {
		return executeStorageBackupRetentionClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.storageArchive)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupRetention {
		return executeDatabaseBackupRetentionClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.database)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeStorageBackupTransfer {
		if claim.Run.RunSpec.HostingMode == domain.HostingStorageBackupDownloadMode {
			return executeStorageBackupDownloadClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.storageArchive)
		}
		return executeStorageBackupTransferClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.storageArchive)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackupTransfer {
		return executeDatabaseBackupTransferClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.database)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeDatabaseBackup {
		return executeDatabaseBackupClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.database)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeStorageArchive {
		return executeStorageArchiveClaim(supervisor, journal, reporter, server, token, claim, options.imageEndpoint, options.storageArchive)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeDatabaseDeploy {
		return executeDatabaseClaim(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.database)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeProxyApply {
		proxyConfig := options.proxy
		proxyConfig.storageDatabase = options.database
		proxyConfig.storageEndpoint = options.imageEndpoint
		return executeProxyClaim(supervisor, journal, reporter, server, token, claim, proxyConfig)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeImageDeploy || claim.Run.RunSpec.Type == domain.RunTypeLocalBuildDeploy {
		if options.database != nil && options.database.ApplicationDatabase {
			return executeConnectedImageClaim(supervisor, journal, reporter, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.localBuild, options.runtimeEnvironment, options.database, options.registryPull)
		}
		return executeImageClaim(supervisor, journal, reporter, databaseCompletionDrain(supervisor, reporter, watcher, renewer), server, token, claim, options.imageEndpoint, options.localBuild, options.runtimeEnvironment, options.registryPull)
	}
	if claim.Run.RunSpec.Type == domain.RunTypeComposeDeploy {
		return resolveComposeClaim(supervisor, journal, reporter, server, token, workDir, secretRoot, imagePolicy, claim)
	}
	if claim.PrimitivePlan.Process == nil {
		if err := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, "failed", nil); err != nil {
			return err
		}
		return errors.New("claimed run did not include a process plan")
	}
	// A claimed attempt never shares repository-controlled ancestors with any
	// previous attempt. Anchor cleanup to the operator's workspace root.
	attemptDir, cleanupAttempt, err := createAttemptWorkspace(workDir)
	if err != nil {
		return err
	}
	defer cleanupAttempt()
	workDir = attemptDir
	preparedSecrets, err := runner.PrepareSecrets(supervisor.Context(), claim.PrimitivePlan.Secrets, secretRoot, func(_ context.Context, binding domain.SecretBinding) error {
		accessID := attemptMutationKey("secret_access", claim.Lease.ID, claim.Lease.Attempt, strings.TrimSpace(binding.Name))
		var grant domain.SecretAccessGrant
		authorizeErr := retryAttemptRequest(supervisor, func(requestCtx context.Context) error {
			var accessErr error
			grant, accessErr = runnerclient.New(server, token).AuthorizeSecret(requestCtx, runnerclient.SecretAccessRequest{
				AccessID: accessID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt,
				Fence: claim.Lease.Fence, Binding: strings.TrimSpace(binding.Name), Provider: strings.ToLower(strings.TrimSpace(binding.Provider)),
				Version: strings.TrimSpace(binding.Version),
			})
			return accessErr
		})
		if authorizeErr != nil {
			supervisor.cancel()
			return authorizeErr
		}
		if grant.AccessID != accessID || grant.RunID != claim.Run.ID || grant.LeaseID != claim.Lease.ID || grant.Attempt != claim.Lease.Attempt || grant.Binding != strings.TrimSpace(binding.Name) || grant.Provider != strings.ToLower(strings.TrimSpace(binding.Provider)) || grant.Version != strings.TrimSpace(binding.Version) {
			supervisor.cancel()
			return errors.New("secret access acknowledgement did not match request")
		}
		return nil
	})
	if reporterErr := reporter.Err(); reporterErr != nil {
		return reporterErr
	}
	if err != nil {
		if supervisor.Context().Err() != nil {
			return fmt.Errorf("%w: secret authorization: %v", errLeaseAuthorityLost, err)
		}
		emit("system", "Secret preparation failed: "+err.Error())
		completeErr := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, "failed", nil)
		if completeErr != nil {
			return completeErr
		}
		return err
	}
	redactor = preparedSecrets.Redactor
	if preparedSecrets.Count > 0 {
		emit("system", fmt.Sprintf("Prepared %d runner secret binding(s)", preparedSecrets.Count))
	}
	if len(preparedSecrets.Environment) > 0 {
		if claim.PrimitivePlan.Process.Environment == nil {
			claim.PrimitivePlan.Process.Environment = map[string]string{}
		}
		for key, value := range preparedSecrets.Environment {
			claim.PrimitivePlan.Process.Environment[key] = value
		}
	}
	artifactRoot := strings.TrimSpace(claim.PrimitivePlan.Process.WorkingDir)
	if artifactRoot == "" {
		artifactRoot = workDir
	}
	if claim.PrimitivePlan.Checkout != nil {
		if supervisor.Context().Err() != nil {
			return fmt.Errorf("%w: before checkout", errLeaseAuthorityLost)
		}
		checkoutPath, err := runner.ExecuteCheckout(processCtx, *claim.PrimitivePlan.Checkout, workDir, func(event runner.ProcessEvent) {
			emit(event.Stream, event.Message)
		})
		if err != nil {
			flushRedactor()
			emit("system", "Checkout failed: "+err.Error())
			completeErr := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, "failed", nil)
			if completeErr != nil {
				return completeErr
			}
			return err
		}
		if claim.PrimitivePlan.Process.WorkingDir == "" {
			claim.PrimitivePlan.Process.WorkingDir = checkoutPath
		}
		artifactRoot = claim.PrimitivePlan.Process.WorkingDir
	}
	if claim.PrimitivePlan.Process.WorkingDir == "" {
		claim.PrimitivePlan.Process.WorkingDir = workDir
	}
	if supervisor.Context().Err() != nil {
		return fmt.Errorf("%w: before process spawn", errLeaseAuthorityLost)
	}
	emit("system", "Runner starting process")
	result, err := runner.ExecuteProcess(processCtx, *claim.PrimitivePlan.Process, func(event runner.ProcessEvent) {
		emit(event.Stream, event.Message)
	})
	flushRedactor()
	stopProcessPolling()
	if supervisor.Context().Err() != nil {
		return fmt.Errorf("%w: %v", errLeaseAuthorityLost, supervisor.Context().Err())
	}
	if err != nil {
		emit("system", "Process execution failed: "+err.Error())
		completeErr := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, "failed", nil)
		if completeErr != nil {
			return completeErr
		}
		return err
	}

	status := "succeeded"
	switch {
	case result.Canceled:
		status = "canceled"
	case result.TimedOut || result.ExitCode != 0:
		status = "failed"
	}
	emit("system", fmt.Sprintf("Process exited with code %d", result.ExitCode))
	artifactResults, err := runner.CaptureArtifacts(artifactRoot, claim.PrimitivePlan.Artifacts, func(event runner.ProcessEvent) {
		emit(event.Stream, event.Message)
	})
	flushRedactor()
	for _, artifact := range artifactResults {
		if supervisor.Context().Err() != nil || reporter.Err() != nil {
			return fmt.Errorf("%w: before artifact reporting", errLeaseAuthorityLost)
		}
		kind := "file"
		if artifact.IsDir {
			kind = "directory"
		}
		reqCtx, cancel, requestErr := supervisor.RequestContext()
		if requestErr != nil {
			return fmt.Errorf("%w: before artifact reporting: %v", errLeaseAuthorityLost, requestErr)
		}
		err := appendArtifactAPIContext(reqCtx, server, token, claim.Run.ID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, artifact.Name, artifact.Path, artifact.Found, artifact.Required, artifact.Size, kind)
		cancel()
		if err != nil {
			supervisor.cancel()
			return fmt.Errorf("%w: append artifact: %v", errLeaseAuthorityLost, err)
		}
	}
	if err != nil {
		status = "failed"
		emit("system", "Artifact capture failed: "+err.Error())
	}
	var completed domain.RunLease
	if err := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, status, &completed); err != nil {
		return err
	}
	return printRunnerAuthorityEvent("completed_run", claim.Run.ID, completed)
}

func completeRunnerLeaseAPIContext(ctx context.Context, server string, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	result, err := runnerclient.New(server, token).Complete(ctx, runnerclient.CompleteRequest{LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, Status: status, CompletionKey: attemptMutationKey("complete", lease.ID, lease.Attempt, status)})
	if completed != nil {
		*completed = result
	}
	return err
}
func supervisedComplete(supervisor *attemptSupervisor, server, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer cancel()
	return completeRunnerLeaseAPIContext(ctx, server, token, lease, status, completed)
}

// databaseCompletionDrain is called only after the engine has returned and the
// exact result is durable. Keep authority workers alive through mutation and
// event drain, then join them before our own completion makes the lease terminal.
// The supervisor's original deadline and every server-side fence remain active.
func databaseCompletionDrain(supervisor *attemptSupervisor, reporter *attemptReporter, watcher *leaseWatcher, renewer *leaseRenewer) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := reporter.WaitEmpty(ctx); err != nil {
			return err
		}
		reporter.Stop()
		watcher.Stop()
		renewer.Stop()
		if err := ctx.Err(); err != nil {
			return err
		}
		return supervisor.Context().Err()
	}
}

func completeAttempt(supervisor *attemptSupervisor, watcher *leaseWatcher, renewer *leaseRenewer, server, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	watcher.Stop()
	renewer.Stop()
	err := supervisedComplete(supervisor, server, token, lease, status, completed)
	if err != nil {
		supervisor.cancel()
		return fmt.Errorf("%w: completion: %v", errLeaseAuthorityLost, err)
	}
	return nil
}

func completeReportedAttempt(supervisor *attemptSupervisor, watcher *leaseWatcher, renewer *leaseRenewer, reporter *attemptReporter, journal *runner.AttemptJournal, server, token, runID string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	// Persist terminal intent before waiting for earlier events to drain. A crash
	// during an outage can then replay events first and this exact completion,
	// without ever inventing a second terminal mutation.
	if _, err := ensureJournaledCompletion(supervisor, journal, runID, lease, status); err != nil {
		supervisor.cancel()
		return fmt.Errorf("%w: journal completion: %v", errLeaseAuthorityLost, err)
	}
	if err := reporter.WaitEmpty(supervisor.Context()); err != nil {
		supervisor.cancel()
		return fmt.Errorf("%w: flush before completion: %v", errLeaseAuthorityLost, err)
	}
	reporter.Stop()
	watcher.Stop()
	renewer.Stop()
	if supervisor.Context().Err() != nil {
		return fmt.Errorf("%w: %v", errLeaseAuthorityLost, supervisor.Context().Err())
	}
	if err := journaledCompletion(supervisor, journal, server, token, runID, lease, status, completed); err != nil {
		supervisor.cancel()
		return fmt.Errorf("%w: completion replay: %v", errLeaseAuthorityLost, err)
	}
	return nil
}

func journaledCompletion(supervisor *attemptSupervisor, journal *runner.AttemptJournal, server, token, runID string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	completion, err := ensureJournaledCompletion(supervisor, journal, runID, lease, status)
	if err != nil {
		return err
	}
	var discard domain.RunLease
	if completed == nil {
		completed = &discard
	}
	err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		result, requestErr := runnerclient.New(server, token).Complete(ctx, runnerclient.CompleteRequest{
			LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, CompletionKey: completion.ID, Status: status,
		})
		*completed = result
		return requestErr
	})
	if err != nil {
		return err
	}
	return journal.AckCompletion(completion.ID)
}

func ensureJournaledCompletion(supervisor *attemptSupervisor, journal *runner.AttemptJournal, runID string, lease domain.RunLease, status string) (runner.JournalCompletion, error) {
	var completion runner.JournalCompletion
	for _, pending := range journal.Snapshot().Completions {
		if pending.Attempt.RunID == runID && pending.Attempt.LeaseID == lease.ID && pending.Attempt.Attempt == lease.Attempt && pending.Attempt.Fence == lease.Fence {
			if pending.Status != status {
				return runner.JournalCompletion{}, runner.ErrJournalConflict
			}
			completion = pending
			break
		}
	}
	if completion.ID == "" {
		id, err := runner.NewJournalID("completion")
		if err != nil {
			return runner.JournalCompletion{}, err
		}
		completion = runner.JournalCompletion{ID: id, Attempt: journalAttemptIdentity(runID, lease, supervisor), Status: status, CreatedAt: time.Now().UTC()}
		if _, err := journal.AppendCompletion(completion); err != nil {
			return runner.JournalCompletion{}, err
		}
	}
	return completion, nil
}

func renewLeaseWhileRunning(worker context.Context, supervisor *attemptSupervisor, server, token string, lease domain.RunLease, interval time.Duration, failClosed func(), validators ...func(domain.RunLease) error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			renewalDelay := boundedRenewalDelay(interval)
			for waitWorkerInterval(worker, renewalDelay) {
				var renewed domain.RunLease
				err := retryWorkerRequest(worker, supervisor, func(ctx context.Context) error {
					var renewErr error
					renewed, renewErr = runnerclient.New(server, token).Renew(ctx, runnerclient.Authority{LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence})
					if renewErr != nil && worker.Err() == nil {
						supervisor.metrics.RenewFailure()
					}
					return renewErr
				})
				if err != nil {
					if worker.Err() == nil {
						failClosed()
					}
					return
				}
				for _, validate := range validators {
					if validate(renewed) != nil {
						failClosed()
						return
					}
				}
				supervisor.Update(renewed)
				renewalDelay = boundedRenewalDelay(time.Until(renewed.ExpiresAt) / 3)
			}
		}()
		go func() {
			defer workers.Done()
			heartbeatDelay := boundedHeartbeatDelay(interval)
			for waitWorkerInterval(worker, heartbeatDelay) {
				err := retryWorkerRequest(worker, supervisor, func(ctx context.Context) error {
					_, heartbeatErr := runnerclient.New(server, token).Heartbeat(ctx)
					return heartbeatErr
				})
				if err != nil {
					if worker.Err() == nil {
						failClosed()
					}
					return
				}
			}
		}()
		workers.Wait()
	}()
	return done
}

func boundedRenewalDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

func boundedHeartbeatDelay(interval time.Duration) time.Duration {
	interval = boundedRenewalDelay(interval)
	if interval > 10*time.Second {
		return 10 * time.Second
	}
	return interval
}

func waitWorkerInterval(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryWorkerRequest(worker context.Context, supervisor *attemptSupervisor, request func(context.Context) error) error {
	backoff := 100 * time.Millisecond
	for {
		requestCtx, cancel, err := supervisor.RequestContextFrom(worker)
		if err != nil {
			if worker.Err() != nil {
				return worker.Err()
			}
			return err
		}
		err = request(requestCtx)
		cancel()
		if err == nil {
			return nil
		}
		if worker.Err() != nil {
			return worker.Err()
		}
		if classifyRunnerFailure(err) != runnerFailureTransient {
			return err
		}
		if !waitRunnerRetry(worker, supervisor, backoff) {
			if worker.Err() != nil {
				return worker.Err()
			}
			return fmt.Errorf("%w: retry deadline: %v", errLeaseAuthorityLost, err)
		}
		supervisor.metrics.Retry()
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

func startLeaseRenewer(parent context.Context, supervisor *attemptSupervisor, server, token string, lease domain.RunLease, interval time.Duration, validators ...func(domain.RunLease) error) *leaseRenewer {
	ctx, cancel := context.WithCancel(parent)
	return &leaseRenewer{cancel: cancel, done: renewLeaseWhileRunning(ctx, supervisor, server, token, lease, interval, supervisor.cancel, validators...)}
}

func appendArtifactAPIContext(ctx context.Context, server string, token string, runID string, leaseID string, attempt int, fence string, name string, path string, found bool, required bool, size int64, kind string) error {
	_, err := runnerclient.New(server, token).AppendArtifact(ctx, runnerclient.ArtifactRequest{RunID: runID, LeaseID: leaseID, Attempt: attempt, Fence: fence, Name: name, Path: path, Found: found, Required: required, Size: size, Kind: kind})
	return err
}

func watchLeaseCancellation(ctx context.Context, server string, token string, lease domain.RunLease, interval time.Duration, emit func(string), cancel func(), validators ...func(domain.RunLease) error) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				observed, err := runnerclient.New(server, token).Lease(ctx, runnerclient.Authority{LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence})
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if classifyRunnerFailure(err) == runnerFailureTransient {
						continue
					}
					cancel()
					return
				}
				for _, validate := range validators {
					if validate(observed) != nil {
						cancel()
						return
					}
				}
				if observed.Status != "active" {
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func startLeaseWatcher(parent context.Context, server, token string, lease domain.RunLease, interval time.Duration, authorityCancel func(), validators ...func(domain.RunLease) error) *leaseWatcher {
	ctx, cancel := context.WithCancel(parent)
	return &leaseWatcher{cancel: cancel, done: watchLeaseCancellation(ctx, server, token, lease, interval, func(string) {}, authorityCancel, validators...)}
}

func attemptMutationKey(kind, leaseID string, attempt int, discriminator string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + leaseID + "\x00" + strconv.Itoa(attempt) + "\x00" + discriminator))
	return kind + "_" + fmt.Sprintf("%x", sum[:16])
}

// A normal failed build is settled work, while every uncertain result remains
// fatal. Once mode keeps its command failure and cancellation always stops.
func settledBuildMayContinue(ctx context.Context, once bool, err error) bool {
	return !once && ctx.Err() == nil && errors.Is(err, errSettledBuildFailure)
}

// Only an acknowledged pre-apply failure may leave the worker available for another claim.
func settledImagePreparationMayContinue(ctx context.Context, once bool, err error) bool {
	return !once && ctx.Err() == nil && errors.Is(err, errSettledImagePreparationFailure)
}

// Capture evidence must pass its dedicated mixed-chain validator before another
// family can replay. An empty capture slice does not suppress other validation.
func reconcileConnectedBeforeCapture(ctx context.Context, snapshot runner.JournalSnapshot, replay func(context.Context) error) error {
	if len(snapshot.DatabaseNetworkCapture) != 0 {
		return nil
	}
	return replay(ctx)
}
