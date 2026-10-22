package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestExecutionRequestValidationIsTyped(t *testing.T) {
	service := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	runnerContext := auth.WithPrincipal(t.Context(), auth.Principal{
		ID:       "runner_validation",
		Name:     "Validation Runner",
		Roles:    []string{domain.RoleRunner},
		Provider: domain.PrincipalRunner,
	})
	validEnrollmentToken := "nce_" + strings.Repeat("0", 64)

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "artifact fields",
			call: func() error {
				_, err := service.CreateArtifact(runnerContext, ArtifactInput{})
				return err
			},
			want: "run_id, lease_id, name, and path are required",
		},
		{
			name: "event batch fields",
			call: func() error {
				_, err := service.AppendRunEvents(runnerContext, RunEventBatchInput{})
				return err
			},
			want: "run_id and lease_id are required",
		},
		{
			name: "template run request",
			call: func() error {
				_, err := service.RequestRun(admin, " ")
				return err
			},
			want: "template_id is required",
		},
		{
			name: "inline run project",
			call: func() error {
				_, err := service.RequestRunWithSpec(admin, RunRequestInput{})
				return err
			},
			want: "project_id is required",
		},
		{
			name: "inline run specification",
			call: func() error {
				_, err := service.RequestRunWithSpec(admin, RunRequestInput{
					ProjectID: "proj_platform",
					RunSpec:   domain.RunSpec{Type: domain.RunTypeShell},
				})
				return err
			},
			want: "shell run requires run_spec.process.command or run_spec.inputs.command",
		},
		{
			name: "runner enrollment",
			call: func() error {
				_, err := service.CreateRunnerEnrollment(admin, RunnerEnrollmentInput{})
				return err
			},
			want: "runner_id is invalid",
		},
		{
			name: "runner enrollment revocation",
			call: func() error {
				_, err := service.RevokeRunnerEnrollment(admin, RunnerEnrollmentRevokeInput{})
				return err
			},
			want: "enrollment_id is required",
		},
		{
			name: "runner enrollment consumption",
			call: func() error {
				_, err := service.ConsumeRunnerEnrollment(t.Context(), validEnrollmentToken, RunnerEnrollmentConsumeInput{})
				return err
			},
			want: "request_id or credential_hash is invalid",
		},
		{
			name: "runner registration",
			call: func() error {
				_, err := service.RegisterRunner(admin, RunnerInput{ID: "runner_validation"})
				return err
			},
			want: "capabilities are required",
		},
		{
			name: "runner token rotation",
			call: func() error {
				_, err := service.RotateRunnerToken(admin, RunnerTokenInput{})
				return err
			},
			want: "runner_id is required",
		},
		{
			name: "runner token revocation",
			call: func() error {
				_, err := service.RevokeRunnerToken(admin, RunnerTokenInput{})
				return err
			},
			want: "runner_id is required",
		},
		{
			name: "runner telemetry",
			call: func() error {
				return service.RecordRunnerOperationalTelemetry(runnerContext, RunnerOperationalTelemetry{JournalDepth: -1})
			},
			want: "runner telemetry is invalid",
		},
		{
			name: "lease renewal fields",
			call: func() error {
				_, err := service.RenewLease(runnerContext, "", "", 0)
				return err
			},
			want: "lease_id, attempt, and fence are required",
		},
		{
			name: "lease completion status",
			call: func() error {
				_, err := service.CompleteLease(runnerContext, "lease", "unknown", 1, "fence", "completion")
				return err
			},
			want: "lease completion status is invalid",
		},
		{
			name: "lease observation fields",
			call: func() error {
				_, err := service.RunnerLease(runnerContext, "", 0, "")
				return err
			},
			want: "lease_id, attempt, and fence are required",
		},
		{
			name: "secret access authority fields",
			call: func() error {
				_, err := service.AuthorizeSecretAccess(runnerContext, SecretAccessInput{})
				return err
			},
			want: "access_id, run_id, lease_id, attempt, fence, binding, and provider are required",
		},
		{
			name: "bootstrap input",
			call: func() error {
				_, err := service.BootstrapAdmin(t.Context(), BootstrapAdminInput{})
				return err
			},
			want: "bootstrap email, name, and password are required",
		},
		{
			name: "api token creation",
			call: func() error {
				_, err := service.CreateAPIToken(admin, APITokenInput{})
				return err
			},
			want: "name is required",
		},
		{
			name: "api token revocation",
			call: func() error {
				_, err := service.RevokeAPIToken(admin, RevokeAPITokenInput{})
				return err
			},
			want: "token_id is required",
		},
		{
			name: "session revocation",
			call: func() error {
				_, err := service.RevokeSession(admin, " ")
				return err
			},
			want: "session_id is required",
		},
		{
			name: "retention execution request ID",
			call: func() error {
				_, err := service.ExecuteRunLogRetention(admin, RunLogRetentionExecuteInput{PolicyVersion: 1})
				return err
			},
			want: "request ID is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("request unexpectedly succeeded")
			}
			if got := err.Error(); got != test.want {
				t.Errorf("error text = %q, want %q", got, test.want)
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error %v does not match ErrInvalidInput", err)
			}
		})
	}
}

