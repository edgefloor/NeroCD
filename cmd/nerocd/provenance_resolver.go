package main

// The compose provenance resolver deliberately has no apply operation.  It
// checks out one immutable commit and asks Compose to render its configuration;
// a later, separately fenced deployment adapter is responsible for mutation.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/source"
)

type provenanceCommand interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

type provenanceCommandWithEnvironment interface {
	RunEnvironment(context.Context, string, []string, string, []string) ([]byte, error)
}

var lookupRepositoryIP = net.DefaultResolver.LookupNetIP
var gitCurlResolveAvailable = nativeGitCurlResolveAvailable

type osProvenanceCommand struct{}

const provenanceCommandOutputLimit = 5 << 20

type boundedCommandOutput struct{ bytes.Buffer }

type provenanceExecutionError struct {
	err    error
	reason string
}

func (e *provenanceExecutionError) Error() string { return e.err.Error() }
func (e *provenanceExecutionError) Unwrap() error { return e.err }

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > provenanceCommandOutputLimit {
		return 0, errors.New("provenance command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (osProvenanceCommand) Run(ctx context.Context, name string, args []string, dir string) ([]byte, error) {
	return osProvenanceCommand{}.RunEnvironment(ctx, name, args, dir, nil)
}

func (osProvenanceCommand) RunEnvironment(ctx context.Context, name string, args []string, dir string, extra []string) ([]byte, error) {
	// A resolver must not be allowed to keep a process alive beyond an attempt
	// boundary.  The caller may impose a shorter deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}
	if dir == "" {
		dir = os.TempDir()
	}
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	configureProvenanceProcess(c)
	// No ambient git config, prompt, hooks, or credential helper may influence
	// provenance. Git's network protocol is constrained at the invocation.
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	// Do not inherit GIT_*, SSH_AUTH_SOCK, proxy, or credential-helper state.
	// HOME is the attempt directory, so even a tool which ignores the Git config
	// guards has no access to a user's default keys or configuration.
	c.Env = append([]string{"PATH=" + path, "HOME=" + dir, "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_ALLOW_PROTOCOL=https:http:ssh"}, extra...)
	// The acceptance-only Docker wrapper records a fixed allowlist of command
	// actions on the runner's durable journal. It receives no raw command args
	// or credentials. Production leaves this unset.
	if trace := strings.TrimSpace(os.Getenv("NEROCD_COMPOSE_TRACE")); trace != "" {
		c.Env = append(c.Env, "NEROCD_COMPOSE_TRACE="+trace)
	}
	var output boundedCommandOutput
	c.Stdout, c.Stderr = &output, &output
	err := c.Run()
	if ctx.Err() != nil {
		killProvenanceProcess(c)
	}
	if err != nil {
		return output.Bytes(), &provenanceExecutionError{err: err, reason: classifyProvenanceCommandOutput(output.Bytes())}
	}
	return output.Bytes(), err
}

func runProvenanceCommand(command provenanceCommand, ctx context.Context, name string, args []string, dir string, environment []string) ([]byte, error) {
	if withEnvironment, ok := command.(provenanceCommandWithEnvironment); ok {
		return withEnvironment.RunEnvironment(ctx, name, args, dir, environment)
	}
	return command.Run(ctx, name, args, dir)
}

type resolvedProvenance struct {
	GitCommit, ComposeHash string
	ImageDigests           []string
}

// deploymentStatusWatcher owns only the mutable Compose operation context. It
// deliberately does not cancel the fenced supervisor: cancellation still has
// to be acknowledged by the active runner through the server-owned receipt.
type deploymentStatusWatcher struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.RWMutex
	receipt string
}

// deploymentCancellationReceipt is deliberately a second, fenced read of the
// dedicated status capability.  A cancelled process can return concurrently
// with its polling goroutine, so the owner must not infer "not cancelled" from
// an in-memory scheduling race.  This lookup carries the complete attempt
// identity and never grants a new fence or invents a receipt.
func deploymentCancellationReceipt(supervisor *attemptSupervisor, server, token string, plan domain.DeploymentPlan) string {
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return ""
	}
	defer cancel()
	query := url.Values{"deployment_id": {plan.DeploymentID}, "run_id": {plan.RunID}, "lease_id": {plan.LeaseID}, "attempt": {fmt.Sprint(plan.Attempt)}, "fence": {plan.Fence}}
	var observed domain.DeploymentPlan
	if err := getAPIIntoContext(ctx, server+"/api/v1/runners/deployments/status?"+query.Encode(), token, &observed); err != nil {
		return ""
	}
	if observed.DeploymentID != plan.DeploymentID || observed.RunID != plan.RunID || observed.LeaseID != plan.LeaseID || observed.Attempt != plan.Attempt || observed.Fence != plan.Fence || observed.Status != domain.DeploymentCancelRequested || observed.CancellationRequestID == nil {
		return ""
	}
	return strings.TrimSpace(*observed.CancellationRequestID)
}

func startDeploymentStatusWatcher(parent context.Context, supervisor *attemptSupervisor, server, token string, plan domain.DeploymentPlan, operationCancel context.CancelFunc) *deploymentStatusWatcher {
	w := &deploymentStatusWatcher{cancel: operationCancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		provenanceDiagnostic("deployment_cancellation", "watching")
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			ctx, stop, err := supervisor.RequestContextFrom(parent)
			if err != nil {
				return
			}
			query := url.Values{"deployment_id": {plan.DeploymentID}, "run_id": {plan.RunID}, "lease_id": {plan.LeaseID}, "attempt": {fmt.Sprint(plan.Attempt)}, "fence": {plan.Fence}}
			var observed domain.DeploymentPlan
			err = getAPIIntoContext(ctx, server+"/api/v1/runners/deployments/status?"+query.Encode(), token, &observed)
			stop()
			if err == nil && observed.DeploymentID == plan.DeploymentID && observed.RunID == plan.RunID && observed.LeaseID == plan.LeaseID && observed.Attempt == plan.Attempt && observed.Fence == plan.Fence && observed.Status == domain.DeploymentCancelRequested && observed.CancellationRequestID != nil && strings.TrimSpace(*observed.CancellationRequestID) != "" {
				provenanceDiagnostic("deployment_cancellation", "receipt_observed")
				w.mu.Lock()
				w.receipt = *observed.CancellationRequestID
				w.mu.Unlock()
				operationCancel()
				return
			}
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return w
}

func (w *deploymentStatusWatcher) Stop() { w.cancel(); <-w.done }
func (w *deploymentStatusWatcher) Receipt() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.receipt
}

