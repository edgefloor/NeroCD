package app

import (
	"context"
	"errors"
	"fmt"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/source"
	"net"
	"regexp"
	"sort"
	"strings"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// ProjectEnvironmentInput creates a shared environment in an existing project.
type ProjectEnvironmentInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

// ResourceInput creates desired application configuration.
type ResourceInput struct {
	ProjectID            string                `json:"project_id"`
	ProjectEnvironmentID string                `json:"project_environment_id"`
	Name                 string                `json:"name"`
	Kind                 string                `json:"kind"`
	Source               domain.ResourceSource `json:"source"`
}

// ResourcePatch changes only mutable configuration at the expected generation.
type ResourcePatch struct {
	Name               *string                `json:"name,omitempty"`
	Source             *domain.ResourceSource `json:"source,omitempty"`
	ExpectedGeneration int64                  `json:"expected_generation"`
}

func (s *Service) authorizeHostingProject(ctx context.Context, projectID, role string) (auth.Principal, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	if err = s.requireProjectRole(ctx, principal, projectID, role); err != nil {
		return auth.Principal{}, err
	}
	// Administrators bypass membership checks, but never project existence.
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	for _, p := range projects {
		if p.ID == projectID && p.ArchivedAt == nil {
			return principal, nil
		}
	}
	return auth.Principal{}, store.ErrNotFound
}
func (s *Service) hostingEnvironmentInProject(ctx context.Context, projectID, environmentID string) (domain.ProjectEnvironment, error) {
	environment, err := s.hosting.GetProjectEnvironment(ctx, environmentID)
	if err != nil {
		return domain.ProjectEnvironment{}, err
	}
	if environment.ProjectID != projectID {
		return domain.ProjectEnvironment{}, auth.ErrForbidden
	}
	return environment, nil
}

// ListProjectEnvironments requires an explicit authorized project scope.
func (s *Service) ListProjectEnvironments(ctx context.Context, projectID string, page store.Page) (store.PageResult[domain.ProjectEnvironment], error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.authorizeHostingProject(ctx, projectID, domain.RoleViewer); err != nil {
		return store.PageResult[domain.ProjectEnvironment]{}, err
	}
	return s.hosting.ListProjectEnvironments(ctx, projectID, page)
}

// CreateProjectEnvironment persists shared configuration with atomic audit evidence.
func (s *Service) CreateProjectEnvironment(ctx context.Context, in ProjectEnvironmentInput) (domain.ProjectEnvironment, error) {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.Name = strings.TrimSpace(in.Name)
	principal, err := s.authorizeHostingProject(ctx, in.ProjectID, domain.RoleMaintainer)
	if err != nil {
		return domain.ProjectEnvironment{}, err
	}
	if err = domain.ValidateHostingName(in.Name); err != nil {
		return domain.ProjectEnvironment{}, InvalidInput(err)
	}
	id, err := prefixedID("penv")
	if err != nil {
		return domain.ProjectEnvironment{}, err
	}
	environment := domain.ProjectEnvironment{ID: id, ProjectID: in.ProjectID, Name: in.Name, CreatedAt: s.clock().UTC()}
	audit, err := s.auditEvent(ctx, principal.ID, "project_environment.created", id, map[string]any{"project_id": in.ProjectID})
	if err != nil {
		return domain.ProjectEnvironment{}, err
	}
	return s.hosting.CreateProjectEnvironment(ctx, environment, store.WithAudit(audit))
}

// ListResources lists desired resources after checking project and environment scope.
func (s *Service) ListResources(ctx context.Context, projectID, environmentID string, page store.Page) (store.PageResult[domain.Resource], error) {
	projectID = strings.TrimSpace(projectID)
	environmentID = strings.TrimSpace(environmentID)
	if _, err := s.authorizeHostingProject(ctx, projectID, domain.RoleViewer); err != nil {
		return store.PageResult[domain.Resource]{}, err
	}
	if environmentID != "" {
		if _, err := s.hostingEnvironmentInProject(ctx, projectID, environmentID); err != nil {
			return store.PageResult[domain.Resource]{}, err
		}
	}
	return s.hosting.ListResources(ctx, projectID, environmentID, page)
}

// GetResource authorizes access using persisted scope rather than caller input.
func (s *Service) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return domain.Resource{}, err
	}
	item, err := s.hosting.GetResource(ctx, id)
	if err != nil {
		return domain.Resource{}, err
	}
	if _, err = s.authorizeHostingProject(ctx, item.ProjectID, domain.RoleViewer); err != nil {
		return domain.Resource{}, err
	}
	return item, nil
}

