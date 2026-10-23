package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrGitHubPushSignature is closed; no signature or body is reflected in errors.
var ErrGitHubPushSignature = errors.New("github_push_signature_invalid")

// ConfigureHostingGitHubPush installs the trusted fixed-provider dependency before serving.
func (s *Service) ConfigureHostingGitHubPush(provider *githubpush.Client) {
	s.githubPushProvider = provider
}
func (s *Service) githubPushRepository() (store.GitHubPushRepository, error) {
	repo, ok := s.runs.(store.GitHubPushRepository)
	if !ok {
		return nil, store.ErrGitHubPushUnavailable
	}
	return repo, nil
}
func (s *Service) githubPushActor(ctx context.Context, resource, role string) (store.GitHubPushRepository, string, error) {
	_, actor, err := s.resourceForDestinationRole(ctx, resource, role)
	if err != nil {
		return nil, "", err
	}
	repo, err := s.githubPushRepository()
	return repo, actor.ID, err
}

// GetGitHubPushPolicy returns safe current viewer metadata and owner-only selectors.
func (s *Service) GetGitHubPushPolicy(ctx context.Context, resource string) (domain.GitHubPushPolicy, error) {
	repo, actor, err := s.githubPushActor(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	return repo.GetGitHubPushPolicy(ctx, actor, resource)
}

// ConfigureGitHubPushPolicy records explicit disabled delegation with reviewed current generations.
func (s *Service) ConfigureGitHubPushPolicy(ctx context.Context, resource string, in domain.GitHubPushConfigure) (domain.GitHubPushPolicy, error) {
	if err := in.Validate(); err != nil {
		return domain.GitHubPushPolicy{}, InvalidInput(err)
	}
	item, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	if item.Source.Kind != "git" || item.Source.Git == nil {
		return domain.GitHubPushPolicy{}, InvalidInput(errors.New("select a supported GitHub.com Git source"))
	}
	source := item.Source.Git
	if _, _, _, err = domain.GitHubPushRepositoryBranch(domain.HostingGitSource{RepositoryURL: source.RepositoryURL, RequestedRef: source.RequestedRef, CredentialKind: source.CredentialKind}); err != nil {
		return domain.GitHubPushPolicy{}, InvalidInput(errors.New("select a GitHub.com HTTPS source with one exact branch"))
	}
	if source.CredentialKind == "https_basic" && in.Configuration.ProviderReadSecret == nil {
		return domain.GitHubPushPolicy{}, InvalidInput(errors.New("select a provider-read managed secret version"))
	}
	repo, err := s.githubPushRepository()
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.github_push.configured", resource, nil)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	return repo.ConfigureGitHubPushPolicy(ctx, actor.ID, resource, in, event)
}
func validPushCommand(in domain.GitHubPushCommand) bool {
	return in.ExpectedPolicyGeneration > 0 && domain.ValidGitHubPushRequestID(in.RequestID)
}

// VerifyGitHubPushPolicy verifies current repository identity without admitting work.
func (s *Service) VerifyGitHubPushPolicy(ctx context.Context, resource string, in domain.GitHubPushCommand) (domain.GitHubPushPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !validPushCommand(in) {
		return domain.GitHubPushPolicy{}, InvalidInput(errors.New("invalid verification command"))
	}
	repo, actor, err := s.githubPushActor(ctx, resource, domain.RoleOwner)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	if s.githubPushProvider == nil {
		return domain.GitHubPushPolicy{}, store.ErrGitHubPushUnavailable
	}
	authorized, err := repo.BeginGitHubPushVerification(ctx, actor, resource, in)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	if authorized.Replay != nil {
		return *authorized.Replay, nil
	}
	evidence, reason := s.verifyGitHubPush(ctx, repo, authorized)
	return repo.FinishGitHubPushVerification(ctx, authorized, evidence, reason)
}

// SetGitHubPushEnabled requires fresh verification and current owner authorization.
func (s *Service) SetGitHubPushEnabled(ctx context.Context, resource string, in domain.GitHubPushEnable) (domain.GitHubPushPolicy, error) {
	if !validPushCommand(domain.GitHubPushCommand{ExpectedPolicyGeneration: in.ExpectedPolicyGeneration, RequestID: in.RequestID}) {
		return domain.GitHubPushPolicy{}, InvalidInput(errors.New("invalid enabled command"))
	}
	repo, actor, err := s.githubPushActor(ctx, resource, domain.RoleOwner)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	event, err := s.auditEvent(ctx, actor, "hosting.github_push.enabled", resource, nil)
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	return repo.SetGitHubPushEnabled(ctx, actor, resource, in, event)
}

// ListGitHubPushDeliveries reads closed bounded history without exposing payloads or keys.
func (s *Service) ListGitHubPushDeliveries(ctx context.Context, resource, cursor string, limit int) (domain.GitHubPushDeliveryPage, error) {
	if limit < 1 || limit > 50 || cursor != "" && !domain.ValidGitHubPushRequestID(cursor) {
		return domain.GitHubPushDeliveryPage{}, InvalidInput(errors.New("invalid delivery pagination"))
	}
	repo, actor, err := s.githubPushActor(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.GitHubPushDeliveryPage{}, err
	}
	return repo.ListGitHubPushDeliveries(ctx, actor, resource, cursor, limit)
}

// RetryGitHubPushDelivery opens one idempotent bounded owner recovery window.
func (s *Service) RetryGitHubPushDelivery(ctx context.Context, resource string, in domain.GitHubPushRetry) (domain.GitHubPushDelivery, error) {
	if !validPushCommand(domain.GitHubPushCommand{ExpectedPolicyGeneration: in.ExpectedPolicyGeneration, RequestID: in.RequestID}) || !domain.ValidGitHubPushRequestID(in.DeliveryID) {
		return domain.GitHubPushDelivery{}, InvalidInput(errors.New("invalid delivery retry"))
	}
	repo, actor, err := s.githubPushActor(ctx, resource, domain.RoleOwner)
	if err != nil {
		return domain.GitHubPushDelivery{}, err
	}
	event, err := s.auditEvent(ctx, actor, "hosting.github_push.retry_requested", resource, nil)
	if err != nil {
		return domain.GitHubPushDelivery{}, err
	}
	return repo.RetryGitHubPushDelivery(ctx, actor, resource, in, event)
}
func (s *Service) pushSecret(ctx context.Context, repo store.GitHubPushRepository, a store.GitHubPushAuthorization, purpose string) ([]byte, error) {
	if s.secretKeyring == nil {
		return nil, store.ErrGitHubPushUnavailable
	}
	record, err := repo.AuthorizeGitHubPushSecret(ctx, a, purpose)
	if err != nil {
		return nil, err
	}
	defer clear(record.Envelope.Data)
	plaintext, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: a.Policy.ProjectID, SecretID: record.Metadata.SecretID, Version: record.Metadata.Version}, record.Envelope)
	if err != nil {
		return nil, store.ErrGitHubPushUnavailable
	}
	if ctx.Err() != nil || !s.clock().Before(a.Until) {
		clear(plaintext)
		return nil, store.ErrGitHubPushUnavailable
	}
	return plaintext, nil
}
func (s *Service) verifyGitHubPush(ctx context.Context, repo store.GitHubPushRepository, a store.GitHubPushAuthorization) (githubpush.Evidence, string) {
	var zero githubpush.Evidence
	if s.githubPushProvider == nil {
		return zero, "provider_unavailable"
	}
	var credential []byte
	if a.Configuration.ProviderReadSecret != nil {
		plaintext, err := s.pushSecret(ctx, repo, a, "github_read")
		if err != nil {
			return zero, "authority_unavailable"
		}
		defer clear(plaintext)
		material, err := decodeHostingSourceCredential("https_basic", plaintext)
		if err != nil {
			return zero, "provider_credentials_required"
		}
		defer material.Close()
		credential = material.Bytes
	} else if a.Source.CredentialKind == "https_basic" {
		return zero, "provider_credentials_required"
	}
	parts := strings.Split(a.Policy.Repository, "/")
	if len(parts) != 2 {
		return zero, "provider_response_invalid"
	}
	proof, err := s.githubPushProvider.Verify(ctx, githubpush.Request{Owner: parts[0], Repository: parts[1], Branch: a.Policy.Branch, RepositoryID: a.Policy.RepositoryID, DesiredCommit: a.Policy.DesiredCommit, Private: a.Source.CredentialKind == "https_basic"}, credential)
	if err != nil {
		var failure *githubpush.Failure
		if errors.As(err, &failure) {
			return zero, failure.Reason
		}
		return zero, "provider_unavailable"
	}
	return proof, ""
}

