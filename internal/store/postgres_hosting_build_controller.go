package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// deploymentOnlyHandler preserves optional River build-worker registration.
type deploymentOnlyHandler struct{ store *PostgresStore }

func (h deploymentOnlyHandler) PrepareDeployment(ctx context.Context, id string) error {
	return h.store.PrepareDeployment(ctx, id)
}

func (h deploymentOnlyHandler) PrepareDatabaseBackup(ctx context.Context, id string) error {
	return h.store.PrepareDatabaseBackup(ctx, id)
}

func (h deploymentOnlyHandler) PrepareStorageArchive(ctx context.Context, id string) error {
	return h.store.PrepareStorageArchive(ctx, id)
}

func (h deploymentOnlyHandler) PrepareProxy(ctx context.Context, id string) error {
	return h.store.PrepareProxy(ctx, id)
}

var _ hostingjobs.Handler = deploymentOnlyHandler{}
var _ hostingjobs.ProxyHandler = deploymentOnlyHandler{}

// ConfigureHostingBuild freezes trusted operator policy before worker startup.
// A nil policy disables admission and build worker registration.
func (s *PostgresStore) ConfigureHostingBuild(policy *domain.HostingBuildPolicy) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	if policy == nil {
		s.hostingBuildPolicy = nil
		return nil
	}
	if err := domain.ValidateHostingBuildPolicy(*policy); err != nil {
		return err
	}
	copy := *policy
	s.hostingBuildPolicy = &copy
	return nil
}

// RequestPublicHostingBuild admits public Git intent through the real River transaction.
func (s *PostgresStore) RequestPublicHostingBuild(ctx context.Context, id, resource, actor string, in domain.HostingBuildRequest, event domain.AuditEvent) (domain.HostingBuild, error) {
	if s.hostingBuildPolicy == nil || s.hostingJobs == nil {
		return domain.HostingBuild{}, ErrHostingBuildUnavailable
	}
	if in.GrantID != "" {
		return domain.HostingBuild{}, ErrConflict
	}
	src, err := s.GetHostingBuildSource(ctx, actor, resource)
	if err != nil {
		return domain.HostingBuild{}, err
	}
	if src.CredentialKind != "public" {
		return domain.HostingBuild{}, ErrConflict
	}
	return s.RequestConfiguredHostingBuild(ctx, id, resource, actor, in, event)
}

// RequestConfiguredHostingBuild enqueues supported Git intent with its frozen grant.
func (s *PostgresStore) RequestConfiguredHostingBuild(ctx context.Context, id, resource, actor string, in domain.HostingBuildRequest, event domain.AuditEvent) (domain.HostingBuild, error) {
	return s.requestConfiguredHostingBuild(ctx, id, resource, actor, in, nil, event)
}

// RequestConfiguredHostingBuildAtRevision freezes trusted trigger intent before checkout.
func (s *PostgresStore) RequestConfiguredHostingBuildAtRevision(ctx context.Context, id, resource, actor string, in domain.HostingBuildRequest, revision domain.HostingBuildTriggerRevision, event domain.AuditEvent) (domain.HostingBuild, error) {
	return s.requestConfiguredHostingBuild(ctx, id, resource, actor, in, &revision, event)
}

func (s *PostgresStore) requestConfiguredHostingBuild(ctx context.Context, id, resource, actor string, in domain.HostingBuildRequest, revision *domain.HostingBuildTriggerRevision, event domain.AuditEvent) (domain.HostingBuild, error) {
	if s.hostingBuildPolicy == nil || s.hostingJobs == nil {
		return domain.HostingBuild{}, ErrHostingBuildUnavailable
	}
	return s.requestHostingBuild(ctx, id, resource, actor, domain.HostingBuildInput(in), *s.hostingBuildPolicy, func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
		if !configuredBuildSource(b) {
			return ErrConflict
		}
		if err := checkConfiguredSourceGrant(ctx, tx, b); err != nil {
			return err
		}
		_, err := s.hostingJobs.EnqueueBuildTx(ctx, tx, hostingjobs.PrepareBuildArgs{BuildID: b.ID})
		return err
	}, event, true, revision)
}

