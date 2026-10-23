package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

type githubPushDocument struct {
	Policy        domain.GitHubPushPolicy        `json:"policy"`
	Configuration domain.GitHubPushConfiguration `json:"configuration"`
	Source        domain.HostingGitSource        `json:"source"`
}

func pushRandom() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func pushDocument(row sqlcgen.HostingGithubPushPolicy) (githubPushDocument, error) {
	var doc githubPushDocument
	err := json.Unmarshal(row.Body, &doc)
	if err != nil || doc.Policy.ID != row.ID || doc.Policy.Generation != row.Generation || doc.Policy.ResourceID != row.ResourceID || doc.Policy.ProjectID != row.ProjectID {
		return doc, ErrConflict
	}
	return doc, nil
}
func pushPublic(doc githubPushDocument, owner bool) domain.GitHubPushPolicy {
	p := doc.Policy
	p.OwnerConfiguration = nil
	p.OwnerAutomaticDeploymentOptions = nil
	p.CanManage = owner
	if owner {
		cfg := doc.Configuration
		p.OwnerConfiguration = &cfg
	}
	return p
}
func pushSave(ctx context.Context, q *sqlcgen.Queries, doc githubPushDocument) error {
	return q.UpdateGitHubPushPolicy(ctx, sqlcgen.UpdateGitHubPushPolicyParams{ID: doc.Policy.ID, Generation: doc.Policy.Generation, Body: buildJSON(doc)})
}
func pushClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	return sqlcgen.New(tx).HostingObservationClock(ctx)
}

