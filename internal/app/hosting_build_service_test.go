package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/store"
)

type buildResourceReader struct {
	store.HostingRepository
	resource domain.Resource
}

func (r buildResourceReader) GetResource(context.Context, string) (domain.Resource, error) {
	return r.resource, nil
}
func TestHostingBuildMemoryFailsBeforeMutationAndAuthorization(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "build"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "build", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingBuildRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: "test"}
	if _, err = s.RequestHostingBuild(admin, resource.ID, in); err == nil {
		t.Fatal("image accepted as Git")
	}
	resource.Source = domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: "source", Generation: 1, GitResourceConfiguration: domain.GitResourceConfiguration{CredentialKind: "public", RepositoryURL: "https://github.com/example/web", RequestedRef: "main", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}
	s.hosting = buildResourceReader{s.hosting, resource}
	before, _ := mem.ListAuditEvents(t.Context())
	if _, err = s.RequestHostingBuild(admin, resource.ID, in); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatalf("memory: %v", err)
	}
	after, _ := mem.ListAuditEvents(t.Context())
	if len(before) != len(after) {
		t.Fatal("unavailable command wrote audit")
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_build_viewer", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = s.RequestHostingBuild(viewer, resource.ID, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer mutation=%v", err)
	}
	for _, spec := range []domain.RunSpec{{Type: domain.HostingBuildTaskType, HostingBuildID: "test"}, {Type: "ansible", HostingBuildID: "test"}, {Type: "ansible", Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: domain.RunSpec{Type: domain.HostingBuildTaskType}}}}}} {
		if _, err = s.RequestRunWithSpec(admin, RunRequestInput{ProjectID: resource.ProjectID, RunSpec: spec}); err == nil {
			t.Fatal("public internal task accepted")
		}
	}
	a := domain.HostingBuildAuthority{BuildID: "test", RunID: "build_run_test", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	if _, err = s.RunnerHostingBuildStatus(admin, hostingbuildprotocol.StatusRequest{Authority: a}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("human phase=%v", err)
	}
	forged := auth.WithPrincipal(t.Context(), auth.Principal{ID: "absent", Provider: domain.PrincipalRunner})
	if _, err = s.RunnerHostingBuildStatus(forged, hostingbuildprotocol.StatusRequest{Authority: a}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("unpersisted runner phase=%v", err)
	}
}
func TestPublicHostingBuildProjectionExcludesAuthority(t *testing.T) {
	b := domain.HostingBuild{ID: "build", Snapshot: domain.HostingBuildSnapshot{InputID: "private-input-fingerprint", Policy: domain.HostingBuildPolicy{Identity: "private-policy"}, Grant: &domain.HostingSourceGrant{ID: "private-grant"}}, SourceReceipt: &domain.HostingBuildSourceReceipt{PolicyHash: "private-policy-hash"}, Artifact: &domain.HostingBuildArtifact{RequestDigest: "private-request"}}
	raw, err := json.Marshal(domain.PublicBuild(b))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"private-", "operator_policy", "policy_hash", "input_id", "request_digest", "grant"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("public projection exposed %s", s)
		}
	}
}
