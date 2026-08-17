package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/store"
	"nerocd/web"
)

const version = "0.1.0-dev"

var runnerHTTPClient = &http.Client{Timeout: 5 * time.Second}
var errLeaseAuthorityLost = errors.New("lease authority lost")

// attemptSupervisor owns the cancellation boundary for a single fenced attempt.
// Its watchdog is independent of request goroutines, so a blocked request cannot
// let a child continue after the locally known authority deadline.
type attemptSupervisor struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
	expiry time.Time
	margin time.Duration
	done   chan struct{}
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

func newAttemptSupervisor(lease domain.RunLease) *attemptSupervisor {
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
	s := &attemptSupervisor{ctx: ctx, cancel: cancel, expiry: lease.ExpiresAt, margin: margin, done: make(chan struct{})}
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

type runtimeConfig struct {
	addr           string
	databaseURL    string
	leaseTTL       time.Duration
	reaperInterval time.Duration
}

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

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "server":
		return runServer(args[1:])
	case "runner":
		return runRunner(args[1:])
	case "health":
		return callAPI(args[1:], "/api/v1/health")
	case "projects":
		return callAPI(args[1:], "/api/v1/projects")
	case "runs":
		return callAPI(args[1:], "/api/v1/runs")
	case "templates":
		return callAPI(args[1:], "/api/v1/templates")
	case "run-logs":
		return callAPI(args[1:], "/api/v1/run-logs")
	case "session":
		return createSession(args[1:])
	case "migrate":
		return migrateDatabase(args[1:])
	case "smoke":
		return smoke(args[1:])
	case "contract":
		return validateContract(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() error {
	fmt.Println(`NeroCD

Usage:
  nerocd server [--addr :8080]
  nerocd runner [--server http://127.0.0.1:8080] (--token ADMIN_TOKEN | --credential-file /run/secrets/runner-token)
  nerocd health [--addr http://127.0.0.1:8080]
  nerocd projects [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd templates [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd runs [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd run-logs [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd session --email admin@example.local --password admin [--addr http://127.0.0.1:8080]
  nerocd migrate [--database-url postgres://...]
  nerocd smoke [--addr http://127.0.0.1:8080]
  nerocd contract [--openapi openapi.yaml]
  nerocd version`)
	return nil
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadRuntimeConfig(*addr)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	service, closeStore, err := newService(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	reaperCtx, stopReaper := context.WithCancel(context.Background())
	defer stopReaper()
	go func() {
		ticker := time.NewTicker(cfg.reaperInterval)
		defer ticker.Stop()
		for {
			select {
			case <-reaperCtx.Done():
				return
			case <-ticker.C:
				if err := service.ReapExpiredLeases(reaperCtx); err != nil {
					logger.Error("lease reaper", "error", err)
				}
			}
		}
	}()
	server := api.NewServer(service, logger, web.Static())

	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if cfg.databaseURL == "" {
		logger.Warn("server using in-memory store; set NEROCD_DATABASE_URL for durable runtime state")
	}
	logger.Info("server listening", "addr", cfg.addr)
	return httpServer.ListenAndServe()
}

func loadRuntimeConfig(addr string) (runtimeConfig, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return runtimeConfig{}, errors.New("listen address is required")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return runtimeConfig{}, fmt.Errorf("listen address must include host and port or :port: %w", err)
	}
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL"))
	if databaseURL != "" {
		if err := validateDatabaseURL(databaseURL); err != nil {
			return runtimeConfig{}, fmt.Errorf("NEROCD_DATABASE_URL: %w", err)
		}
	}
	if os.Getenv("NEROCD_REQUIRE_DATABASE") == "true" && databaseURL == "" {
		return runtimeConfig{}, errors.New("NEROCD_REQUIRE_DATABASE=true requires NEROCD_DATABASE_URL")
	}
	ttl := 2 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("NEROCD_LEASE_TTL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 5*time.Second || parsed > 10*time.Minute {
			return runtimeConfig{}, errors.New("NEROCD_LEASE_TTL must be between 5s and 10m")
		}
		ttl = parsed
	}
	reaper := 5 * time.Second
	if raw := strings.TrimSpace(os.Getenv("NEROCD_REAPER_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Second || parsed > time.Minute {
			return runtimeConfig{}, errors.New("NEROCD_REAPER_INTERVAL must be between 1s and 1m")
		}
		reaper = parsed
	}
	return runtimeConfig{addr: addr, databaseURL: databaseURL, leaseTTL: ttl, reaperInterval: reaper}, nil
}

func newService(ctx context.Context, cfg runtimeConfig) (*app.Service, func(), error) {
	if cfg.databaseURL != "" {
		pg, err := store.OpenPostgres(ctx, cfg.databaseURL)
		if err != nil {
			return nil, nil, err
		}
		service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)
		if err := service.SetLeaseTTL(cfg.leaseTTL); err != nil {
			_ = pg.Close()
			return nil, nil, err
		}
		return service, func() { _ = pg.Close() }, nil
	}

	mem := store.NewMemoryStore()
	service := app.NewService(auth.ContextProvider{}, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem)
	if err := service.SetLeaseTTL(cfg.leaseTTL); err != nil {
		return nil, nil, err
	}
	return service, func() {}, nil
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
	if err := reconcileRunnerJournal(*server, runnerToken, journal); err != nil {
		return fmt.Errorf("reconcile runner journal: %w", err)
	}
	for {
		var heartbeat domain.Runner
		if err := postAPIInto(*server+"/api/v1/runners/heartbeat", struct{}{}, runnerToken, &heartbeat); err != nil {
			return err
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
			if err := executeClaimWithJournalAndSecretRoot(*server, runnerToken, claim, *workDir, *cancelPollInterval, journal, *secretRoot); err != nil {
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
	supervisor = newAttemptSupervisor(claim.Lease)
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
					return postAPIIntoContext(ctx, server+"/api/v1/runners/renew", struct {
						LeaseID string `json:"lease_id"`
						Attempt int    `json:"attempt"`
						Fence   string `json:"fence"`
					}{lease.ID, lease.Attempt, lease.Fence}, token, &renewed)
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

func callAPI(args []string, path string) error {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	token := fs.String("token", os.Getenv("NEROCD_TOKEN"), "NeroCD bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, *addr+path, nil)
	if err != nil {
		return err
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w\nhint: set --addr or NEROCD_ADDR to the server URL, for example http://127.0.0.1:18080", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func postAPI(url string, body any, token string) (map[string]any, error) {
	var result map[string]any
	if err := postAPIInto(url, body, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func postAPIInto(url string, body any, token string, result any) error {
	return postAPIIntoContext(context.Background(), url, body, token, result)
}
func postAPIIntoContext(ctx context.Context, url string, body any, token string, result any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := runnerHTTPClient.Do(req)
	if err != nil {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), StatusCode: resp.StatusCode, Status: resp.Status, Detail: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), Err: err}
	}
	return nil
}

func getAPI(url string, token string) (map[string]any, error) {
	var result map[string]any
	if err := getAPIInto(url, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func getAPIInto(url string, token string, result any) error {
	return getAPIIntoContext(context.Background(), url, token, result)
}
func getAPIIntoContext(ctx context.Context, url string, token string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := runnerHTTPClient.Do(req)
	if err != nil {
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), StatusCode: resp.StatusCode, Status: resp.Status, Detail: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), Err: err}
	}
	return nil
}

func requireString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("missing string field %q", key)
	}
	return value, nil
}

func requireObject(payload map[string]any, key string) (map[string]any, error) {
	value, ok := payload[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing object field %q", key)
	}
	return value, nil
}

func requireArray(payload map[string]any, key string) ([]any, error) {
	value, ok := payload[key].([]any)
	if !ok {
		return nil, fmt.Errorf("missing array field %q", key)
	}
	return value, nil
}

func requirePagination(payload map[string]any) error {
	for _, key := range []string{"limit", "offset", "count", "total"} {
		if _, ok := payload[key].(float64); !ok {
			return fmt.Errorf("missing numeric field %q", key)
		}
	}
	if _, err := requireArray(payload, "items"); err != nil {
		return err
	}
	return nil
}

func findObjectByID(payload map[string]any, id string) (map[string]any, error) {
	items, err := requireArray(payload, "items")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemID, _ := object["id"].(string)
		if itemID == id {
			return object, nil
		}
	}
	return nil, fmt.Errorf("items did not include id %s", id)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func createSession(args []string) error {
	fs := flag.NewFlagSet("session", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	email := fs.String("email", "admin@example.local", "user email")
	password := fs.String("password", os.Getenv("NEROCD_PASSWORD"), "user password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	body := bytes.NewBufferString(fmt.Sprintf(`{"email":%q,"password":%q}`, *email, *password))
	req, err := http.NewRequest(http.MethodPost, *addr+"/api/v1/sessions", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return printAPIResponse(req)
}

func migrateDatabase(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	databaseURL := fs.String("database-url", os.Getenv("NEROCD_DATABASE_URL"), "PostgreSQL connection URL")
	seed := fs.Bool("seed", true, "apply development seed data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *databaseURL == "" {
		return errors.New("database URL is required via --database-url or NEROCD_DATABASE_URL")
	}
	if err := validateDatabaseURL(*databaseURL); err != nil {
		return err
	}

	database, err := pgxpool.New(context.Background(), *databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := database.Ping(ctx); err != nil {
		return err
	}
	if _, err := database.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return err
	}
	migrations, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, name := range migrations {
		content, err := db.Files.ReadFile(name)
		if err != nil {
			return err
		}
		checksum := sqlChecksum(content)
		appliedChecksum := ""
		err = database.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, name).Scan(&appliedChecksum)
		switch {
		case err == nil && appliedChecksum == checksum:
			fmt.Printf("skipped %s\n", name)
			continue
		case err == nil && appliedChecksum != checksum:
			return fmt.Errorf("migration %s checksum changed after it was applied", name)
		case err != pgx.ErrNoRows:
			return err
		}
		tx, err := database.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(content)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, name, checksum); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		fmt.Printf("applied %s\n", name)
	}
	if *seed {
		content, err := db.Files.ReadFile("seeds/dev.sql")
		if err != nil {
			return err
		}
		if _, err := database.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("apply seeds/dev.sql: %w", err)
		}
		fmt.Println("applied seeds/dev.sql")
	}
	return nil
}

func migrationFiles() ([]string, error) {
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, "migrations/"+entry.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("no migration files found")
	}
	return files, nil
}

func sqlChecksum(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum)
}

func validateDatabaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("database URL is invalid: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("database URL must use postgres:// or postgresql://")
	}
	if parsed.Host == "" {
		return errors.New("database URL host is required")
	}
	return nil
}

func smoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sessionReq, err := http.NewRequest(http.MethodPost, *addr+"/api/v1/sessions", strings.NewReader(`{"email":"admin@example.local","password":"admin"}`))
	if err != nil {
		return err
	}
	sessionReq.Header.Set("Content-Type", "application/json")
	sessionToken, err := requestSessionToken(sessionReq)
	if err != nil {
		return err
	}
	fmt.Printf("ok %s %s\n", http.MethodPost, "/api/v1/sessions")

	checks := []struct {
		method string
		path   string
		body   string
		auth   bool
	}{
		{method: http.MethodGet, path: "/api/v1/health"},
		{method: http.MethodGet, path: "/api/v1/ready"},
		{method: http.MethodGet, path: "/api/v1/me", auth: true},
		{method: http.MethodGet, path: "/api/v1/projects", auth: true},
		{method: http.MethodGet, path: "/api/v1/templates", auth: true},
		{method: http.MethodGet, path: "/api/v1/runs", auth: true},
		{method: http.MethodGet, path: "/api/v1/run-logs?run_id=run_001", auth: true},
	}
	for _, check := range checks {
		var body io.Reader
		if check.body != "" {
			body = strings.NewReader(check.body)
		}
		req, err := http.NewRequest(check.method, *addr+check.path, body)
		if err != nil {
			return err
		}
		if check.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if check.auth {
			req.Header.Set("Authorization", "Bearer "+sessionToken)
		}
		if err := expectOK(req); err != nil {
			return err
		}
		fmt.Printf("ok %s %s\n", check.method, check.path)
	}
	if err := smokeOperatorWorkflow(*addr, sessionToken); err != nil {
		return err
	}
	return nil
}