// pushScope selects destination serialization before resource locks. An unbound
// source may be configured, but executable verification/admission still requires placement.
func pushScope(ctx context.Context, tx pgx.Tx, resource, actor, role string) (domain.HostingGitSource, int64, error) {
	q := sqlcgen.New(tx)
	if err := q.LockGitHubPushQuota(ctx); err != nil {
		return domain.HostingGitSource{}, 0, err
	}
	before, err := q.HostingBuildTarget(ctx, resource)
	if err == nil {
		if _, err = lockBuildResourceDestination(ctx, tx, resource); err != nil {
			return domain.HostingGitSource{}, 0, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingGitSource{}, 0, err
	}
	locked, err := q.LockHostingBuildResource(ctx, resource)
	if err != nil {
		return domain.HostingGitSource{}, 0, err
	}
	if before.BindingID == "" {
		if _, err = q.HostingBuildTarget(ctx, resource); !errors.Is(err, pgx.ErrNoRows) {
			return domain.HostingGitSource{}, 0, ErrConflict
		}
	}
	if actor != "" {
		if err = authorizeBuildActor(ctx, tx, actor, locked.ProjectID, role); err != nil {
			return domain.HostingGitSource{}, 0, err
		}
	}
	source, err := readBuildSource(ctx, tx, resource)
	if err != nil {
		return source, 0, err
	}
	if source.ProjectID != locked.ProjectID {
		return source, 0, ErrConflict
	}
	return source, locked.Generation, nil
}
func pushCurrent(ctx context.Context, tx pgx.Tx, doc githubPushDocument, requireTarget bool) error {
	// Persisted policies can predate request validation. Fail before authorizing
	// either secret purpose; owner disable intentionally does not call this path.
	if provider := doc.Configuration.ProviderReadSecret; provider != nil && provider.ID == doc.Configuration.WebhookSecret.ID {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	source, err := readBuildSource(ctx, tx, doc.Policy.ResourceID)
	if err != nil {
		return err
	}
	resource, err := q.LockHostingBuildResource(ctx, source.ResourceID)
	if err != nil {
		return err
	}
	if source.Generation != doc.Policy.SourceGeneration || resource.Generation != doc.Policy.ResourceGeneration || domain.HostingDocumentHash(source) != domain.HostingDocumentHash(doc.Source) {
		return ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, doc.Configuration.AutomationActorID, source.ProjectID, "maintainer"); err != nil {
		return err
	}
	kind, err := q.HostingBuildPrincipalKind(ctx, doc.Configuration.AutomationActorID)
	if err != nil || kind != "service_account" {
		return auth.ErrForbidden
	}
	if requireTarget {
		target, _, e := lockBuildTarget(ctx, tx, source)
		if e != nil {
			return e
		}
		if _, e = q.LockHostingBuildRunner(ctx, sqlcgen.LockHostingBuildRunnerParams{RunnerID: target.RunnerID, RequirePrivateGit: source.CredentialKind == "https_basic", RequireTriggerRevision: true}); e != nil {
			return ErrGitHubPushRunnerUnsupported
		}
	}
	if _, err = checkBuildGrant(ctx, tx, doc.Configuration.SourceGrantID, source); err != nil {
		return err
	}
	refs := []domain.GitHubPushSecretRef{doc.Configuration.WebhookSecret}
	if source.CredentialKind == "https_basic" && doc.Configuration.ProviderReadSecret == nil {
		return ErrConflict
	}
	if doc.Configuration.ProviderReadSecret != nil {
		refs = append(refs, *doc.Configuration.ProviderReadSecret)
	}
	for _, ref := range refs {
		if _, err = q.LockHostingBuildSecretVersion(ctx, sqlcgen.LockHostingBuildSecretVersionParams{ID: ref.ID, ProjectID: source.ProjectID, Version: ref.Version}); err != nil {
			return ErrConflict
		}
	}
	return nil
}

// lockPushPolicy obtains every existing authority lock before the new policy row.
// The quota lock serializes policy writers, and the comparison fails closed if
// a writer outside this controller changes the document while authority waits.
func lockPushPolicy(ctx context.Context, tx pgx.Tx, resource string, target bool) (sqlcgen.HostingGithubPushPolicy, error) {
	q := sqlcgen.New(tx)
	before, err := q.ReadGitHubPushPolicy(ctx, resource)
	if err != nil {
		return before, err
	}
	doc, err := pushDocument(before)
	if err != nil {
		return before, err
	}
	if err = autoPushCurrent(ctx, tx, doc, target); err != nil {
		return before, err
	}
	locked, err := q.LockGitHubPushPolicy(ctx, resource)
	if err == nil && (locked.Generation != before.Generation || string(locked.Body) != string(before.Body)) {
		err = ErrConflict
	}
	return locked, err
}

func pushCommandHash(actor, kind string, in any) string {
	return domain.HostingDocumentHash(struct {
		Actor, Kind string
		Input       any
	}{actor, kind, in})
}
func pushCommand(ctx context.Context, tx pgx.Tx, actor, resource, kind, requestID string, generation, current int64, in any) (sqlcgen.HostingGithubPushCommand, bool, error) {
	q := sqlcgen.New(tx)
	var zero sqlcgen.HostingGithubPushCommand
	if !domain.ValidGitHubPushRequestID(requestID) || generation < 0 {
		return zero, false, ErrConflict
	}
	old, err := q.ReadGitHubPushCommand(ctx, sqlcgen.ReadGitHubPushCommandParams{ResourceID: resource, RequestID: requestID})
	hash := pushCommandHash(actor, kind, in)
	if err == nil {
		if old.ActorID != actor || old.Kind != kind || old.BodyHash != hash {
			return old, false, ErrConflict
		}
		return old, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, false, err
	}
	if generation != current || current == math.MaxInt64 {
		return zero, false, ErrConflict
	}
	token, err := pushRandom()
	if err != nil {
		return zero, false, err
	}
	err = q.InsertGitHubPushCommand(ctx, sqlcgen.InsertGitHubPushCommandParams{ResourceID: resource, RequestID: requestID, ActorID: actor, Kind: kind, BodyHash: hash, ExpectedGeneration: generation, Token: token})
	if err != nil {
		return zero, false, err
	}
	row, err := q.ReadGitHubPushCommand(ctx, sqlcgen.ReadGitHubPushCommandParams{ResourceID: resource, RequestID: requestID})
	return row, true, err
}
func pushFinishCommand(ctx context.Context, q *sqlcgen.Queries, resource, requestID string, value any) error {
	return q.FinishGitHubPushCommand(ctx, sqlcgen.FinishGitHubPushCommandParams{ResourceID: resource, RequestID: requestID, Receipt: buildJSON(value)})
}
func pushAudit(ctx context.Context, tx pgx.Tx, event domain.AuditEvent, actor, action string, doc githubPushDocument) error {
	if event.ID == "" || event.ActorID != actor {
		return ErrConflict
	}
	event.Action = action
	event.TargetID = doc.Policy.ID
	event.Metadata = map[string]any{"project_id": doc.Policy.ProjectID, "resource_id": doc.Policy.ResourceID, "policy_generation": doc.Policy.Generation}
	if event.CreatedAt.IsZero() {
		now, err := pushClock(ctx, tx)
		if err != nil {
			return err
		}
		event.CreatedAt = now
	}
	return createAuditWithQueries(ctx, sqlcgen.New(tx), event)
}

// GetGitHubPushPolicy returns current viewer metadata, with selectors only for current owners.
func (s *PostgresStore) GetGitHubPushPolicy(ctx context.Context, actor, resource string) (domain.GitHubPushPolicy, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushPolicy, error) {
		src, generation, err := autoPushScope(ctx, tx, resource, actor, "viewer", false)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		owner := authorizeBuildActor(ctx, tx, actor, src.ProjectID, "owner") == nil
		q := sqlcgen.New(tx)
		row, err := q.ReadGitHubPushPolicy(ctx, resource)
		if errors.Is(err, pgx.ErrNoRows) {
			p := domain.GitHubPushPolicy{CanManage: owner, ResourceID: resource, ProjectID: src.ProjectID, ResourceGeneration: generation, SourceGeneration: src.Generation, Status: "unconfigured", Reason: "unverified"}
			o, r, b, e := domain.GitHubPushRepositoryBranch(src)
			if e != nil {
				p.Status = "blocked"
				p.Reason = "unsupported_source"
			} else {
				p.Repository = o + "/" + r
				p.Branch = b
			}
			return autoOwnerOptions(ctx, tx, actor, src.ProjectID, p)
		}
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return doc.Policy, err
		}
		if src.Generation != doc.Policy.SourceGeneration || generation != doc.Policy.ResourceGeneration {
			doc.Policy.Status = "blocked"
			doc.Policy.Reason = "source_changed"
		}
		if err = authorizeBuildActor(ctx, tx, actor, src.ProjectID, "viewer"); err != nil {
			return doc.Policy, err
		}
		doc.Policy.AutomaticDeployment, err = autoStatus(ctx, tx, doc)
		if err != nil {
			return doc.Policy, err
		}
		return autoOwnerOptions(ctx, tx, actor, src.ProjectID, pushPublic(doc, owner))
	})
}

