package app

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/store"
)

func (s *Service) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.list.denied", "runners", nil)
		return nil, auth.ErrForbidden
	}
	return s.runners.ListRunners(ctx)
}

// RunnerByID keeps administrative detail lookup indexed and distinct from the
// paginated inventory. Runner identity is global operational metadata and is
// deliberately visible only to global runner administrators.
type RunnerOperationalStatus struct {
	ObservedAt    time.Time `json:"observed_at"`
	JournalDepth  int       `json:"journal_depth"`
	RetryCount    int       `json:"retry_count"`
	RenewFailures int       `json:"renew_failures"`
}

type RunnerDetail struct {
	domain.Runner
	Telemetry *RunnerOperationalStatus `json:"telemetry,omitempty"`
}

func (s *Service) RunnerByID(ctx context.Context, id string) (RunnerDetail, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return RunnerDetail{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.get.denied", "runners", nil)
		return RunnerDetail{}, auth.ErrForbidden
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return RunnerDetail{}, store.ErrNotFound
	}
	runner, err := s.runners.GetRunnerByID(ctx, id)
	if err != nil {
		return RunnerDetail{}, err
	}
	result := RunnerDetail{Runner: runner}
	if s.operationalReader == nil {
		return result, nil
	}
	telemetry, err := s.operationalReader.RunnerOperationalObservation(ctx, runner.ID)
	if errors.Is(err, store.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return RunnerDetail{}, err
	}
	result.Telemetry = &RunnerOperationalStatus{ObservedAt: telemetry.ObservedAt, JournalDepth: telemetry.JournalDepth, RetryCount: telemetry.RetryCount, RenewFailures: telemetry.RenewFailures}
	return result, nil
}

type RunnerInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
}

type RegisteredRunner struct {
	Runner domain.Runner `json:"runner"`
	Token  string        `json:"token"`
}