// resolveComposeClaim owns the complete fenced typed deployment protocol. It
// never falls back to a generic process plan: all Compose mutation is through
// the small engine below, and every terminal state is reported through the
// deployment endpoints rather than lease completion.
func resolveComposeClaim(supervisor *attemptSupervisor, journal *runner.AttemptJournal, reporter *attemptReporter, server, token, workDir, secretRoot string, imagePolicy composeImagePolicy, claim domain.ClaimedRun) error {
	deploymentID, _ := claim.Run.RunSpec.Inputs["deployment_id"].(string)
	if strings.TrimSpace(deploymentID) == "" {
		return errors.New("compose deployment is missing deployment_id")
	}
	query := url.Values{"deployment_id": {deploymentID}, "run_id": {claim.Run.ID}, "lease_id": {claim.Lease.ID}, "attempt": {fmt.Sprint(claim.Lease.Attempt)}, "fence": {claim.Lease.Fence}}
	requestCtx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer cancel()
	var plan domain.DeploymentPlan
	if err = getAPIIntoContext(requestCtx, server+"/api/v1/runners/deployments/plan?"+query.Encode(), token, &plan); err != nil {
		return err
	}
	// A fixed, server-owned stage marker gives operators a minimal durable
	// indication that a first fenced Compose owner started resolution.  It has
	// no user-controlled identifiers, paths, URLs, or provenance values.  A
	// reclaimed owner observes applying/verifying and intentionally does not
	// repeat this logical stage, so journal replay remains one exact event.
	if plan.Status == domain.DeploymentAssigned {
		if reporter == nil {
			return errors.New("compose stage reporter is unavailable")
		}
		if err := reporter.Emit(domain.LogSystem, "compose-stage-resolution", 1); err != nil {
			return err
		}
		// The deployment transition and run-event append both fence the same
		// authority. Drain this one fixed stage before acquiring the transition
		// transaction so the runner never creates an avoidable lock cycle.
		if err := reporter.WaitEmpty(supervisor.Context()); err != nil {
			return fmt.Errorf("flush compose stage: %w", err)
		}
	}
	transition := func(expected, target, key, code string, health *bool) error {
		ctx, stop, e := supervisor.RequestContext()
		if e != nil {
			return e
		}
		defer stop()
		var ignored domain.Deployment
		return postAPIIntoContext(ctx, server+"/api/v1/runners/deployments/transition", app.DeploymentTransitionInput{DeploymentID: plan.DeploymentID, RunID: plan.RunID, LeaseID: plan.LeaseID, Attempt: plan.Attempt, Fence: plan.Fence, TransitionKey: key, ExpectedStatus: expected, TargetStatus: target, FailureCode: code, HealthPassed: health}, token, &ignored)
	}
	// A reclaimed lease observes the authoritative deployment status. Never
	// regress it: only a first owner advances assigned into preparation.
	if plan.Status == domain.DeploymentAssigned {
		if err = transition(domain.DeploymentAssigned, domain.DeploymentPreparing, attemptMutationKey("provenance-prepare", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
			return err
		}
		plan.Status = domain.DeploymentPreparing
	}
	// A restart after a lost cancellation-handoff response receives the same
	// server-issued receipt with a fresh fence. Replaying that receipt is the
	// only legal action; the runner never derives a replacement rollback ID.
	if plan.Status == domain.DeploymentCancelRequested {
		if plan.CancellationRequestID == nil || strings.TrimSpace(*plan.CancellationRequestID) == "" {
			return errors.New("cancel_requested deployment is missing cancellation receipt")
		}
		return composeCancellationRollback(supervisor, server, token, plan, claim, *plan.CancellationRequestID)
	}
	if plan.Status != domain.DeploymentPreparing && plan.Status != domain.DeploymentApplying && plan.Status != domain.DeploymentVerifying {
		return errors.New("deployment is not resumable by this attempt")
	}
	operationCtx, stopOperation := context.WithCancel(supervisor.Context())
	cancelWatcher := startDeploymentStatusWatcher(supervisor.Context(), supervisor, server, token, plan, stopOperation)
	defer cancelWatcher.Stop()
	authorizeCredential := func(ctx context.Context, binding domain.SecretBinding) error {
		accessID := attemptMutationKey("secret_access", claim.Lease.ID, claim.Lease.Attempt, strings.TrimSpace(binding.Name))
		var grant domain.SecretAccessGrant
		if err := postAPIIntoContext(ctx, server+"/api/v1/runners/secrets/access", app.SecretAccessInput{AccessID: accessID, RunID: plan.RunID, LeaseID: plan.LeaseID, Attempt: plan.Attempt, Fence: plan.Fence, Binding: strings.TrimSpace(binding.Name), Provider: strings.ToLower(strings.TrimSpace(binding.Provider)), Version: strings.TrimSpace(binding.Version)}, token, &grant); err != nil {
			return err
		}
		if grant.AccessID != accessID || grant.RunID != plan.RunID || grant.LeaseID != plan.LeaseID || grant.Attempt != plan.Attempt || grant.Binding != strings.TrimSpace(binding.Name) || grant.Provider != strings.ToLower(strings.TrimSpace(binding.Provider)) || grant.Version != strings.TrimSpace(binding.Version) {
			return errors.New("secret access acknowledgement did not match request")
		}
		return nil
	}
	isRollback := plan.RollbackOfID != nil && strings.TrimSpace(*plan.RollbackOfID) != ""
	prepareWorkspace := func(ctx context.Context, workspace string) (runner.PreparedComposeSecrets, error) {
		return runner.PrepareComposeSecrets(ctx, composeApplicationSecretBindings(plan.SecretBindings), secretRoot, workspace, authorizeCredential)
	}
	value, err := resolveDeploymentProvenanceWithCredentialWorkspace(operationCtx, plan, workDir, nil, secretRoot, authorizeCredential, prepareWorkspace, func(value resolvedProvenance, workspace, secretOverride string) error {
		resolutionID := attemptMutationKey("provenance", claim.Lease.ID, claim.Lease.Attempt, value.GitCommit)
		pending := runner.JournalProvenance{ID: resolutionID, Attempt: journalAttemptIdentity(plan.RunID, claim.Lease, supervisor), DeploymentID: plan.DeploymentID, GitCommit: value.GitCommit, ComposeHash: value.ComposeHash, ImageDigests: append([]string(nil), value.ImageDigests...), ContentIdentity: value.GitCommit + ":" + value.ComposeHash, CreatedAt: time.Now().UTC()}
		if _, err := journal.AppendProvenance(pending); err != nil {
			return fmt.Errorf("journal provenance before send: %w", err)
		}
		if err := replayJournalProvenance(server, token, supervisor, pending); err != nil {
			return err
		}
		if err := journal.AckProvenance(pending.ID); err != nil {
			return err
		}
		engine := newComposeEngine(nil, nil, imagePolicy)
		if err := engine.EnsureAvailability(operationCtx, plan, workspace, value, secretOverride); err != nil {
			if plan.Status == domain.DeploymentPreparing {
				return composePreApplyFailure(transition, claim, isRollback, "resolved_image_unavailable", err)
			}
			return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, plan.Status, "resolved_image_unavailable", err)
		}
		apply := func() error { return engine.Apply(operationCtx, plan, workspace, value, secretOverride) }
		// A new attempt after apply must inspect the server-owned project before
		// deciding whether mutation is necessary. Verifying may only continue if
		// that inspection confirms the immutable target still exists.
		if plan.Status == domain.DeploymentApplying || plan.Status == domain.DeploymentVerifying {
			reconciled, reconcileErr := engine.Reconcile(operationCtx, plan, workspace, value, secretOverride)
			if reconcileErr != nil {
				return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, plan.Status, "compose_reconcile_failed", reconcileErr)
			}
			if plan.Status == domain.DeploymentVerifying {
				if !reconciled {
					return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentVerifying, "compose_reconcile_failed", errors.New("verified deployment target is absent"))
				}
				if err := engine.Verify(operationCtx, plan, value); err != nil {
					return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentVerifying, "compose_health_failed", err)
				}
				target := domain.DeploymentSucceeded
				if isRollback {
					target = domain.DeploymentRolledBack
				}
				passed := true
				return transition(domain.DeploymentVerifying, target, attemptMutationKey("compose-resume-terminal", claim.Lease.ID, claim.Lease.Attempt, ""), "", &passed)
			}
			if !reconciled {
				if err := apply(); err != nil {
					return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentApplying, "compose_apply_failed", err)
				}
			}
			if err := transition(domain.DeploymentApplying, domain.DeploymentVerifying, attemptMutationKey("compose-resume-verify", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
				return err
			}
			if err := engine.Verify(operationCtx, plan, value); err != nil {
				return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentVerifying, "compose_health_failed", err)
			}
			target := domain.DeploymentSucceeded
			if isRollback {
				target = domain.DeploymentRolledBack
			}
			passed := true
			return transition(domain.DeploymentVerifying, target, attemptMutationKey("compose-resume-terminal", claim.Lease.ID, claim.Lease.Attempt, ""), "", &passed)
		}
		if reconciled, err := engine.Reconcile(operationCtx, plan, workspace, value, secretOverride); err != nil {
			return composePreApplyFailure(transition, claim, isRollback, "compose_reconcile_failed", err)
		} else if reconciled {
			if err := transition(domain.DeploymentPreparing, domain.DeploymentApplying, attemptMutationKey("compose-reconcile-apply", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
				return err
			}
			if err := transition(domain.DeploymentApplying, domain.DeploymentVerifying, attemptMutationKey("compose-reconcile-verify", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
				return err
			}
			if err := engine.Verify(operationCtx, plan, value); err != nil {
				return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentVerifying, "compose_health_failed", err)
			}
			target := domain.DeploymentSucceeded
			if isRollback {
				target = domain.DeploymentRolledBack
			}
			passed := true
			return transition(domain.DeploymentVerifying, target, attemptMutationKey("compose-reconcile-terminal", claim.Lease.ID, claim.Lease.Attempt, ""), "", &passed)
		}
		// Secret material was authorized and created before the first Compose
		// parse. Its one cleanup lifetime covers all read, reconcile, pull, and
		// mutation stages for this fenced attempt.
		if err := transition(domain.DeploymentPreparing, domain.DeploymentApplying, attemptMutationKey("compose-apply", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
			return err
		}
		if err := engine.Apply(operationCtx, plan, workspace, value, secretOverride); err != nil {
			return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentApplying, "compose_apply_failed", err)
		}
		if err := transition(domain.DeploymentApplying, domain.DeploymentVerifying, attemptMutationKey("compose-verify", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil); err != nil {
			return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentApplying, "compose_transition_failed", err)
		}
		if err := engine.Verify(operationCtx, plan, value); err != nil {
			return composePostApplyFailure(supervisor, server, token, plan, claim, isRollback, domain.DeploymentVerifying, "compose_health_failed", err)
		}
		target := domain.DeploymentSucceeded
		if isRollback {
			target = domain.DeploymentRolledBack
		}
		passed := true
		return transition(domain.DeploymentVerifying, target, attemptMutationKey("compose-terminal", claim.Lease.ID, claim.Lease.Attempt, ""), "", &passed)
	})
	if err != nil {
		receipt := cancelWatcher.Receipt()
		if receipt == "" {
			receipt = deploymentCancellationReceipt(supervisor, server, token, plan)
		}
		if receipt != "" && !isRollback {
			return composeCancellationRollback(supervisor, server, token, plan, claim, receipt)
		}
		// A pre-apply cancel revokes the lease in the same transaction as the
		// terminal deployment state. The canceled operation must not write again,
		// but it is an expected fenced outcome rather than a fatal runner process
		// error that prevents later claims.
		if supervisor.Context().Err() != nil {
			return nil
		}
		if supervisor.Context().Err() == nil {
			// Once the callback has started it reports post-apply failures through
			// the atomic rollback protocol. A failure still in preparation is safe
			// to terminalize directly.
			target := domain.DeploymentFailed
			if isRollback {
				// A rollback child has no ordinary failed terminal.  Even a
				// preparation failure is reported through its explicit rollback
				// verification path so the source settles loudly and atomically.
				_ = transition(domain.DeploymentPreparing, domain.DeploymentApplying, attemptMutationKey("rollback-prepare-failed-apply", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil)
				_ = transition(domain.DeploymentApplying, domain.DeploymentVerifying, attemptMutationKey("rollback-prepare-failed-verify", claim.Lease.ID, claim.Lease.Attempt, ""), "", nil)
				target = domain.DeploymentRollbackFailed
			}
			expected := domain.DeploymentPreparing
			if isRollback {
				expected = domain.DeploymentVerifying
			}
			_ = transition(expected, target, attemptMutationKey("provenance-fail", claim.Lease.ID, claim.Lease.Attempt, ""), "provenance_resolution_failed", nil)
		}
		return err
	}
	_ = value
	return nil
}

