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

const (
	defaultManagedCredentialLifetime = 90 * 24 * time.Hour
	maxManagedCredentialLifetime     = 365 * 24 * time.Hour
)

// ServiceAccountCreateInput creates one stable principal and its first
// bounded credential.
type ServiceAccountCreateInput struct {
	ProjectID string     `json:"project_id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// ServiceAccountUpdateInput changes mutable account metadata only.
type ServiceAccountUpdateInput struct {
	ServiceAccountID string  `json:"service_account_id"`
	Name             *string `json:"name,omitempty"`
	Role             *string `json:"role,omitempty"`
}

// ServiceAccountRotateInput replaces every active credential for an account.
type ServiceAccountRotateInput struct {
	ServiceAccountID string     `json:"service_account_id"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// ServiceAccountRevokeInput disables an account and all its credentials.
type ServiceAccountRevokeInput struct {
	ServiceAccountID string `json:"service_account_id"`
}

// CreatedServiceAccount returns credential plaintext exactly once.
type CreatedServiceAccount struct {
	ServiceAccount domain.ServiceAccount `json:"service_account"`
	Credential     domain.APIToken       `json:"credential"`
	Token          string                `json:"token"`
}

// RotatedServiceAccountCredential returns replacement plaintext exactly once.
type RotatedServiceAccountCredential struct {
	ServiceAccountID string          `json:"service_account_id"`
	Credential       domain.APIToken `json:"credential"`
	Token            string          `json:"token"`
}

// ListServiceAccounts lists a bounded page for one authorized project.
func (s *Service) ListServiceAccounts(ctx context.Context, projectID string, page store.Page) (store.PageResult[domain.ServiceAccount], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.ServiceAccount]{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if principal.Provider != domain.PrincipalLocal {
		s.writeDeniedAudit(ctx, principal, "service_account.list.denied", projectID, nil)
		return store.PageResult[domain.ServiceAccount]{}, auth.ErrForbidden
	}
	if projectID == "" {
		return store.PageResult[domain.ServiceAccount]{}, InvalidInput(errors.New("project_id is required"))
	}
	if err := s.requireServiceAccountManager(ctx, principal, projectID); err != nil {
		s.writeDeniedAudit(ctx, principal, "service_account.list.denied", projectID, nil)
		return store.PageResult[domain.ServiceAccount]{}, err
	}
	return s.serviceAccounts.ListServiceAccounts(ctx, projectID, page)
}

// CreateServiceAccount creates a project-scoped account and first credential.
func (s *Service) CreateServiceAccount(ctx context.Context, input ServiceAccountCreateInput) (CreatedServiceAccount, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if principal.Provider != domain.PrincipalLocal {
		s.writeDeniedAudit(ctx, principal, "service_account.create.denied", projectID, nil)
		return CreatedServiceAccount{}, auth.ErrForbidden
	}
	if projectID == "" {
		return CreatedServiceAccount{}, InvalidInput(errors.New("project_id is required"))
	}
	if err := s.requireServiceAccountManager(ctx, principal, projectID); err != nil {
		s.writeDeniedAudit(ctx, principal, "service_account.create.denied", projectID, nil)
		return CreatedServiceAccount{}, err
	}
	name, role, err := normalizedServiceAccountMetadata(input.Name, input.Role)
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	now := s.clock().UTC()
	expiresAt, err := managedCredentialExpiry(now, input.ExpiresAt)
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	accountID, err := prefixedID("svcacct")
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	tokenID, err := prefixedID("pat")
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	plaintext, tokenHash, err := newAPITokenSecret()
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	account := domain.ServiceAccount{ID: accountID, ProjectID: projectID, OwnerUserID: principal.ID, Name: name, Role: role, Status: domain.ServiceAccountActive, CreatedAt: now, UpdatedAt: now}
	token := domain.APIToken{ID: tokenID, Name: name, Kind: domain.TokenKindServiceAccount, TokenHash: tokenHash, Roles: []string{}, Status: domain.TokenActive, CreatedBy: principal.ID, ServiceAccountID: &accountID, CreatedAt: now, ExpiresAt: &expiresAt}
	audit, err := s.auditEvent(ctx, principal.ID, "service_account.create", account.ID, map[string]any{"project_id": projectID, "role": role, "credential_id": token.ID, "expires_at": expiresAt})
	if err != nil {
		return CreatedServiceAccount{}, err
	}
	created, err := s.serviceAccounts.CreateServiceAccount(ctx, account, token, audit)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return CreatedServiceAccount{}, auth.ErrForbidden
		}
		return CreatedServiceAccount{}, err
	}
	return CreatedServiceAccount{ServiceAccount: created, Credential: token, Token: plaintext}, nil
}

// UpdateServiceAccount changes an active account's name and project role.
func (s *Service) UpdateServiceAccount(ctx context.Context, input ServiceAccountUpdateInput) (domain.ServiceAccount, error) {
	principal, accountID, projectID, err := s.authorizeServiceAccountMutation(ctx, input.ServiceAccountID)
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	if input.Name == nil && input.Role == nil {
		return domain.ServiceAccount{}, InvalidInput(errors.New("name or role is required"))
	}
	name, role := "", ""
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
		if name == "" || len(name) > 128 {
			return domain.ServiceAccount{}, InvalidInput(errors.New("invalid name: must be between 1 and 128 characters"))
		}
	}
	if input.Role != nil {
		role = strings.TrimSpace(*input.Role)
		if !isProjectRole(role) {
			return domain.ServiceAccount{}, InvalidInput(errors.New("invalid role: must be owner, maintainer, or viewer"))
		}
	}
	audit, err := s.auditEvent(ctx, principal.ID, "service_account.update", accountID, map[string]any{"project_id": projectID, "name_changed": input.Name != nil, "role": role})
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	return s.serviceAccounts.UpdateServiceAccount(ctx, accountID, name, role, audit)
}