// ConfigureGitHubPushPolicy saves explicit owner delegation disabled with a fresh baseline requirement.
func (s *PostgresStore) ConfigureGitHubPushPolicy(ctx context.Context, actor, resource string, in domain.GitHubPushConfigure, event domain.AuditEvent) (domain.GitHubPushPolicy, error) {
	if in.Validate() != nil {
		return domain.GitHubPushPolicy{}, ErrConflict
	}
	if in.Configuration.AutomaticDeployment != nil {
		cfg, _ := domain.CanonicalHostingAutoDeployConfiguration(*in.Configuration.AutomaticDeployment)
		in.Configuration.AutomaticDeployment = &cfg
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushPolicy, error) {
		src, generation, err := autoPushScope(ctx, tx, resource, actor, "owner", in.Configuration.AutomaticDeployment != nil)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		owner, repo, branch, err := domain.GitHubPushRepositoryBranch(src)
		if err != nil {
			return domain.GitHubPushPolicy{}, ErrConflict
		}
		q := sqlcgen.New(tx)
		old, err := q.ReadGitHubPushPolicy(ctx, resource)
		current := int64(0)
		id := ""
		if err == nil {
			current = old.Generation
			id = old.ID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return domain.GitHubPushPolicy{}, err
		}
		command, _, err := pushCommand(ctx, tx, actor, resource, "configure", in.RequestID, in.ExpectedPolicyGeneration, current, in)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		if len(command.Receipt) > 0 {
			var p domain.GitHubPushPolicy
			err = json.Unmarshal(command.Receipt, &p)
			p.OwnerAutomaticDeploymentOptions = nil
			return p, err
		}
		if in.ExpectedResourceGeneration != generation || in.ExpectedSourceGeneration != src.Generation {
			return domain.GitHubPushPolicy{}, ErrConflict
		}
		if id == "" {
			random, e := pushRandom()
			if e != nil {
				return domain.GitHubPushPolicy{}, e
			}
			id = "ghp_" + random
		}
		doc := githubPushDocument{Policy: domain.GitHubPushPolicy{ID: id, ResourceID: resource, ProjectID: src.ProjectID, Generation: current + 1, ResourceGeneration: generation, SourceGeneration: src.Generation, Status: "disabled", Reason: "baseline_reset", Repository: owner + "/" + repo, Branch: branch, ProviderReadConfigured: in.Configuration.ProviderReadSecret != nil}, Configuration: in.Configuration, Source: src}
		if err = pushCurrent(ctx, tx, doc, false); err != nil {
			return doc.Policy, err
		}
		if current > 0 {
			locked, e := q.LockGitHubPushPolicy(ctx, resource)
			if e != nil {
				return doc.Policy, e
			}
			if locked.Generation != old.Generation || string(locked.Body) != string(old.Body) {
				return doc.Policy, ErrConflict
			}
		}
		if current == 0 {
			err = q.InsertGitHubPushPolicy(ctx, sqlcgen.InsertGitHubPushPolicyParams{ID: id, ResourceID: resource, ProjectID: src.ProjectID, Generation: doc.Policy.Generation, Body: buildJSON(doc)})
		} else {
			err = pushSave(ctx, q, doc)
		}
		if err != nil {
			return doc.Policy, err
		}
		if err = autoConfigure(ctx, tx, doc, actor); err != nil {
			return doc.Policy, err
		}
		if doc.Configuration.AutomaticDeployment != nil {
			doc.Policy.AutomaticDeployment = &domain.HostingAutoDeployStatus{Mode: domain.HostingAutoDeployMode, State: "idle", Reason: "awaiting_push"}
			if err = pushSave(ctx, q, doc); err != nil {
				return doc.Policy, err
			}
		}
		if err = q.ObsoleteGitHubPushDeliveries(ctx, id); err != nil {
			return doc.Policy, err
		}
		if err = pushAudit(ctx, tx, event, actor, "hosting.github_push.configured", doc); err != nil {
			return doc.Policy, err
		}
		result := pushPublic(doc, true)
		if err = pushFinishCommand(ctx, q, resource, in.RequestID, result); err != nil {
			return result, err
		}
		if doc.Configuration.AutomaticDeployment != nil {
			if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
				return result, err
			}
		}
		return result, authorizeBuildActor(ctx, tx, actor, src.ProjectID, "owner")
	})
}

// BeginGitHubPushVerification grants a30-second one-command provider verification attempt.
func (s *PostgresStore) BeginGitHubPushVerification(ctx context.Context, actor, resource string, in domain.GitHubPushCommand) (GitHubPushAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (GitHubPushAuthorization, error) {
		var out GitHubPushAuthorization
		src, _, err := autoPushScope(ctx, tx, resource, actor, "owner", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, resource, false)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		command, fresh, err := pushCommand(ctx, tx, actor, resource, "verify", in.RequestID, in.ExpectedPolicyGeneration, row.Generation, in)
		if err != nil {
			return out, err
		}
		if len(command.Receipt) > 0 {
			var replay domain.GitHubPushPolicy
			if err = json.Unmarshal(command.Receipt, &replay); err != nil {
				return out, err
			}
			out.Replay = &replay
			return out, nil
		}
		if in.ExpectedPolicyGeneration != row.Generation {
			return out, ErrConflict
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if !command.ExpiresAt.After(now) {
			return out, ErrConflict
		}
		if err = autoPushCurrent(ctx, tx, doc, false); err != nil {
			return out, err
		}
		if fresh {
			if err = q.SetGitHubPushVerification(ctx, sqlcgen.SetGitHubPushVerificationParams{ID: row.ID, VerificationToken: command.Token}); err != nil {
				return out, err
			}
		} else if row.VerificationToken != command.Token {
			return out, ErrConflict
		}
		out = GitHubPushAuthorization{Policy: doc.Policy, Configuration: doc.Configuration, Source: src, Token: command.Token, Until: command.ExpiresAt, ActorID: actor, RequestID: in.RequestID}
		return out, authorizeBuildActor(ctx, tx, actor, src.ProjectID, "owner")
	})
}

// pushFailureReason prevents provider errors from becoming public diagnostics.
func pushFailureReason(reason string) string {
	switch reason {
	case "", "repository_identity_mismatch", "repository_or_branch_unavailable", "provider_credentials_required", "provider_response_invalid", "rate_limited", "provider_unavailable", "comparison_limit", "ancestry_unverified", "authority_unavailable":
		return reason
	default:
		return "provider_unavailable"
	}
}