// composeApplicationSecretBindings selects the file-target bindings used by
// the application. Repository credentials remain confined to provenance
// transport setup and are never exposed to Compose.
func composeApplicationSecretBindings(bindings []domain.SecretBinding) []domain.SecretBinding {
	selected := make([]domain.SecretBinding, 0, len(bindings))
	for _, binding := range bindings {
		if strings.HasPrefix(strings.TrimSpace(binding.Target), "file:") {
			selected = append(selected, binding)
		}
	}
	return selected
}

func composeCancellationRollback(supervisor *attemptSupervisor, server, token string, plan domain.DeploymentPlan, claim domain.ClaimedRun, receipt string) error {
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer cancel()
	var ignored domain.DeploymentFailureRollbackResult
	return postAPIIntoContext(ctx, server+"/api/v1/runners/deployments/fail-and-rollback", app.DeploymentFailureRollbackInput{DeploymentID: plan.DeploymentID, RunID: plan.RunID, LeaseID: plan.LeaseID, Attempt: plan.Attempt, Fence: plan.Fence, RequestID: receipt, CancellationRequestID: receipt, ExpectedStatus: domain.DeploymentCancelRequested, FailureCode: "cancellation_requested"}, token, &ignored)
}

func composePreApplyFailure(transition func(string, string, string, string, *bool) error, claim domain.ClaimedRun, isRollback bool, code string, cause error) error {
	if isRollback {
		if err := transition(domain.DeploymentPreparing, domain.DeploymentApplying, attemptMutationKey("rollback-preapply-failed-apply", claim.Lease.ID, claim.Lease.Attempt, code), "", nil); err != nil {
			return err
		}
		if err := transition(domain.DeploymentApplying, domain.DeploymentVerifying, attemptMutationKey("rollback-preapply-failed-verify", claim.Lease.ID, claim.Lease.Attempt, code), "", nil); err != nil {
			return err
		}
		return transition(domain.DeploymentVerifying, domain.DeploymentRollbackFailed, attemptMutationKey("rollback-preapply-failed-terminal", claim.Lease.ID, claim.Lease.Attempt, code), code, nil)
	}
	if err := transition(domain.DeploymentPreparing, domain.DeploymentFailed, attemptMutationKey("compose-preapply-failed", claim.Lease.ID, claim.Lease.Attempt, code), code, nil); err != nil {
		return err
	}
	// The failed deployment is durably terminal. Returning an execution error
	// here would terminate the long-lived runner before it can claim later work.
	return nil
}

