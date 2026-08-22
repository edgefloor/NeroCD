package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/source"
	"nerocd/internal/store"
)

type TemplateInput struct {
	ProjectID   string          `json:"project_id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	RunSpec     domain.RunSpec  `json:"run_spec"`
	Workflow    domain.Workflow `json:"workflow"`
	RunnerTags  []string        `json:"runner_tags"`
	RequiresAck bool            `json:"requires_ack"`
}

type RepositoryInput struct {
	ProjectID  string                  `json:"project_id"`
	Name       string                  `json:"name"`
	URL        string                  `json:"url"`
	Provider   string                  `json:"provider"`
	DefaultRef string                  `json:"default_ref"`
	Policy     domain.RepositoryPolicy `json:"policy"`
}

func validateRepositoryPolicy(p domain.RepositoryPolicy) error {
	return (source.RepositoryPolicy{Version: p.Version, State: p.State, Mode: p.Mode, AllowedSchemes: p.AllowedSchemes, AllowedHosts: p.AllowedHosts, AllowedCIDRs: p.AllowedCIDRs, RedirectHosts: p.RedirectHosts, SSHHostFingerprints: p.SSHHostFingerprints, CredentialReferenceID: p.CredentialReferenceID, AllowInternal: p.AllowInternal}).ValidatePolicy()
}

func (s *Service) CreateRepository(ctx context.Context, input RepositoryInput) (domain.Repository, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	url := strings.TrimSpace(input.URL)
	if projectID == "" {
		return domain.Repository{}, errors.New("project_id is required")
	}
	if name == "" {
		return domain.Repository{}, errors.New("name is required")
	}
	if url == "" {
		return domain.Repository{}, errors.New("url is required")
	}
	if err := source.ValidateRepositoryURL(url); err != nil {
		return domain.Repository{}, err
	}
	// An omitted policy is explicitly normalized to legacy_unverified. It is
	// visible in API responses and cannot be used by the provenance runner.
	if input.Policy.State == "" {
		input.Policy = domain.RepositoryPolicy{Version: 1, State: "legacy_unverified"}
	}
	if input.Policy.State == "configured" {
		if err := validateRepositoryPolicy(input.Policy); err != nil {
			return domain.Repository{}, err
		}
	} else if input.Policy.Version != 1 || input.Policy.State != "legacy_unverified" {
		return domain.Repository{}, errors.New("repository policy state is invalid")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.Repository{}, err
	}
	id, err := prefixedID("repo")
	if err != nil {
		return domain.Repository{}, err
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = domain.ProviderGit
	}
	defaultRef := strings.TrimSpace(input.DefaultRef)
	if defaultRef == "" {
		defaultRef = "main"
	}
	repository := domain.Repository{ID: id, ProjectID: projectID, Name: name, URL: url, Provider: provider, DefaultRef: defaultRef, Policy: input.Policy, CreatedAt: time.Now().UTC()}
	audit, err := s.auditEvent(ctx, principal.ID, "repository.create", repository.ID, map[string]any{"project_id": repository.ProjectID, "provider": repository.Provider})
	if err != nil {
		return domain.Repository{}, err
	}
	return s.sources.CreateRepository(ctx, repository, store.WithAudit(audit))
}

type RepositoryPolicyInput struct {
	ID              string                  `json:"id"`
	ProjectID       string                  `json:"project_id"`
	ConfigurationID string                  `json:"configuration_id"`
	Policy          domain.RepositoryPolicy `json:"policy"`
}

var opaqueConfigurationID = regexp.MustCompile(`^cfg_[A-Za-z0-9_-]{8,128}$`)
var opaqueCredentialReferenceID = regexp.MustCompile(`^cred_[A-Za-z0-9_-]{8,128}$`)

func canonicalRepositoryPolicy(policy domain.RepositoryPolicy) (domain.RepositoryPolicy, []byte, error) {
	if policy.Version != 1 || policy.State != "configured" {
		return domain.RepositoryPolicy{}, nil, errors.New("repository policy must be configured")
	}
	canonicalStrings := func(values []string, normalize func(string) string) ([]string, error) {
		out := make([]string, 0, len(values))
		seen := map[string]bool{}
		for _, value := range values {
			value = normalize(value)
			if value == "" || seen[value] {
				return nil, errors.New("repository policy contains invalid or duplicate values")
			}
			seen[value] = true
			out = append(out, value)
		}
		sort.Strings(out)
		return out, nil
	}
	var err error
	if policy.AllowedSchemes, err = canonicalStrings(policy.AllowedSchemes, func(v string) string { return strings.ToLower(strings.TrimSpace(v)) }); err != nil {
		return policy, nil, err
	}
	if policy.AllowedHosts, err = canonicalStrings(policy.AllowedHosts, func(v string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), ".")) }); err != nil {
		return policy, nil, err
	}
	if policy.RedirectHosts, err = canonicalStrings(policy.RedirectHosts, func(v string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), ".")) }); err != nil {
		return policy, nil, err
	}
	if policy.SSHHostFingerprints, err = canonicalStrings(policy.SSHHostFingerprints, strings.TrimSpace); err != nil {
		return policy, nil, err
	}
	if policy.AllowedCIDRs, err = canonicalStrings(policy.AllowedCIDRs, func(v string) string {
		prefix, e := netip.ParsePrefix(strings.TrimSpace(v))
		if e != nil {
			return ""
		}
		return prefix.Masked().String()
	}); err != nil {
		return policy, nil, err
	}
	policy.Mode = strings.ToLower(strings.TrimSpace(policy.Mode))
	policy.CredentialReferenceID = strings.TrimSpace(policy.CredentialReferenceID)
	if policy.CredentialReferenceID != "" && !opaqueCredentialReferenceID.MatchString(policy.CredentialReferenceID) {
		return policy, nil, errors.New("repository credential reference must be an opaque ID")
	}
	if err = validateRepositoryPolicy(policy); err != nil {
		return policy, nil, err
	}
	encoded, err := json.Marshal(policy)
	return policy, encoded, err
}

func (s *Service) ConfigureRepositoryPolicy(ctx context.Context, input RepositoryPolicyInput) (domain.Repository, error) {
	p, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	input.ID, input.ProjectID, input.ConfigurationID = strings.TrimSpace(input.ID), strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.ConfigurationID)
	if input.ID == "" || input.ProjectID == "" || !opaqueConfigurationID.MatchString(input.ConfigurationID) {
		return domain.Repository{}, errors.New("repository policy configuration_id is invalid")
	}
	policy, canonical, err := canonicalRepositoryPolicy(input.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	// Authorize the requested project before inspecting repository identity so a
	// caller cannot use this endpoint to probe repositories in another project.
	if err = s.requireProjectRole(ctx, p, input.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.Repository{}, err
	}
	hash := sha256.Sum256(canonical)
	audit, err := s.auditEvent(ctx, p.ID, "repository.policy.configure", input.ID, map[string]any{"project_id": input.ProjectID, "configuration_id": input.ConfigurationID, "policy_sha256": hex.EncodeToString(hash[:]), "mode": policy.Mode})
	if err != nil {
		return domain.Repository{}, err
	}
	return s.sources.ConfigureRepositoryPolicy(ctx, store.RepositoryPolicyConfiguration{RepositoryID: input.ID, ProjectID: input.ProjectID, ActorID: p.ID, ConfigurationID: input.ConfigurationID, Policy: policy, PolicyHash: hex.EncodeToString(hash[:]), Audit: audit})
}

func (s *Service) CreateTemplate(ctx context.Context, input TemplateInput) (domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(input.ProjectID), domain.RoleMaintainer); err != nil {
		return domain.TaskTemplate{}, err
	}
	template, err := s.templateFromInput("", input)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	id, err := prefixedID("tpl")
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	template.ID = id
	audit, err := s.auditEvent(ctx, principal.ID, "template.create", template.ID, map[string]any{"project_id": template.ProjectID, "kind": template.Kind})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return s.templates.CreateTemplate(ctx, template, store.WithAudit(audit))
}

func (s *Service) UpdateTemplate(ctx context.Context, id string, input TemplateInput) (domain.TaskTemplate, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	existing, err := s.templates.GetTemplate(ctx, id)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := s.requireProjectRole(ctx, principal, existing.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskTemplate{}, err
	}
	if input.ProjectID == "" {
		input.ProjectID = existing.ProjectID
	}
	if strings.TrimSpace(input.ProjectID) != existing.ProjectID {
		return domain.TaskTemplate{}, errors.New("template project_id cannot be changed")
	}
	template, err := s.templateFromInput(id, input)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "template.update", template.ID, map[string]any{"project_id": template.ProjectID, "kind": template.Kind})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return s.templates.UpdateTemplate(ctx, template, store.WithAudit(audit))
}