func pushProof(doc githubPushDocument, e githubpush.Evidence) bool {
	if e.RepositoryID < 1 || !strings.EqualFold(e.Repository, doc.Policy.Repository) || e.Branch != doc.Policy.Branch || e.Private != (doc.Source.CredentialKind == "https_basic") || !domain.ValidGitHubPushCommit(e.Head) || doc.Policy.RepositoryID != 0 && doc.Policy.RepositoryID != e.RepositoryID {
		return false
	}
	desired := doc.Policy.DesiredCommit
	return desired == "" && e.Status == "initial" || desired == e.Head && e.Status == "identical" && e.Base == desired && e.MergeBase == desired || desired != "" && desired != e.Head && e.Status == "ahead" && e.Base == desired && e.MergeBase == desired && e.AheadBy > 0 && e.AheadBy <= domain.GitHubPushComparisonLimit && e.BehindBy == 0
}

// FinishGitHubPushVerification persists only fresh current identity evidence, never a build.
func (s *PostgresStore) FinishGitHubPushVerification(ctx context.Context, a GitHubPushAuthorization, e githubpush.Evidence, reason string) (domain.GitHubPushPolicy, error) {
	reason = pushFailureReason(reason)
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushPolicy, error) {
		_, _, err := autoPushScope(ctx, tx, a.Policy.ResourceID, a.ActorID, "owner", false)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, a.Policy.ResourceID, false)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return doc.Policy, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return doc.Policy, err
		}
		if row.Generation != a.Policy.Generation || row.VerificationToken != a.Token || !a.Until.After(now) || row.VerificationExpiresAt == nil || !row.VerificationExpiresAt.After(now) {
			return doc.Policy, ErrConflict
		}
		if err = autoPushCurrent(ctx, tx, doc, false); err != nil {
			return doc.Policy, err
		}
		if reason == "" && !pushProof(doc, e) {
			reason = "ancestry_unverified"
		}
		doc.Policy.Generation++
		if reason != "" {
			doc.Policy.Status = "blocked"
			doc.Policy.Reason = reason
			doc.Policy.VerifiedAt = nil
		} else {
			doc.Policy.Status = "verified"
			doc.Policy.Reason = "verified"
			doc.Policy.VerifiedAt = &now
			doc.Policy.RepositoryID = e.RepositoryID
			if doc.Policy.DesiredCommit == "" {
				doc.Policy.DesiredCommit = e.Head
			}
		}
		if err = pushSave(ctx, q, doc); err != nil {
			return doc.Policy, err
		}
		result := pushPublic(doc, true)
		if err = pushAudit(ctx, tx, domain.AuditEvent{ID: "aud_push_verify_" + a.Token, ActorID: a.ActorID}, a.ActorID, "hosting.github_push.verified", doc); err != nil {
			return result, err
		}
		if err = pushFinishCommand(ctx, q, a.Policy.ResourceID, a.RequestID, result); err != nil {
			return result, err
		}
		now, err = pushClock(ctx, tx)
		if err != nil {
			return result, err
		}
		if !a.Until.After(now) {
			return result, ErrConflict
		}
		return result, authorizeBuildActor(ctx, tx, a.ActorID, doc.Policy.ProjectID, "owner")
	})
}

// SetGitHubPushEnabled consumes fresh verification or explicitly disables demand.
func (s *PostgresStore) SetGitHubPushEnabled(ctx context.Context, actor, resource string, in domain.GitHubPushEnable, event domain.AuditEvent) (domain.GitHubPushPolicy, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushPolicy, error) {
		_, _, err := autoPushScope(ctx, tx, resource, actor, "owner", false)
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		q := sqlcgen.New(tx)
		// A current owner can always stop demand after delegated authority is
		// revoked. Enabling still obtains all current automation/target locks.
		var row sqlcgen.HostingGithubPushPolicy
		if in.Enabled {
			row, err = lockPushPolicy(ctx, tx, resource, true)
		} else {
			row, err = q.LockGitHubPushPolicy(ctx, resource)
		}
		if err != nil {
			return domain.GitHubPushPolicy{}, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return doc.Policy, err
		}
		command, _, err := pushCommand(ctx, tx, actor, resource, "enabled", in.RequestID, in.ExpectedPolicyGeneration, row.Generation, in)
		if err != nil {
			return doc.Policy, err
		}
		if len(command.Receipt) > 0 {
			var p domain.GitHubPushPolicy
			err = json.Unmarshal(command.Receipt, &p)
			p.OwnerAutomaticDeploymentOptions = nil
			return p, err
		}
		if in.Enabled {
			if doc.Configuration.AutomaticDeployment != nil && (s.hostingJobs == nil || !s.hostingJobs.AutoDeployEnabled()) {
				return doc.Policy, ErrGitHubPushUnavailable
			}
			if s.hostingJobs == nil || s.hostingBuildPolicy == nil {
				return doc.Policy, ErrGitHubPushUnavailable
			}
			if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
				return doc.Policy, err
			}
			now, e := pushClock(ctx, tx)
			if e != nil {
				return doc.Policy, e
			}
			if doc.Policy.VerifiedAt == nil || doc.Policy.VerifiedAt.After(now) || now.Sub(*doc.Policy.VerifiedAt) > time.Minute || doc.Policy.RepositoryID < 1 {
				return doc.Policy, ErrConflict
			}
		}
		doc.Policy.Generation++
		doc.Policy.Enabled = in.Enabled
		doc.Policy.Status = "disabled"
		doc.Policy.Reason = "disabled"
		if in.Enabled {
			doc.Policy.Status = "enabled"
			doc.Policy.Reason = "verified"
		}
		if err = pushSave(ctx, q, doc); err != nil {
			return doc.Policy, err
		}
		if err = q.ObsoleteGitHubPushDeliveries(ctx, row.ID); err != nil {
			return doc.Policy, err
		}
		if err = pushAudit(ctx, tx, event, actor, "hosting.github_push.enabled", doc); err != nil {
			return doc.Policy, err
		}
		result := pushPublic(doc, true)
		if err = pushFinishCommand(ctx, q, resource, in.RequestID, result); err != nil {
			return result, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, doc.Policy.ProjectID, "owner"); err != nil {
			return result, err
		}
		if in.Enabled {
			now, e := pushClock(ctx, tx)
			if e != nil {
				return result, e
			}
			if doc.Policy.VerifiedAt == nil || doc.Policy.VerifiedAt.After(now) || now.Sub(*doc.Policy.VerifiedAt) > time.Minute {
				return result, ErrConflict
			}
		}
		return result, nil
	})
}