func composePostApplyFailure(supervisor *attemptSupervisor, server, token string, plan domain.DeploymentPlan, claim domain.ClaimedRun, isRollback bool, expected string, code string, cause error) error {
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	defer cancel()
	if isRollback {
		var ignored domain.Deployment
		err = postAPIIntoContext(ctx, server+"/api/v1/runners/deployments/transition", app.DeploymentTransitionInput{DeploymentID: plan.DeploymentID, RunID: plan.RunID, LeaseID: plan.LeaseID, Attempt: plan.Attempt, Fence: plan.Fence, TransitionKey: attemptMutationKey("rollback-failed", claim.Lease.ID, claim.Lease.Attempt, ""), ExpectedStatus: expected, TargetStatus: domain.DeploymentRollbackFailed, FailureCode: code}, token, &ignored)
		if err != nil {
			return err
		}
		return nil
	}
	requestID := attemptMutationKey("compose-failure", claim.Lease.ID, claim.Lease.Attempt, code)
	var ignored domain.DeploymentFailureRollbackResult
	err = postAPIIntoContext(ctx, server+"/api/v1/runners/deployments/fail-and-rollback", app.DeploymentFailureRollbackInput{DeploymentID: plan.DeploymentID, RunID: plan.RunID, LeaseID: plan.LeaseID, Attempt: plan.Attempt, Fence: plan.Fence, RequestID: requestID, ExpectedStatus: expected, FailureCode: code}, token, &ignored)
	if err != nil {
		return err
	}
	// The atomic endpoint created the recovery child and settled the source.
	// This is an expected per-deployment outcome, not a runner-fatal error.
	return nil
}

func sourcePolicyFromPlan(p domain.RepositoryPolicy) source.RepositoryPolicy {
	return source.RepositoryPolicy{Version: p.Version, State: p.State, Mode: p.Mode, AllowedSchemes: p.AllowedSchemes, AllowedHosts: p.AllowedHosts, AllowedCIDRs: p.AllowedCIDRs, RedirectHosts: p.RedirectHosts, SSHHostFingerprints: p.SSHHostFingerprints, CredentialReferenceID: p.CredentialReferenceID, AllowInternal: p.AllowInternal}
}

func resolveDeploymentProvenance(ctx context.Context, plan domain.DeploymentPlan, root string, command provenanceCommand) (resolvedProvenance, error) {
	return resolveDeploymentProvenanceWithCredential(ctx, plan, root, command, "", nil)
}

func resolveDeploymentProvenanceWithCredential(ctx context.Context, plan domain.DeploymentPlan, root string, command provenanceCommand, secretRoot string, authorize runner.SecretAuthorizer) (resolvedProvenance, error) {
	return resolveDeploymentProvenanceWithCredentialWorkspace(ctx, plan, root, command, secretRoot, authorize, nil, nil)
}

