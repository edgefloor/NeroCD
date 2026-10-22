package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func buildTransaction[T any](ctx context.Context, s *PostgresStore, f func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer rollbackTransaction(ctx, tx)
	value, err := f(tx)
	if err != nil {
		return zero, hostingError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, hostingError(err)
	}
	return value, nil
}
func buildRoleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "maintainer":
		return 2
	case "viewer":
		return 1
	}
	return 0
}

// Locks keep authorization current through commit; token expiry is checked again
// after all blocking work. Legacy tokens retain existing global-admin semantics.
func authorizeBuildActor(ctx context.Context, tx pgx.Tx, actor, project, minimum string) error {
	q := sqlcgen.New(tx)
	if _, err := q.LockHostingBuildProject(ctx, project); err != nil {
		return auth.ErrForbidden
	}
	kind, err := q.HostingBuildPrincipalKind(ctx, actor)
	if err != nil {
		return auth.ErrForbidden
	}
	role := ""
	switch kind {
	case "human":
		u, e := q.LockHostingBuildUser(ctx, actor)
		if e != nil || u.Status != "active" {
			return auth.ErrForbidden
		}
		if u.GlobalRole == domain.RoleSystemAdmin {
			return nil
		}
		role, err = q.LockHostingBuildMember(ctx, sqlcgen.LockHostingBuildMemberParams{ProjectID: project, UserID: actor})
	case "service_account":
		sa, e := q.LockHostingBuildServiceAccount(ctx, actor)
		if e != nil || sa.ProjectID != project {
			return auth.ErrForbidden
		}
		role = sa.Role
	case "api_token":
		roles, e := q.LockHostingBuildToken(ctx, actor)
		if e == nil && slices.Contains(roles, domain.RoleSystemAdmin) {
			return nil
		}
		return auth.ErrForbidden
	default:
		return auth.ErrForbidden
	}
	if err != nil || buildRoleRank(role) < buildRoleRank(minimum) {
		return auth.ErrForbidden
	}
	return nil
}
func buildAudit(ctx context.Context, tx pgx.Tx, event domain.AuditEvent, actor, action, target string) error {
	if event.ID == "" || event.ActorID != actor {
		return ErrConflict
	}
	event.Action = action
	event.TargetID = target
	event.Metadata = nil
	if b, err := readBuild(ctx, sqlcgen.New(tx), target); err == nil {
		event.Metadata = map[string]any{"project_id": b.ProjectID, "resource_id": b.ResourceID, "binding_id": b.Snapshot.BindingID}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return createAuditWithQueries(ctx, sqlcgen.New(tx), event)
}
func readBuildSource(ctx context.Context, tx pgx.Tx, resource string) (domain.HostingGitSource, error) {
	var value domain.HostingGitSource
	b, err := sqlcgen.New(tx).LockHostingBuildSource(ctx, resource)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal(b, &value); err != nil {
		return value, err
	}
	return value, domain.ValidateHostingGitSource(value)
}
func readBuild(ctx context.Context, q *sqlcgen.Queries, id string) (domain.HostingBuild, error) {
	var value domain.HostingBuild
	b, err := q.GetHostingBuildDocument(ctx, id)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(b, &value)
	return value, err
}
func buildJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func casBuild(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// ConfigureHostingBuildSource is retired. Use the atomic resource command.
func (s *PostgresStore) ConfigureHostingBuildSource(context.Context, string, int64, domain.HostingGitSource, domain.AuditEvent) (domain.HostingGitSource, error) {
	return domain.HostingGitSource{}, ErrConflict
}

// GetHostingBuildSource reads current source settings with current project viewer authorization.
func (s *PostgresStore) GetHostingBuildSource(ctx context.Context, actor, resourceID string) (domain.HostingGitSource, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingGitSource, error) {
		r, err := sqlcgen.New(tx).LockHostingBuildResource(ctx, resourceID)
		if err != nil {
			return domain.HostingGitSource{}, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer"); err != nil {
			return domain.HostingGitSource{}, err
		}
		src, readErr := readBuildSource(ctx, tx, resourceID)
		if readErr != nil {
			return src, readErr
		}
		return src, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer")
	})
}
func grantDomain(row sqlcgen.HostingSourceGrant) domain.HostingSourceGrant {
	return domain.HostingSourceGrant{ID: row.ID, Version: row.Version, SourceID: row.SourceID, ResourceID: row.ResourceID, ProjectID: row.ProjectID, RepositoryURL: row.RepositoryUrl, PolicyHash: row.PolicyHash, SecretID: row.SecretID, SecretVersion: row.SecretVersion, Kind: row.Kind, CreatedBy: row.CreatedBy, Revoked: row.RevokedAt != nil}
}
func checkBuildGrant(ctx context.Context, tx pgx.Tx, id string, src domain.HostingGitSource) (*domain.HostingSourceGrant, error) {
	if src.CredentialKind == "public" {
		if id != "" {
			return nil, ErrConflict
		}
		return nil, nil
	}
	if id == "" {
		return nil, ErrConflict
	}
	q := sqlcgen.New(tx)
	row, err := q.LockHostingSourceGrant(ctx, id)
	if err != nil {
		return nil, ErrConflict
	}
	g := grantDomain(row)
	if g.Revoked || g.SourceID != src.ID || g.ResourceID != src.ResourceID || g.ProjectID != src.ProjectID || g.RepositoryURL != src.RepositoryURL || g.PolicyHash != domain.HostingDocumentHash(src.Policy) || g.Kind != src.CredentialKind || g.Version != 1 {
		return nil, ErrConflict
	}
	if _, err = q.LockHostingBuildSecretVersion(ctx, sqlcgen.LockHostingBuildSecretVersionParams{ID: g.SecretID, ProjectID: g.ProjectID, Version: g.SecretVersion}); err != nil {
		return nil, ErrConflict
	}
	return &g, nil
}