// GitHubPushWebhookAuthority reveals only current authorized key scope to the ingress verifier.
func (s *PostgresStore) GitHubPushWebhookAuthority(ctx context.Context, id string) (GitHubPushAuthorization, error) {
	before, err := s.queries.ReadGitHubPushPolicyByID(ctx, id)
	if err != nil {
		return GitHubPushAuthorization{}, hostingError(err)
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (GitHubPushAuthorization, error) {
		var out GitHubPushAuthorization
		_, _, err := autoPushScope(ctx, tx, before.ResourceID, "", "", false)
		if err != nil {
			return out, err
		}
		row, err := lockPushPolicy(ctx, tx, before.ResourceID, false)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		if err = autoPushCurrent(ctx, tx, doc, false); err != nil {
			return out, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		out = GitHubPushAuthorization{Policy: doc.Policy, Configuration: doc.Configuration, Source: doc.Source, Until: now.Add(30 * time.Second)}
		return out, nil
	})
}

// AuthorizeGitHubPushSecret audits an immutable purpose scope before decryption.
// The idempotent audit records delegation scope, not each rejected unauthenticated
// request. Every use still rechecks current authority, revocation and deadlines.
func (s *PostgresStore) AuthorizeGitHubPushSecret(ctx context.Context, a GitHubPushAuthorization, purpose string) (ManagedSecretRecord, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (ManagedSecretRecord, error) {
		var out ManagedSecretRecord
		_, _, err := autoPushScope(ctx, tx, a.Policy.ResourceID, "", "", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, a.Policy.ResourceID, false)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		if row.Generation != a.Policy.Generation {
			return out, ErrConflict
		}
		if err = autoPushCurrent(ctx, tx, doc, false); err != nil {
			return out, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if !a.Until.After(now) {
			return out, ErrConflict
		}
		ref := doc.Configuration.WebhookSecret
		if purpose == "github_read" {
			if doc.Configuration.ProviderReadSecret == nil {
				return out, ErrConflict
			}
			ref = *doc.Configuration.ProviderReadSecret
			if a.IntentID == "" {
				if row.VerificationToken != a.Token || row.VerificationExpiresAt == nil || !row.VerificationExpiresAt.After(now) {
					return out, ErrConflict
				}
			} else {
				delivery, e := q.LockGitHubPushDelivery(ctx, a.IntentID)
				if e != nil || delivery.Token != a.Token || delivery.TokenExpiresAt == nil || !delivery.TokenExpiresAt.After(now) || !delivery.ExpiresAt.After(now) || !doc.Policy.Enabled {
					return out, ErrConflict
				}
			}
		} else if purpose != "webhook_hmac" {
			return out, ErrConflict
		}
		record, err := q.ReadGitHubPushSecret(ctx, sqlcgen.ReadGitHubPushSecretParams{ProjectID: doc.Policy.ProjectID, ID: ref.ID, Version: ref.Version})
		if err != nil {
			return out, ErrConflict
		}
		identity := domain.HostingDocumentHash(struct {
			Policy     string
			Generation int64
			Purpose    string
			Secret     domain.GitHubPushSecretRef
		}{row.ID, row.Generation, purpose, ref})
		auditID := "aud_push_key_" + strings.TrimPrefix(identity, "sha256:")[:40]
		exists, err := q.GitHubPushAuditExists(ctx, auditID)
		if err != nil {
			return out, err
		}
		if !exists {
			if err = pushAudit(ctx, tx, domain.AuditEvent{ID: auditID, ActorID: doc.Configuration.AutomationActorID}, doc.Configuration.AutomationActorID, "hosting.github_push.secret_authorized."+purpose, doc); err != nil {
				return out, err
			}
		}
		now, err = pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if !a.Until.After(now) {
			return out, ErrConflict
		}
		out = ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: record.SecretID, Version: record.Version, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt}, Envelope: secretcrypto.Envelope{Version: int(record.EnvelopeVersion), KeyID: record.KeyID, Data: record.Ciphertext}}
		return out, nil
	})
}

// ConfigureGitHubPushProcessor installs bounded provider verification before worker startup.
func (s *PostgresStore) ConfigureGitHubPushProcessor(handler func(context.Context, string) error) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	s.githubPushHandler = handler
	return nil
}

// PrepareGitHubPush delegates opaque intent processing without capturing credentials in jobs.
func (s *PostgresStore) PrepareGitHubPush(ctx context.Context, id string) error {
	if s.githubPushHandler == nil {
		return ErrGitHubPushUnavailable
	}
	return s.githubPushHandler(ctx, id)
}