// resolveDeploymentProvenanceWithCredentialWorkspace keeps the checked-out
// immutable input alive only while the caller performs its fenced deployment
// action. It is intentionally not exported: no other runner path may reuse a
// checkout after the controlling attempt ends.
func resolveDeploymentProvenanceWithCredentialWorkspace(ctx context.Context, plan domain.DeploymentPlan, root string, command provenanceCommand, secretRoot string, authorize runner.SecretAuthorizer, prepareWorkspace func(context.Context, string) (runner.PreparedComposeSecrets, error), afterResolved func(resolvedProvenance, string, string) error) (resolvedProvenance, error) {
	provenanceDiagnostic("resolve", "start")
	policy := sourcePolicyFromPlan(plan.RepositoryPolicy)
	repositoryURL, err := policy.ValidateURL(plan.RepositoryURL, false)
	if err != nil {
		return resolvedProvenance{}, fmt.Errorf("source policy: %w", err)
	}
	// Resolve immediately before constructing Git's transport configuration. The
	// chosen answer is supplied to libcurl as a host/port/IP pin, so a later DNS
	// rebind cannot affect the dial target while TLS SNI and Host remain the
	// original policy-authorized hostname.
	ips, err := lookupRepositoryIP(ctx, "ip", repositoryURL.Hostname())
	if err != nil || policy.ValidateResolvedHost(repositoryURL.Hostname(), netIPAddrs(ips)) != nil {
		return resolvedProvenance{}, errors.New("repository DNS policy rejected source")
	}
	pinnedIP, err := selectPinnedAddress(policy, ips)
	if err != nil {
		return resolvedProvenance{}, err
	}
	if repositoryURL.Scheme != "https" && repositoryURL.Scheme != "ssh" && (repositoryURL.Scheme != "http" || policy.Mode != "internal" || !policy.AllowInternal) {
		return resolvedProvenance{}, errors.New("repository transport is not permitted")
	}
	if !requestedGitRef(plan.RequestedRef) {
		return resolvedProvenance{}, errors.New("requested repository ref is invalid")
	}
	if command == nil && repositoryURL.Scheme != "ssh" {
		if !gitCurlResolveAvailable(ctx) {
			return resolvedProvenance{}, errors.New("installed Git lacks http.curloptResolve; refusing uncontrolled provenance fetch")
		}
	}
	if command == nil {
		command = osProvenanceCommand{}
	}
	provenanceDiagnostic("git", "available")
	if err := os.MkdirAll(root, 0700); err != nil {
		return resolvedProvenance{}, err
	}
	workspace, err := os.MkdirTemp(root, "nerocd-provenance-")
	if err != nil {
		return resolvedProvenance{}, err
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if err := os.Chmod(workspace, 0700); err != nil {
		return resolvedProvenance{}, err
	}
	gitEnvironment := []string(nil)
	if repositoryURL.Scheme == "https" && strings.TrimSpace(policy.CredentialReferenceID) != "" {
		return resolvedProvenance{}, errors.New("HTTPS credential references are unsupported by the controlled transport")
	}
	if repositoryURL.Scheme == "ssh" {
		if authorize == nil || strings.TrimSpace(secretRoot) == "" {
			return resolvedProvenance{}, errors.New("SSH provenance requires a fenced runner_file credential resolver")
		}
		sshEnvironment, cleanup, err := prepareSSHTransport(ctx, command, workspace, secretRoot, plan, policy, repositoryURL, pinnedIP, authorize)
		if err != nil {
			return resolvedProvenance{}, err
		}
		defer cleanup()
		gitEnvironment = sshEnvironment
	}
	// Detached init/fetch avoids remote tracking refs and leaves no branch name
	// that could move after the policy-checked fetch.
	gitBase := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "-c", "credential.helper=", "-c", "http.followRedirects=false"}
	provenanceDiagnostic("git_init", "start")
	if _, err := runProvenanceCommand(command, ctx, "git", append(append([]string{}, gitBase...), "init", "--quiet", workspace), "", gitEnvironment); err != nil {
		return resolvedProvenance{}, commandFailure("git init", err)
	}
	provenanceDiagnostic("git_remote", "start")
	if _, err := runProvenanceCommand(command, ctx, "git", append(append([]string{}, gitBase...), "remote", "add", "origin", repositoryURL.String()), workspace, gitEnvironment); err != nil {
		return resolvedProvenance{}, commandFailure("git remote add", err)
	}
	fetchArgs := append(append([]string{}, gitBase...), "fetch", "--no-tags", "--depth=1", "origin", plan.RequestedRef)
	if repositoryURL.Scheme != "ssh" {
		resolveOption, err := gitCurlResolveOption(repositoryURL, pinnedIP)
		if err != nil {
			return resolvedProvenance{}, err
		}
		fetchArgs = append(append([]string{}, gitBase...), "-c", "http.curloptResolve="+resolveOption, "fetch", "--no-tags", "--depth=1", "origin", plan.RequestedRef)
	}
	provenanceDiagnostic("git_fetch", "start")
	if _, err := runProvenanceCommand(command, ctx, "git", fetchArgs, workspace, gitEnvironment); err != nil {
		return resolvedProvenance{}, commandFailure("git fetch", err)
	}
	provenanceDiagnostic("git_checkout", "start")
	if _, err := runProvenanceCommand(command, ctx, "git", append(append([]string{}, gitBase...), "checkout", "--detach", "--quiet", "FETCH_HEAD"), workspace, gitEnvironment); err != nil {
		return resolvedProvenance{}, commandFailure("git checkout", err)
	}
	provenanceDiagnostic("git_rev_parse", "start")
	head, err := runProvenanceCommand(command, ctx, "git", []string{"rev-parse", "--verify", "HEAD^{commit}"}, workspace, gitEnvironment)
	if err != nil {
		return resolvedProvenance{}, commandFailure("git rev-parse", err)
	}
	commit := strings.TrimSpace(string(head))
	if !isHexCommit(commit) {
		return resolvedProvenance{}, errors.New("checkout did not produce a full immutable commit")
	}
	composePath := filepath.Clean(plan.ComposePath)
	if filepath.IsAbs(composePath) || composePath == "." || strings.HasPrefix(composePath, ".."+string(os.PathSeparator)) {
		return resolvedProvenance{}, errors.New("compose path escapes checkout")
	}
	if !composeProjectName(plan.ComposeProject) {
		return resolvedProvenance{}, errors.New("invalid compose project name")
	}
	secretOverride := ""
	secretSources := map[string]string(nil)
	if prepareWorkspace != nil {
		prepared, prepareErr := prepareWorkspace(ctx, workspace)
		if prepareErr != nil {
			return resolvedProvenance{}, prepareErr
		}
		defer prepared.Cleanup()
		secretOverride = prepared.OverridePath
		secretSources = prepared.DescriptorSources
	}
	// Compose otherwise auto-loads a checkout-controlled .env file. Its only
	// input here is a runner-created server value; deployment secrets never take
	// part in provenance resolution. This also lets a service expose the exact
	// resolved immutable revision from a health endpoint without trusting a
	// checkout-selected value.
	emptyEnv := filepath.Join(workspace, ".nerocd-provenance.env")
	if err := os.WriteFile(emptyEnv, []byte(deploymentRevisionEnv+"="+commit+"\n"), 0600); err != nil {
		return resolvedProvenance{}, err
	}
	composeArgs := []string{"compose", "--project-name", plan.ComposeProject, "--env-file", emptyEnv, "--file", composePath}
	if secretOverride != "" {
		composeArgs = append(composeArgs, "--file", secretOverride)
	}
	profiles := append([]string(nil), plan.Profiles...)
	sort.Strings(profiles)
	for _, profile := range profiles {
		if strings.TrimSpace(profile) == "" || strings.ContainsAny(profile, "\x00\r\n") {
			return resolvedProvenance{}, errors.New("invalid compose profile")
		}
		composeArgs = append(composeArgs, "--profile", profile)
	}
	composeArgs = append(composeArgs, "config", "--format", "json")
	provenanceDiagnostic("docker_compose_config", "start")
	compose, err := runProvenanceCommand(command, ctx, "docker", composeArgs, workspace, gitEnvironment)
	if err != nil {
		return resolvedProvenance{}, commandFailure("docker compose config", err)
	}
	canonical, images, err := canonicalComposeWithSecretSources(compose, plan.ComposeProject, composeApplicationSecretBindings(plan.SecretBindings), secretSources)
	if err != nil {
		return resolvedProvenance{}, err
	}
	sum := sha256.Sum256(canonical)
	resolved := resolvedProvenance{GitCommit: commit, ComposeHash: "sha256:" + hex.EncodeToString(sum[:]), ImageDigests: images}
	if afterResolved != nil {
		if err := afterResolved(resolved, workspace, secretOverride); err != nil {
			return resolvedProvenance{}, err
		}
	}
	return resolved, nil
}

