package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) ListProjects(ctx context.Context) ([]domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return projects, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Project, 0, len(projects))
	for _, project := range projects {
		if _, ok := allowed[project.ID]; ok {
			out = append(out, project)
		}
	}
	return out, nil
}

func (s *Service) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		return s.members.ListProjectMembers(ctx, projectID)
	}
	members, err := s.members.ListProjectMembers(ctx, "")
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return members, nil
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ProjectMember, 0, len(members))
	for _, member := range members {
		if _, ok := allowed[member.ProjectID]; ok {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *Service) ProjectRole(ctx context.Context, projectID string) (domain.ProjectRole, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.ProjectRole{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.ProjectRole{}, errors.New("project_id is required")
	}
	if isSystemAdmin(principal) {
		return projectRole(projectID, domain.RoleSystemAdmin), nil
	}
	members, err := s.members.ListProjectMembers(ctx, projectID)
	if err != nil {
		return domain.ProjectRole{}, err
	}
	for _, member := range members {
		if member.UserID == principal.ID {
			return projectRole(projectID, member.Role), nil
		}
	}
	return domain.ProjectRole{}, auth.ErrForbidden
}

type ProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ProjectMemberInput struct {
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

type AccessKeyInput struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type InventoryInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
}

func (s *Service) CreateProject(ctx context.Context, input ProjectInput) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Project{}, errors.New("name is required")
	}
	now := time.Now().UTC()
	id, err := prefixedID("proj")
	if err != nil {
		return domain.Project{}, err
	}
	memberID, err := prefixedID("pm")
	if err != nil {
		return domain.Project{}, err
	}
	project := domain.Project{ID: id, Name: name, Description: strings.TrimSpace(input.Description), CreatedAt: now}
	member := domain.ProjectMember{ID: memberID, ProjectID: project.ID, UserID: principal.ID, Email: principal.Email, Name: principal.Name, Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now}
	audit, err := s.auditEvent(ctx, principal.ID, "project.create", project.ID, map[string]any{"name": project.Name})
	if err != nil {
		return domain.Project{}, err
	}
	return s.projects.CreateProjectWithOwner(ctx, project, member, audit)
}

func (s *Service) UpsertProjectMember(ctx context.Context, input ProjectMemberInput) (domain.ProjectMember, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	email := strings.TrimSpace(strings.ToLower(input.Email))
	role := strings.TrimSpace(input.Role)
	if projectID == "" {
		return domain.ProjectMember{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleOwner); err != nil {
		return domain.ProjectMember{}, err
	}
	if email == "" {
		return domain.ProjectMember{}, errors.New("email is required")
	}
	switch role {
	case domain.RoleOwner, domain.RoleMaintainer, domain.RoleViewer:
	default:
		return domain.ProjectMember{}, errors.New("role must be owner, maintainer, or viewer")
	}
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	foundProject := false
	for _, project := range projects {
		if project.ID == projectID {
			foundProject = true
			break
		}
	}
	if !foundProject {
		return domain.ProjectMember{}, store.ErrNotFound
	}
	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	if user.Status != domain.UserActive {
		return domain.ProjectMember{}, errors.New("user is not active")
	}
	now := time.Now().UTC()
	id, err := prefixedID("pm")
	if err != nil {
		return domain.ProjectMember{}, err
	}
	member := domain.ProjectMember{ID: id, ProjectID: projectID, UserID: user.ID, Email: user.Email, Name: user.Name, Role: role, CreatedAt: now, UpdatedAt: now}
	audit, err := s.auditEvent(ctx, principal.ID, "project.member.upsert", member.ProjectID, map[string]any{"member_user_id": member.UserID, "role": member.Role})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return s.members.UpsertProjectMember(ctx, member, store.WithAudit(audit))
}

func (s *Service) CreateAccessKey(ctx context.Context, input AccessKeyInput) (domain.AccessKey, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.AccessKey{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	kind := strings.TrimSpace(input.Kind)
	fingerprint := strings.TrimSpace(input.Fingerprint)
	if projectID == "" {
		return domain.AccessKey{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.AccessKey{}, err
	}
	if name == "" {
		return domain.AccessKey{}, errors.New("name is required")
	}
	switch kind {
	case domain.AccessKeySSH, domain.AccessKeyPassword, domain.AccessKeyToken:
	default:
		return domain.AccessKey{}, errors.New("kind must be ssh, password, or token")
	}
	if fingerprint == "" {
		return domain.AccessKey{}, errors.New("fingerprint is required")
	}
	id, err := prefixedID("key")
	if err != nil {
		return domain.AccessKey{}, err
	}
	key := domain.AccessKey{ID: id, ProjectID: projectID, Name: name, Kind: kind, Fingerprint: fingerprint, CreatedAt: time.Now().UTC()}
	audit, err := s.auditEvent(ctx, principal.ID, "access_key.create", key.ID, map[string]any{"project_id": key.ProjectID, "kind": key.Kind, "fingerprint": key.Fingerprint})
	if err != nil {
		return domain.AccessKey{}, err
	}
	return s.sources.CreateAccessKey(ctx, key, store.WithAudit(audit))
}

func (s *Service) CreateInventory(ctx context.Context, input InventoryInput) (domain.Inventory, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Inventory{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	name := strings.TrimSpace(input.Name)
	kind := strings.TrimSpace(input.Kind)
	source := strings.TrimSpace(input.Source)
	if projectID == "" {
		return domain.Inventory{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.Inventory{}, err
	}
	if name == "" {
		return domain.Inventory{}, errors.New("name is required")
	}
	switch kind {
	case domain.InventoryStatic, domain.InventoryDynamic:
	default:
		return domain.Inventory{}, errors.New("kind must be static or dynamic")
	}
	if source == "" {
		return domain.Inventory{}, errors.New("source is required")
	}
	id, err := prefixedID("inv")
	if err != nil {
		return domain.Inventory{}, err
	}
	inventory := domain.Inventory{ID: id, ProjectID: projectID, Name: name, Kind: kind, Source: source, CreatedAt: time.Now().UTC()}
	audit, err := s.auditEvent(ctx, principal.ID, "inventory.create", inventory.ID, map[string]any{"project_id": inventory.ProjectID, "kind": inventory.Kind, "source": inventory.Source})
	if err != nil {
		return domain.Inventory{}, err
	}
	return s.sources.CreateInventory(ctx, inventory, store.WithAudit(audit))
}

func (s *Service) UpdateProject(ctx context.Context, id string, input ProjectInput) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(id), domain.RoleMaintainer); err != nil {
		return domain.Project{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Project{}, errors.New("name is required")
	}
	project := domain.Project{ID: id, Name: name, Description: strings.TrimSpace(input.Description)}
	audit, err := s.auditEvent(ctx, principal.ID, "project.update", project.ID, map[string]any{"name": project.Name})
	if err != nil {
		return domain.Project{}, err
	}
	return s.projects.UpdateProject(ctx, project, store.WithAudit(audit))
}

func (s *Service) ArchiveProject(ctx context.Context, id string) (domain.Project, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	if err := s.requireProjectRole(ctx, principal, strings.TrimSpace(id), domain.RoleMaintainer); err != nil {
		return domain.Project{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "project.archive", id, nil)
	if err != nil {
		return domain.Project{}, err
	}
	return s.projects.ArchiveProject(ctx, id, time.Now().UTC(), store.WithAudit(audit))
}