func pushDelivery(row sqlcgen.HostingGithubPushDelivery) domain.GitHubPushDelivery {
	d := domain.GitHubPushDelivery{ID: row.DeliveryID, PolicyID: row.PolicyID, PolicyGeneration: row.PolicyGeneration, State: row.State, Reason: row.Reason, AfterCommit: row.AfterCommit, VerifiedCommit: row.VerifiedCommit, BuildID: row.BuildID, Attempts: int(row.Attempts), ReceivedAt: row.ReceivedAt, ExpiresAt: row.ExpiresAt}
	if row.State == "received" || row.State == "verifying" || row.State == "retry_wait" {
		stamp := row.NextRetryAt
		d.NextRetryAt = &stamp
	}
	return d
}
func pushCapacity(ctx context.Context, q *sqlcgen.Queries, resource, project string, allocate bool) error {
	counts, err := q.GitHubPushCounts(ctx, sqlcgen.GitHubPushCountsParams{ResourceID: resource, ProjectID: project})
	if err != nil {
		return err
	}
	if allocate && (counts.PendingResource >= domain.GitHubPushPendingResourceLimit || counts.PendingProject >= domain.GitHubPushPendingProjectLimit) {
		return ErrGitHubPushCapacity
	}
	if counts.RetainedResource >= domain.GitHubPushReceiptResourceLimit {
		n, e := q.EvictGitHubPushResourceReceipt(ctx, resource)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrGitHubPushCapacity
		}
	}
	if counts.RetainedProject >= domain.GitHubPushReceiptProjectLimit {
		n, e := q.EvictGitHubPushProjectReceipt(ctx, project)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrGitHubPushCapacity
		}
	}
	return nil
}

// AcceptGitHubPushDelivery records signature-verified closed intent, never raw payload.
func (s *PostgresStore) AcceptGitHubPushDelivery(ctx context.Context, a GitHubPushAuthorization, delivery, hash, after string, ping bool) (domain.GitHubPushReceipt, error) {
	if !domain.ValidGitHubPushRequestID(delivery) || !strings.HasPrefix(hash, "sha256:") || len(hash) != 71 || !ping && !domain.ValidGitHubPushCommit(after) {
		return domain.GitHubPushReceipt{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushReceipt, error) {
		out := domain.GitHubPushReceipt{DeliveryID: delivery, State: "accepted"}
		_, _, err := autoPushScope(ctx, tx, a.Policy.ResourceID, "", "", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, a.Policy.ResourceID, false)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		if row.Generation != a.Policy.Generation {
			return out, ErrConflict
		}
		if err = autoPushCurrent(ctx, tx, doc, false); err != nil {
			return out, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if !a.Until.After(now) {
			return out, ErrConflict
		}
		old, err := q.FindGitHubPushDelivery(ctx, sqlcgen.FindGitHubPushDeliveryParams{PolicyID: row.ID, DeliveryID: delivery})
		if err == nil {
			if old.BodyHash != hash {
				return out, ErrConflict
			}
			if old.State == "ignored" && old.Reason == "" {
				out.State = "ignored"
			}
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		ignored := ping || !doc.Policy.Enabled
		if !ignored && (s.hostingJobs == nil || s.hostingBuildPolicy == nil || s.githubPushHandler == nil) {
			return out, ErrGitHubPushUnavailable
		}
		if err = pushCapacity(ctx, q, doc.Policy.ResourceID, doc.Policy.ProjectID, !ignored); err != nil {
			return out, err
		}
		id, err := pushRandom()
		if err != nil {
			return out, err
		}
		state := "received"
		if ignored {
			state = "ignored"
			out.State = "ignored"
		}
		inserted, err := q.InsertGitHubPushDelivery(ctx, sqlcgen.InsertGitHubPushDeliveryParams{ID: "gpd_" + id, PolicyID: row.ID, PolicyGeneration: row.Generation, ResourceID: row.ResourceID, ProjectID: row.ProjectID, DeliveryID: delivery, BodyHash: hash, AfterCommit: after, State: state})
		if err != nil {
			return out, err
		}
		if !ignored {
			if _, err = s.hostingJobs.EnqueueGitHubPushTx(ctx, tx, hostingjobs.GitHubPushArgs{IntentID: inserted.ID}); err != nil {
				return out, err
			}
		}
		now, err = pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if !a.Until.After(now) {
			return out, ErrConflict
		}
		return out, nil
	})
}

// ListGitHubPushDeliveries returns bounded closed history under current viewer access.
func (s *PostgresStore) ListGitHubPushDeliveries(ctx context.Context, actor, resource, cursor string, limit int) (domain.GitHubPushDeliveryPage, error) {
	if limit < 1 || limit > 50 || cursor != "" && !domain.ValidGitHubPushRequestID(cursor) {
		return domain.GitHubPushDeliveryPage{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushDeliveryPage, error) {
		out := domain.GitHubPushDeliveryPage{Items: []domain.GitHubPushDelivery{}}
		src, _, err := autoPushScope(ctx, tx, resource, actor, "viewer", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		policy, err := q.ReadGitHubPushPolicy(ctx, resource)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		rows, err := q.ListGitHubPushDeliveries(ctx, sqlcgen.ListGitHubPushDeliveriesParams{PolicyID: policy.ID, Cursor: cursor, PageLimit: int32(limit + 1)})
		if err != nil {
			return out, err
		}
		if len(rows) > limit {
			out.HasMore = true
			rows = rows[:limit]
		}
		for _, row := range rows {
			out.Items = append(out.Items, pushDelivery(row))
			out.NextCursor = row.DeliveryID
		}
		return out, authorizeBuildActor(ctx, tx, actor, src.ProjectID, "viewer")
	})
}

// RetryGitHubPushDelivery starts one owner-requested bounded window, preserving commit intent identity.
func (s *PostgresStore) RetryGitHubPushDelivery(ctx context.Context, actor, resource string, in domain.GitHubPushRetry, event domain.AuditEvent) (domain.GitHubPushDelivery, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.GitHubPushDelivery, error) {
		var out domain.GitHubPushDelivery
		_, _, err := autoPushScope(ctx, tx, resource, actor, "owner", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, resource, true)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		if !doc.Policy.Enabled {
			return out, ErrConflict
		}
		if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
			return out, err
		}
		command, _, err := pushCommand(ctx, tx, actor, resource, "retry", in.RequestID, in.ExpectedPolicyGeneration, row.Generation, in)
		if err != nil {
			return out, err
		}
		if len(command.Receipt) > 0 {
			err = json.Unmarshal(command.Receipt, &out)
			return out, err
		}
		delivery, err := q.FindGitHubPushDelivery(ctx, sqlcgen.FindGitHubPushDeliveryParams{PolicyID: row.ID, DeliveryID: in.DeliveryID})
		if err != nil {
			return out, err
		}
		if delivery.PolicyGeneration != row.Generation || delivery.State != "blocked_exhausted" && delivery.State != "retry_wait" {
			return out, ErrConflict
		}
		if delivery.State == "blocked_exhausted" {
			counts, e := q.GitHubPushCounts(ctx, sqlcgen.GitHubPushCountsParams{ResourceID: resource, ProjectID: doc.Policy.ProjectID})
			if e != nil {
				return out, e
			}
			if counts.PendingResource >= domain.GitHubPushPendingResourceLimit || counts.PendingProject >= domain.GitHubPushPendingProjectLimit {
				return out, ErrGitHubPushCapacity
			}
		}
		current, err := q.RetryGitHubPushDelivery(ctx, delivery.ID)
		if err != nil {
			return out, err
		}
		if s.hostingJobs == nil {
			return out, ErrGitHubPushUnavailable
		}
		if _, err = s.hostingJobs.EnqueueGitHubPushTx(ctx, tx, hostingjobs.GitHubPushArgs{IntentID: current.ID}); err != nil {
			return out, err
		}
		if err = pushAudit(ctx, tx, event, actor, "hosting.github_push.retry_requested", doc); err != nil {
			return out, err
		}
		out = pushDelivery(current)
		if err = pushFinishCommand(ctx, q, resource, in.RequestID, out); err != nil {
			return out, err
		}
		return out, authorizeBuildActor(ctx, tx, actor, doc.Policy.ProjectID, "owner")
	})
}