// ProcessGitHubPushDelivery resolves only opaque durable intent. No keys enter River arguments.
func (s *Service) ProcessGitHubPushDelivery(ctx context.Context, id string) error {
	repo, err := s.githubPushRepository()
	if err != nil {
		return err
	}
	a, err := repo.BeginGitHubPushDelivery(ctx, id)
	if err != nil {
		return err
	}
	if a.IntentID == "" {
		return nil
	}
	evidence, reason := s.verifyGitHubPush(ctx, repo, a)
	return repo.CommitGitHubPushDelivery(ctx, a, evidence, reason)
}

// ReceiveGitHubPush verifies HMAC over bounded raw bytes before JSON interpretation or persistence.
func (s *Service) ReceiveGitHubPush(ctx context.Context, policy, delivery, event, signature string, raw []byte) (domain.GitHubPushReceipt, error) {
	var zero domain.GitHubPushReceipt
	if len(raw) > domain.GitHubPushPayloadLimit || !domain.ValidGitHubPushRequestID(delivery) || (event != "push" && event != "ping") {
		return zero, InvalidInput(errors.New("invalid GitHub webhook headers"))
	}
	repo, err := s.githubPushRepository()
	if err != nil {
		return zero, err
	}
	a, err := repo.GitHubPushWebhookAuthority(ctx, policy)
	if err != nil {
		return zero, err
	}
	key, err := s.pushSecret(ctx, repo, a, "webhook_hmac")
	if err != nil {
		return zero, store.ErrGitHubPushUnavailable
	}
	defer clear(key)
	if len(key) < 32 || len(key) > 256 || !githubpush.VerifySignature(key, raw, signature) {
		return zero, ErrGitHubPushSignature
	}
	if !uniquePushJSON(raw) {
		return zero, InvalidInput(errors.New("invalid GitHub webhook JSON"))
	}
	var payload struct {
		Ref        *string `json:"ref"`
		Before     *string `json:"before"`
		After      *string `json:"after"`
		Deleted    *bool   `json:"deleted"`
		Forced     *bool   `json:"forced"`
		Repository *struct {
			ID   *int64 `json:"id"`
			Fork *bool  `json:"fork"`
		} `json:"repository"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Repository == nil || payload.Repository.ID == nil || payload.Repository.Fork == nil || *payload.Repository.ID != a.Policy.RepositoryID || *payload.Repository.Fork {
		return zero, InvalidInput(errors.New("GitHub repository identity mismatch"))
	}
	after := ""
	if event == "push" {
		if payload.Ref == nil || payload.Before == nil || payload.After == nil || payload.Deleted == nil || payload.Forced == nil || *payload.Deleted || *payload.Forced || *payload.Ref != "refs/heads/"+a.Policy.Branch || !domain.ValidGitHubPushCommit(*payload.After) {
			return zero, InvalidInput(errors.New("unsupported GitHub push"))
		}
		before, err := hex.DecodeString(*payload.Before)
		if err != nil || len(before) != 20 || strings.ToLower(*payload.Before) != *payload.Before {
			return zero, InvalidInput(errors.New("invalid GitHub before commit"))
		}
		after = *payload.After
	}
	digest := sha256.Sum256(raw)
	return repo.AcceptGitHubPushDelivery(ctx, a, delivery, "sha256:"+hex.EncodeToString(digest[:]), after, event == "ping")
}

// uniquePushJSON rejects duplicate keys and trailing documents while permitting
// unrelated nullable fields in GitHub's extensible payload. Only fixed fields are interpreted.
func uniquePushJSON(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, e := decoder.Token()
				key, ok := token.(string)
				if e != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := decoder.Token()
			return e == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := decoder.Token()
			return e == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !walk(0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

var _ store.GitHubPushRepository = (*store.PostgresStore)(nil)