// CreateHostingSourceGrant records an owner-authorized exact source and immutable secret version.
func (s *PostgresStore) CreateHostingSourceGrant(ctx context.Context, actor string, g domain.HostingSourceGrant, event domain.AuditEvent) (domain.HostingSourceGrant, error) {
	if g.ID == "" || len(g.ID) > 128 || g.Version != 1 || g.SecretID == "" || g.SecretVersion < 1 || g.Revoked {
		return g, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingSourceGrant, error) {
		q := sqlcgen.New(tx)
		r, err := q.LockHostingBuildResource(ctx, g.ResourceID)
		if err != nil {
			return g, err
		}
		if r.ProjectID != g.ProjectID {
			return g, ErrConflict
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "owner"); err != nil {
			return g, err
		}
		src, err := readBuildSource(ctx, tx, g.ResourceID)
		if err != nil {
			return g, err
		}
		if src.CredentialKind == "public" || g.SourceID != src.ID || g.RepositoryURL != src.RepositoryURL || g.PolicyHash != domain.HostingDocumentHash(src.Policy) || g.Kind != src.CredentialKind {
			return g, ErrConflict
		}
		if _, err = q.LockHostingBuildSecretVersion(ctx, sqlcgen.LockHostingBuildSecretVersionParams{ID: g.SecretID, ProjectID: g.ProjectID, Version: g.SecretVersion}); err != nil {
			return g, ErrConflict
		}
		g.CreatedBy = actor
		if err = q.CreateHostingSourceGrant(ctx, sqlcgen.CreateHostingSourceGrantParams{ID: g.ID, SourceID: g.SourceID, ResourceID: g.ResourceID, ProjectID: g.ProjectID, SecretID: g.SecretID, SecretVersion: g.SecretVersion, Kind: g.Kind, RepositoryUrl: g.RepositoryURL, PolicyHash: g.PolicyHash, CreatedBy: actor}); err != nil {
			return g, err
		}
		if err = buildAudit(ctx, tx, event, actor, "hosting.source_grant.created", g.ID); err != nil {
			return g, err
		}
		return g, authorizeBuildActor(ctx, tx, actor, g.ProjectID, "owner")
	})
}