func smokeOperatorWorkflow(addr string, sessionToken string) error {
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	tag := "smoke_" + suffix
	runPayload := map[string]any{
		"project_id": "proj_platform",
		"run_spec": map[string]any{
			"type":   "shell",
			"inputs": map[string]any{"command": "echo smoke"},
			"process": map[string]any{
				"command": []string{"echo", "smoke"},
			},
			"artifacts": []map[string]any{
				{"name": "smoke", "path": "smoke.txt", "required": false},
			},
		},
		"workflow": map[string]any{
			"steps": []map[string]any{
				{
					"id":   "prepare",
					"name": "Prepare",
					"run_spec": map[string]any{
						"type":   "shell",
						"inputs": map[string]any{"command": "echo prepare"},
						"process": map[string]any{
							"command": []string{"echo", "prepare"},
						},
					},
				},
				{
					"id":         "execute",
					"name":       "Execute",
					"depends_on": []string{"prepare"},
					"run_spec": map[string]any{
						"type":   "shell",
						"inputs": map[string]any{"command": "echo execute"},
						"process": map[string]any{
							"command": []string{"echo", "execute"},
						},
						"artifacts": []map[string]any{
							{"name": "smoke", "path": "smoke.txt", "required": false},
						},
					},
				},
			},
		},
		"runner_tags": []string{tag},
	}
	run, err := postAPI(addr+"/api/v1/runs", runPayload, sessionToken)
	if err != nil {
		return err
	}
	runID, err := requireString(run, "id")
	if err != nil {
		return fmt.Errorf("smoke run: %w", err)
	}

	registered, err := postAPI(addr+"/api/v1/runners/register", map[string]any{
		"id":           "runner_smoke_" + suffix,
		"name":         "Smoke Runner " + suffix,
		"tags":         []string{tag},
		"capabilities": []string{"shell"},
	}, sessionToken)
	if err != nil {
		return err
	}
	runnerToken, err := requireString(registered, "token")
	if err != nil {
		return fmt.Errorf("runner registration: %w", err)
	}

	for step := 1; step <= 2; step++ {
		claim, err := postAPI(addr+"/api/v1/runners/claim", map[string]any{}, runnerToken)
		if err != nil {
			return err
		}
		claimedRun, err := requireObject(claim, "run")
		if err != nil {
			return fmt.Errorf("claim %d: %w", step, err)
		}
		claimedRunID, err := requireString(claimedRun, "id")
		if err != nil {
			return fmt.Errorf("claim %d run: %w", step, err)
		}
		if claimedRunID != runID {
			return fmt.Errorf("claim %d returned run %s, want %s", step, claimedRunID, runID)
		}
		lease, err := requireObject(claim, "lease")
		if err != nil {
			return fmt.Errorf("claim %d: %w", step, err)
		}
		leaseID, err := requireString(lease, "id")
		if err != nil {
			return fmt.Errorf("claim %d lease: %w", step, err)
		}
		attempt, ok := lease["attempt"].(float64)
		if !ok {
			return errors.New("claim lease missing attempt")
		}
		fence, err := requireString(lease, "fence")
		if err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/logs", map[string]any{
			"run_id":   runID,
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"event_key": fmt.Sprintf("smoke_%s_event_%d", suffix, step),
			"sequence":  10 + step,
			"stream":    "stdout",
			"message":   fmt.Sprintf("smoke step %d", step),
		}, runnerToken); err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/artifacts", map[string]any{
			"run_id":   runID,
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"name":     fmt.Sprintf("smoke-step-%d", step),
			"path":     fmt.Sprintf("smoke-step-%d.txt", step),
			"found":    false,
			"required": false,
			"size":     0,
			"kind":     "file",
		}, runnerToken); err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/complete", map[string]any{
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"completion_key": fmt.Sprintf("smoke_%s_completion_%d", suffix, step),
			"status":         "succeeded",
		}, runnerToken); err != nil {
			return err
		}
	}

	runs, err := getAPI(addr+"/api/v1/runs?project_id=proj_platform&limit=1&offset=0", sessionToken)
	if err != nil {
		return err
	}
	if err := requirePagination(runs); err != nil {
		return fmt.Errorf("runs pagination: %w", err)
	}
	allRuns, err := getAPI(addr+"/api/v1/runs?project_id=proj_platform", sessionToken)
	if err != nil {
		return err
	}
	finalRun, err := findObjectByID(allRuns, runID)
	if err != nil {
		return err
	}
	status, err := requireString(finalRun, "status")
	if err != nil {
		return fmt.Errorf("final run: %w", err)
	}
	if status != "succeeded" {
		return fmt.Errorf("final run status = %s, want succeeded", status)
	}
	artifacts, err := getAPI(addr+"/api/v1/artifacts?run_id="+url.QueryEscape(runID), sessionToken)
	if err != nil {
		return err
	}
	if err := requirePagination(artifacts); err != nil {
		return fmt.Errorf("artifacts pagination: %w", err)
	}
	items, err := requireArray(artifacts, "items")
	if err != nil {
		return err
	}
	if len(items) < 2 {
		return fmt.Errorf("artifact list returned %d items, want at least 2", len(items))
	}
	fmt.Printf("ok operator workflow %s\n", runID)
	return nil
}