// CreateResource validates and stores an image application without starting it.
func (s *Service) CreateResource(ctx context.Context, in ResourceInput) (domain.Resource, error) {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.ProjectEnvironmentID = strings.TrimSpace(in.ProjectEnvironmentID)
	in.Name = strings.TrimSpace(in.Name)
	principal, err := s.authorizeHostingProject(ctx, in.ProjectID, domain.RoleMaintainer)
	if err != nil {
		return domain.Resource{}, err
	}
	if err = domain.ValidateHostingName(in.Name); err != nil {
		return domain.Resource{}, InvalidInput(err)
	}
	if in.Kind != "application" {
		return domain.Resource{}, InvalidInput(errors.New("kind must be application"))
	}
	if in.Source.Kind != "git" {
		if err = domain.ValidateResourceSource(in.Source); err != nil {
			return domain.Resource{}, InvalidInput(err)
		}
	}
	if in.ProjectEnvironmentID == "" {
		return domain.Resource{}, InvalidInput(errors.New("project_environment_id is required"))
	}
	if _, err = s.hostingEnvironmentInProject(ctx, in.ProjectID, in.ProjectEnvironmentID); err != nil {
		return domain.Resource{}, err
	}
	id, err := prefixedID("res")
	if err != nil {
		return domain.Resource{}, err
	}
	now := s.clock().UTC()
	item := domain.Resource{ID: id, ProjectID: in.ProjectID, ProjectEnvironmentID: in.ProjectEnvironmentID, Name: in.Name, Kind: in.Kind, Source: in.Source, Generation: 1, CreatedBy: principal.ID, CreatedAt: now, UpdatedAt: now}
	audit, err := s.auditEvent(ctx, principal.ID, "resource.created", id, map[string]any{"project_id": in.ProjectID, "project_environment_id": in.ProjectEnvironmentID, "generation": int64(1)})
	if err != nil {
		return domain.Resource{}, err
	}
	opts := []store.MutationOption{store.WithAudit(audit)}
	if item.Source.Kind == "git" {
		src, sourceErr := s.publicGitSource(item, in.Source, "", 1)
		if sourceErr != nil {
			return domain.Resource{}, sourceErr
		}
		item.Source = publicGitResourceSource(src)
		opts = append(opts, store.WithHostingGitSource(src))
	}
	out, err := s.hosting.CreateResource(ctx, item, opts...)
	return out, gitResourceError(err)
}

// UpdateResource advances generation for every accepted change, including names.
func (s *Service) UpdateResource(ctx context.Context, id string, in ResourcePatch) (domain.Resource, error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return domain.Resource{}, err
	}
	item, err := s.hosting.GetResource(ctx, id)
	if err != nil {
		return domain.Resource{}, err
	}
	principal, err := s.authorizeHostingProject(ctx, item.ProjectID, domain.RoleMaintainer)
	if err != nil {
		return domain.Resource{}, err
	}
	if in.ExpectedGeneration < 1 || in.Name == nil && in.Source == nil {
		return domain.Resource{}, InvalidInput(errors.New("expected_generation and at least one of name or source are required"))
	}
	if in.Name != nil {
		item.Name = strings.TrimSpace(*in.Name)
		if err = domain.ValidateHostingName(item.Name); err != nil {
			return domain.Resource{}, InvalidInput(err)
		}
	}
	originalSource := item.Source
	if in.Source != nil {
		if item.Source.Kind != in.Source.Kind {
			return domain.Resource{}, InvalidInput(errors.New("image and Git source conversion is not supported"))
		}
		item.Source = *in.Source
		if item.Source.Kind != "git" {
			if err = domain.ValidateResourceSource(item.Source); err != nil {
				return domain.Resource{}, InvalidInput(err)
			}
		}
	}
	if item.Generation != in.ExpectedGeneration || item.Generation == 9223372036854775807 {
		return domain.Resource{}, store.ErrConflict
	}
	item.UpdatedAt = s.clock().UTC()
	audit, err := s.auditEvent(ctx, principal.ID, "resource.updated", id, map[string]any{"project_id": item.ProjectID, "project_environment_id": item.ProjectEnvironmentID, "generation": in.ExpectedGeneration + 1})
	if err != nil {
		return domain.Resource{}, err
	}
	opts := []store.MutationOption{store.WithAudit(audit)}
	if in.Source != nil && item.Source.Kind == "git" {
		src, sourceErr := s.publicGitSource(item, *in.Source, originalSource.Git.SourceID, originalSource.Git.Generation)
		if sourceErr != nil {
			return domain.Resource{}, sourceErr
		}
		item.Source = publicGitResourceSource(src)
		opts = append(opts, store.WithHostingGitSource(src))
	}
	out, err := s.hosting.UpdateResource(ctx, item, in.ExpectedGeneration, opts...)
	return out, gitResourceError(err)
}