// RevokeHostingSourceGrant revokes future use while retaining grant history.
func (s *PostgresStore) RevokeHostingSourceGrant(ctx context.Context, actor, id string, event domain.AuditEvent) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		before, err := q.GetHostingSourceGrant(ctx, id)
		if err != nil {
			return false, err
		}
		if _, err = q.LockHostingBuildResource(ctx, before.ResourceID); err != nil {
			return false, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, before.ProjectID, "owner"); err != nil {
			return false, err
		}
		g, err := q.LockHostingSourceGrant(ctx, id)
		if err != nil {
			return false, err
		}
		if g.RevokedAt != nil {
			return true, authorizeBuildActor(ctx, tx, actor, g.ProjectID, "owner")
		}
		if err = casBuild(q.RevokeHostingSourceGrant(ctx, id)); err != nil {
			return false, err
		}
		if err = buildAudit(ctx, tx, event, actor, "hosting.source_grant.revoked", id); err != nil {
			return false, err
		}
		return true, authorizeBuildActor(ctx, tx, actor, g.ProjectID, "owner")
	})
	return err
}

// lockBuildResourceDestination follows image admission's destination-before-resource
// order. The unlocked immutable placement read selects only the advisory lock;
// the resource/target join is re-read under the resource lock and must match.
func lockBuildResourceDestination(ctx context.Context, tx pgx.Tx, resourceID string) (sqlcgen.LockHostingResourceTargetRow, error) {
	q := sqlcgen.New(tx)
	var zero sqlcgen.LockHostingResourceTargetRow
	before, err := q.HostingBuildTarget(ctx, resourceID)
	if err != nil {
		return zero, err
	}
	if err = q.LockHostingExecutionDestination(ctx, before.DestinationID); err != nil {
		return zero, err
	}
	locked, err := q.LockHostingResourceTarget(ctx, resourceID)
	if err != nil {
		return zero, err
	}
	if locked.BindingID != before.BindingID || locked.DestinationID != before.DestinationID || locked.ServerID != before.ServerID || locked.RunnerID != before.RunnerID {
		return zero, ErrConflict
	}
	return locked, nil
}

// lockBuildTarget never changes hosting_binding_state or a workload mutation hold.
// Callers must already hold lockBuildResourceDestination for src.ResourceID.
func lockBuildTarget(ctx context.Context, tx pgx.Tx, src domain.HostingGitSource) (sqlcgen.HostingBuildTargetRow, string, error) {
	q := sqlcgen.New(tx)
	target, err := q.HostingBuildTarget(ctx, src.ResourceID)
	if err != nil {
		return target, "", err
	}
	revoked, err := q.LockHostingProjectGrant(ctx, sqlcgen.LockHostingProjectGrantParams{DestinationID: target.DestinationID, ProjectID: src.ProjectID})
	if err != nil || revoked != nil {
		return target, "", ErrConflict
	}
	held, err := q.HostingBindingRestoreHeld(ctx, target.BindingID)
	if err != nil {
		return target, "", err
	}
	if held {
		return target, "", ErrHostingRestoreHold
	}
	daemon, err := q.HostingBuildObservedDaemon(ctx, target.BindingID)
	if errors.Is(err, pgx.ErrNoRows) {
		daemon = ""
	} else if err != nil {
		return target, "", err
	}
	if _, err = q.LockHostingBuildRunner(ctx, target.RunnerID); err != nil {
		return target, "", ErrConflict
	}
	if _, err = q.LockHostingBuildEnrollment(ctx, target.RunnerID); err != nil {
		return target, "", ErrConflict
	}
	return target, daemon, nil
}

