package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
	"net/url"
	"strings"
)

type storageRetentionScope struct {
	Resource domain.Resource
	Binding  domain.ResourceBinding
	Storage  domain.HostingStorage
}

func storageRetentionScopeAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (storageRetentionScope, error) {
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return storageRetentionScope{}, err
	}
	r, b, e := hostingStoragePlacement(ctx, tx, actor, resource, role)
	if e != nil {
		return storageRetentionScope{}, e
	}
	st, e := currentHostingStorage(ctx, tx, r.ProjectID, resource)
	return storageRetentionScope{r, b, st}, e
}
func storageRetentionActive(ctx context.Context, tx pgx.Tx, runner, backup string) (bool, error) {
	var active bool
	active, e := sqlcgen.New(tx).StorageRetentionActiveUse(ctx, sqlcgen.StorageRetentionActiveUseParams{Column1: runner, Column2: backup})
	return active, e
}
func storageRetentionLocation(c domain.StorageBackupRetentionCopy) string {
	if c.Local != nil {
		return domain.HostingDocumentHash(struct{ Server, Pool, Digest string }{c.ServerID, c.Local.PoolID, c.Item.Digest})
	}
	if c.Target == nil {
		return domain.HostingDocumentHash(struct{ Runner, Digest string }{c.RunnerID, c.Item.Digest})
	}
	u, e := url.Parse(c.Target.Target.Endpoint)
	if e != nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return domain.HostingDocumentHash(struct{ Scheme, Host, Port, Bucket, Key string }{u.Scheme, strings.ToLower(u.Hostname()), port, c.Target.Target.Bucket, c.ObjectKey})
}
func storageRetentionRegister(ctx context.Context, tx pgx.Tx, c domain.StorageBackupRetentionCopy, resource string) error {
	if e := storageRetentionLock(ctx, tx); e != nil {
		return e
	}
	if err := sqlcgen.New(tx).StorageRetentionDeferCopyProof(ctx); err != nil {
		return err
	}
	location := storageRetentionLocation(c)
	c.IdentityHash = domain.HostingDocumentHash(struct {
		Location string
		Local    *domain.BackupLocalCopyProof
		Remote   *domain.BackupRemoteCopyProof
	}{location, c.Local, c.Remote})
	c.Item.Availability = "unproven"
	if c.Local != nil && c.Local.Created && c.Local.Validate() == nil || c.Remote != nil && c.Remote.Created && c.Remote.Validate() == nil {
		c.Item.Availability = "available"
	}
	if strings.HasPrefix(c.Item.CopyID, "sbc_download_") && c.Local != nil && !c.Local.Created {
		var raw []byte
		raw, e := sqlcgen.New(tx).StorageRetentionReadReusableLocal(ctx, sqlcgen.StorageRetentionReadReusableLocalParams{BackupID: c.Item.BackupID, LocationHash: location})
		if e == nil {
			var prior domain.StorageBackupRetentionCopy
			if json.Unmarshal(raw, &prior) != nil || prior.Local == nil {
				return ErrConflict
			}
			want := *c.Local
			want.Created = prior.Local.Created
			if *prior.Local != want || prior.Item.Digest != c.Item.Digest || prior.Item.Bytes != c.Item.Bytes {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	// A fresh downloaded inode supersedes the old local generation. Neither the
	// original receipt nor its physical deletion outcome is rewritten.
	if strings.HasPrefix(c.Item.CopyID, "sbc_download_") && c.Local != nil && c.Local.Created {
		e := sqlcgen.New(tx).StorageRetentionSupersedeLocal(ctx, sqlcgen.StorageRetentionSupersedeLocalParams{BackupID: c.Item.BackupID, SupersededByCopyID: optionalStorageRestore(c.Item.CopyID)})
		if e != nil {
			return e
		}
	}
	e := sqlcgen.New(tx).StorageRetentionInsertCopy(ctx, sqlcgen.StorageRetentionInsertCopyParams{ID: c.Item.CopyID, BackupID: c.Item.BackupID, TransferID: optionalStorageRestore(c.Item.TransferID), ResourceID: resource, RunnerID: c.RunnerID, Location: c.Item.Location, IdentityHash: c.IdentityHash, LocationHash: location, Body: buildJSON(c), Availability: c.Item.Availability, CreatedAt: c.Item.CreatedAt})
	return e
}
func storageRetentionRegisterArchive(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, blob domain.HostingStorageArchiveBlob, proof *domain.BackupLocalCopyProof) error {
	p := op.StorageArchive.Plan
	if p.ComposeSource != nil {
		return nil
	}
	c := domain.StorageBackupRetentionCopy{Item: domain.BackupRetentionItem{CopyID: "sbc_local_" + p.BackupID, BackupID: p.BackupID, Location: "local", Digest: blob.SHA256, Bytes: blob.SizeBytes, CreatedAt: op.CreatedAt, ProtectedReasons: []string{}}, RunnerID: p.RunnerID, ServerID: p.ServerID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, Storage: p.SourceStorage.Reference(), Local: proof}
	return storageRetentionRegister(ctx, tx, c, p.SourceStorage.ResourceID)
}
func storageRetentionRegisterTransfer(ctx context.Context, tx pgx.Tx, r storageRemoteRow, in domain.HostingStorageBackupTransferMutation) error {
	if in.Phase != "complete" || r.view.Mode == "validate" {
		return nil
	}
	backup, e := sqlcgen.New(tx).ReadStorageBackup(ctx, r.view.BackupID)
	if e != nil {
		return e
	}
	op, found, e := archiveOperation(ctx, tx, backup.OperationID)
	if e != nil {
		return e
	}
	if !found {
		return ErrConflict
	}
	p := r.plan
	b := p.Operation.Blob
	c := domain.StorageBackupRetentionCopy{Item: domain.BackupRetentionItem{CopyID: "sbc_remote_" + r.view.ID, BackupID: r.view.BackupID, TransferID: r.view.ID, Location: "remote", Digest: b.SHA256, Bytes: b.SizeBytes, CreatedAt: backup.CreatedAt, ProtectedReasons: []string{}}, RunnerID: p.RunnerID, ServerID: p.ServerID, DestinationID: p.Operation.DestinationID, DaemonID: p.DaemonID, Storage: op.StorageArchive.Plan.SourceStorage.Reference(), Remote: in.RemoteCopy, Target: &p.Target, ObjectKey: p.ObjectKey}
	if r.view.Mode == "download" {
		c.Item.CopyID = "sbc_download_" + r.view.ID
		c.Item.Location = "local"
		c.Item.TransferID = ""
		c.Remote = nil
		c.Target = nil
		c.ObjectKey = ""
		c.Local = in.LocalCopy
	}
	return storageRetentionRegister(ctx, tx, c, backup.ResourceID)
}
func storageRetentionInventory(ctx context.Context, tx pgx.Tx, resource string) ([]domain.StorageBackupRetentionCopy, error) {
	// Legacy successful archives are visible but lack physical deletion ownership.
	rows, e := sqlcgen.New(tx).StorageRetentionLegacyLocal(ctx, resource)
	if e != nil {
		return nil, e
	}
	ids := rows
	if len(ids) > 100000 {
		return nil, ErrConflict
	}
	for _, id := range ids {
		op, _, e := archiveOperation(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		raw, e := sqlcgen.New(tx).ReadStorageArchiveBlob(ctx, op.StorageArchive.BackupID)
		if e != nil {
			return nil, e
		}
		var blob domain.HostingStorageArchiveBlob
		if json.Unmarshal(raw, &blob) != nil {
			return nil, ErrConflict
		}
		if e = storageRetentionRegisterArchive(ctx, tx, op, blob, nil); e != nil {
			return nil, e
		}
	}
	rows, e = sqlcgen.New(tx).StorageRetentionLegacyRemote(ctx, resource)
	if e != nil {
		return nil, e
	}
	ids = rows
	if len(ids) > 100000 {
		return nil, ErrConflict
	}
	for _, id := range ids {
		r, e := storageRemoteLoad(ctx, tx, id, false)
		if e != nil {
			return nil, e
		}
		if e = storageRetentionRegisterTransfer(ctx, tx, r, domain.HostingStorageBackupTransferMutation{Phase: "complete"}); e != nil {
			return nil, e
		}
	}
	inventoryRows, e := sqlcgen.New(tx).StorageRetentionInventory(ctx, resource)
	if e != nil {
		return nil, e
	}
	out := []domain.StorageBackupRetentionCopy{}
	for _, row := range inventoryRows {
		raw, availability, shared, reserved := row.Body, row.Availability, row.Shared, row.Reserved
		var c domain.StorageBackupRetentionCopy
		if json.Unmarshal(raw, &c) != nil {
			return nil, ErrConflict
		}
		c.Item.Availability = availability
		c.Item.ProtectedReasons = []string{}
		if shared {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "shared")
		}
		if reserved {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "reserved")
		}
		if c.Local == nil && c.Remote == nil || c.Local != nil && !c.Local.Created || c.Remote != nil && !c.Remote.Created {
			c.Item.ProtectedReasons = append(c.Item.ProtectedReasons, "unproven_ownership")
		}
		out = append(out, c)
	}
	if len(out) > 100000 {
		return nil, ErrConflict
	}
	for i := range out {
		active, e := storageRetentionActive(ctx, tx, out[i].RunnerID, out[i].Item.BackupID)
		if e != nil {
			return nil, e
		}
		if active {
			out[i].Item.ProtectedReasons = append(out[i].Item.ProtectedReasons, "active_or_recovery")
		}
	}
	return out, nil
}
func storageRetentionArchiveAdmission(ctx context.Context, tx pgx.Tx, backup string, remote *domain.BackupRemoteSource) error {
	if e := storageRetentionLock(ctx, tx); e != nil {
		return e
	}
	location := "local"
	transfer := ""
	if remote != nil {
		location = "remote"
		transfer = remote.TransferID
	}
	var denied bool
	denied, e := sqlcgen.New(tx).StorageRetentionRestoreDenied(ctx, sqlcgen.StorageRetentionRestoreDeniedParams{BackupID: backup, Location: location, TransferID: transfer})
	if e != nil {
		return e
	}
	if denied {
		return ErrConflict
	}
	return nil
}
