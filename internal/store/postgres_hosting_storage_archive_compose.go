package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// Archive metadata authorization is independent of the original settled runtime.
func archivePlacement(ctx context.Context, tx pgx.Tx, actor, resource, role string) (domain.Resource, domain.ResourceBinding, error) {
	q := sqlcgen.New(tx)
	var r domain.Resource
	row, err := q.LockHostingRuntimeResource(ctx, resource)
	if err != nil {
		return r, domain.ResourceBinding{}, hostingError(err)
	}
	r.ID, r.ProjectID, r.Kind, r.Generation = row.ID, row.ProjectID, row.Kind, row.Generation
	if json.Unmarshal(row.Source, &r.Source) != nil {
		return r, domain.ResourceBinding{}, ErrConflict
	}
	if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, role, false); err != nil {
		return r, domain.ResourceBinding{}, err
	}
	if r.Kind != "application" || r.Source.Kind != "image" && r.Source.Kind != "compose" {
		return r, domain.ResourceBinding{}, ErrConflict
	}
	b, err := q.GetResourceBinding(ctx, resource)
	if err != nil {
		return r, domain.ResourceBinding{}, hostingError(err)
	}
	if err = databaseBindingGrant(ctx, q, r.ProjectID, b.DestinationID); err != nil {
		return r, domain.ResourceBinding{}, err
	}
	return r, bindingFromDestinationSQL(b), nil
}

func composeArchiveVolume(ctx context.Context, tx pgx.Tx, c domain.HostingStorageArchiveComposeSource) error {
	v, err := sqlcgen.New(tx).GetHostingComposeVolume(ctx, sqlcgen.GetHostingComposeVolumeParams{BindingID: c.Healthy.Snapshot.BindingID, Name: c.Volume.Name})
	if err != nil {
		return hostingError(err)
	}
	expected := domain.HostingStackVolume{Name: v.Name, VolumeName: v.VolumeName, CreationOperationID: v.CreationOperationID, CreationNonce: v.CreationNonce, CreatedAt: v.CreatedAt}
	if v.State != "materialized" || expected != c.Volume || v.DestinationID != c.Healthy.Snapshot.DestinationID || v.DaemonID != c.Healthy.Release.DaemonID {
		return ErrConflict
	}
	return nil
}

// StorageBackupVolumes returns named-volume eligibility under current viewer authority.
func (s *PostgresStore) StorageBackupVolumes(ctx context.Context, actor, resource string) (domain.HostingStorageBackupVolumes, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupVolumes, error) {
		out := domain.HostingStorageBackupVolumes{ResourceID: resource, Reason: "unavailable", Volumes: []domain.HostingStorageBackupVolume{}}
		r, _, err := archivePlacement(ctx, tx, actor, resource, domain.RoleViewer)
		if err != nil {
			return out, err
		}
		out.ResourceGeneration = r.Generation
		if r.Source.Kind != "compose" {
			out.Reason = "unsupported_source"
			return out, nil
		}
		if err = s.archiveConfigured(ctx, sqlcgen.New(tx)); err != nil {
			if errors.Is(err, ErrStorageArchiveUnavailable) {
				out.Reason = "archive_unavailable"
				return out, nil
			}
			return out, err
		}
		h, op, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, "", domain.RoleViewer)
		if errors.Is(err, ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if generation != h.Snapshot.ResourceGeneration {
			out.Reason = "deployment_changed"
			return out, nil
		}
		if err = archiveRunner(ctx, tx, domain.HostingStorageArchivePlan{ComposeSource: &domain.HostingStorageArchiveComposeSource{}, RunnerID: h.Snapshot.RunnerID, ServerID: h.Snapshot.ServerID, DestinationID: h.Snapshot.DestinationID, ProjectID: r.ProjectID}); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
				out.Reason = "runner_unavailable"
				return out, nil
			}
			return out, err
		}
		out.OperationID = h.OperationID
		for _, v := range h.Stack.Volumes {
			item := domain.HostingStorageBackupVolume{Volume: v.Name, Eligible: true, Mounts: []domain.HostingStorageBackupVolumeMount{}}
			for _, svc := range op.Plan.Stack.Definition.Services {
				for _, m := range svc.Mounts {
					if m.Volume == v.Name {
						item.Mounts = append(item.Mounts, domain.HostingStorageBackupVolumeMount{Component: svc.Name, MountPath: m.Target, ReadOnly: m.ReadOnly})
					}
				}
			}
			c := domain.HostingStorageArchiveComposeSource{Healthy: h, Plan: *op.Plan, Volume: v}
			if len(item.Mounts) == 0 {
				item.Eligible = false
				item.Reason = "unused_volume"
			} else if err = composeArchiveVolume(ctx, tx, c); err != nil {
				if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
					return out, err
				}
				item.Eligible = false
				item.Reason = "volume_unavailable"
			}
			out.Volumes = append(out.Volumes, item)
		}
		out.Available = slices.ContainsFunc(out.Volumes, func(v domain.HostingStorageBackupVolume) bool { return v.Eligible })
		if out.Available {
			out.Reason = "requires_runtime_ownership_verification"
		} else {
			out.Reason = "no_eligible_volumes"
		}
		return out, nil
	})
}