func TestRequestRunWithSpecValidationPreservesRequestContext(t *testing.T) {
	service := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	tests := []struct {
		name   string
		input  RunRequestInput
		prefix string
		detail string
	}{
		{
			name:   "run spec",
			input:  RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Process: &domain.ProcessSpec{}}},
			prefix: "invalid run spec:",
			detail: "run_spec.process.command is required",
		},
		{
			name: "workflow",
			input: RunRequestInput{
				ProjectID: "proj_platform",
				RunSpec:   domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}},
				Workflow:  domain.Workflow{Steps: []domain.WorkflowStep{{RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}}},
			},
			prefix: "invalid workflow:",
			detail: "workflow step id is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.RequestRunWithSpec(admin, test.input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
			}
			if !strings.HasPrefix(err.Error(), test.prefix) {
				t.Errorf("error = %q, want prefix %q", err, test.prefix)
			}
			if !strings.Contains(err.Error(), test.detail) {
				t.Errorf("error = %q, want detail %q", err, test.detail)
			}
		})
	}
}

func TestSecretAccessValidationPreservesMetadataCause(t *testing.T) {
	service := newTestService()
	runnerContext := auth.WithPrincipal(t.Context(), auth.Principal{
		ID:       "runner_validation",
		Name:     "Validation Runner",
		Roles:    []string{domain.RoleRunner},
		Provider: domain.PrincipalRunner,
	})
	_, err := service.AuthorizeSecretAccess(runnerContext, SecretAccessInput{
		AccessID: "invalid-access-id",
		RunID:    "run_validation",
		LeaseID:  "lease_validation",
		Attempt:  1,
		Fence:    "fence_validation",
		Binding:  "database_password",
		Provider: domain.ProviderEnv,
	})
	if err == nil {
		t.Fatal("invalid secret-access metadata unexpectedly succeeded")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("secret-access metadata error does not match ErrInvalidInput: %v", err)
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("secret-access metadata error does not preserve its cause: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("secret-access metadata error does not match its cause: %v", err)
	}
	if got, want := cause.Error(), "secret access id is invalid"; got != want {
		t.Fatalf("secret-access metadata cause = %q, want %q", got, want)
	}
}

type executionValidationAuthProvider struct {
	err error
}

func (p executionValidationAuthProvider) CurrentPrincipal(context.Context) (auth.Principal, error) {
	return auth.Principal{}, p.err
}

func TestExecutionValidationPreservesAuthenticationErrorIdentity(t *testing.T) {
	service := newTestService()
	cause := errors.New("authentication dependency failed")
	service.auth = executionValidationAuthProvider{err: cause}

	_, err := service.CreateAPIToken(t.Context(), APITokenInput{})
	if !errors.Is(err, cause) {
		t.Fatalf("CreateAPIToken error = %v, want authentication cause", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("authentication cause was classified as invalid input: %v", err)
	}
}

func TestExecutionValidationPreservesAuthenticationSentinels(t *testing.T) {
	service := newTestService()
	if _, err := service.CreateAPIToken(t.Context(), APITokenInput{}); !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unauthenticated CreateAPIToken error = %v", err)
	}

	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Roles: []string{domain.RoleViewer}, Provider: domain.PrincipalLocal})
	if _, err := service.CreateRunnerEnrollment(viewer, RunnerEnrollmentInput{}); !errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("forbidden CreateRunnerEnrollment error = %v", err)
	}

	service.SetLoginLimiter(auth.NewLoginLimiter(time.Now, 1, time.Minute, 64))
	metadata := SessionCreateMetadata{SourceIP: "192.0.2.1"}
	if _, err := service.CreateSessionWithMetadata(t.Context(), "missing@example.invalid", "bad-password", metadata); !errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("first invalid login error = %v", err)
	}
	if _, err := service.CreateSessionWithMetadata(t.Context(), "missing@example.invalid", "bad-password", metadata); !errors.Is(err, auth.ErrRateLimited) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rate-limited login error = %v", err)
	}
}

func TestExecutionValidationPreservesLeaseAuthorityAndValidTelemetry(t *testing.T) {
	mem := store.NewMemoryStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: mem, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	registered, err := service.RegisterRunner(admin, RunnerInput{
		ID:           "runner_validation",
		Name:         "Validation Runner",
		Tags:         []string{"local"},
		Capabilities: []string{domain.RunTypeShell},
	})
	if err != nil {
		t.Fatalf("register valid runner: %v", err)
	}
	runnerContext := auth.WithPrincipal(t.Context(), auth.Principal{
		ID:       registered.Runner.ID,
		Name:     registered.Runner.Name,
		Roles:    []string{domain.RoleRunner},
		Provider: domain.PrincipalRunner,
	})
	if err := service.RecordRunnerOperationalTelemetry(runnerContext, RunnerOperationalTelemetry{}); err != nil {
		t.Fatalf("record valid telemetry: %v", err)
	}
	if _, err := mem.CreateRun(t.Context(), domain.TaskRun{
		ID: "run_validation", ProjectID: "proj_validation", Status: domain.RunQueued,
		RunSpec:    domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}},
		RunnerTags: []string{"local"}, RequestedBy: "usr_bootstrap", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create valid run: %v", err)
	}
	claim, err := service.ClaimRun(runnerContext)
	if err != nil {
		t.Fatalf("claim valid run: %v", err)
	}
	_, err = service.RunnerLease(runnerContext, claim.Lease.ID, claim.Lease.Attempt, "wrong-fence")
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("wrong-fence lease error = %v, want forbidden", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("wrong-fence lease error was classified as invalid input: %v", err)
	}

	_, err = service.ExecuteRunLogRetention(WithRequestID(admin, "retention-request"), RunLogRetentionExecuteInput{})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("retention policy-version error = %v, want conflict", err)
	}
	if errors.Is(err, ErrInvalidInput) {
		t.Fatalf("retention policy-version conflict was classified as invalid input: %v", err)
	}
}