// RotateServiceAccountCredential atomically revokes old active credentials and
// issues one bounded replacement without changing the stable account ID.
func (s *Service) RotateServiceAccountCredential(ctx context.Context, input ServiceAccountRotateInput) (RotatedServiceAccountCredential, error) {
	principal, accountID, projectID, err := s.authorizeServiceAccountMutation(ctx, input.ServiceAccountID)
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	now := s.clock().UTC()
	expiresAt, err := managedCredentialExpiry(now, input.ExpiresAt)
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	tokenID, err := prefixedID("pat")
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	plaintext, tokenHash, err := newAPITokenSecret()
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	token := domain.APIToken{ID: tokenID, Kind: domain.TokenKindServiceAccount, TokenHash: tokenHash, Roles: []string{}, Status: domain.TokenActive, ServiceAccountID: &accountID, CreatedAt: now, ExpiresAt: &expiresAt}
	audit, err := s.auditEvent(ctx, principal.ID, "service_account.rotate", accountID, map[string]any{"project_id": projectID, "credential_id": token.ID, "expires_at": expiresAt})
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	created, err := s.serviceAccounts.RotateServiceAccountCredential(ctx, accountID, token, now, audit)
	if err != nil {
		return RotatedServiceAccountCredential{}, err
	}
	return RotatedServiceAccountCredential{ServiceAccountID: accountID, Credential: created, Token: plaintext}, nil
}

// RevokeServiceAccount disables the account and all credentials atomically.
func (s *Service) RevokeServiceAccount(ctx context.Context, input ServiceAccountRevokeInput) (domain.ServiceAccount, error) {
	principal, accountID, projectID, err := s.authorizeServiceAccountMutation(ctx, input.ServiceAccountID)
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	now := s.clock().UTC()
	audit, err := s.auditEvent(ctx, principal.ID, "service_account.revoke", accountID, map[string]any{"project_id": projectID})
	if err != nil {
		return domain.ServiceAccount{}, err
	}
	return s.serviceAccounts.RevokeServiceAccount(ctx, accountID, now, audit)
}

func (s *Service) authorizeServiceAccountMutation(ctx context.Context, accountID string) (auth.Principal, string, string, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return auth.Principal{}, "", "", err
	}
	accountID = strings.TrimSpace(accountID)
	if principal.Provider != domain.PrincipalLocal {
		s.writeDeniedAudit(ctx, principal, "service_account.manage.denied", accountID, nil)
		return auth.Principal{}, "", "", auth.ErrForbidden
	}
	if accountID == "" {
		return auth.Principal{}, "", "", InvalidInput(errors.New("service_account_id is required"))
	}
	projectID, _, err := s.serviceAccounts.ServiceAccountProjectRole(ctx, accountID)
	if err != nil {
		return auth.Principal{}, "", "", err
	}
	if err := s.requireServiceAccountManager(ctx, principal, projectID); err != nil {
		s.writeDeniedAudit(ctx, principal, "service_account.manage.denied", accountID, map[string]any{"project_id": projectID})
		return auth.Principal{}, "", "", err
	}
	return principal, accountID, projectID, nil
}

func (s *Service) requireServiceAccountManager(ctx context.Context, principal auth.Principal, projectID string) error {
	if principal.Provider != domain.PrincipalLocal {
		return auth.ErrForbidden
	}
	if isSystemAdmin(principal) {
		return nil
	}
	return s.requireProjectRole(ctx, principal, projectID, domain.RoleOwner)
}

func normalizedServiceAccountMetadata(name, role string) (string, string, error) {
	name = strings.TrimSpace(name)
	role = strings.TrimSpace(role)
	if name == "" || len(name) > 128 {
		return "", "", InvalidInput(errors.New("invalid name: must be between 1 and 128 characters"))
	}
	if !isProjectRole(role) {
		return "", "", InvalidInput(errors.New("invalid role: must be owner, maintainer, or viewer"))
	}
	return name, role, nil
}

func isProjectRole(role string) bool {
	switch role {
	case domain.RoleOwner, domain.RoleMaintainer, domain.RoleViewer:
		return true
	default:
		return false
	}
}

func managedCredentialExpiry(now time.Time, requested *time.Time) (time.Time, error) {
	expiresAt := now.Add(defaultManagedCredentialLifetime)
	if requested != nil {
		expiresAt = requested.UTC()
	}
	if !expiresAt.After(now) {
		return time.Time{}, InvalidInput(errors.New("invalid expires_at: must be in the future"))
	}
	if expiresAt.After(now.Add(maxManagedCredentialLifetime)) {
		return time.Time{}, InvalidInput(errors.New("invalid expires_at: must be no more than one year in the future"))
	}
	return expiresAt, nil
}