type RunnerEnrollmentInput struct {
	RunnerID     string   `json:"runner_id"`
	RunnerName   string   `json:"runner_name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
	TTLSeconds   int      `json:"ttl_seconds"`
}

type CreatedRunnerEnrollment struct {
	Enrollment domain.RunnerEnrollment `json:"enrollment"`
	Token      string                  `json:"token"`
}

type RunnerEnrollmentRevokeInput struct {
	EnrollmentID string `json:"enrollment_id"`
}

type RunnerEnrollmentConsumeInput struct {
	RequestID      string `json:"request_id"`
	CredentialHash string `json:"credential_hash"`
}

type ConsumedRunnerEnrollment struct {
	Runner domain.Runner `json:"runner"`
}

type RunnerTokenInput struct {
	RunnerID string `json:"runner_id"`
}

func (s *Service) AuthenticateRunnerToken(ctx context.Context, token string) (auth.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	runner, err := s.runners.GetRunnerByTokenHash(ctx, runnerTokenHash(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return auth.Principal{
		ID:       runner.ID,
		Email:    "",
		Name:     runner.Name,
		Roles:    []string{domain.RoleRunner},
		Provider: domain.PrincipalRunner,
	}, nil
}

var runnerEnrollmentIDPattern = regexp.MustCompile(`^runner_[a-z0-9][a-z0-9_-]{2,62}$`)
var enrollmentConsumeIDPattern = regexp.MustCompile(`^enroll_consume_[0-9a-f]{32}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var enrollmentTokenPattern = regexp.MustCompile(`^nce_[0-9a-f]{64}$`)

func (s *Service) CreateRunnerEnrollment(ctx context.Context, input RunnerEnrollmentInput) (CreatedRunnerEnrollment, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	if !isRunnerAdmin(principal) {
		return CreatedRunnerEnrollment{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if !runnerEnrollmentIDPattern.MatchString(runnerID) {
		return CreatedRunnerEnrollment{}, errors.New("runner_id is invalid")
	}
	name := strings.TrimSpace(input.RunnerName)
	if name == "" || len(name) > 128 {
		return CreatedRunnerEnrollment{}, errors.New("runner_name is invalid")
	}
	tags := normalizeTags(input.Tags)
	capabilities := normalizeTags(input.Capabilities)
	if len(tags) > 32 || len(capabilities) == 0 || len(capabilities) > 32 {
		return CreatedRunnerEnrollment{}, errors.New("runner tags or capabilities are invalid")
	}
	if composeRunnerHasGenericShell(capabilities) {
		return CreatedRunnerEnrollment{}, errors.New("compose-deploy runner pools must not advertise generic shell")
	}
	for _, value := range append(append([]string(nil), tags...), capabilities...) {
		if len(value) > 64 {
			return CreatedRunnerEnrollment{}, errors.New("runner tags or capabilities are invalid")
		}
	}
	ttl := 10 * time.Minute
	if input.TTLSeconds != 0 {
		ttl = time.Duration(input.TTLSeconds) * time.Second
	}
	if ttl < time.Minute || ttl > time.Hour {
		return CreatedRunnerEnrollment{}, errors.New("ttl_seconds is invalid")
	}
	id, err := prefixedID("enroll")
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	token, tokenHash, err := newEnrollmentToken()
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	now := time.Now().UTC()
	audit, err := s.auditEvent(ctx, principal.ID, "runner.enrollment.create", id, map[string]any{"enrollment_id": id, "runner_id": runnerID, "expires_in_seconds": int(ttl.Seconds())})
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	enrollment, err := s.runners.CreateRunnerEnrollment(ctx, domain.RunnerEnrollment{ID: id, TokenHash: tokenHash, RunnerID: runnerID, RunnerName: name, Tags: tags, Capabilities: capabilities, CreatedBy: principal.ID, CreatedAt: now, ExpiresAt: now.Add(ttl)}, audit)
	if err != nil {
		return CreatedRunnerEnrollment{}, err
	}
	return CreatedRunnerEnrollment{Enrollment: enrollment, Token: token}, nil
}

func (s *Service) RevokeRunnerEnrollment(ctx context.Context, input RunnerEnrollmentRevokeInput) (domain.RunnerEnrollment, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if !isRunnerAdmin(principal) {
		return domain.RunnerEnrollment{}, auth.ErrForbidden
	}
	id := strings.TrimSpace(input.EnrollmentID)
	if id == "" {
		return domain.RunnerEnrollment{}, errors.New("enrollment_id is required")
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.enrollment.revoke", id, map[string]any{"enrollment_id": id})
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return s.runners.RevokeRunnerEnrollment(ctx, id, audit)
}

func (s *Service) ConsumeRunnerEnrollment(ctx context.Context, token string, input RunnerEnrollmentConsumeInput) (ConsumedRunnerEnrollment, error) {
	token = strings.TrimSpace(token)
	if !enrollmentTokenPattern.MatchString(token) {
		return ConsumedRunnerEnrollment{}, auth.ErrUnauthenticated
	}
	requestID := strings.TrimSpace(input.RequestID)
	credentialHash := strings.TrimSpace(input.CredentialHash)
	if !enrollmentConsumeIDPattern.MatchString(requestID) || !sha256HexPattern.MatchString(credentialHash) {
		return ConsumedRunnerEnrollment{}, errors.New("request_id or credential_hash is invalid")
	}
	auditID, err := prefixedID("aud")
	if err != nil {
		return ConsumedRunnerEnrollment{}, err
	}
	runner, err := s.runners.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: enrollmentTokenHash(token), RequestID: requestID, CredentialHash: credentialHash}, domain.AuditEvent{ID: auditID, Action: "runner.enrollment.consume", CreatedAt: time.Now().UTC()})
	if err != nil {
		return ConsumedRunnerEnrollment{}, err
	}
	return ConsumedRunnerEnrollment{Runner: runner}, nil
}

func (s *Service) RegisterRunner(ctx context.Context, input RunnerInput) (RegisteredRunner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return RegisteredRunner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.register.denied", strings.TrimSpace(input.ID), map[string]any{"name": input.Name})
		return RegisteredRunner{}, auth.ErrForbidden
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id, err = prefixedID("runner")
		if err != nil {
			return RegisteredRunner{}, err
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	capabilities := normalizeTags(input.Capabilities)
	if len(capabilities) == 0 {
		return RegisteredRunner{}, errors.New("capabilities are required")
	}
	if composeRunnerHasGenericShell(capabilities) {
		// This is untrusted caller input; keep the error in the stable invalid
		// request class rather than exposing it as a generic server failure.
		return RegisteredRunner{}, errors.New("runner capabilities are invalid: compose-deploy runner pools must not advertise generic shell")
	}
	token, tokenHash, err := newRunnerToken()
	if err != nil {
		return RegisteredRunner{}, err
	}
	now := time.Now().UTC()
	runner := domain.Runner{ID: id, Name: name, Tags: normalizeTags(input.Tags), Capabilities: capabilities, TokenHash: tokenHash, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.register", runner.ID, map[string]any{"name": runner.Name})
	if err != nil {
		return RegisteredRunner{}, err
	}
	runner, err = s.runners.RegisterRunner(ctx, runner, store.WithAudit(audit))
	if err != nil {
		return RegisteredRunner{}, err
	}
	return RegisteredRunner{Runner: runner, Token: token}, nil
}

func composeRunnerHasGenericShell(capabilities []string) bool {
	compose, shell := false, false
	for _, capability := range capabilities {
		compose = compose || capability == domain.RunTypeComposeDeploy
		shell = shell || capability == domain.RunTypeShell
	}
	return compose && shell
}

func (s *Service) RotateRunnerToken(ctx context.Context, input RunnerTokenInput) (RegisteredRunner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return RegisteredRunner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.token.rotate.denied", strings.TrimSpace(input.RunnerID), nil)
		return RegisteredRunner{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if runnerID == "" {
		return RegisteredRunner{}, errors.New("runner_id is required")
	}
	token, tokenHash, err := newRunnerToken()
	if err != nil {
		return RegisteredRunner{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.token.rotate", runnerID, map[string]any{"runner_id": runnerID})
	if err != nil {
		return RegisteredRunner{}, err
	}
	runner, err := s.runners.UpdateRunnerToken(ctx, runnerID, tokenHash, domain.RunnerActive, time.Now().UTC(), store.WithAudit(audit))
	if err != nil {
		return RegisteredRunner{}, err
	}
	return RegisteredRunner{Runner: runner, Token: token}, nil
}

func (s *Service) RevokeRunnerToken(ctx context.Context, input RunnerTokenInput) (domain.Runner, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	if !isRunnerAdmin(principal) {
		s.writeDeniedAudit(ctx, principal, "runner.token.revoke.denied", strings.TrimSpace(input.RunnerID), nil)
		return domain.Runner{}, auth.ErrForbidden
	}
	runnerID := strings.TrimSpace(input.RunnerID)
	if runnerID == "" {
		return domain.Runner{}, errors.New("runner_id is required")
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.token.revoke", runnerID, map[string]any{"runner_id": runnerID})
	if err != nil {
		return domain.Runner{}, err
	}
	return s.runners.UpdateRunnerToken(ctx, runnerID, "", domain.RunnerRevoked, time.Now().UTC(), store.WithAudit(audit))
}

func (s *Service) HeartbeatRunner(ctx context.Context) (domain.Runner, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	runner, err := s.runners.HeartbeatRunner(ctx, principal.ID, time.Now().UTC())
	if err != nil {
		return domain.Runner{}, err
	}
	// Heartbeats are high-frequency liveness telemetry, not an operator action.
	// Persisting one audit event per beat both obscures useful evidence and makes
	// the append-only audit log an unbounded ingestion path.
	return runner, nil
}

// RunnerOperationalTelemetry is a bounded latest-state report. Runner
// identity is authenticated from the bearer context; callers cannot select a
// target runner or attach paths, logs, errors, or arbitrary labels.
type RunnerOperationalTelemetry struct {
	JournalDepth  int `json:"journal_depth"`
	RetryCount    int `json:"retry_count"`
	RenewFailures int `json:"renew_failures"`
}

func (s *Service) RecordRunnerOperationalTelemetry(ctx context.Context, input RunnerOperationalTelemetry) error {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return err
	}
	if input.JournalDepth < 0 || input.JournalDepth > 8192 || input.RetryCount < 0 || input.RetryCount > 100000 || input.RenewFailures < 0 || input.RenewFailures > 100000 {
		return errors.New("runner telemetry is invalid")
	}
	if s.operationalWriter == nil {
		return errors.New("runner telemetry is unavailable")
	}
	return s.operationalWriter.RecordRunnerOperationalObservation(ctx, principal.ID, input.JournalDepth, input.RetryCount, input.RenewFailures)
}

func (s *Service) ClaimRun(ctx context.Context) (domain.ClaimedRun, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.claim", "", nil)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim, err := s.runners.ClaimRun(ctx, principal.ID, time.Now().UTC(), s.leaseTTL, store.WithAudit(audit))
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim.Run = s.ensureWorkflowState(claim.Run)
	claim.Run, err = s.markWorkflowStepRunning(ctx, claim.Run, time.Now().UTC())
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	executableRun := s.executableRunForWorkflowStep(claim.Run)
	plan, err := s.registry.BuildPlan(executableRun)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	claim.PrimitivePlan = plan
	_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: claim.Run.ID, Sequence: 2, Stream: domain.LogSystem, Message: "Run leased to runner " + claim.Lease.RunnerID, CreatedAt: time.Now().UTC()})
	return claim, nil
}

func (s *Service) RenewLease(ctx context.Context, leaseID, fence string, attempt int) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	if leaseID == "" || fence == "" || attempt <= 0 {
		return domain.RunLease{}, errors.New("lease_id, attempt, and fence are required")
	}
	return s.runners.RenewLease(ctx, principal.ID, leaseID, fence, attempt, time.Now().UTC(), s.leaseTTL)
}

func (s *Service) ReapExpiredLeases(ctx context.Context) error {
	return s.runners.ExpireLeases(ctx, time.Now().UTC())
}

func (s *Service) CompleteLease(ctx context.Context, leaseID string, status string, attempt int, fence string, completionKey ...string) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	status = strings.TrimSpace(status)
	switch status {
	case domain.RunSucceeded, domain.RunFailed, domain.RunCanceled:
	default:
		return domain.RunLease{}, errors.New("lease completion status is invalid")
	}
	leaseID = strings.TrimSpace(leaseID)
	key := ""
	if len(completionKey) > 0 {
		key = strings.TrimSpace(completionKey[0])
	}
	if key == "" {
		return domain.RunLease{}, errors.New("completion_key is required")
	}
	lease, err := s.runners.GetLeaseForCompletion(ctx, leaseID, principal.ID, attempt, fence)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.RunLease{}, auth.ErrForbidden
		}
		return domain.RunLease{}, err
	}
	if attempt <= 0 || fence == "" || lease.Attempt != attempt || lease.Fence != fence {
		return domain.RunLease{}, auth.ErrForbidden
	}
	run, err := s.runByID(ctx, lease.RunID)
	if err != nil {
		return domain.RunLease{}, err
	}
	if run.RunSpec.Type == domain.RunTypeComposeDeploy {
		return domain.RunLease{}, store.ErrConflict
	}
	if lease.CompletionKey != "" {
		if lease.CompletionKey == key && lease.Status == status {
			return lease, nil
		}
		return domain.RunLease{}, store.ErrConflict
	}
	now := time.Now().UTC()
	runStatus, finishedAt, workflowState, queueNext, err := completionRunState(run, status, now)
	if err != nil {
		return domain.RunLease{}, err
	}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.RunLease{}, err
	}
	logs := []domain.RunLog{{ID: logID, RunID: lease.RunID, Sequence: 3, Stream: domain.LogSystem, Message: "Runner completed lease with status " + status, CreatedAt: now}}
	if queueNext {
		nextLogID, err := prefixedID("log")
		if err != nil {
			return domain.RunLease{}, err
		}
		logs = append(logs, domain.RunLog{ID: nextLogID, RunID: lease.RunID, Sequence: 3, Stream: domain.LogSystem, Message: "Workflow queued next step", CreatedAt: now})
	}
	audit, err := s.auditEvent(ctx, principal.ID, "runner.complete", lease.RunID, map[string]any{"lease_id": lease.ID, "status": status})
	if err != nil {
		return domain.RunLease{}, err
	}
	return s.runners.CompleteLeaseRequest(ctx, leaseID, principal.ID, status, attempt, fence, key, now, runStatus, finishedAt, workflowState, logs, audit)
}