// PrepareBuild is DB-only: dispatch binds exactly one canonical internal task.
func (s *PostgresStore) PrepareBuild(ctx context.Context, id string) error {
	if s.hostingBuildPolicy == nil {
		return ErrHostingBuildUnavailable
	}
	b, err := s.DispatchHostingBuild(ctx, id, func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) (string, error) {
		if !configuredBuildSource(b) {
			return "", ErrConflict
		}
		if err := checkConfiguredSourceGrant(ctx, tx, b); err != nil {
			return "", err
		}
		if err := currentBuildExecution(ctx, tx, b); err != nil {
			return "", err
		}
		runID := "build_run_" + b.ID
		err := sqlcgen.New(tx).InsertCanonicalHostingBuildRun(ctx, sqlcgen.InsertCanonicalHostingBuildRunParams{RunID: runID, ProjectID: b.ProjectID, BuildID: b.ID, Actor: b.CreatedBy})
		return runID, err
	}, domain.AuditEvent{ID: "aud_build_dispatch_" + id, ActorID: s.buildCreator(ctx, id)})
	_ = b
	if errors.Is(err, ErrConflict) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrNotFound) {
		// A durable terminal preflight outcome prevents finite River retries from
		// silently stranding accepted work. A concurrent dispatch wins instead.
		_, recordErr := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
			q := sqlcgen.New(tx)
			if _, e := q.LockHostingBuildState(ctx, id); e != nil {
				return false, e
			}
			current, e := readBuild(ctx, q, id)
			if e != nil {
				return false, e
			}
			if current.State != "queued" || current.RunID != "" {
				return true, nil
			}
			if e = casBuild(q.SetHostingBuildState(ctx, sqlcgen.SetHostingBuildStateParams{ID: id, Revision: current.Revision, State: "failed", FailureCode: "authority_lost"})); e != nil {
				return false, e
			}
			return true, buildRunnerAudit(ctx, tx, current, "dispatch_failed", id)
		})
		return recordErr
	}
	return err
}