// RequestHostingBuild snapshots trusted placement and inputs and invokes a DB-only enqueuer atomically.
func (s *PostgresStore) RequestHostingBuild(ctx context.Context, id, resourceID, actor string, in domain.HostingBuildInput, policy domain.HostingBuildPolicy, enqueue HostingBuildEnqueueTx, event domain.AuditEvent) (domain.HostingBuild, error) {
	if enqueue == nil {
		return domain.HostingBuild{}, ErrHostingBuildUnavailable
	}
	if !buildRecordID(id) || in.ExpectedResourceGeneration < 1 || in.ExpectedSourceGeneration < 1 || in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey || domain.ValidateHostingBuildPolicy(policy) != nil {
		return domain.HostingBuild{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		resource, err := lockBuildResourceDestination(ctx, tx, resourceID)
		if err != nil {
			return domain.HostingBuild{}, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, resource.ProjectID, "maintainer"); err != nil {
			return domain.HostingBuild{}, err
		}
		var desired domain.ResourceSource
		if json.Unmarshal(resource.Source, &desired) != nil || desired.Kind != "git" {
			return domain.HostingBuild{}, ErrConflict
		}
		hash := domain.HostingDocumentHash(struct {
			Input  domain.HostingBuildInput
			Policy domain.HostingBuildPolicy
			Actor  string
		}{in, policy, actor})
		old, err := q.HostingBuildIdempotency(ctx, sqlcgen.HostingBuildIdempotencyParams{ResourceID: resourceID, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			if old.RequestHash != hash {
				return domain.HostingBuild{}, ErrConflict
			}
			original, readErr := readBuild(ctx, q, old.ID)
			if readErr != nil {
				return original, readErr
			}
			if err = checkBuildPlacement(ctx, tx, original, true); err != nil {
				return original, err
			}
			return original, authorizeBuildActor(ctx, tx, actor, resource.ProjectID, "maintainer")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.HostingBuild{}, err
		}
		src, err := readBuildSource(ctx, tx, resourceID)
		if err != nil {
			return domain.HostingBuild{}, err
		}
		if !exactBuildResourceSource(resource.Source, src) || resource.Generation != in.ExpectedResourceGeneration || src.Generation != in.ExpectedSourceGeneration {
			return domain.HostingBuild{}, ErrConflict
		}
		if src.Settings.Strategy == "railpack" && policy.Limits.MemoryBytes < 512<<20 {
			return domain.HostingBuild{}, ErrConflict
		}
		target, daemon, err := lockBuildTarget(ctx, tx, src)
		if err != nil {
			return domain.HostingBuild{}, err
		}
		grant, err := checkBuildGrant(ctx, tx, in.GrantID, src)
		if err != nil {
			return domain.HostingBuild{}, err
		}
		snap := domain.HostingBuildSnapshot{BuildID: id, ResourceGeneration: resource.Generation, Source: src, BindingID: target.BindingID, DestinationID: target.DestinationID, ServerID: target.ServerID, RunnerID: target.RunnerID, ExpectedDaemonID: daemon, Policy: policy, Grant: grant}
		snap.InputID = domain.HostingDocumentHash(snap)
		if err = q.InsertHostingBuild(ctx, sqlcgen.InsertHostingBuildParams{ID: id, ResourceID: resourceID, ProjectID: resource.ProjectID, SourceID: src.ID, SourceGeneration: src.Generation, IdempotencyKey: in.IdempotencyKey, RequestHash: hash, Snapshot: buildJSON(snap), SnapshotHash: domain.HostingDocumentHash(snap), CreatedBy: actor}); err != nil {
			return domain.HostingBuild{}, err
		}
		b, err := readBuild(ctx, q, id)
		if err != nil {
			return b, err
		}
		if err = enqueue(ctx, tx, b); err != nil {
			return b, err
		}
		if err = buildAudit(ctx, tx, event, actor, "hosting.build.queued", id); err != nil {
			return b, err
		}
		return b, authorizeBuildActor(ctx, tx, actor, resource.ProjectID, "maintainer")
	})
}
func buildRecordID(id string) bool {
	if len(id) < 1 || len(id) > 63 {
		return false
	}
	for i, c := range id {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' || i == 0 {
					return false
				}
			}
		}
	}
	return true
}