// BeginGitHubPushDelivery acquires a bounded processing token; keys and provider IO remain outside transactions.
func (s *PostgresStore) BeginGitHubPushDelivery(ctx context.Context, id string) (GitHubPushAuthorization, error) {
	before, err := s.queries.ReadGitHubPushDelivery(ctx, id)
	if err != nil {
		return GitHubPushAuthorization{}, hostingError(err)
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (GitHubPushAuthorization, error) {
		var out GitHubPushAuthorization
		_, _, err := autoPushScope(ctx, tx, before.ResourceID, "", "", false)
		if err != nil {
			return out, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, before.ResourceID, true)
		if err != nil {
			return out, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return out, err
		}
		if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
			return out, err
		}
		delivery, err := q.LockGitHubPushDelivery(ctx, id)
		if err != nil {
			return out, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return out, err
		}
		if delivery.PolicyGeneration != row.Generation || !doc.Policy.Enabled {
			_, err = q.FinishGitHubPushDelivery(ctx, sqlcgen.FinishGitHubPushDeliveryParams{ID: id, State: "obsolete", Reason: "policy_changed"})
			return out, err
		}
		if delivery.State != "received" && delivery.State != "retry_wait" && delivery.State != "verifying" {
			return out, nil
		}
		if delivery.Attempts >= domain.GitHubPushMaxAttempts || !delivery.ExpiresAt.After(now) {
			_, err = q.FinishGitHubPushDelivery(ctx, sqlcgen.FinishGitHubPushDeliveryParams{ID: id, State: "blocked_exhausted", Reason: "blocked_exhausted"})
			return out, err
		}
		if delivery.State == "verifying" && delivery.TokenExpiresAt != nil && delivery.TokenExpiresAt.After(now) {
			return out, nil
		}
		if delivery.State == "retry_wait" && delivery.NextRetryAt.After(now) {
			return out, nil
		}
		token, err := pushRandom()
		if err != nil {
			return out, err
		}
		claimed, err := q.ClaimGitHubPushDelivery(ctx, sqlcgen.ClaimGitHubPushDeliveryParams{ID: id, Token: token})
		if err != nil {
			return out, err
		}
		if claimed.TokenExpiresAt == nil {
			return out, ErrConflict
		}
		until := *claimed.TokenExpiresAt
		if claimed.ExpiresAt.Before(until) {
			until = claimed.ExpiresAt
		}
		out = GitHubPushAuthorization{Policy: doc.Policy, Configuration: doc.Configuration, Source: doc.Source, Token: token, Until: until, IntentID: id}
		return out, nil
	})
}