func requestSessionToken(req *http.Request) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%s %s returned %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", errors.New("session response did not include a token")
	}
	return payload.Token, nil
}

func validateContract(args []string) error {
	fs := flag.NewFlagSet("contract", flag.ExitOnError)
	openAPIPath := fs.String("openapi", "openapi.yaml", "OpenAPI contract path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	document, err := loadOpenAPIContract(*openAPIPath)
	if err != nil {
		return err
	}
	documented, err := readOpenAPIOperations(document, *openAPIPath)
	if err != nil {
		return err
	}
	implemented := make(map[string]struct{})
	for _, route := range api.PublicRoutes() {
		key := route.Method + " " + route.Path
		implemented[key] = struct{}{}
		if _, ok := documented[key]; !ok {
			return fmt.Errorf("%s is implemented but missing from %s", key, *openAPIPath)
		}
	}
	for key := range documented {
		if _, ok := implemented[key]; !ok {
			return fmt.Errorf("%s is documented in %s but not implemented", key, *openAPIPath)
		}
	}
	if err := validateConsumersUseDocumentedRoutes(documented); err != nil {
		return err
	}
	if err := validateOpenAPIOperations(documented); err != nil {
		return err
	}
	if err := validateContractResponses(documented); err != nil {
		return err
	}
	fmt.Printf("ok contract %s (%d routes)\n", *openAPIPath, len(documented))
	return nil
}

type documentedOperation struct {
	Method            string
	Path              string
	OperationID       bool
	SecurityEmpty     bool
	RequestBody       bool
	JSONRequestBody   bool
	Responses         map[string]bool
	JSONResponseCodes map[string]bool
}

func loadOpenAPIContract(path string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := document.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return document, nil
}

func readOpenAPIOperations(document *openapi3.T, path string) (map[string]documentedOperation, error) {
	if document == nil || document.Paths == nil {
		return nil, fmt.Errorf("%s does not document any /api/v1 routes", path)
	}
	routes := make(map[string]documentedOperation)
	for _, routePath := range document.Paths.Keys() {
		if !strings.HasPrefix(routePath, "/api/v1/") {
			continue
		}
		pathItem := document.Paths.Value(routePath)
		if pathItem == nil {
			continue
		}
		for method, operation := range pathItem.Operations() {
			key := strings.ToUpper(method) + " " + routePath
			routes[key] = documentedOperationFromOperation(strings.ToUpper(method), routePath, operation)
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("%s does not document any /api/v1 routes", path)
	}
	return routes, nil
}

func documentedOperationFromOperation(method string, path string, operation *openapi3.Operation) documentedOperation {
	op := documentedOperation{
		Method:            method,
		Path:              path,
		OperationID:       operation != nil && operation.OperationID != "",
		Responses:         map[string]bool{},
		JSONResponseCodes: map[string]bool{},
	}
	if operation == nil {
		return op
	}
	if operation.Security != nil && len(*operation.Security) == 0 {
		op.SecurityEmpty = true
	}
	if operation.RequestBody != nil {
		op.RequestBody = true
		if requestBody := operation.RequestBody.Value; requestBody != nil {
			_, op.JSONRequestBody = requestBody.Content["application/json"]
		}
	}
	if operation.Responses != nil {
		for _, code := range operation.Responses.Keys() {
			op.Responses[code] = true
			responseRef := operation.Responses.Value(code)
			if responseRef == nil || responseRef.Value == nil {
				continue
			}
			if _, ok := responseRef.Value.Content["application/json"]; ok {
				op.JSONResponseCodes[code] = true
			}
		}
	}
	return op
}

func validateOpenAPIOperations(documented map[string]documentedOperation) error {
	for key, op := range documented {
		if !op.OperationID {
			return fmt.Errorf("%s is missing operationId", key)
		}
		public := !requiresContractAuth(op.Method, op.Path)
		if public && !op.SecurityEmpty {
			return fmt.Errorf("%s is public in implementation but missing security: []", key)
		}
		if !public && op.SecurityEmpty {
			return fmt.Errorf("%s is protected in implementation but documents security: []", key)
		}
		if !public && !op.Responses["401"] {
			return fmt.Errorf("%s is protected but does not document 401", key)
		}
		if mutates(op.Method, op.Path) && !op.RequestBody {
			return fmt.Errorf("%s mutates state but is missing requestBody", key)
		}
		if op.RequestBody && !op.JSONRequestBody {
			return fmt.Errorf("%s requestBody does not document application/json", key)
		}
		hasSuccess := false
		for code := range op.Responses {
			if strings.HasPrefix(code, "2") {
				hasSuccess = true
				if code == "204" {
					continue
				}
				if !op.JSONResponseCodes[code] {
					return fmt.Errorf("%s response %s does not document application/json content", key, code)
				}
			}
		}
		if !hasSuccess {
			return fmt.Errorf("%s does not document a 2xx response", key)
		}
	}
	return nil
}

func requiresContractAuth(method string, path string) bool {
	return path != "/api/v1/health" && path != "/api/v1/ready" && !(method == http.MethodPost && path == "/api/v1/sessions")
}

func requiresContractRunnerAuth(path string) bool {
	switch path {
	case "/api/v1/runners/heartbeat", "/api/v1/runners/claim", "/api/v1/runners/renew", "/api/v1/runners/lease", "/api/v1/runners/logs", "/api/v1/runners/events/batch", "/api/v1/runners/secrets/access", "/api/v1/runners/artifacts", "/api/v1/runners/complete":
		return true
	default:
		return false
	}
}

func mutates(method string, path string) bool {
	if method == http.MethodDelete && path == "/api/v1/sessions" {
		return false
	}
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func validateContractResponses(documented map[string]documentedOperation) error {
	mem := store.NewMemoryStore()
	service := app.NewService(auth.ContextProvider{}, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem)
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())

	sessionPayload, err := contractRequest(server, http.MethodPost, "/api/v1/sessions", `{"email":"admin@example.local","password":"admin"}`, "")
	if err != nil {
		return err
	}
	token, ok := sessionPayload["token"].(string)
	if !ok || token == "" {
		return errors.New("POST /api/v1/sessions did not return token")
	}

	cases := []struct {
		method      string
		path        string
		body        string
		auth        bool
		shape       string
		useAPIToken bool
	}{
		{method: http.MethodGet, path: "/api/v1/health", shape: "health"},
		{method: http.MethodGet, path: "/api/v1/ready", shape: "ready"},
		{method: http.MethodGet, path: "/api/v1/me", auth: true, shape: "principal"},
		{method: http.MethodPost, path: "/api/v1/api-tokens", body: `{"name":"Contract Bootstrap","roles":["system_admin"]}`, auth: true, shape: "api-token-registration"},
		{method: http.MethodGet, path: "/api/v1/capabilities", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/projects", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/projects", body: `{"name":"Contract Project","description":"contract"}`, auth: true, shape: "project"},
		{method: http.MethodGet, path: "/api/v1/project-members", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/project-members", body: `{"project_id":"proj_platform","email":"viewer@example.local","role":"viewer"}`, auth: true, shape: "project-member"},
		{method: http.MethodGet, path: "/api/v1/project-role?project_id=proj_platform", auth: true, shape: "project-role"},
		{method: http.MethodGet, path: "/api/v1/repositories", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/repositories", body: `{"project_id":"proj_platform","name":"Contract Repo","url":"https://example.local/repo.git"}`, auth: true, shape: "repository"},
		{method: http.MethodGet, path: "/api/v1/access-keys", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/access-keys", body: `{"project_id":"proj_platform","name":"Contract SSH","kind":"ssh","fingerprint":"SHA256:contract"}`, auth: true, shape: "access-key"},
		{method: http.MethodGet, path: "/api/v1/inventories", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/inventories", body: `{"project_id":"proj_platform","name":"Contract Inventory","kind":"static","source":"inventories/contract.ini"}`, auth: true, shape: "inventory"},
		{method: http.MethodGet, path: "/api/v1/templates", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/templates", body: `{"project_id":"proj_platform","name":"Contract Shell","run_spec":{"type":"shell","inputs":{"command":"echo ok"}},"runner_tags":["local"]}`, auth: true, shape: "template"},
		{method: http.MethodGet, path: "/api/v1/runs", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo ok"},"process":{"command":["echo","ok"]},"artifacts":[{"name":"out","path":"out.txt","required":false}],"secrets":[{"name":"contract-secret","provider":"runner_file","reference":"contract-secret","target":"env:CONTRACT_SECRET","required":true,"version":"v1"}]},"runner_tags":["local"]}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"template_id":"tpl_patch"}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs/reject", body: `{"run_id":"{{reject_run_id}}"}`, auth: true, shape: "approval"},
		{method: http.MethodGet, path: "/api/v1/runners", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/runners/register", body: `{"id":"runner_contract","name":"Contract Runner","tags":["local"],"capabilities":["shell"]}`, auth: true, shape: "registered-runner", useAPIToken: true},
		{method: http.MethodPost, path: "/api/v1/runners/rotate-token", body: `{"runner_id":"runner_contract"}`, auth: true, shape: "registered-runner"},
		{method: http.MethodPost, path: "/api/v1/runners/heartbeat", body: `{}`, auth: true, shape: "runner"},
		{method: http.MethodPost, path: "/api/v1/runners/claim", body: `{}`, auth: true, shape: "claim"},
		{method: http.MethodPost, path: "/api/v1/runners/logs", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","event_key":"contract-event-one","sequence":4,"stream":"stdout","message":"ok"}`, auth: true, shape: "run-log"},
		{method: http.MethodPost, path: "/api/v1/runners/events/batch", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","events":[{"event_key":"contract-event-two","sequence":5,"stream":"stdout","message":"batch ok"}]}`, auth: true, shape: "runner-event-ack"},
		{method: http.MethodPost, path: "/api/v1/runners/secrets/access", body: `{"access_id":"secret_access_0123456789abcdef0123456789abcdef","run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","binding":"contract-secret","provider":"runner_file","version":"v1"}`, auth: true, shape: "secret-access"},
		{method: http.MethodPost, path: "/api/v1/runners/artifacts", body: `{"run_id":"{{run_id}}","lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","name":"out","path":"out.txt","found":false,"required":false,"size":0,"kind":"file"}`, auth: true, shape: "artifact"},
		{method: http.MethodPost, path: "/api/v1/runners/complete", body: `{"lease_id":"{{lease_id}}","attempt":{{attempt}},"fence":"{{fence}}","completion_key":"contract-completion-one","status":"succeeded"}`, auth: true, shape: "lease"},
		{method: http.MethodPost, path: "/api/v1/runners/revoke-token", body: `{"runner_id":"runner_contract"}`, auth: true, shape: "runner"},
		{method: http.MethodPost, path: "/api/v1/runs", body: `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo cancel"},"process":{"command":["echo","cancel"]}},"runner_tags":["local"]}`, auth: true, shape: "run"},
		{method: http.MethodPost, path: "/api/v1/runs/cancel", body: `{"run_id":"{{cancel_run_id}}"}`, auth: true, shape: "run"},
		{method: http.MethodGet, path: "/api/v1/run-logs", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/artifacts", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/runner-primitive-plan?run_id=run_001", auth: true, shape: "primitive-plan"},
		{method: http.MethodGet, path: "/api/v1/approvals", auth: true, shape: "list"},
		{method: http.MethodGet, path: "/api/v1/audit-events", auth: true, shape: "list"},
		{method: http.MethodPost, path: "/api/v1/api-tokens/revoke", body: `{"token_id":"{{api_token_id}}"}`, auth: true, shape: "api-token"},
	}
	leaseID := ""
	attempt := ""
	fence := ""
	runID := ""
	rejectRunID := ""
	cancelRunID := ""
	runnerToken := ""
	apiToken := ""
	apiTokenID := ""
	for _, tc := range cases {
		docPath := strings.SplitN(tc.path, "?", 2)[0]
		key := tc.method + " " + docPath
		if _, ok := documented[key]; !ok {
			return fmt.Errorf("contract response case references undocumented %s", key)
		}
		tokenValue := ""
		if tc.auth {
			tokenValue = token
		}
		if tc.useAPIToken {
			if apiToken == "" {
				return fmt.Errorf("%s requires api token before request", key)
			}
			tokenValue = apiToken
		}
		if requiresContractRunnerAuth(docPath) {
			if runnerToken == "" {
				return fmt.Errorf("%s requires runner token before registration response", key)
			}
			tokenValue = runnerToken
		}
		body := strings.ReplaceAll(tc.body, "{{lease_id}}", leaseID)
		body = strings.ReplaceAll(body, "{{attempt}}", attempt)
		body = strings.ReplaceAll(body, "{{fence}}", fence)
		body = strings.ReplaceAll(body, "{{run_id}}", runID)
		body = strings.ReplaceAll(body, "{{reject_run_id}}", rejectRunID)
		body = strings.ReplaceAll(body, "{{cancel_run_id}}", cancelRunID)
		body = strings.ReplaceAll(body, "{{api_token_id}}", apiTokenID)
		payload, err := contractRequest(server, tc.method, tc.path, body, tokenValue)
		if err != nil {
			return err
		}
		if err := validatePayloadShape(key, tc.shape, payload); err != nil {
			return err
		}
		if tc.shape == "registered-runner" {
			value, ok := payload["token"].(string)
			if !ok || value == "" {
				return fmt.Errorf("%s response missing runner token", key)
			}
			runnerToken = value
		}
		if tc.shape == "api-token-registration" {
			value, ok := payload["token"].(string)
			if !ok || value == "" {
				return fmt.Errorf("%s response missing api token", key)
			}
			apiToken = value
			tokenPayload, ok := payload["api_token"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing api_token object", key)
			}
			apiTokenID, ok = tokenPayload["id"].(string)
			if !ok || apiTokenID == "" {
				return fmt.Errorf("%s response api_token missing id", key)
			}
		}
		if tc.shape == "claim" {
			lease, ok := payload["lease"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing lease object", key)
			}
			leaseID, ok = lease["id"].(string)
			if !ok || leaseID == "" {
				return fmt.Errorf("%s response lease missing id", key)
			}
			attempt = strconv.Itoa(int(lease["attempt"].(float64)))
			fence, _ = lease["fence"].(string)
			run, ok := payload["run"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s response missing run object", key)
			}
			runID, ok = run["id"].(string)
			if !ok || runID == "" {
				return fmt.Errorf("%s response run missing id", key)
			}
		}
		if tc.shape == "run" {
			if status, _ := payload["status"].(string); status == "waiting_approval" {
				if id, ok := payload["id"].(string); ok {
					rejectRunID = id
				}
			}
			if status, _ := payload["status"].(string); status == "queued" {
				if id, ok := payload["id"].(string); ok {
					cancelRunID = id
				}
			}
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		return fmt.Errorf("GET /api/v1/projects without auth returned %d, want 401", rec.Code)
	}
	var errorPayload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&errorPayload); err != nil {
		return fmt.Errorf("decode unauthorized error response: %w", err)
	}
	if _, ok := errorPayload["error"].(string); !ok {
		return errors.New("unauthorized response did not match ErrorResponse envelope")
	}
	if code, ok := errorPayload["code"].(string); !ok || code != "unauthenticated" {
		return errors.New("unauthorized response did not include stable unauthenticated code")
	}
	return nil
}

func contractRequest(server http.Handler, method string, path string, body string, token string) (map[string]any, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		return nil, fmt.Errorf("%s %s returned %d during contract response validation: %s", method, path, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return payload, nil
}

func validatePayloadShape(key string, shape string, payload map[string]any) error {
	requireString := func(field string) error {
		if _, ok := payload[field].(string); !ok {
			return fmt.Errorf("%s response missing string field %q", key, field)
		}
		return nil
	}
	switch shape {
	case "health":
		if payload["status"] != "ok" {
			return fmt.Errorf("%s response status = %v, want ok", key, payload["status"])
		}
	case "ready":
		if payload["status"] != "ready" {
			return fmt.Errorf("%s response status = %v, want ready", key, payload["status"])
		}
	case "principal":
		for _, field := range []string{"id", "email", "name", "provider"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["roles"].([]any); !ok {
			return fmt.Errorf("%s response missing roles array", key)
		}
	case "list":
		if _, ok := payload["items"].([]any); !ok {
			return fmt.Errorf("%s response missing items array", key)
		}
	case "project":
		for _, field := range []string{"id", "name", "description", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "project-member":
		for _, field := range []string{"id", "project_id", "user_id", "email", "name", "role", "created_at", "updated_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "project-role":
		for _, field := range []string{"project_id", "role"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		for _, field := range []string{"can_view", "can_run", "can_admin"} {
			if _, ok := payload[field].(bool); !ok {
				return fmt.Errorf("%s response missing boolean field %q", key, field)
			}
		}
	case "template":
		for _, field := range []string{"id", "project_id", "name", "kind"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["run_spec"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run_spec object", key)
		}
		if _, ok := payload["workflow"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing workflow object", key)
		}
		if _, ok := payload["runner_tags"].([]any); !ok {
			return fmt.Errorf("%s response missing runner_tags array", key)
		}
	case "repository":
		for _, field := range []string{"id", "project_id", "name", "url", "provider", "default_ref", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "access-key":
		for _, field := range []string{"id", "project_id", "name", "kind", "fingerprint", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "inventory":
		for _, field := range []string{"id", "project_id", "name", "kind", "source", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "run":
		for _, field := range []string{"id", "project_id", "status", "requested_by", "started_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["run_spec"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run_spec object", key)
		}
		if _, ok := payload["workflow"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing workflow object", key)
		}
		if _, ok := payload["runner_tags"].([]any); !ok {
			return fmt.Errorf("%s response missing runner_tags array", key)
		}
	case "runner":
		for _, field := range []string{"id", "name", "status", "registered_at", "last_heartbeat_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["tags"].([]any); !ok {
			return fmt.Errorf("%s response missing tags array", key)
		}
		if _, ok := payload["capabilities"].([]any); !ok {
			return fmt.Errorf("%s response missing capabilities array", key)
		}
	case "registered-runner":
		if err := requireString("token"); err != nil {
			return err
		}
		runnerPayload, ok := payload["runner"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response missing runner object", key)
		}
		return validatePayloadShape(key, "runner", runnerPayload)
	case "api-token-registration":
		if err := requireString("token"); err != nil {
			return err
		}
		tokenPayload, ok := payload["api_token"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response missing api_token object", key)
		}
		return validatePayloadShape(key, "api-token", tokenPayload)
	case "api-token":
		for _, field := range []string{"id", "name", "kind", "status", "created_by", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["roles"].([]any); !ok {
			return fmt.Errorf("%s response missing roles array", key)
		}
	case "claim":
		if _, ok := payload["lease"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing lease object", key)
		}
		if _, ok := payload["run"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing run object", key)
		}
		if _, ok := payload["primitive_plan"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing primitive_plan object", key)
		}
	case "lease":
		for _, field := range []string{"id", "run_id", "runner_id", "status", "expires_at", "created_at", "completed_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "run-log":
		for _, field := range []string{"id", "run_id", "stream", "message", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["sequence"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric sequence", key)
		}
	case "runner-event-ack":
		events, ok := payload["events"].([]any)
		if !ok || len(events) == 0 {
			return fmt.Errorf("%s response missing events array", key)
		}
		first, ok := events[0].(map[string]any)
		if !ok {
			return fmt.Errorf("%s response event is not an object", key)
		}
		return validatePayloadShape(key, "run-log", first)
	case "secret-access":
		for _, field := range []string{"access_id", "run_id", "lease_id", "binding", "provider", "authorized_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		if _, ok := payload["attempt"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric attempt", key)
		}
	case "artifact":
		for _, field := range []string{"id", "run_id", "lease_id", "name", "path", "kind", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
		for _, field := range []string{"found", "required"} {
			if _, ok := payload[field].(bool); !ok {
				return fmt.Errorf("%s response missing boolean field %q", key, field)
			}
		}
		if _, ok := payload["size"].(float64); !ok {
			return fmt.Errorf("%s response missing numeric size", key)
		}
	case "approval":
		for _, field := range []string{"id", "run_id", "status", "requested_by", "created_at"} {
			if err := requireString(field); err != nil {
				return err
			}
		}
	case "primitive-plan":
		if err := requireString("run_id"); err != nil {
			return err
		}
		if _, ok := payload["checkout"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing checkout object", key)
		}
		if _, ok := payload["process"].(map[string]any); !ok {
			return fmt.Errorf("%s response missing process object", key)
		}
	default:
		return fmt.Errorf("unknown contract payload shape %q for %s", shape, key)
	}
	return nil
}

func validateConsumersUseDocumentedRoutes(documented map[string]documentedOperation) error {
	files := []string{"cmd/nerocd/main.go", "web/static/app.js", "web/app/src/api.ts"}
	apiPathPattern := regexp.MustCompile(`/api/v1/[A-Za-z0-9/_-]+`)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, match := range apiPathPattern.FindAllString(string(content), -1) {
			if isDocumentedPath(documented, match) {
				continue
			}
			return fmt.Errorf("%s consumes undocumented API route %s", file, match)
		}
	}
	return nil
}

func isDocumentedPath(documented map[string]documentedOperation, path string) bool {
	for route := range documented {
		if strings.HasSuffix(route, " "+path) {
			return true
		}
	}
	return false
}

func printAPIResponse(req *http.Request) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func expectOK(req *http.Request) error {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s returned %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func defaultAPIAddr() string {
	if addr := os.Getenv("NEROCD_ADDR"); addr != "" {
		return addr
	}
	return "http://127.0.0.1:8080"
}