// GetHostingBuild reads historical metadata with current project authorization.
func (s *PostgresStore) GetHostingBuild(ctx context.Context, actor, id string) (domain.HostingBuild, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuild, error) {
		b, err := readBuild(ctx, sqlcgen.New(tx), id)
		if err != nil {
			return b, err
		}
		return b, authorizeBuildActor(ctx, tx, actor, b.ProjectID, "viewer")
	})
}

// DispatchHostingBuild atomically creates and binds a new canonical internal task using a trusted callback.
func (s *PostgresStore) DispatchHostingBuild(ctx context.Context, id string, dispatch HostingBuildDispatchTx, event domain.AuditEvent) (domain.HostingBuild, error) {
	if dispatch == nil {
		return domain.HostingBuild{}, ErrHostingBuildUnavailable
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		b, err := readBuild(ctx, q, id)
		if err != nil {
			return b, err
		}
		if _, err = lockBuildResourceDestination(ctx, tx, b.ResourceID); err != nil {
			return b, err
		}
		if err = authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer"); err != nil {
			return b, err
		}
		if _, err = readBuildSource(ctx, tx, b.ResourceID); err != nil {
			return b, err
		}
		if err = checkBuildPlacement(ctx, tx, b, true); err != nil {
			return b, err
		}
		if _, err = q.LockHostingBuildState(ctx, id); err != nil {
			return b, err
		}
		b, err = readBuild(ctx, q, id)
		if err != nil {
			return b, err
		}
		if b.RunID != "" {
			return b, authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer")
		}
		if b.State != "queued" {
			return b, ErrConflict
		}
		exists, err := q.HostingBuildTaskExists(ctx, "build_run_"+id)
		if err != nil {
			return b, err
		}
		if exists {
			return b, ErrConflict
		}
		runID, err := dispatch(ctx, tx, b)
		if err != nil {
			return b, err
		}
		if runID != "build_run_"+b.ID {
			return b, ErrConflict
		}
		if _, err = q.ValidateHostingBuildTask(ctx, sqlcgen.ValidateHostingBuildTaskParams{ID: runID, ProjectID: b.ProjectID, RequestedBy: b.CreatedBy, Column4: b.ID}); err != nil {
			return b, ErrConflict
		}
		if err = q.InsertHostingBuildTask(ctx, sqlcgen.InsertHostingBuildTaskParams{BuildID: id, RunID: runID, RunnerID: b.Snapshot.RunnerID}); err != nil {
			return b, err
		}
		if err = casBuild(q.BindHostingBuildRun(ctx, sqlcgen.BindHostingBuildRunParams{ID: id, Revision: b.Revision, RunID: &runID})); err != nil {
			return b, err
		}
		if err = buildAudit(ctx, tx, event, b.CreatedBy, "hosting.build.task_bound", id); err != nil {
			return b, err
		}
		if err = authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer"); err != nil {
			return b, err
		}
		return readBuild(ctx, q, id)
	})
}
func checkBuildPlacement(ctx context.Context, tx pgx.Tx, b domain.HostingBuild, credentials bool) error {
	target, daemon, err := lockBuildTarget(ctx, tx, b.Snapshot.Source)
	if err != nil {
		return err
	}
	snap := b.Snapshot
	if target.BindingID != snap.BindingID || target.DestinationID != snap.DestinationID || target.ServerID != snap.ServerID || target.RunnerID != snap.RunnerID {
		return ErrConflict
	}
	if daemon != "" && b.DaemonID != "" && daemon != b.DaemonID {
		return ErrConflict
	}
	if credentials {
		grantID := ""
		if snap.Grant != nil {
			grantID = snap.Grant.ID
		}
		current, err := checkBuildGrant(ctx, tx, grantID, snap.Source)
		if err != nil {
			return err
		}
		if domain.HostingDocumentHash(current) != domain.HostingDocumentHash(snap.Grant) {
			return ErrConflict
		}
	}
	return nil
}

