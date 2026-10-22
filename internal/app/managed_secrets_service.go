package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// ErrManagedSecretsUnavailable safely reports an unconfigured encryption dependency.
var ErrManagedSecretsUnavailable = errors.New("managed secrets unavailable")

// ManagedSecretInput supplies an exact value. Value is never trimmed or included in errors.
type ManagedSecretInput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Value     string `json:"value"`
}

// ManagedSecretRotateInput appends a version only at the caller's expected version.
type ManagedSecretRotateInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Value           string `json:"value"`
}

// ManagedSecretRevokeInput makes revocation conditional on the expected version.
type ManagedSecretRevokeInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// ManagedSecretRevealInput selects an explicit immutable historical version.
type ManagedSecretRevealInput struct {
	Version int64 `json:"version"`
}

// ManagedSecretReveal is the only response that contains a plaintext value.
type ManagedSecretReveal struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
	Value    string `json:"value"`
}

func validateManagedValue(value string) error {
	if len(value) == 0 || len(value) > 65536 || !utf8.ValidString(value) {
		return InvalidInput(errors.New("value must be nonempty UTF-8 and at most 65536 bytes"))
	}
	return nil
}
func (s *Service) managedSecretForRole(ctx context.Context, id, role string) (domain.ManagedSecret, auth.Principal, error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return domain.ManagedSecret{}, auth.Principal{}, err
	}
	item, err := s.managedSecrets.GetManagedSecret(ctx, id)
	if err != nil {
		return domain.ManagedSecret{}, auth.Principal{}, err
	}
	principal, err := s.authorizeHostingProject(ctx, item.ProjectID, role)
	if err != nil {
		return domain.ManagedSecret{}, auth.Principal{}, err
	}
	return item, principal, nil
}

// ListManagedSecrets requires an explicit project and viewer access.
func (s *Service) ListManagedSecrets(ctx context.Context, project string, page store.Page) (store.PageResult[domain.ManagedSecret], error) {
	project = strings.TrimSpace(project)
	if _, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer); err != nil {
		return store.PageResult[domain.ManagedSecret]{}, err
	}
	return s.managedSecrets.ListManagedSecrets(ctx, project, page)
}

// GetManagedSecret checks persisted project scope before returning metadata.
func (s *Service) GetManagedSecret(ctx context.Context, id string) (domain.ManagedSecret, error) {
	item, _, err := s.managedSecretForRole(ctx, id, domain.RoleViewer)
	return item, err
}

// ListManagedSecretVersions returns metadata even when encryption is unconfigured or the secret is revoked.
func (s *Service) ListManagedSecretVersions(ctx context.Context, id string, page store.Page) (store.PageResult[domain.ManagedSecretVersion], error) {
	if _, _, err := s.managedSecretForRole(ctx, id, domain.RoleViewer); err != nil {
		return store.PageResult[domain.ManagedSecretVersion]{}, err
	}
	return s.managedSecrets.ListManagedSecretVersions(ctx, id, page)
}

// CreateManagedSecret encrypts before atomically committing version one and its audit.
func (s *Service) CreateManagedSecret(ctx context.Context, in ManagedSecretInput) (domain.ManagedSecret, error) {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.Name = strings.TrimSpace(in.Name)
	principal, err := s.authorizeHostingProject(ctx, in.ProjectID, domain.RoleMaintainer)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	if err = domain.ValidateHostingName(in.Name); err != nil {
		return domain.ManagedSecret{}, InvalidInput(err)
	}
	if err = validateManagedValue(in.Value); err != nil {
		return domain.ManagedSecret{}, err
	}
	if s.secretKeyring == nil {
		return domain.ManagedSecret{}, ErrManagedSecretsUnavailable
	}
	id, err := prefixedID("msec")
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	now := s.clock().UTC()
	item := domain.ManagedSecret{ID: id, ProjectID: in.ProjectID, Name: in.Name, CurrentVersion: 1, Status: "active", CreatedBy: principal.ID, CreatedAt: now, UpdatedAt: now}
	envelope, err := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: in.ProjectID, SecretID: id, Version: 1}, []byte(in.Value))
	if err != nil {
		return domain.ManagedSecret{}, errors.New("managed secret encryption failed")
	}
	record := store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: id, Version: 1, CreatedBy: principal.ID, CreatedAt: now}, Envelope: envelope}
	event, err := s.auditEvent(ctx, principal.ID, "managed_secret.created", id, nil)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	return s.managedSecrets.CreateManagedSecret(ctx, item, record, event)
}

