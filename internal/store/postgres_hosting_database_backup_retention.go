package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type databaseBackupRetentionSource struct {
	databaseBackupScheduleSource
	Scope *domain.HostingDatabaseBackupRetentionSourceScope
}

// Saved policies retain their original archive scope even when the deployment
// is unavailable or now runs another engine. Placement and owner checks remain current.
// The immutable scope is read without a policy lock before the pool lock.
func databaseBackupRetentionAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (databaseBackupRetentionSource, error) {
	base, err := databaseBackupScheduleAuthority(ctx, tx, actor, resource, role)
	value := databaseBackupRetentionSource{databaseBackupScheduleSource: base}
	if err != nil {
		return value, err
	}
	raw, err := sqlcgen.New(tx).ReadDatabaseBackupRetentionSourceScope(ctx, resource)
	if err == nil {
		var saved domain.HostingDatabaseBackupRetentionPolicy
		if json.Unmarshal(raw, &saved) != nil || saved.ResourceID != resource || saved.ProjectID != base.Resource.ProjectID || !backupRetentionScopeValid(saved.Version, saved.SourceScope) {
			return value, ErrConflict
		}
		value.Scope = saved.SourceScope
		value.Version = domain.HostingDatabaseBackupScheduleVersion
		if saved.Version == domain.HostingDatabaseMySQLBackupRetentionVersion {
			value.Version = domain.HostingDatabaseMySQLBackupScheduleVersion
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return value, err
	} else if base.Resource.Source.Kind == "compose" || base.Resource.Source.Kind == "git" {
		h, op, _, sourceErr := databaseBackupComponentSource(ctx, tx, actor, resource, "", role)
		if sourceErr != nil {
			return value, sourceErr
		}
		postgres := false
		for _, component := range h.Stack.Components {
			version := domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, component.Component)
			postgres = postgres || version == domain.HostingDatabaseBackupComponentVersion
			if version == domain.HostingDatabaseMySQLBackupComponentVersion {
				value.Scope = &domain.HostingDatabaseBackupRetentionSourceScope{BackupVersion: domain.HostingDatabaseMySQLBackupComponentVersion, Component: component.Component}
				value.Version = domain.HostingDatabaseMySQLBackupComponentScheduleVersion
			}
		}
		if !postgres && value.Scope == nil || postgres && value.Scope != nil {
			return value, ErrConflict
		}
	}
	if value.Scope != nil {
		present, schemaErr := sqlcgen.New(tx).MySQLComponentBackupRetentionSchemaPresent(ctx)
		if schemaErr != nil {
			return value, schemaErr
		}
		if !present {
			return value, ErrConflict
		}
	}
	if domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version) == "" {
		return value, ErrConflict
	}
	if domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version) == domain.HostingDatabaseMySQLBackupRetentionVersion {
		present, e := sqlcgen.New(tx).MySQLBackupRetentionSchemaPresent(ctx)
		if e != nil {
			return value, e
		}
		if !present {
			return value, ErrConflict
		}
	}
	return value, nil
}

func backupRetentionScopeValid(version string, scope *domain.HostingDatabaseBackupRetentionSourceScope) bool {
	return domain.HostingDatabaseBackupRetentionFormat(version) != "" && (scope == nil || version == domain.HostingDatabaseMySQLBackupRetentionVersion && scope.Validate() == nil)
}

func backupRetentionScopeMatches(version string, scope *domain.HostingDatabaseBackupRetentionSourceScope, p domain.HostingDatabaseBackupPlan) bool {
	if domain.HostingDatabaseBackupRetentionVersionForBackup(p.Operation.Version) != version {
		return false
	}
	if scope == nil {
		// An old policy never gains the newly supported component scope.
		return p.Operation.Version != domain.HostingDatabaseMySQLBackupComponentVersion
	}
	return scope.Validate() == nil && p.Component != nil && p.Operation.Version == scope.BackupVersion && p.Component.Source == scope.Component && p.Operation.SourceComponent == scope.Component
}