func (s *PostgresStore) requestComposeStorageBackup(ctx context.Context, tx pgx.Tx, id, resource, actor, requestHash string, in domain.HostingStorageBackupInput) (domain.HostingStorageBackup, error) {
	var zero domain.HostingStorageBackup
	q := sqlcgen.New(tx)
	if in.Volume == "" || in.RemoteTarget != nil {
		return zero, domain.ErrInvalidStorageArchive
	}
	h, op, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, "", domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if generation != in.ExpectedResourceGeneration || generation != h.Snapshot.ResourceGeneration || h.OperationID != in.ExpectedDeployedOperationID {
		return zero, ErrConflict
	}
	c := &domain.HostingStorageArchiveComposeSource{Healthy: h, Plan: *op.Plan, Retained: []domain.HostingStorageArchiveStackProof{}}
	for _, v := range h.Stack.Volumes {
		if v.Name == in.Volume {
			c.Volume = v
		}
	}
	if c.Volume.Name == "" {
		return zero, ErrConflict
	}
	if err = composeArchiveVolume(ctx, tx, *c); err != nil {
		return zero, err
	}
	retained, err := q.GetHostingRetainedReleases(ctx, h.Snapshot.BindingID)
	if err != nil {
		return zero, err
	}
	for _, row := range retained {
		var old domain.HostingHealthy
		if json.Unmarshal(row.Body, &old) != nil {
			return zero, ErrConflict
		}
		if old.OperationID == h.OperationID {
			continue
		}
		// Cleanup admission cannot be raced with a new stop/resume obligation.
		if row.State == "cleanup_pending" {
			return zero, ErrConflict
		}
		prior, e := hostingOperation(ctx, tx, old.OperationID)
		if e != nil {
			return zero, e
		}
		if prior.Plan == nil || prior.ProjectID != op.ProjectID || prior.ResourceID != resource || prior.BindingID != h.Snapshot.BindingID {
			return zero, ErrConflict
		}
		c.Retained = append(c.Retained, domain.HostingStorageArchiveStackProof{Healthy: old, Plan: *prior.Plan})
	}
	backupID := "hsb_" + domain.HostingDocumentHash(struct{ Resource, Request string }{resource, in.RequestID})[7:39]
	p := domain.HostingStorageArchivePlan{ComposeSource: c, OperationID: id, ProjectID: op.ProjectID, ServerID: h.Snapshot.ServerID, DestinationID: h.Snapshot.DestinationID, RunnerID: h.Snapshot.RunnerID, Mode: "backup", BackupID: backupID, Writers: []domain.HostingHealthy{}, Helper: s.storageArchiveConfiguration.helper, Limits: s.storageArchiveConfiguration.limits, CompilerVersion: domain.HostingComposeStorageArchiveVersion, PolicyVersion: domain.HostingComposeStorageArchiveVersion}
	p.Hash = domain.HostingDocumentHash(p)
	if err = archiveRunner(ctx, tx, p); err != nil {
		return zero, err
	}
	if err = archiveInsertPlan(ctx, tx, p, actor, "storage_backup_"+in.RequestID, requestHash); err != nil {
		return zero, err
	}
	if err = q.InsertStorageBackup(ctx, sqlcgen.InsertStorageBackupParams{ID: backupID, OperationID: id, ProjectID: p.ProjectID, ResourceID: resource, RequestID: in.RequestID, RequestHash: requestHash, CreatedBy: actor}); err != nil {
		return zero, err
	}
	if err = q.AdmitStorageArchiveHold(ctx, sqlcgen.AdmitStorageArchiveHoldParams{BindingID: h.Snapshot.BindingID, OperationID: id}); err != nil {
		return zero, err
	}
	created, _, err := archiveOperation(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	if err = archiveAudit(ctx, tx, actor, "requested", created); err != nil {
		return zero, err
	}
	if _, err = s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: id}); err != nil {
		return zero, err
	}
	row, err := q.ReadStorageBackup(ctx, backupID)
	if err != nil {
		return zero, err
	}
	return archiveLoadBackup(ctx, tx, row)
}

func composeArchiveGate(ctx context.Context, tx pgx.Tx, actor string, p domain.HostingStorageArchivePlan) error {
	r, b, err := archivePlacement(ctx, tx, actor, p.SourceResourceID(), domain.RoleOwner)
	if err != nil {
		return err
	}
	if r.ProjectID != p.ProjectID || b.ID != p.SourceBindingID() || b.DestinationID != p.DestinationID {
		return ErrConflict
	}
	// History and recovery deliberately do not require the desired generation to
	// remain deployed. The durable hold and immutable origin retain authority.
	return composeArchiveVolume(ctx, tx, *p.ComposeSource)
}