func (s *Service) RunnerLease(ctx context.Context, leaseID string, attempt int, fence string) (domain.RunLease, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" || attempt <= 0 || fence == "" {
		return domain.RunLease{}, errors.New("lease_id, attempt, and fence are required")
	}
	lease, err := s.runners.GetLeaseForRunner(ctx, leaseID, principal.ID)
	if err != nil {
		return domain.RunLease{}, err
	}
	if lease.Attempt != attempt || lease.Fence != fence {
		return domain.RunLease{}, auth.ErrForbidden
	}
	return lease, nil
}

type SecretAccessInput struct {
	AccessID string `json:"access_id"`
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	Binding  string `json:"binding"`
	Provider string `json:"provider"`
	Version  string `json:"version"`
}

func (s *Service) AuthorizeSecretAccess(ctx context.Context, input SecretAccessInput) (domain.SecretAccessGrant, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	request := domain.SecretAccessRequest{
		AccessID: strings.TrimSpace(input.AccessID), RunnerID: principal.ID,
		RunID: strings.TrimSpace(input.RunID), LeaseID: strings.TrimSpace(input.LeaseID),
		Attempt: input.Attempt, Fence: strings.TrimSpace(input.Fence), Binding: strings.TrimSpace(input.Binding),
		Provider: strings.ToLower(strings.TrimSpace(input.Provider)), Version: strings.TrimSpace(input.Version), RequestedAt: time.Now().UTC(),
	}
	if request.AccessID == "" || request.RunID == "" || request.LeaseID == "" || request.Attempt <= 0 || request.Fence == "" || request.Binding == "" || request.Provider == "" {
		return domain.SecretAccessGrant{}, errors.New("access_id, run_id, lease_id, attempt, fence, binding, and provider are required")
	}
	if err := runner.ValidateSecretAccessMetadata(request.AccessID, request.Binding, request.Provider, request.Version); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	return s.runners.AuthorizeSecretAccess(ctx, request)
}