// ConfigureHostingGitHosts sets the startup-only public HTTPS repository allowlist.
func (s *Service) ConfigureHostingGitHosts(hosts []string) error {
	policy, err := PublicHostingGitPolicy(hosts)
	if err != nil {
		return err
	}
	s.hostingGitPolicy = policy
	return nil
}

// PublicHostingGitPolicy validates bounded exact public DNS hosts without network IO.
func PublicHostingGitPolicy(hosts []string) (source.RepositoryPolicy, error) {
	if len(hosts) == 0 {
		hosts = []string{"github.com"}
	}
	if len(hosts) > 32 {
		return source.RepositoryPolicy{}, errors.New("at most 32 Git repository hosts are allowed")
	}
	normalized := make([]string, 0, len(hosts))
	seen := map[string]bool{}
	label := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
			return source.RepositoryPolicy{}, errors.New("git repository hosts must be exact public DNS names")
		}
		for _, part := range strings.Split(host, ".") {
			if !label.MatchString(part) {
				return source.RepositoryPolicy{}, errors.New("invalid Git repository host")
			}
		}
		if !seen[host] {
			normalized = append(normalized, host)
			seen[host] = true
		}
	}
	sort.Strings(normalized)
	p := source.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: normalized}
	return p, p.ValidatePolicy()
}
func (s *Service) publicGitSource(item domain.Resource, input domain.ResourceSource, id string, generation int64) (domain.HostingGitSource, error) {
	if input.Git == nil || input.Image.Reference != "" || input.Git.SourceID != "" || input.Git.Generation != 0 {
		return domain.HostingGitSource{}, InvalidInput(errors.New("git input must contain configuration only"))
	}
	c := input.Git.GitResourceConfiguration
	if c.CredentialKind != "public" {
		return domain.HostingGitSource{}, InvalidInput(errors.New("private Git credentials and SSH are not supported by resource configuration yet"))
	}
	if id == "" {
		var err error
		id, err = prefixedID("hsrc")
		if err != nil {
			return domain.HostingGitSource{}, err
		}
	}
	src := domain.HostingGitSource{ID: id, ResourceID: item.ID, ProjectID: item.ProjectID, Generation: generation, RepositoryURL: c.RepositoryURL, RequestedRef: c.RequestedRef, CredentialKind: c.CredentialKind, Settings: c.Settings, Policy: s.hostingGitPolicy}
	if err := domain.ValidateHostingGitSource(src); err != nil {
		return src, InvalidInput(err)
	}
	return src, nil
}
func publicGitResourceSource(src domain.HostingGitSource) domain.ResourceSource {
	return domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: src.ID, Generation: src.Generation, GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: src.RepositoryURL, RequestedRef: src.RequestedRef, CredentialKind: src.CredentialKind, Settings: src.Settings}}}
}
func gitResourceError(err error) error {
	if errors.Is(err, store.ErrGitResourceUnavailable) {
		return fmt.Errorf("git resource configuration requires PostgreSQL: %w", hostingjobs.ErrUnavailable)
	}
	return err
}