func netIPAddrs(values []netip.Addr) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(values))
	for _, value := range values {
		out = append(out, net.IPAddr{IP: net.IP(value.AsSlice())})
	}
	return out
}

func selectPinnedAddress(policy source.RepositoryPolicy, values []netip.Addr) (netip.Addr, error) {
	for _, value := range values {
		if err := policy.ValidateAddress(value); err == nil {
			return value.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("repository DNS policy rejected source")
}

// Git intentionally permits arbitrary config keys, so `git config --get` is
// not a feature probe. `git help --config` is generated from this installed
// Git's supported-variable table and lets us fail closed before any network
// traffic on releases without the libcurl resolve bridge.
func nativeGitCurlResolveAvailable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, "git", "help", "--config")
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	return err == nil && bytes.Contains(output, []byte("http.curloptResolve\n"))
}

// gitCurlResolveOption produces the libcurl CURLOPT_RESOLVE syntax accepted by
// modern Git.  It connects to this literal IP while retaining the URL hostname
// for TLS verification/SNI and HTTP Host.  The option is deliberately passed
// only to fetch; no global config is mutated.
func gitCurlResolveOption(repositoryURL *url.URL, address netip.Addr) (string, error) {
	if repositoryURL == nil || repositoryURL.Hostname() == "" || !address.IsValid() {
		return "", errors.New("controlled Git transport is unavailable")
	}
	port := 443
	if repositoryURL.Scheme == "http" {
		port = 80
	}
	if raw := repositoryURL.Port(); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", errors.New("repository port is invalid")
		}
		port = parsed
	}
	ip := address.String()
	if address.Is6() {
		ip = "[" + ip + "]"
	}
	return strings.ToLower(strings.TrimSuffix(repositoryURL.Hostname(), ".")) + ":" + strconv.Itoa(port) + ":" + ip, nil
}

func prepareSSHTransport(ctx context.Context, command provenanceCommand, workspace, secretRoot string, plan domain.DeploymentPlan, policy source.RepositoryPolicy, repositoryURL *url.URL, pinnedIP netip.Addr, authorize runner.SecretAuthorizer) ([]string, func(), error) {
	provenanceDiagnostic("ssh_credential", "start")
	reference := strings.TrimSpace(policy.CredentialReferenceID)
	if reference == "" {
		return nil, nil, errors.New("SSH repository policy requires a credential reference")
	}
	var binding *domain.SecretBinding
	for i := range plan.SecretBindings {
		candidate := &plan.SecretBindings[i]
		if strings.TrimSpace(candidate.Reference) == reference && strings.EqualFold(strings.TrimSpace(candidate.Provider), domain.ProviderRunnerFile) {
			if binding != nil {
				return nil, nil, errors.New("SSH credential reference is ambiguous")
			}
			binding = candidate
		}
	}
	if binding == nil {
		return nil, nil, errors.New("SSH credential reference is not an authorized runner_file binding")
	}
	if err := authorize(ctx, *binding); err != nil {
		return nil, nil, errors.New("SSH credential access was denied")
	}
	resolver, err := runner.OpenFileSecretResolver(secretRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("open SSH credential resolver: %w", err)
	}
	defer func() { _ = resolver.Close() }()
	privateKey, err := resolver.ReadBytes(reference)
	if err != nil || !bytes.Contains(privateKey, []byte("PRIVATE KEY-----")) {
		return nil, nil, errors.New("SSH credential is not a private key")
	}
	keyPath := filepath.Join(workspace, ".nerocd-identity")
	if err := os.WriteFile(keyPath, privateKey, 0600); err != nil {
		return nil, nil, err
	}
	knownHosts, err := pinnedKnownHosts(ctx, command, repositoryURL, pinnedIP, policy.SSHHostFingerprints)
	if err != nil {
		_ = os.Remove(keyPath)
		return nil, nil, err
	}
	provenanceDiagnostic("ssh_transport", "ready")
	knownHostsPath := filepath.Join(workspace, ".nerocd-known-hosts")
	if err := os.WriteFile(knownHostsPath, knownHosts, 0600); err != nil {
		_ = os.Remove(keyPath)
		return nil, nil, err
	}
	port := 22
	if raw := repositoryURL.Port(); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			_ = os.Remove(keyPath)
			_ = os.Remove(knownHostsPath)
			return nil, nil, errors.New("repository port is invalid")
		}
		port = parsed
	}
	host := strings.ToLower(strings.TrimSuffix(repositoryURL.Hostname(), "."))
	sshCommand := "ssh -F /dev/null -o BatchMode=yes -o PreferredAuthentications=publickey -o PubkeyAuthentication=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ChallengeResponseAuthentication=no -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + shellQuote(knownHostsPath) + " -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias=" + shellQuote(host) + " -o HostName=" + shellQuote(pinnedIP.String()) + " -o Port=" + strconv.Itoa(port) + " -i " + shellQuote(keyPath)
	cleanup := func() { _ = os.Remove(keyPath); _ = os.Remove(knownHostsPath) }
	return []string{"GIT_SSH_COMMAND=" + sshCommand}, cleanup, nil
}