func configuredBuildSource(b domain.HostingBuild) bool {
	return b.Snapshot.Source.CredentialKind == "public" && b.Snapshot.Grant == nil || b.Snapshot.Source.CredentialKind == "https_basic" && b.Snapshot.Grant != nil && b.Snapshot.Grant.Kind == "https_basic"
}
func (s *PostgresStore) buildCreator(ctx context.Context, id string) string {
	b, err := readBuild(ctx, s.queries, id)
	if err != nil {
		return ""
	}
	return b.CreatedBy
}
func (s *PostgresStore) recoverBuildDispatch(ctx context.Context, tx pgx.Tx) error {
	if s.hostingBuildPolicy == nil {
		return nil
	}
	q := sqlcgen.New(tx)
	ids, err := q.RecoverQueuedHostingBuilds(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueBuildTx(ctx, tx, hostingjobs.PrepareBuildArgs{BuildID: id}); err != nil {
			return err
		}
		if err = q.TouchHostingBuild(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// currentBuildExecution checks current authority while destination/resource locks
// serialize placement and source changes. The lease clock is checked separately last.
func currentBuildExecution(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
	if err := currentBuildIntent(ctx, tx, b); err != nil {
		return err
	}
	if err := requireBuildArgumentCapability(ctx, tx, b.Snapshot.RunnerID, b.Snapshot.Source.Settings); err != nil {
		return err
	}
	return validateBuildRegistrySecret(ctx, tx, b.ProjectID, b.Snapshot.Source.Settings)
}

// currentBuildIntent also supports metadata-only retries of private requests.
// It cannot authorize source acquisition or another engine effect on its own.
func currentBuildIntent(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
	r, err := lockBuildResourceDestination(ctx, tx, b.ResourceID)
	if err != nil {
		return err
	}
	if err = authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer"); err != nil {
		return err
	}
	src, err := readBuildSource(ctx, tx, b.ResourceID)
	if err != nil {
		return err
	}
	if r.ProjectID != b.ProjectID || r.Generation != b.Snapshot.ResourceGeneration || !exactBuildResourceSource(r.Source, src) || domain.HostingDocumentHash(src) != domain.HostingDocumentHash(b.Snapshot.Source) {
		return ErrConflict
	}
	if err = checkBuildPlacement(ctx, tx, b, true); err != nil {
		return err
	}
	if err = checkBuildTriggerRevision(ctx, tx, b.Snapshot); err != nil {
		return err
	}
	// Placement, runner and enrollment locks may wait after the first creator
	// check. Row locks serialize revocation, but token expiry advances with time.
	return authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer")
}

// validateHostingBuildClaim runs in the existing claim transaction after every
// lease and audit write. Starting another transaction here would wait on our own
// task lock. The initial candidate gate alone cannot authorize a later commit.
func (s *PostgresStore) validateHostingBuildClaim(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string, lease domain.RunLease) error {
	if domain.ValidateHostingBuildTask(run) != nil {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	id, err := q.GetHostingBuildTask(ctx, sqlcgen.GetHostingBuildTaskParams{RunID: run.ID, RunnerID: runner})
	if err != nil {
		return hostingError(err)
	}
	b, err := readBuild(ctx, q, id)
	if err != nil {
		return err
	}
	if b.ID != run.RunSpec.HostingBuildID || b.RunID != run.ID || b.Snapshot.RunnerID != runner || b.State != "queued" {
		return ErrConflict
	}
	if err = currentBuildExecution(ctx, tx, b); err != nil {
		return err
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: run.ID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
	return checkBuildAuthority(ctx, q, runner, a, b, true)
}
func (s *PostgresStore) buildClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateHostingBuildTask(run) != nil {
		return false, nil
	}
	q := sqlcgen.New(tx)
	id, err := q.GetHostingBuildTask(ctx, sqlcgen.GetHostingBuildTaskParams{RunID: run.ID, RunnerID: runner})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, err := readBuild(ctx, q, id)
	if err != nil {
		return false, err
	}
	if b.State != "queued" || b.RunID != run.ID || b.ID != run.RunSpec.HostingBuildID || b.Snapshot.RunnerID != runner {
		return false, nil
	}
	if err = currentBuildExecution(ctx, tx, b); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// HostingBuildStatus separates exact historical proof from permission to execute.
// Current authorization failures never erase previously committed acknowledgments.
func (s *PostgresStore) HostingBuildStatus(ctx context.Context, runner string, a domain.HostingBuildAuthority) (hostingbuildprotocol.StatusResponse, error) {
	var result hostingbuildprotocol.StatusResponse
	if hostingbuildprotocol.ValidateAuthority(a) != nil {
		return result, ErrConflict
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		if _, e := q.LockHostingTaskForAuthority(ctx, a.RunID); e != nil {
			return false, e
		}
		b, e := readBuild(ctx, q, a.BuildID)
		if e != nil {
			return false, e
		}
		if b.RunID != a.RunID || b.Snapshot.RunnerID != runner {
			return false, ErrConflict
		}
		if e = checkBuildAuthority(ctx, q, runner, a, b, false); e != nil {
			return false, e
		}
		result.Build = b
		result.CancelRequested = b.State == "cleanup_required" || b.State == "canceled"
		result.Receipts = []hostingbuildprotocol.ReceiptAcknowledgment{}
		for _, phase := range []string{"source", "complete", "cleanup"} {
			row, e := q.GetHostingBuildReceipt(ctx, sqlcgen.GetHostingBuildReceiptParams{BuildID: b.ID, Phase: phase})
			if errors.Is(e, pgx.ErrNoRows) {
				continue
			}
			if e != nil {
				return false, e
			}
			var historical domain.HostingBuildAuthority
			if json.Unmarshal(row.Authority, &historical) != nil {
				return false, ErrConflict
			}
			if historical == a {
				result.Receipts = append(result.Receipts, hostingbuildprotocol.ReceiptAcknowledgment{Authority: a, Phase: phase, ReceiptID: row.ReceiptID, BodyHash: row.BodyHash})
			}
		}
		if b.State != "queued" && b.State != "sourcing" && b.State != "building" {
			return true, nil
		}
		// Expected authorization denial still permits historical proof. SQL errors
		// abort this transaction and are never converted into execution permission.
		if e = currentBuildExecution(ctx, tx, b); e != nil {
			if errors.Is(e, ErrConflict) || errors.Is(e, ErrNotFound) || errors.Is(e, auth.ErrForbidden) {
				return true, nil
			}
			return false, e
		}
		if e = checkBuildAuthority(ctx, q, runner, a, b, true); e != nil {
			return true, nil
		}
		result.MayExecute = true
		return true, nil
	})
	return result, err
}

// ListHostingBuilds authorizes persisted resource ownership before bounded paging.
func (s *PostgresStore) ListHostingBuilds(ctx context.Context, actor, resource string, page Page) (PageResult[domain.HostingBuild], error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (PageResult[domain.HostingBuild], error) {
		q := sqlcgen.New(tx)
		var result PageResult[domain.HostingBuild]
		r, err := q.LockHostingBuildResource(ctx, resource)
		if err != nil {
			return result, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer"); err != nil {
			return result, err
		}
		count, err := q.CountHostingBuilds(ctx, resource)
		if err != nil {
			return result, err
		}
		page = hostingPage(page, int(count))
		result = PageResult[domain.HostingBuild]{Items: []domain.HostingBuild{}, Total: int(count), Limit: page.Limit, Offset: page.Offset}
		ids, err := q.ListHostingBuildIDs(ctx, sqlcgen.ListHostingBuildIDsParams{ResourceID: resource, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
		if err != nil {
			return result, err
		}
		for _, id := range ids {
			b, e := readBuild(ctx, q, id)
			if e != nil {
				return result, e
			}
			result.Items = append(result.Items, b)
		}
		return result, authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer")
	})
}

// renewHostingBuildLease preserves the ordinary fencing CAS while serializing
// current build permission with cancellation. Cleanup evidence retains only the
// previously granted lease window and can never extend it here.
func (s *PostgresStore) renewHostingBuildLease(ctx context.Context, runner string, a domain.HostingBuildAuthority, ttl time.Duration) (domain.RunLease, error) {
	var renewed domain.RunLease
	_, err := s.buildRunnerTransaction(ctx, runner, a, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		if b.State != "queued" && b.State != "sourcing" && b.State != "building" {
			return b, ErrConflict
		}
		if e := freshBuildAuthority(ctx, tx, runner, a, b); e != nil {
			return b, e
		}
		row, e := sqlcgen.New(tx).RenewAuthorizedLease(ctx, sqlcgen.RenewAuthorizedLeaseParams{LeaseID: a.LeaseID, RunnerID: runner, Attempt: int32(a.Attempt), Fence: a.Fence, TtlMicroseconds: ttl.Microseconds()})
		if e != nil {
			return b, hostingError(e)
		}
		renewed = runLeaseFromSQLC(row)
		return b, freshBuildAuthority(ctx, tx, runner, a, b)
	})
	if err != nil {
		return domain.RunLease{}, err
	}
	return renewed, nil
}

// Public API grants retain the exact source generation in their atomic audit.
func checkConfiguredSourceGrant(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
	if b.Snapshot.Grant == nil {
		return nil
	}
	audit, err := sqlcgen.New(tx).GetHostingSourceDeliveryAudit(ctx, "aud_"+b.Snapshot.Grant.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	var metadata struct {
		SourceGeneration int64 `json:"source_generation"`
	}
	if audit.Action != "hosting.source_grant.created" || audit.TargetID != b.Snapshot.Grant.ID || audit.ActorID != b.Snapshot.Grant.CreatedBy || json.Unmarshal(audit.Metadata, &metadata) != nil || metadata.SourceGeneration != b.Snapshot.Source.Generation {
		return ErrConflict
	}
	return nil
}
