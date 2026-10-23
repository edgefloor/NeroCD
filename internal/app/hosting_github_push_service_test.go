package app

import (
	"context"
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"nerocd/internal/store"
	"testing"
	"time"
)

func TestGitHubPushPayloadFraming(t *testing.T) {
	for _, raw := range []string{`{"repository":{"id":1,"fork":false},"optional":null}`, `{"array":[null,{"x":true}]}`} {
		if !uniquePushJSON([]byte(raw)) {
			t.Fatal("extensible GitHub document rejected")
		}
	}
	for _, raw := range []string{`{"ref":"A","ref":"B"}`, `{"repository":{"id":1,"id":2}}`, `{} {}`, `{"nested":[{"x":1,"x":2}]}`, `{"x":`} {
		if uniquePushJSON([]byte(raw)) {
			t.Fatal("ambiguous GitHub document accepted")
		}
	}
}
func TestGitHubPushMemoryAndInvalidCommandsDoNotSimulateAuthority(t *testing.T) {
	s := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if _, err := s.ReceiveGitHubPush(t.Context(), "policy", "00000000-0000-0000-0000-000000000001", "push", "invalid", []byte(`{}`)); !errors.Is(err, store.ErrGitHubPushUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.ConfigureGitHubPushPolicy(admin, "resource", domain.GitHubPushConfigure{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := s.VerifyGitHubPushPolicy(admin, "resource", domain.GitHubPushCommand{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := s.RetryGitHubPushDelivery(admin, "resource", domain.GitHubPushRetry{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}

// Embedding the repository exposes the production interface while overriding
// only the initial verification boundary used to observe the service deadline.
type pushDeadlineRuns struct {
	*store.MemoryStore
	store.GitHubPushRepository
	deadline func(context.Context)
}

func (r pushDeadlineRuns) BeginGitHubPushVerification(ctx context.Context, _, _ string, _ domain.GitHubPushCommand) (store.GitHubPushAuthorization, error) {
	r.deadline(ctx)
	return store.GitHubPushAuthorization{}, store.ErrConflict
}
func TestGitHubPushVerifyHasOneBoundedDeadline(t *testing.T) {
	s := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "push deadline"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "push deadline", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	s.runs = pushDeadlineRuns{MemoryStore: s.runs.(*store.MemoryStore), deadline: func(ctx context.Context) {
		observed = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 20*time.Second || time.Until(deadline) < 19*time.Second {
			t.Error("verify did not bound all work to20 seconds")
		}
	}}
	s.githubPushProvider, err = githubpush.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.VerifyGitHubPushPolicy(admin, resource.ID, domain.GitHubPushCommand{ExpectedPolicyGeneration: 1, RequestID: "00000000-0000-0000-0000-000000000001"})
	if !errors.Is(err, store.ErrConflict) || !observed {
		t.Fatal(err, observed)
	}
}
