package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
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
	ctx, cancel := context.WithCancel(context.Background())
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

type runnerRegisterRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
}

type runnerRegisterResponse struct {
	Runner domain.Runner `json:"runner"`
	Token  string        `json:"token"`
}

type runnerCompleteRequest struct {
	LeaseID       string `json:"lease_id"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	Fence         string `json:"fence"`
	CompletionKey string `json:"completion_key"`
}

type runnerLogRequest struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	EventKey string `json:"event_key"`
}

type runnerEventRequest struct {
	EventKey string `json:"event_key"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
}

type runnerEventBatchRequest struct {
	RunID   string               `json:"run_id"`
	LeaseID string               `json:"lease_id"`
	Attempt int                  `json:"attempt"`
	Fence   string               `json:"fence"`
	Events  []runnerEventRequest `json:"events"`
}

type runnerEventBatchAck struct {
	Events []domain.RunLog `json:"events"`
}

type runnerArtifactRequest struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
}

func runRunner(args []string) error {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	server := fs.String("server", defaultAPIAddr(), "NeroCD server URL")
	token := fs.String("token", os.Getenv("NEROCD_TOKEN"), "privileged bearer token used only to register this runner")
	credentialFile := fs.String("credential-file", os.Getenv("NEROCD_RUNNER_CREDENTIAL_FILE"), "mode-0600 file containing an already-issued dedicated runner credential; skips registration")
	enrollmentFile := fs.String("enrollment-file", "", "mode-0600 file containing a one-time enrollment token; requires --credential-file as the durable credential destination")
	id := fs.String("id", "runner_local", "runner id")
	name := fs.String("name", "Local Runner", "runner name")
	tags := fs.String("tags", "local", "comma-separated runner tags")
	capabilities := fs.String("capabilities", "shell", "comma-separated runner capabilities")
	execute := fs.Bool("execute", true, "execute the claimed process plan")
	once := fs.Bool("once", false, "claim at most one run and then exit")
	pollInterval := fs.Duration("poll-interval", 5*time.Second, "delay between claim attempts")
	cancelPollInterval := fs.Duration("cancel-poll-interval", time.Second, "delay between lease cancellation checks while executing")
	workDir := fs.String("work-dir", os.TempDir(), "runner workspace root for checkouts")
	journalDir := fs.String("journal-dir", os.Getenv("NEROCD_RUNNER_JOURNAL_DIR"), "owner-only durable runner event/completion journal directory")
	secretRoot := fs.String("secret-root", os.Getenv("NEROCD_RUNNER_SECRET_ROOT"), "owner-only mode-0700 root for logical runner_file secrets")
	completeStatus := fs.String("complete-status", "", "optional status to immediately complete a claimed lease: succeeded, failed, or canceled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	registrationToken := strings.TrimSpace(*token)
	credentialPath := strings.TrimSpace(*credentialFile)
	enrollmentPath := strings.TrimSpace(*enrollmentFile)
	if registrationToken != "" && (credentialPath != "" || enrollmentPath != "") {
		return errors.New("runner --token registration mode is mutually exclusive with credential and enrollment files")
	}
	if enrollmentPath != "" && credentialPath == "" {
		return errors.New("runner --enrollment-file requires --credential-file")
	}
	var runnerToken string
	effectiveRunnerID := strings.TrimSpace(*id)
	if enrollmentPath != "" {
		enrollmentToken, credential, requestID, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
		if err != nil {
			return err
		}
		credentialSum := sha256.Sum256([]byte(credential))
		consume := app.RunnerEnrollmentConsumeInput{RequestID: requestID, CredentialHash: hex.EncodeToString(credentialSum[:])}
		var consumed app.ConsumedRunnerEnrollment
		var consumeErr error
		backoff := 100 * time.Millisecond
		for attempt := 0; attempt < 5; attempt++ {
			consumeErr = postAPIInto(*server+"/api/v1/runner-enrollments/consume", consume, enrollmentToken, &consumed)
			if consumeErr == nil || classifyRunnerFailure(consumeErr) != runnerFailureTransient {
				break
			}
			time.Sleep(backoff)
			backoff *= 2
		}
		if consumeErr != nil {
			return fmt.Errorf("consume runner enrollment: %w", consumeErr)
		}
		if strings.TrimSpace(consumed.Runner.ID) == "" {
			return errors.New("runner enrollment response did not include identity")
		}
		if err := removeRunnerEnrollmentFile(enrollmentPath); err != nil {
			return fmt.Errorf("remove consumed enrollment file: %w", err)
		}
		runnerToken = credential
		effectiveRunnerID = consumed.Runner.ID
	} else if credentialPath != "" {
		credential, err := readRunnerCredentialFile(credentialPath)
		if err != nil {
			return fmt.Errorf("runner credential file: %w", err)
		}
		runnerToken = credential
	} else {
		if registrationToken == "" {
			return errors.New("runner requires registration --token/NEROCD_TOKEN or --credential-file/NEROCD_RUNNER_CREDENTIAL_FILE")
		}
		registerBody := runnerRegisterRequest{ID: *id, Name: *name, Tags: splitCSV(*tags), Capabilities: splitCSV(*capabilities)}
		var registered runnerRegisterResponse
		if err := postAPIInto(*server+"/api/v1/runners/register", registerBody, registrationToken, &registered); err != nil {
			return err
		}
		runnerToken = strings.TrimSpace(registered.Token)
		if runnerToken == "" {
			return errors.New("runner registration did not return a runner token")
		}
	}
	resolvedJournalDir := strings.TrimSpace(*journalDir)
	if resolvedJournalDir == "" {
		resolvedJournalDir = filepath.Join(*workDir, ".nerocd-journal-"+attemptMutationKey("runner", effectiveRunnerID, 1, "journal"))
	}
	journal, err := runner.OpenAttemptJournal(resolvedJournalDir)
	if err != nil {
		return fmt.Errorf("open runner journal: %w", err)
	}
	defer journal.Close()
	operational := &runnerOperationalCounters{}
	if err := reconcileRunnerJournalWithCounters(*server, runnerToken, journal, operational); err != nil {
		return fmt.Errorf("reconcile runner journal: %w", err)
	}
	for {
		var heartbeat domain.Runner
		if err := postAPIInto(*server+"/api/v1/runners/heartbeat", struct{}{}, runnerToken, &heartbeat); err != nil {
			return err
		}
		// This is aggregate-only, authenticated telemetry. It deliberately does
		// not include workspace paths, journal entries, lease IDs, or failures.
		retryCount, renewFailures := operational.Snapshot()
		if err := postAPINoResponse(*server+"/api/v1/runners/telemetry", app.RunnerOperationalTelemetry{JournalDepth: journal.Depth(), RetryCount: retryCount, RenewFailures: renewFailures}, runnerToken); err != nil {
			return fmt.Errorf("report runner telemetry: %w", err)
		}
		var claim domain.ClaimedRun
		if err := postAPIInto(*server+"/api/v1/runners/claim", struct{}{}, runnerToken, &claim); err != nil {
			if runnerHTTPStatus(err, http.StatusNotFound) {
				if *once {
					fmt.Println("runner registered; no matching queued run")
					return nil
				}
				time.Sleep(*pollInterval)
				continue
			}
			return err
		}
		if err := printRunnerAuthorityEvent("claimed_run", claim.Run.ID, claim.Lease); err != nil {
			return err
		}
		if *execute {
			if err := executeClaimWithJournalAndSecretRootAndCounters(*server, runnerToken, claim, *workDir, *cancelPollInterval, journal, *secretRoot, operational); err != nil {
				return err
			}
		} else if *completeStatus != "" {
			if strings.TrimSpace(claim.Lease.ID) == "" {
				return errors.New("claim response lease did not include id")
			}
			supervisor := newAttemptSupervisor(claim.Lease)
			var completed domain.RunLease
			if err := journaledCompletion(supervisor, journal, *server, runnerToken, claim.Run.ID, claim.Lease, *completeStatus, &completed); err != nil {
				supervisor.Close()
				return err
			}
			supervisor.Close()
			if err := printRunnerAuthorityEvent("completed_run", claim.Run.ID, completed); err != nil {
				return err
			}
		}
		if *once {
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
	defer journal.Close()
	return executeClaimWithJournalAndSecretRoot(server, token, claim, workDir, cancelPollInterval, journal, os.Getenv("NEROCD_RUNNER_SECRET_ROOT"))
}

func executeClaimWithJournal(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal) error {
	return executeClaimWithJournalAndSecretRoot(server, token, claim, workDir, cancelPollInterval, journal, os.Getenv("NEROCD_RUNNER_SECRET_ROOT"))
}

func executeClaimWithJournalAndSecretRoot(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string) error {
	return executeClaimWithJournalAndSecretRootAndCounters(server, token, claim, workDir, cancelPollInterval, journal, secretRoot, &runnerOperationalCounters{})
}

func executeClaimWithJournalAndSecretRootAndCounters(server string, token string, claim domain.ClaimedRun, workDir string, cancelPollInterval time.Duration, journal *runner.AttemptJournal, secretRoot string, operational *runnerOperationalCounters) error {
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
	supervisor = newAttemptSupervisor(claim.Lease, operational)
	defer supervisor.Close()
	processCtx, stopProcessPolling := context.WithCancel(supervisor.Context())
	defer stopProcessPolling()
	// Confirm authority before any secret resolution or checkout side effect.
	var initialRenew domain.RunLease
	preflightCtx, preflightCancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer preflightCancel()
	if err := postAPIIntoContext(preflightCtx, server+"/api/v1/runners/renew", struct {
		LeaseID string `json:"lease_id"`
		Attempt int    `json:"attempt"`
		Fence   string `json:"fence"`
	}{claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence}, token, &initialRenew); err != nil {
		supervisor.metrics.RenewFailure()
		return fmt.Errorf("confirm lease authority: %w", err)
	}
	claim.Lease.ExpiresAt = initialRenew.ExpiresAt
	supervisor.Update(initialRenew)
	renewEvery := claim.Lease.ExpiresAt.Sub(time.Now()) / 3
	if renewEvery <= 0 || renewEvery > 30*time.Second {
		renewEvery = 5 * time.Second
	}
	renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, claim.Lease, renewEvery)
	defer renewer.Stop()
	watcher := startLeaseWatcher(supervisor.Context(), server, token, claim.Lease, cancelPollInterval, supervisor.cancel)
	defer watcher.Stop()
	reporter = startAttemptReporter(supervisor.Context(), supervisor, journal, server, token, claim.Run.ID, claim.Lease)
	defer reporter.Stop()
	if claim.PrimitivePlan.Process == nil {
		if claim.Run.RunSpec.Type == domain.RunTypeComposeDeploy {
			// Resolution is read-only: it cannot start containers or settle the
			// deployment successfully. A later fenced adapter owns application.
			return resolveComposeClaim(supervisor, journal, reporter, server, token, workDir, secretRoot, claim)
		}
		if err := completeReportedAttempt(supervisor, watcher, renewer, reporter, journal, server, token, claim.Run.ID, claim.Lease, "failed", nil); err != nil {
			return err
		}
		return errors.New("claimed run did not include a process plan")
	}
	preparedSecrets, err := runner.PrepareSecrets(supervisor.Context(), claim.PrimitivePlan.Secrets, secretRoot, func(_ context.Context, binding domain.SecretBinding) error {
		accessID := attemptMutationKey("secret_access", claim.Lease.ID, claim.Lease.Attempt, strings.TrimSpace(binding.Name))
		var grant domain.SecretAccessGrant
		authorizeErr := retryAttemptRequest(supervisor, func(requestCtx context.Context) error {
			return postAPIIntoContext(requestCtx, server+"/api/v1/runners/secrets/access", app.SecretAccessInput{
				AccessID: accessID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt,
				Fence: claim.Lease.Fence, Binding: strings.TrimSpace(binding.Name), Provider: strings.ToLower(strings.TrimSpace(binding.Provider)),
				Version: strings.TrimSpace(binding.Version),
			}, token, &grant)
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
	if supervisor.Context().Err() != nil {
		return fmt.Errorf("%w: before process spawn", errLeaseAuthorityLost)
	}
	emit("system", "Runner starting process: "+strings.Join(claim.PrimitivePlan.Process.Command, " "))
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

func completeRunnerLeaseAPI(server string, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	return completeRunnerLeaseAPIContext(context.Background(), server, token, lease, status, completed)
}
func completeRunnerLeaseAPIContext(ctx context.Context, server string, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	var discard domain.RunLease
	if completed == nil {
		completed = &discard
	}
	return postAPIIntoContext(ctx, server+"/api/v1/runners/complete", runnerCompleteRequest{LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, Status: status, CompletionKey: attemptMutationKey("complete", lease.ID, lease.Attempt, status)}, token, completed)
}
func supervisedComplete(supervisor *attemptSupervisor, server, token string, lease domain.RunLease, status string, completed *domain.RunLease) error {
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer cancel()
	return completeRunnerLeaseAPIContext(ctx, server, token, lease, status, completed)
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
		return postAPIIntoContext(ctx, server+"/api/v1/runners/complete", runnerCompleteRequest{
			LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, CompletionKey: completion.ID, Status: status,
		}, token, completed)
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

func renewLeaseWhileRunning(worker context.Context, supervisor *attemptSupervisor, server, token string, lease domain.RunLease, interval time.Duration, failClosed func()) <-chan struct{} {
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
					err := postAPIIntoContext(ctx, server+"/api/v1/runners/renew", struct {
						LeaseID string `json:"lease_id"`
						Attempt int    `json:"attempt"`
						Fence   string `json:"fence"`
					}{lease.ID, lease.Attempt, lease.Fence}, token, &renewed)
					if err != nil && worker.Err() == nil {
						supervisor.metrics.RenewFailure()
					}
					return err
				})
				if err != nil {
					if worker.Err() == nil {
						failClosed()
					}
					return
				}
				supervisor.Update(renewed)
				renewalDelay = boundedRenewalDelay(renewed.ExpiresAt.Sub(time.Now()) / 3)
			}
		}()
		go func() {
			defer workers.Done()
			heartbeatDelay := boundedHeartbeatDelay(interval)
			for waitWorkerInterval(worker, heartbeatDelay) {
				var heartbeat domain.Runner
				err := retryWorkerRequest(worker, supervisor, func(ctx context.Context) error {
					return postAPIIntoContext(ctx, server+"/api/v1/runners/heartbeat", struct{}{}, token, &heartbeat)
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

func startLeaseRenewer(parent context.Context, supervisor *attemptSupervisor, server, token string, lease domain.RunLease, interval time.Duration) *leaseRenewer {
	ctx, cancel := context.WithCancel(parent)
	return &leaseRenewer{cancel: cancel, done: renewLeaseWhileRunning(ctx, supervisor, server, token, lease, interval, supervisor.cancel)}
}

func appendArtifactAPI(server string, token string, runID string, leaseID string, attempt int, fence string, name string, path string, found bool, required bool, size int64, kind string) error {
	return appendArtifactAPIContext(context.Background(), server, token, runID, leaseID, attempt, fence, name, path, found, required, size, kind)
}
func appendArtifactAPIContext(ctx context.Context, server string, token string, runID string, leaseID string, attempt int, fence string, name string, path string, found bool, required bool, size int64, kind string) error {
	var artifact domain.ArtifactRecord
	return postAPIIntoContext(ctx, server+"/api/v1/runners/artifacts", runnerArtifactRequest{RunID: runID, LeaseID: leaseID, Attempt: attempt, Fence: fence, Name: name, Path: path, Found: found, Required: required, Size: size, Kind: kind}, token, &artifact)
}

func watchLeaseCancellation(ctx context.Context, server string, token string, lease domain.RunLease, interval time.Duration, emit func(string), cancel func()) <-chan struct{} {
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
				var observed domain.RunLease
				err := getAPIIntoContext(ctx, server+"/api/v1/runners/lease?lease_id="+url.QueryEscape(lease.ID)+"&attempt="+strconv.Itoa(lease.Attempt)+"&fence="+url.QueryEscape(lease.Fence), token, &observed)
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
				if observed.Status != "active" {
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func startLeaseWatcher(parent context.Context, server, token string, lease domain.RunLease, interval time.Duration, authorityCancel func()) *leaseWatcher {
	ctx, cancel := context.WithCancel(parent)
	return &leaseWatcher{cancel: cancel, done: watchLeaseCancellation(ctx, server, token, lease, interval, func(string) {}, authorityCancel)}
}

func appendRunLogAPI(server string, token string, runID string, leaseID string, attempt int, fence string, sequence int, stream string, message string) error {
	return appendRunLogAPIContext(context.Background(), server, token, runID, leaseID, attempt, fence, sequence, stream, message)
}
func appendRunLogAPIContext(ctx context.Context, server string, token string, runID string, leaseID string, attempt int, fence string, sequence int, stream string, message string) error {
	var log domain.RunLog
	return postAPIIntoContext(ctx, server+"/api/v1/runners/logs", runnerLogRequest{RunID: runID, LeaseID: leaseID, Attempt: attempt, Fence: fence, Sequence: sequence, Stream: stream, Message: message, EventKey: attemptMutationKey("event", leaseID, attempt, strconv.Itoa(sequence))}, token, &log)
}

func attemptMutationKey(kind, leaseID string, attempt int, discriminator string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + leaseID + "\x00" + strconv.Itoa(attempt) + "\x00" + discriminator))
	return kind + "_" + fmt.Sprintf("%x", sum[:16])
}