// CancelHostingBuild requests cleanup for dispatched work without taking a workload mutation hold.
func (s *PostgresStore) CancelHostingBuild(ctx context.Context, actor, id string, revision int64, event domain.AuditEvent) (domain.HostingBuild, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		b, err := readBuild(ctx, q, id)
		if err != nil {
			return b, err
		}
		if b.RunID != "" {
			if _, err = q.LockHostingTaskForAuthority(ctx, b.RunID); err != nil {
				return b, err
			}
		}
		if _, err = q.LockHostingBuildResource(ctx, b.ResourceID); err != nil {
			return b, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, b.ProjectID, "maintainer"); err != nil {
			return b, err
		}
		if _, err = q.LockHostingBuildState(ctx, id); err != nil {
			return b, err
		}
		b, err = readBuild(ctx, q, id)
		if err != nil || b.Revision != revision {
			return b, ErrConflict
		}
		if b.State == "succeeded" || b.State == "failed" || b.State == "canceled" {
			return b, ErrConflict
		}
		state := "cleanup_required"
		if b.RunID == "" {
			state = "canceled"
		} else {
			changed, e := q.CancelQueuedHostingBuildTask(ctx, b.RunID)
			if e != nil {
				return b, e
			}
			if changed == 1 {
				state = "canceled"
			}
		}
		if err = casBuild(q.SetHostingBuildState(ctx, sqlcgen.SetHostingBuildStateParams{ID: id, Revision: revision, State: state, FailureCode: "cancel_requested"})); err != nil {
			return b, err
		}
		if err = buildAudit(ctx, tx, event, actor, "hosting.build.cancel_requested", id); err != nil {
			return b, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, b.ProjectID, "maintainer"); err != nil {
			return b, err
		}
		return readBuild(ctx, q, id)
	})
}

// ResolveCurrentHostingBuildArtifact rechecks current generations, authorization, placement, and credential revocation.
func (s *PostgresStore) ResolveCurrentHostingBuildArtifact(ctx context.Context, actor, resourceID, id string) (domain.HostingBuildArtifact, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuildArtifact, error) {
		b, _, err := resolveCurrentHostingBuildArtifactTx(ctx, tx, actor, resourceID, id)
		if err != nil {
			return domain.HostingBuildArtifact{}, err
		}
		return *b.Artifact, nil
	})
}

// resolveCurrentHostingBuildArtifactTx keeps artifact selection in the admission
// transaction. Callers retain the destination/resource locks through commit.
func resolveCurrentHostingBuildArtifactTx(ctx context.Context, tx pgx.Tx, actor, resourceID, id string) (domain.HostingBuild, sqlcgen.LockHostingResourceTargetRow, error) {
	r, err := lockBuildResourceDestination(ctx, tx, resourceID)
	if err != nil {
		return domain.HostingBuild{}, r, err
	}
	fail := func(err error) (domain.HostingBuild, sqlcgen.LockHostingResourceTargetRow, error) {
		return domain.HostingBuild{}, r, err
	}
	if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "maintainer"); err != nil {
		return fail(err)
	}
	src, err := readBuildSource(ctx, tx, resourceID)
	if err != nil {
		return fail(err)
	}
	if !exactBuildResourceSource(r.Source, src) {
		return fail(ErrConflict)
	}
	b, err := readBuild(ctx, sqlcgen.New(tx), id)
	if err != nil {
		return fail(err)
	}
	if b.ResourceID != resourceID || b.ProjectID != r.ProjectID || b.State != "succeeded" || b.Artifact == nil || b.Snapshot.ResourceGeneration != r.Generation || domain.HostingDocumentHash(src) != domain.HostingDocumentHash(b.Snapshot.Source) {
		return fail(ErrConflict)
	}
	if err = checkBuildPlacement(ctx, tx, b, true); err != nil {
		return fail(err)
	}
	if err = domain.ValidateHostingBuildArtifact(*b.Artifact, b); err != nil {
		return fail(ErrConflict)
	}
	if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "maintainer"); err != nil {
		return fail(err)
	}
	return b, r, nil
}