// RotateManagedSecret encrypts a new version and commits with compare-and-swap and audit.
func (s *Service) RotateManagedSecret(ctx context.Context, id string, in ManagedSecretRotateInput) (domain.ManagedSecret, error) {
	item, principal, err := s.managedSecretForRole(ctx, id, domain.RoleMaintainer)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	if in.ExpectedVersion < 1 {
		return domain.ManagedSecret{}, InvalidInput(errors.New("expected_version must be positive"))
	}
	if err = validateManagedValue(in.Value); err != nil {
		return domain.ManagedSecret{}, err
	}
	if item.Status != "active" || item.CurrentVersion != in.ExpectedVersion || in.ExpectedVersion == 9223372036854775807 {
		return domain.ManagedSecret{}, store.ErrConflict
	}
	if s.secretKeyring == nil {
		return domain.ManagedSecret{}, ErrManagedSecretsUnavailable
	}
	version := in.ExpectedVersion + 1
	envelope, err := s.secretKeyring.Seal(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: id, Version: version}, []byte(in.Value))
	if err != nil {
		return domain.ManagedSecret{}, errors.New("managed secret encryption failed")
	}
	record := store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: id, Version: version, CreatedBy: principal.ID, CreatedAt: s.clock().UTC()}, Envelope: envelope}
	event, err := s.auditEvent(ctx, principal.ID, "managed_secret.rotated", id, nil)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	return s.managedSecrets.RotateManagedSecret(ctx, id, in.ExpectedVersion, record, event)
}

// RevokeManagedSecret needs owner access but no decryption keys.
func (s *Service) RevokeManagedSecret(ctx context.Context, id string, in ManagedSecretRevokeInput) (domain.ManagedSecret, error) {
	_, principal, err := s.managedSecretForRole(ctx, id, domain.RoleOwner)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	if in.ExpectedVersion < 1 {
		return domain.ManagedSecret{}, InvalidInput(errors.New("expected_version must be positive"))
	}
	event, err := s.auditEvent(ctx, principal.ID, "managed_secret.revoked", id, nil)
	if err != nil {
		return domain.ManagedSecret{}, err
	}
	return s.managedSecrets.RevokeManagedSecret(ctx, id, in.ExpectedVersion, event)
}

// RevealManagedSecret returns plaintext only after decryption and a fresh committed active-state audit.
func (s *Service) RevealManagedSecret(ctx context.Context, id string, in ManagedSecretRevealInput) (ManagedSecretReveal, error) {
	item, principal, err := s.managedSecretForRole(ctx, id, domain.RoleOwner)
	if err != nil {
		return ManagedSecretReveal{}, err
	}
	if in.Version < 1 {
		return ManagedSecretReveal{}, InvalidInput(errors.New("version must be positive"))
	}
	if item.Status != "active" {
		return ManagedSecretReveal{}, store.ErrConflict
	}
	if s.secretKeyring == nil {
		return ManagedSecretReveal{}, ErrManagedSecretsUnavailable
	}
	record, err := s.managedSecrets.GetManagedSecretRecord(ctx, id, in.Version)
	if err != nil {
		return ManagedSecretReveal{}, err
	}
	plaintext, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: item.ProjectID, SecretID: id, Version: in.Version}, record.Envelope)
	if err != nil {
		return ManagedSecretReveal{}, errors.New("managed secret decryption failed")
	}
	if !utf8.Valid(plaintext) {
		return ManagedSecretReveal{}, errors.New("managed secret decryption failed")
	}
	event, err := s.auditEvent(ctx, principal.ID, "managed_secret.revealed", id, nil)
	if err != nil {
		return ManagedSecretReveal{}, err
	}
	if err = s.managedSecrets.AuthorizeManagedSecretReveal(ctx, id, in.Version, event); err != nil {
		return ManagedSecretReveal{}, err
	}
	return ManagedSecretReveal{SecretID: id, Version: in.Version, Value: string(plaintext)}, nil
}