func pinnedKnownHosts(ctx context.Context, command provenanceCommand, repositoryURL *url.URL, address netip.Addr, fingerprints []string) ([]byte, error) {
	if len(fingerprints) == 0 {
		return nil, errors.New("SSH repository policy requires host fingerprints")
	}
	port := "22"
	if value := repositoryURL.Port(); value != "" {
		port = value
	}
	provenanceDiagnostic("ssh_keyscan", "start")
	output, err := runProvenanceCommand(command, ctx, "ssh-keyscan", []string{"-T", "5", "-p", port, address.String()}, "", nil)
	if err != nil {
		return nil, errors.New("SSH host-key scan failed")
	}
	allowed := make(map[string]struct{}, len(fingerprints))
	for _, value := range fingerprints {
		allowed[strings.TrimSpace(value)] = struct{}{}
	}
	host := strings.ToLower(strings.TrimSuffix(repositoryURL.Hostname(), "."))
	var selected []string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !allowedSSHHostKeyAlgorithm(fields[1]) {
			continue
		}
		key, decodeErr := base64.StdEncoding.DecodeString(fields[2])
		if decodeErr != nil || !validSSHHostKeyBlob(fields[1], key) {
			continue
		}
		sum := sha256.Sum256(key)
		fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
		if _, ok := allowed[fingerprint]; ok {
			selected = append(selected, host+" "+fields[1]+" "+fields[2])
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("SSH host key does not match repository policy")
	}
	provenanceDiagnostic("ssh_fingerprint", "matched")
	sort.Strings(selected)
	return []byte(strings.Join(selected, "\n") + "\n"), nil
}

func allowedSSHHostKeyAlgorithm(value string) bool {
	switch value {
	case "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa-sha2-256", "rsa-sha2-512":
		return true
	default:
		// ssh-rsa uses SHA-1 signatures and is intentionally not admitted by
		// this policy version. A future explicitly-versioned policy can opt in.
		return false
	}
}

func validSSHHostKeyBlob(algorithm string, blob []byte) bool {
	if len(blob) < 8 || len(blob) > 16<<10 {
		return false
	}
	nameLength := int(binary.BigEndian.Uint32(blob[:4]))
	if nameLength <= 0 || nameLength > len(blob)-4 || string(blob[4:4+nameLength]) != algorithm {
		return false
	}
	// Require at least one following SSH wire field (curve/key, exponent/modulus,
	// or public key) and ensure its declared size remains within the blob.
	offset := 4 + nameLength
	if offset+4 > len(blob) {
		return false
	}
	fieldLength := int(binary.BigEndian.Uint32(blob[offset : offset+4]))
	return fieldLength > 0 && offset+4+fieldLength <= len(blob)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func composeProjectName(value string) bool {
	return composeProjectPattern.MatchString(strings.ToLower(strings.TrimSpace(value))) && value == strings.ToLower(strings.TrimSpace(value))
}

func requestedGitRef(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n\t ")
}

func provenanceDiagnostic(stage, status string) {
	// This output is intentionally a fixed vocabulary. Command output, URLs,
	// identities, paths, credentials, fences, and other attempt authority never
	// enter runner logs because Git/SSH diagnostics can contain any of them.
	fmt.Fprintf(os.Stderr, "provenance stage=%s status=%s\n", stage, status)
}

func commandFailure(stage string, err error) error {
	// Keep runner-visible failures useful without exposing command output. Exit
	// code and Go error type distinguish a remote command failure from a timeout
	// or unavailable executable, but are not attacker-controlled content.
	code := -1
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		code = exited.ExitCode()
	}
	reason := "unknown"
	var execution *provenanceExecutionError
	if errors.As(err, &execution) && execution.reason != "" {
		reason = execution.reason
	}
	provenanceDiagnostic(strings.ReplaceAll(stage, " ", "_"), "failed_"+reason+"_exit_"+strconv.Itoa(code))
	return fmt.Errorf("%s failed (exit=%d)", stage, code)
}