func backupRetentionLock(ctx context.Context, tx pgx.Tx) error {
	e := sqlcgen.New(tx).LockDatabaseBackupCopyAdmission(ctx)
	return e
}
func backupRetentionPolicy(ctx context.Context, tx pgx.Tx, resource, version string) (*domain.HostingDatabaseBackupRetentionPolicy, error) {
	var raw []byte
	raw, err := sqlcgen.New(tx).LockDatabaseBackupRetentionPolicy(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p domain.HostingDatabaseBackupRetentionPolicy
	if json.Unmarshal(raw, &p) != nil || p.Version != version || p.ResourceID != resource || !backupRetentionScopeValid(p.Version, p.SourceScope) {
		return nil, ErrConflict
	}
	return &p, nil
}
func backupRetentionReplay(ctx context.Context, tx pgx.Tx, resource, actor, kind, request, hash string, out any) (bool, error) {
	var got, savedActor string
	var raw []byte
	readDatabaseBackupRetentionRequestRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionRequest(ctx, sqlcgen.ReadDatabaseBackupRetentionRequestParams{ResourceID: resource, Kind: kind, RequestID: request})
	savedActor = readDatabaseBackupRetentionRequestRow.ActorID
	got = readDatabaseBackupRetentionRequestRow.RequestHash
	raw = readDatabaseBackupRetentionRequestRow.Response
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if got != hash || savedActor != actor || json.Unmarshal(raw, out) != nil {
		return true, ErrConflict
	}
	return true, nil
}
func backupRetentionSaveRequest(ctx context.Context, tx pgx.Tx, resource, actor, kind, request, hash string, out any) error {
	e := sqlcgen.New(tx).CreateDatabaseBackupRetentionRequest(ctx, sqlcgen.CreateDatabaseBackupRetentionRequestParams{ResourceID: resource, Kind: kind, RequestID: request, ActorID: actor, RequestHash: hash, Response: buildJSON(out)})
	return e
}

// GetDatabaseBackupRetention returns the authorized database backup retention policy.
func (s *PostgresStore) GetDatabaseBackupRetention(ctx context.Context, actor, resource string) (*domain.HostingDatabaseBackupRetentionPolicy, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (*domain.HostingDatabaseBackupRetentionPolicy, error) {
		value, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleViewer)
		if e != nil {
			return nil, e
		}
		return backupRetentionPolicy(ctx, tx, resource, domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version))
	})
}