// CommitGitHubPushDelivery atomically advances verified desired commit and admits the existing pinned build.
func (s *PostgresStore) CommitGitHubPushDelivery(ctx context.Context, a GitHubPushAuthorization, e githubpush.Evidence, reason string) error {
	reason = pushFailureReason(reason)
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (accepted bool, resultErr error) {
		_, _, err := autoPushScope(ctx, tx, a.Policy.ResourceID, "", "", false)
		if err != nil {
			return false, err
		}
		q := sqlcgen.New(tx)
		row, err := lockPushPolicy(ctx, tx, a.Policy.ResourceID, true)
		if err != nil {
			return false, err
		}
		doc, err := pushDocument(row)
		if err != nil {
			return false, err
		}
		if err = autoPushCurrent(ctx, tx, doc, true); err != nil {
			return false, err
		}
		delivery, err := q.LockGitHubPushDelivery(ctx, a.IntentID)
		if err != nil {
			return false, err
		}
		now, err := pushClock(ctx, tx)
		if err != nil {
			return false, err
		}
		if row.Generation != a.Policy.Generation || !doc.Policy.Enabled || delivery.PolicyGeneration != row.Generation || delivery.Token != a.Token || delivery.TokenExpiresAt == nil || !delivery.TokenExpiresAt.After(now) || !delivery.ExpiresAt.After(now) || !a.Until.After(now) {
			return false, ErrConflict
		}
		defer func() {
			if resultErr != nil {
				return
			}
			stamp, e := pushClock(ctx, tx)
			if e != nil {
				resultErr = e
				return
			}
			if !a.Until.After(stamp) || !delivery.ExpiresAt.After(stamp) {
				resultErr = ErrConflict
			}
		}()
		finish := func(state, why, commit, build string, retry int32) error {
			_, e := q.FinishGitHubPushDelivery(ctx, sqlcgen.FinishGitHubPushDeliveryParams{ID: delivery.ID, State: state, Reason: why, VerifiedCommit: commit, BuildID: build, RetrySeconds: retry})
			return e
		}
		if doc.Policy.DesiredCommit != a.Policy.DesiredCommit {
			reason = "policy_changed"
		}
		if reason == "" && !pushProof(doc, e) {
			reason = "ancestry_unverified"
		}
		if reason != "" {
			state := "retry_wait"
			if delivery.Attempts >= domain.GitHubPushMaxAttempts {
				state = "blocked_exhausted"
			}
			seconds := int32(10)
			for n := int32(1); n < delivery.Attempts && seconds < 300; n++ {
				seconds *= 2
			}
			if seconds > 300 {
				seconds = 300
			}
			doc.Policy.Status = "blocked"
			doc.Policy.Reason = reason
			if err = pushSave(ctx, q, doc); err != nil {
				return false, err
			}
			return true, finish(state, reason, "", "", seconds)
		}
		// A signed event authorizes only its own after commit. Observing a newer
		// provider HEAD does not turn a delayed A event into a B build request.
		if delivery.AfterCommit != e.Head {
			return true, finish("ignored", "obsolete", e.Head, "", 0)
		}
		existing, err := q.ReadGitHubPushCommitIntent(ctx, sqlcgen.ReadGitHubPushCommitIntentParams{PolicyID: row.ID, PolicyGeneration: row.Generation, SourceGeneration: doc.Policy.SourceGeneration, CommitSha: e.Head})
		if err == nil {
			return true, finish("coalesced", "already_queued", e.Head, existing, 0)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if s.hostingBuildPolicy == nil || s.hostingJobs == nil {
			return false, ErrGitHubPushUnavailable
		}
		identity := domain.HostingDocumentHash(struct {
			Policy             string
			Generation, Source int64
			Commit             string
		}{row.ID, row.Generation, doc.Policy.SourceGeneration, e.Head})
		suffix := strings.TrimPrefix(identity, "sha256:")[:40]
		buildID := "push-build-" + suffix
		request := domain.HostingBuildInput{ExpectedResourceGeneration: doc.Policy.ResourceGeneration, ExpectedSourceGeneration: doc.Policy.SourceGeneration, IdempotencyKey: "github-push-" + suffix, GrantID: doc.Configuration.SourceGrantID}
		revision := domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: e.Head}
		b, err := s.requestHostingBuildTx(ctx, tx, buildID, doc.Policy.ResourceID, doc.Configuration.AutomationActorID, request, *s.hostingBuildPolicy, func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
			if !configuredBuildSource(b) {
				return ErrConflict
			}
			if e := checkConfiguredSourceGrant(ctx, tx, b); e != nil {
				return e
			}
			_, e := s.hostingJobs.EnqueueBuildTx(ctx, tx, hostingjobs.PrepareBuildArgs{BuildID: b.ID})
			return e
		}, domain.AuditEvent{ID: "aud_push_build_" + suffix, ActorID: doc.Configuration.AutomationActorID}, true, &revision)
		if err != nil {
			return false, err
		}
		if err = q.InsertGitHubPushCommitIntent(ctx, sqlcgen.InsertGitHubPushCommitIntentParams{PolicyID: row.ID, PolicyGeneration: row.Generation, SourceGeneration: doc.Policy.SourceGeneration, CommitSha: e.Head, BuildID: b.ID}); err != nil {
			return false, err
		}
		doc.Policy.DesiredCommit = e.Head
		doc.Policy.LastBuildID = b.ID
		doc.Policy.Status = "enabled"
		doc.Policy.Reason = "build_queued"
		if err = pushSave(ctx, q, doc); err != nil {
			return false, err
		}
		if err = s.autoRecordDemand(ctx, tx, doc, delivery, b.ID); err != nil {
			return false, err
		}
		if err = finish("queued", "build_queued", e.Head, b.ID, 0); err != nil {
			return false, err
		}
		// This clock runs after enqueue/audit/write waits. Expiry rolls back the
		// desired pointer, build, durable identity and both River inserts together.
		now, err = pushClock(ctx, tx)
		if err != nil {
			return false, err
		}
		if !a.Until.After(now) || !delivery.ExpiresAt.After(now) {
			return false, ErrConflict
		}
		return true, autoPushCurrent(ctx, tx, doc, true)
	})
	return err
}

// recoverGitHubPushDispatch uses a separate bounded transaction so finite River
// retention/retries never determine whether persisted due demand is discovered.
func (s *PostgresStore) recoverGitHubPushDispatch(ctx context.Context) error {
	if s.githubPushHandler == nil || s.hostingJobs == nil {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		if err := q.LockGitHubPushQuota(ctx); err != nil {
			return false, err
		}
		if err := q.ExpireGitHubPushDeliveries(ctx); err != nil {
			return false, err
		}
		if err := q.PurgeGitHubPushReceipts(ctx); err != nil {
			return false, err
		}
		ids, err := q.RecoverGitHubPushDeliveries(ctx)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err = s.hostingJobs.EnqueueGitHubPushTx(ctx, tx, hostingjobs.GitHubPushArgs{IntentID: id}); err != nil {
				return false, err
			}
			if err = q.TouchGitHubPushDelivery(ctx, id); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}