func classifyProvenanceCommandOutput(output []byte) string {
	// These categories are constants, deliberately not excerpts. They are only
	// an operator aid: raw Git/SSH output may carry repository URL components,
	// user names, hostnames, paths, credentials, or server-controlled text.
	text := strings.ToLower(string(output))
	switch {
	case strings.Contains(text, "invalid reference format"), strings.Contains(text, "invalid image reference"):
		return "image_reference"
	case strings.Contains(text, "no such image"), strings.Contains(text, "unable to get image"), strings.Contains(text, "manifest unknown"):
		return "image_unavailable"
	case strings.Contains(text, "pull access denied"), strings.Contains(text, "authentication required"):
		return "image_access"
	case strings.Contains(text, "port is already allocated"), strings.Contains(text, "bind: address already in use"):
		return "port_conflict"
	case strings.Contains(text, "permission denied while trying to connect"), strings.Contains(text, "cannot connect to the docker daemon"):
		return "docker_access"
	case strings.Contains(text, "host key verification failed"), strings.Contains(text, "remote host identification has changed"):
		return "host_key"
	case strings.Contains(text, "permission denied"), strings.Contains(text, "publickey"):
		return "authentication"
	case strings.Contains(text, "not a git repository"), strings.Contains(text, "does not appear to be a git repository"):
		return "repository"
	case strings.Contains(text, "bad owner or permissions"), strings.Contains(text, "unprotected private key file"):
		return "permissions"
	case strings.Contains(text, "not found"), strings.Contains(text, "no such file"):
		return "unavailable"
	default:
		return "unknown"
	}
}
func isHexCommit(v string) bool {
	if len(v) != 40 && len(v) != 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func canonicalCompose(raw []byte, serverProject string, secretBindings ...[]domain.SecretBinding) ([]byte, []string, error) {
	if len(secretBindings) > 1 {
		return nil, nil, errors.New("compose canonicalization accepts one secret binding set")
	}
	var bindings []domain.SecretBinding
	if len(secretBindings) == 1 {
		bindings = secretBindings[0]
	}
	return canonicalComposeWithSecretSources(raw, serverProject, bindings, nil)
}

func canonicalComposeWithSecretSources(raw []byte, serverProject string, bindings []domain.SecretBinding, sources map[string]string) ([]byte, []string, error) {
	if len(raw) > 4<<20 {
		return nil, nil, errors.New("compose config exceeds limit")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, errors.New("compose config is not JSON")
	}
	// Compose v2 emits the effective project name in `config --format json`,
	// even when the checkout did not declare one. The runner supplies that name
	// with --project-name, so accept exactly that server-owned value and remove
	// it from provenance: it is execution metadata, not checkout content.
	if name, exists := doc["name"]; exists {
		if configured, ok := name.(string); !ok || configured != serverProject {
			return nil, nil, errors.New("compose config overrides the server-owned project name")
		}
		delete(doc, "name")
	}
	if err := normalizeComposeSecretDescriptors(doc, serverProject, bindings, sources); err != nil {
		return nil, nil, err
	}
	// A deployment may not attach itself to pre-existing engine objects.  The
	// later adapter creates only a controlled project namespace.
	for _, section := range []string{"networks", "volumes"} {
		if values, exists := doc[section].(map[string]any); exists {
			for _, rawValue := range values {
				if value, ok := rawValue.(map[string]any); ok && value["external"] == true {
					return nil, nil, fmt.Errorf("compose uses external %s", section)
				}
			}
		}
	}
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 || len(services) > 128 {
		return nil, nil, errors.New("compose config has no bounded services")
	}
	images := make([]string, 0, len(services))
	for _, value := range services {
		svc, ok := value.(map[string]any)
		if !ok {
			return nil, nil, errors.New("invalid compose service")
		}
		for _, forbidden := range []string{"build", "privileged", "devices", "cap_add", "network_mode", "pid", "ipc", "volumes", "volumes_from", "security_opt", "userns_mode", "extra_hosts", "links", "container_name"} {
			if _, exists := svc[forbidden]; exists {
				return nil, nil, fmt.Errorf("compose uses forbidden %s", forbidden)
			}
		}
		image, ok := svc["image"].(string)
		if !ok || validateProductionImageReference(strings.TrimSpace(image)) != nil {
			return nil, nil, errors.New("compose services require digest-pinned prebuilt images")
		}
		images = append(images, strings.TrimSpace(image))
	}
	sort.Strings(images)
	for i := 1; i < len(images); i++ {
		if images[i] == images[i-1] {
			images = append(images[:i], images[i+1:]...)
			i--
		}
	}
	canonical, err := json.Marshal(doc) // encoding/json deterministically sorts map keys
	return canonical, images, err
}

// normalizeComposeSecretDescriptors removes attempt-specific file paths from
// the provenance input. Runtime Compose still receives the real private paths;
// only the content hash sees stable placeholders derived from validated binding
// targets, never from secret values.
func normalizeComposeSecretDescriptors(doc map[string]any, serverProject string, bindings []domain.SecretBinding, sources map[string]string) error {
	allowed := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		target := strings.TrimSpace(binding.Target)
		name, ok := strings.CutPrefix(target, "file:")
		if !ok || name == "" {
			return errors.New("compose secret binding target is invalid")
		}
		if _, exists := allowed[name]; exists {
			return errors.New("compose secret binding target is duplicated")
		}
		allowed[name] = struct{}{}
	}
	if len(allowed) == 0 {
		if secrets, exists := doc["secrets"]; exists {
			values, ok := secrets.(map[string]any)
			if !ok || len(values) != 0 {
				return errors.New("compose declares unbound secrets")
			}
		}
		return validateComposeServiceSecrets(doc, allowed)
	}
	secrets, ok := doc["secrets"].(map[string]any)
	if !ok || len(secrets) != len(allowed) {
		return errors.New("effective compose secrets do not exactly match authorized bindings")
	}
	for name := range allowed {
		descriptor, ok := secrets[name].(map[string]any)
		if !ok {
			return fmt.Errorf("compose secret override missing target %q", name)
		}
		expectedFields := 1
		if sources != nil {
			expectedName := serverProject + "_" + name
			derivedName, ok := descriptor["name"].(string)
			if !ok || derivedName != expectedName {
				return fmt.Errorf("compose secret descriptor for target %q has an invalid derived name", name)
			}
			expectedFields++
		}
		if len(descriptor) != expectedFields {
			return fmt.Errorf("compose secret descriptor for target %q has unsupported fields", name)
		}
		file, ok := descriptor["file"].(string)
		if !ok || strings.TrimSpace(file) == "" {
			return fmt.Errorf("compose secret override for target %q is invalid", name)
		}
		if sources != nil && file != sources[name] {
			return fmt.Errorf("compose secret descriptor for target %q does not use its validated source", name)
		}
		descriptor["file"] = "nerocd-secret://" + name
	}
	return validateComposeServiceSecrets(doc, allowed)
}

func validateComposeServiceSecrets(doc map[string]any, allowed map[string]struct{}) error {
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return errors.New("compose config has no services")
	}
	for serviceName, rawService := range services {
		service, ok := rawService.(map[string]any)
		if !ok {
			return errors.New("invalid compose service")
		}
		rawSecrets, exists := service["secrets"]
		if !exists {
			continue
		}
		entries, ok := rawSecrets.([]any)
		if !ok {
			return fmt.Errorf("compose service %q has invalid secret references", serviceName)
		}
		for _, rawEntry := range entries {
			name, err := composeServiceSecretName(rawEntry)
			if err != nil {
				return fmt.Errorf("compose service %q has invalid secret reference: %w", serviceName, err)
			}
			if _, ok := allowed[name]; !ok {
				return fmt.Errorf("compose service %q references unbound secret %q", serviceName, name)
			}
		}
	}
	return nil
}

func composeServiceSecretName(raw any) (string, error) {
	if name, ok := raw.(string); ok && strings.TrimSpace(name) != "" {
		return name, nil
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return "", errors.New("unsupported secret reference")
	}
	name, ok := entry["source"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", errors.New("secret source is required")
	}
	return name, nil
}