// SaveDatabaseBackupRetention updates the authorized database backup retention policy.
func (s *PostgresStore) SaveDatabaseBackupRetention(ctx context.Context, actor, resource string, in domain.HostingDatabaseBackupRetentionInput) (domain.HostingDatabaseBackupRetentionPolicy, error) {
	var zero domain.HostingDatabaseBackupRetentionPolicy
	if in.Validate() != nil {
		return zero, domain.ErrInvalidDatabaseBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionPolicy, error) {
		value, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e = backupRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := backupRetentionReplay(ctx, tx, resource, actor, "policy", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		old, e := backupRetentionPolicy(ctx, tx, resource, domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version))
		if e != nil {
			return zero, e
		}
		revision := int64(0)
		now := time.Now().UTC()
		created := now
		if old != nil {
			revision = old.Revision
			created = old.CreatedAt
		}
		if revision != in.ExpectedRevision || value.Resource.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		if in.Enabled && value.Scope != nil {
			h, op, _, sourceErr := databaseBackupComponentSource(ctx, tx, actor, resource, value.Scope.Component, domain.RoleOwner)
			if sourceErr != nil {
				return zero, sourceErr
			}
			if domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, value.Scope.Component) != value.Scope.BackupVersion {
				return zero, ErrConflict
			}
			check := domain.HostingDatabaseBackupPlan{Operation: domain.HostingDatabaseBackupOperation{Version: value.Scope.BackupVersion}, Component: &domain.HostingDatabaseBackupComponentPlan{Source: value.Scope.Component}, RunnerID: h.Snapshot.RunnerID}
			if e = databaseBackupAgent(ctx, tx, check); e != nil {
				return zero, e
			}
			if _, e = sqlcgen.New(tx).LockDatabaseBackupRetentionRunner(ctx, sqlcgen.LockDatabaseBackupRetentionRunnerParams{ID: h.Snapshot.RunnerID, Column2: domain.HostingDatabaseMySQLBackupRetentionCapability, Column3: domain.RunTypeDatabaseBackupRetention}); e != nil {
				return zero, hostingError(e)
			}
		}
		out := domain.HostingDatabaseBackupRetentionPolicy{SourceScope: value.Scope, Version: domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version), ProjectID: value.Resource.ProjectID, ResourceID: resource, Revision: revision + 1, ResourceGeneration: value.Resource.Generation, Enabled: in.Enabled, Automatic: in.Automatic, Local: in.Local, Remote: in.Remote, CreatedAt: created, UpdatedAt: now}
		e = sqlcgen.New(tx).SaveDatabaseBackupRetentionPolicy(ctx, sqlcgen.SaveDatabaseBackupRetentionPolicyParams{ResourceID: resource, ProjectID: out.ProjectID, ActorID: actor, Revision: int64(out.Revision), Body: buildJSON(out)})
		if e != nil {
			return zero, e
		}
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "policy", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		if e = databaseBackupAudit(ctx, tx, actor, resource, fmt.Sprintf("retention_policy_%d", out.Revision), out.ProjectID); e != nil {
			return zero, e
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

func backupRetentionRemoteLocation(p domain.HostingDatabaseBackupTransferPlan) string {
	u, e := url.Parse(p.Target.Target.Endpoint)
	if e != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return domain.HostingDocumentHash(struct{ Scheme, Host, Port, Bucket, Key string }{u.Scheme, host, port, p.Target.Target.Bucket, p.ObjectKey})
}

// Copy registration happens in the original exact completion transaction. A
// previously proven inode/object can be referenced by another backup, but that
// shared reference prevents either owner deleting it independently.
func backupRetentionRegisterLocal(ctx context.Context, tx pgx.Tx, r sqlcgen.HostingDatabaseBackup, in domain.HostingDatabaseBackupMutation) error {
	return backupRetentionRegisterLocalCopy(ctx, tx, r, in, "dbc_local_"+r.ID)
}

func backupRetentionRegisterLocalCopy(ctx context.Context, tx pgx.Tx, r sqlcgen.HostingDatabaseBackup, in domain.HostingDatabaseBackupMutation, copyID string) error {
	if r.Mode != "backup" || in.Phase != "complete" || in.Blob == nil {
		return nil
	}
	p, e := databaseBackupPlan(r)
	if e != nil {
		return e
	}
	c := domain.BackupRetentionCopy{Item: domain.BackupRetentionItem{CopyID: copyID, BackupID: r.ID, Location: "local", Digest: in.Blob.Digest, Bytes: in.Blob.Bytes, CreatedAt: r.CreatedAt, Availability: "unproven", ProtectedReasons: []string{}}, RunnerID: r.RunnerID, DestinationID: p.Source.Snapshot.DestinationID, ServerID: p.Source.Snapshot.ServerID, DaemonID: p.Source.Release.DaemonID, Local: in.LocalCopy}
	location := domain.HostingDocumentHash(struct{ Runner, Digest string }{r.RunnerID, in.Blob.Digest})
	if in.LocalCopy != nil {
		location = backupRetentionLocalLocation(p.Source.Snapshot.ServerID, *in.LocalCopy, in.Blob.Digest)
		c.IdentityHash = domain.HostingDocumentHash(struct {
			Location string
			Proof    *domain.BackupLocalCopyProof
		}{location, in.LocalCopy})
		if !c.Local.Created {
			var prior []byte
			prior, e = sqlcgen.New(tx).FindOwnedDatabaseBackupLocalCopy(ctx, location)
			if e == nil {
				var known domain.BackupRetentionCopy
				if json.Unmarshal(prior, &known) != nil {
					return ErrConflict
				}
				want := *c.Local
				want.Created = true
				if known.Local != nil && *known.Local == want {
					c.Local = &want
				}
			}
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		if c.Local.Created {
			c.Item.Availability = "available"
		}
	}
	if c.Local != nil {
		c.IdentityHash = domain.HostingDocumentHash(struct {
			Location string
			Proof    *domain.BackupLocalCopyProof
		}{location, c.Local})
	}
	if c.IdentityHash == "" {
		c.IdentityHash = location
	}
	// A no-clobber download can prove a new generation at a previously recorded
	// location. Preserve the old ownership document; supersession is not deletion.
	// Reusing known ownership above does not turn adoption into fresh creation.
	if strings.HasPrefix(copyID, "dbc_download_") && in.LocalCopy != nil && in.LocalCopy.Created {
		if e = backupRetentionLock(ctx, tx); e != nil {
			return e
		}
		var priorID string
		var raw []byte
		lockDatabaseBackupLocalCopyPredecessorRow, e := sqlcgen.New(tx).LockDatabaseBackupLocalCopyPredecessor(ctx, sqlcgen.LockDatabaseBackupLocalCopyPredecessorParams{BackupID: r.ID, LocationHash: location})
		priorID = lockDatabaseBackupLocalCopyPredecessorRow.ID
		raw = lockDatabaseBackupLocalCopyPredecessorRow.Body
		if e == nil {
			var prior domain.BackupRetentionCopy
			if json.Unmarshal(raw, &prior) != nil || prior.Local == nil || prior.Local.PoolID != c.Local.PoolID || prior.Local.Device != c.Local.Device || prior.Local.Inode == c.Local.Inode || prior.Local.Bytes != c.Local.Bytes || c.Local.Bytes != c.Item.Bytes || prior.Item.Digest != c.Item.Digest || prior.Item.Bytes != c.Item.Bytes || prior.ServerID != c.ServerID || prior.DestinationID != c.DestinationID || prior.DaemonID != c.DaemonID {
				return ErrConflict
			}
			// Current-authority validation flushes deferred constraints. Defer only
			// this new link until its copy and completion receipt have been written.
			if e = sqlcgen.New(tx).DeferDatabaseBackupCopySupersession(ctx); e != nil {
				return e
			}
			if e = sqlcgen.New(tx).SupersedeDatabaseBackupCopy(ctx, sqlcgen.SupersedeDatabaseBackupCopyParams{ID: priorID, SupersededByCopyID: &copyID}); e != nil {
				return e
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	e = sqlcgen.New(tx).CreateDatabaseBackupLocalCopy(ctx, sqlcgen.CreateDatabaseBackupLocalCopyParams{ID: c.Item.CopyID, BackupID: r.ID, ResourceID: r.SourceResourceID, RunnerID: r.RunnerID, IdentityHash: c.IdentityHash, LocationHash: location, Body: buildJSON(c), Availability: c.Item.Availability, CreatedAt: r.CreatedAt})
	return e
}
func backupRetentionRegisterRemote(ctx context.Context, tx pgx.Tx, r backupRemoteRow, in domain.HostingDatabaseBackupTransferMutation) error {
	if r.view.Mode != "upload" || in.Phase != "complete" {
		return nil
	}
	p := r.plan
	location := backupRetentionRemoteLocation(p)
	c := domain.BackupRetentionCopy{Item: domain.BackupRetentionItem{CopyID: "dbc_remote_" + r.view.ID, BackupID: r.view.BackupID, TransferID: r.view.ID, Location: "remote", Digest: p.Operation.Blob.Digest, Bytes: p.Operation.Blob.Bytes, CreatedAt: r.view.CreatedAt, Availability: "unproven", ProtectedReasons: []string{}}, RunnerID: p.RunnerID, DestinationID: p.Operation.DestinationID, ServerID: p.ServerID, DaemonID: in.DaemonID, Remote: in.RemoteCopy, Target: &p.Target, ObjectKey: p.ObjectKey}
	if c.Remote != nil && !c.Remote.Created {
		var raw []byte
		raw, err := sqlcgen.New(tx).FindOwnedDatabaseBackupRemoteCopy(ctx, location)
		if err == nil {
			var known domain.BackupRetentionCopy
			if json.Unmarshal(raw, &known) != nil {
				return ErrConflict
			}
			want := *c.Remote
			want.Created = true
			if known.Remote != nil && *known.Remote == want && known.Item.Digest == c.Item.Digest && known.Item.Bytes == c.Item.Bytes {
				c.Remote = &want
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if c.Remote != nil && c.Remote.Created {
		c.Item.Availability = "available"
	}
	c.IdentityHash = domain.HostingDocumentHash(struct {
		Location string
		Proof    *domain.BackupRemoteCopyProof
	}{location, c.Remote})
	var resource string
	readDatabaseBackupCopySourceRow, e := sqlcgen.New(tx).ReadDatabaseBackupCopySource(ctx, r.view.BackupID)
	resource = readDatabaseBackupCopySourceRow.SourceResourceID
	c.Item.CreatedAt = readDatabaseBackupCopySourceRow.CreatedAt
	if e != nil {
		return e
	}
	e = sqlcgen.New(tx).CreateDatabaseBackupRemoteCopy(ctx, sqlcgen.CreateDatabaseBackupRemoteCopyParams{ID: c.Item.CopyID, BackupID: c.Item.BackupID, TransferID: &c.Item.TransferID, ResourceID: resource, RunnerID: p.RunnerID, IdentityHash: c.IdentityHash, LocationHash: location, Body: buildJSON(c), Availability: c.Item.Availability, CreatedAt: c.Item.CreatedAt})
	return e
}

func backupRetentionInventory(ctx context.Context, tx pgx.Tx, resource string, policy domain.HostingDatabaseBackupRetentionPolicy) ([]domain.BackupRetentionCopy, error) {
	// Successful legacy operations remain visible, but never acquire new deletion
	// rights merely because a current agent can read their matching contents.
	ids, e := sqlcgen.New(tx).ListLegacyDatabaseBackupIDs(ctx, resource)
	if e != nil {
		return nil, e
	}
	if len(ids) > 100000 {
		return nil, ErrConflict
	}
	for _, id := range ids {
		b, e := sqlcgen.New(tx).ReadDatabaseBackup(ctx, id)
		if e != nil {
			return nil, e
		}
		v, e := databaseBackupView(b)
		if e != nil {
			return nil, e
		}
		if e = backupRetentionRegisterLocal(ctx, tx, b, domain.HostingDatabaseBackupMutation{Phase: "complete", Blob: v.Blob}); e != nil {
			return nil, e
		}
	}
	ids, e = sqlcgen.New(tx).ListLegacyDatabaseBackupUploadIDs(ctx, resource)
	if e != nil {
		return nil, e
	}
	if len(ids) > 100000 {
		return nil, ErrConflict
	}
	for _, id := range ids {
		r, err := backupRemoteLoad(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		if err = backupRetentionRegisterRemote(ctx, tx, r, domain.HostingDatabaseBackupTransferMutation{Phase: "complete", DaemonID: r.plan.DaemonID}); err != nil {
			return nil, err
		}
	}
	rows, e := sqlcgen.New(tx).ListDatabaseBackupRetentionInventory(ctx, resource)
	if e != nil {
		return nil, e
	}
	out := []domain.BackupRetentionCopy{}
	for _, row := range rows {
		raw, availability, refs, active, upload, reserved := row.Body, row.Availability, row.SharedReferences, row.Active, row.UploadPending, row.Reserved
		var c domain.BackupRetentionCopy
		if json.Unmarshal(raw, &c) != nil {
			return nil, ErrConflict
		}
		original, e := sqlcgen.New(tx).ReadDatabaseBackup(ctx, c.Item.BackupID)
		if e != nil {
			return nil, e
		}
		originalPlan, e := databaseBackupPlan(original)
		if e != nil {
			return nil, e
		}
		if !backupRetentionScopeMatches(policy.Version, policy.SourceScope, originalPlan) {
			continue
		}
		c.Item.Availability = availability
		c.Item.ProtectedReasons = []string{}
		if refs > 0 {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "shared")
		}
		if active {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "active")
		}
		if c.Item.Location == "local" && upload {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "upload_pending")
		}
		if reserved {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "reserved")
		}

		owned := c.Local != nil && c.Local.Created && c.Local.Validate() == nil || c.Remote != nil && c.Remote.Created && c.Remote.Validate() == nil
		if !owned {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "unproven_ownership")
		}
		out = append(out, c)
	}
	if len(out) > 100000 {
		return nil, ErrConflict
	}
	return out, nil
}
func backupRetentionPreviewTx(ctx context.Context, tx pgx.Tx, id, resource, actor string, p domain.HostingDatabaseBackupRetentionPolicy) (domain.HostingDatabaseBackupRetentionPreview, []domain.BackupRetentionCopy, error) {
	var zero domain.HostingDatabaseBackupRetentionPreview
	inventory, e := backupRetentionInventory(ctx, tx, resource, p)
	if e != nil {
		return zero, nil, e
	}
	visible := make([]domain.BackupRetentionItem, len(inventory))
	byID := map[string]domain.BackupRetentionCopy{}
	for i, c := range inventory {
		visible[i] = c.Item
		byID[c.Item.CopyID] = c
	}
	items, local, remote, truncated := domain.SelectBackupRetention(p, visible)
	out := domain.HostingDatabaseBackupRetentionPreview{SourceScope: p.SourceScope, ID: id, Version: p.Version, ProjectID: p.ProjectID, ResourceID: resource, PolicyRevision: p.Revision, LocalReferenceAt: local, RemoteReferenceAt: remote, SelectionTruncated: truncated, Items: items, CreatedAt: time.Now().UTC()}
	chosen := []domain.BackupRetentionCopy{}
	for _, item := range items {
		if item.Selected {
			c := byID[item.CopyID]
			c.Item = item
			chosen = append(chosen, c)
		}
	}
	out.PlanHash = domain.HostingDocumentHash(struct {
		Preview domain.HostingDatabaseBackupRetentionPreview
		Copies  []domain.BackupRetentionCopy
	}{out, chosen})
	e = sqlcgen.New(tx).CreateDatabaseBackupRetentionPreview(ctx, sqlcgen.CreateDatabaseBackupRetentionPreviewParams{ID: id, ResourceID: resource, ActorID: actor, PolicyRevision: int64(p.Revision), Body: buildJSON(out), Copies: buildJSON(chosen), PlanHash: out.PlanHash})
	return out, chosen, e
}

// PreviewDatabaseBackupRetention records a retention preview for database backup copies.
func (s *PostgresStore) PreviewDatabaseBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingDatabaseBackupRetentionPreviewInput) (domain.HostingDatabaseBackupRetentionPreview, error) {
	var zero domain.HostingDatabaseBackupRetentionPreview
	if in.Validate() != nil {
		return zero, domain.ErrInvalidDatabaseBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionPreview, error) {
		value, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e := backupRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := backupRetentionReplay(ctx, tx, resource, actor, "preview", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		p, e := backupRetentionPolicy(ctx, tx, resource, domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version))
		if e != nil {
			return zero, e
		}
		if p == nil || p.Revision != in.ExpectedPolicyRevision {
			return zero, ErrConflict
		}
		out, _, e := backupRetentionPreviewTx(ctx, tx, id, resource, actor, *p)
		if e != nil {
			return zero, e
		}
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "preview", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

// ListDatabaseBackupRetentionExecutions lists authorized database backup retention executions.
func (s *PostgresStore) ListDatabaseBackupRetentionExecutions(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupRetentionExecution, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupRetentionExecution, error) {
		if _, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleViewer); e != nil {
			return nil, e
		}
		rows, e := sqlcgen.New(tx).ListDatabaseBackupRetentionExecutionBodies(ctx, resource)
		if e != nil {
			return nil, e
		}
		out := []domain.HostingDatabaseBackupRetentionExecution{}
		for _, raw := range rows {
			var v domain.HostingDatabaseBackupRetentionExecution
			if json.Unmarshal(raw, &v) != nil {
				return nil, ErrConflict
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// GetDatabaseBackupRetentionExecution returns an authorized database backup retention execution.
func (s *PostgresStore) GetDatabaseBackupRetentionExecution(ctx context.Context, actor, resource, id string) (domain.HostingDatabaseBackupRetentionExecution, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionExecution, error) {
		var v domain.HostingDatabaseBackupRetentionExecution
		if _, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleViewer); e != nil {
			return v, e
		}
		var raw []byte
		raw, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionExecutionBody(ctx, sqlcgen.ReadDatabaseBackupRetentionExecutionBodyParams{ID: id, ResourceID: resource})
		if e != nil {
			return v, hostingError(e)
		}
		if json.Unmarshal(raw, &v) != nil {
			return v, ErrConflict
		}
		return v, nil
	})
}

// ExecuteDatabaseBackupRetention admits deletion of the database copies selected by a saved
// preview.
func (s *PostgresStore) ExecuteDatabaseBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingDatabaseBackupRetentionExecutionInput) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var zero domain.HostingDatabaseBackupRetentionExecution
	if in.Validate() != nil {
		return zero, domain.ErrInvalidDatabaseBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionExecution, error) {
		value, e := databaseBackupRetentionAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e := backupRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := backupRetentionReplay(ctx, tx, resource, actor, "execution", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		var existing string
		existing, lookup := sqlcgen.New(tx).FindDatabaseBackupRetentionPreviewExecution(ctx, sqlcgen.FindDatabaseBackupRetentionPreviewExecutionParams{PreviewID: in.PreviewID, ResourceID: resource})
		if lookup == nil {
			return s.backupRetentionResumeTx(ctx, tx, existing, actor, resource, in, hash)
		}
		if !errors.Is(lookup, pgx.ErrNoRows) {
			return zero, lookup
		}
		p, e := backupRetentionPolicy(ctx, tx, resource, domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version))
		if e != nil {
			return zero, e
		}
		if p == nil || p.Revision != in.ExpectedPolicyRevision || !p.Enabled {
			return zero, ErrConflict
		}
		var raw, copyRaw []byte
		var preview domain.HostingDatabaseBackupRetentionPreview
		var chosen []domain.BackupRetentionCopy
		readDatabaseBackupRetentionPreviewRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionPreview(ctx, sqlcgen.ReadDatabaseBackupRetentionPreviewParams{ID: in.PreviewID, ResourceID: resource, PolicyRevision: int64(p.Revision)})
		raw = readDatabaseBackupRetentionPreviewRow.Body
		copyRaw = readDatabaseBackupRetentionPreviewRow.Copies
		if e != nil {
			return zero, hostingError(e)
		}
		if json.Unmarshal(raw, &preview) != nil || json.Unmarshal(copyRaw, &chosen) != nil || preview.PlanHash != in.ExpectedPlanHash || domain.HostingDocumentHash(preview.SourceScope) != domain.HostingDocumentHash(p.SourceScope) || len(chosen) == 0 || len(chosen) > 100 {
			return zero, ErrConflict
		}
		// Recompute against the complete current inventory, never trust UI selection.
		current, e := backupRetentionInventory(ctx, tx, resource, *p)
		if e != nil {
			return zero, e
		}
		var all []domain.BackupRetentionItem
		byID := map[string]domain.BackupRetentionCopy{}
		for _, c := range current {
			all = append(all, c.Item)
			byID[c.Item.CopyID] = c
		}
		selected, _, _, _ := domain.SelectBackupRetention(*p, all)
		eligible := map[string]bool{}
		for _, c := range selected {
			if c.Selected {
				eligible[c.CopyID] = true
			}
		}
		if len(eligible) != len(chosen) {
			return zero, ErrConflict
		}
		runnerID := chosen[0].RunnerID
		now := time.Now().UTC()
		out := domain.HostingDatabaseBackupRetentionExecution{ID: id, Version: p.Version, ProjectID: p.ProjectID, ResourceID: resource, PreviewID: preview.ID, PolicyRevision: p.Revision, PlanHash: preview.PlanHash, State: "queued", ReservationHeld: true, LocalState: "not_selected", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{}, CreatedAt: now, UpdatedAt: now}
		for _, c := range chosen {
			fresh, ok := byID[c.Item.CopyID]
			if !ok || !eligible[c.Item.CopyID] || c.RunnerID != runnerID || fresh.IdentityHash != c.IdentityHash || fresh.Item.Availability != "available" {
				return zero, ErrConflict
			}
			out.Items = append(out.Items, domain.BackupRetentionExecutionItem{CopyID: c.Item.CopyID, BackupID: c.Item.BackupID, Location: c.Item.Location, State: "pending", ReservationHeld: true})
			if c.Item.Location == "local" {
				out.LocalState = "pending"
			} else {
				out.RemoteState = "pending"
			}
		}
		plan := domain.HostingDatabaseBackupRetentionPlan{Operation: out, ActorID: actor, RunnerID: runnerID, Copies: chosen, TimeoutSeconds: 900}
		plan.Hash = domain.HostingDocumentHash(plan)
		if plan.Validate() != nil {
			return zero, ErrConflict
		}
		if e = backupRetentionCurrent(ctx, tx, plan); e != nil {
			return zero, e
		}
		var active bool
		active, e = sqlcgen.New(tx).DatabaseBackupRetentionRunnerBusy(ctx, runnerID)
		if e != nil {
			return zero, e
		}
		if active {
			return zero, ErrBackupRetentionBusy
		}
		if !s.hostingJobs.DatabaseBackupRetentionEnabled() {
			return zero, ErrConflict
		}
		e = sqlcgen.New(tx).CreateDatabaseBackupRetentionExecution(ctx, sqlcgen.CreateDatabaseBackupRetentionExecutionParams{ID: id, ResourceID: resource, ActorID: actor, RunnerID: runnerID, PreviewID: preview.ID, Plan: buildJSON(plan), PlanHash: plan.Hash, Body: buildJSON(out)})
		if e != nil {
			return zero, hostingError(e)
		}
		for _, c := range chosen {
			var location string
			location, e = sqlcgen.New(tx).ReadDatabaseBackupCopyLocation(ctx, c.Item.CopyID)
			if e != nil {
				return zero, e
			}
			if e = sqlcgen.New(tx).CreateDatabaseBackupRetentionItem(ctx, sqlcgen.CreateDatabaseBackupRetentionItemParams{ExecutionID: id, CopyID: c.Item.CopyID, IdentityHash: c.IdentityHash, LocationHash: location}); e != nil {
				return zero, hostingError(e)
			}
			if e = sqlcgen.New(tx).ReserveDatabaseBackupCopy(ctx, c.Item.CopyID); e != nil {
				return zero, e
			}
		}
		if _, e = s.hostingJobs.EnqueueDatabaseBackupRetentionTx(ctx, tx, hostingjobs.DatabaseBackupRetentionArgs{OperationID: id}); e != nil {
			return zero, e
		}
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		if e = databaseBackupAudit(ctx, tx, actor, id, "retention_requested", p.ProjectID); e != nil {
			return zero, e
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}
func backupRetentionCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupRetentionPlan) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	value, e := databaseBackupRetentionAuthority(ctx, tx, p.ActorID, p.Operation.ResourceID, domain.RoleOwner)
	if e != nil {
		return e
	}
	if p.Operation.Version != domain.HostingDatabaseBackupRetentionVersionForSchedule(value.Version) {
		return ErrConflict
	}
	_, e = sqlcgen.New(tx).LockDatabaseBackupRetentionRunner(ctx, sqlcgen.LockDatabaseBackupRetentionRunnerParams{ID: p.RunnerID, Column2: domain.HostingDatabaseBackupRetentionCapabilityFor(p.Operation.Version), Column3: domain.RunTypeDatabaseBackupRetention})
	if e != nil {
		return hostingError(e)
	}
	previewRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionPreview(ctx, sqlcgen.ReadDatabaseBackupRetentionPreviewParams{ID: p.Operation.PreviewID, ResourceID: p.Operation.ResourceID, PolicyRevision: p.Operation.PolicyRevision})
	if e != nil {
		return e
	}
	var preview domain.HostingDatabaseBackupRetentionPreview
	if json.Unmarshal(previewRow.Body, &preview) != nil || preview.PlanHash != p.Operation.PlanHash || domain.HostingDocumentHash(preview.SourceScope) != domain.HostingDocumentHash(value.Scope) || !backupRetentionScopeValid(p.Operation.Version, preview.SourceScope) {
		return ErrConflict
	}
	for _, c := range p.Copies {
		original, e := sqlcgen.New(tx).ReadDatabaseBackup(ctx, c.Item.BackupID)
		if e != nil {
			return hostingError(e)
		}
		originalPlan, e := databaseBackupPlan(original)
		if e != nil {
			return e
		}
		if !backupRetentionScopeMatches(p.Operation.Version, preview.SourceScope, originalPlan) {
			return ErrConflict
		}
		if c.DestinationID != value.Binding.DestinationID {
			return ErrConflict
		}
		var server, runner string
		lockDatabaseBackupRetentionDestinationRow, e := sqlcgen.New(tx).LockDatabaseBackupRetentionDestination(ctx, c.DestinationID)
		server = lockDatabaseBackupRetentionDestinationRow.ServerID
		runner = lockDatabaseBackupRetentionDestinationRow.RunnerID
		if e != nil {
			return e
		}
		if runner != p.RunnerID || server != c.ServerID {
			return ErrConflict
		}
		if c.Target != nil {
			upload, e := backupRemoteLoad(ctx, tx, c.Item.TransferID, false)
			if e != nil {
				return e
			}
			check := upload.plan
			check.ActorID = p.ActorID
			check.Hash = ""
			check.Hash = domain.HostingDocumentHash(check)
			if domain.HostingDocumentHash(check.Target) != domain.HostingDocumentHash(c.Target) || check.ObjectKey != c.ObjectKey {
				return ErrConflict
			}
			if e = backupRemoteCurrent(ctx, tx, check); e != nil {
				return e
			}
		}
	}
	return databaseBackupFinalizeActor(ctx, tx, p.ActorID)
}

func backupRetentionArchiveAdmission(ctx context.Context, tx pgx.Tx, backup string, remote *domain.BackupRemoteSource) error {
	if e := backupRetentionLock(ctx, tx); e != nil {
		return e
	}
	var denied bool
	if remote == nil {
		var e error
		denied, e = sqlcgen.New(tx).DatabaseBackupLocalArchiveDenied(ctx, backup)
		if e != nil {
			return e
		}
	} else {
		upload, e := backupRemoteLoad(ctx, tx, remote.TransferID, false)
		if e != nil {
			return e
		}
		location := backupRetentionRemoteLocation(upload.plan)
		denied, e = sqlcgen.New(tx).DatabaseBackupRemoteArchiveDenied(ctx, location)
		if e != nil {
			return e
		}
	}
	if denied {
		return ErrConflict
	}
	return nil
}

// ErrBackupRetentionBusy reports a conflicting reservation in the backup copy pool.
var ErrBackupRetentionBusy = fmt.Errorf("%w: backup copy pool busy", ErrConflict)

func backupRetentionLocalLocation(server string, proof domain.BackupLocalCopyProof, digest string) string {
	return domain.HostingDocumentHash(struct{ Server, Pool, Digest string }{server, proof.PoolID, digest})
}
