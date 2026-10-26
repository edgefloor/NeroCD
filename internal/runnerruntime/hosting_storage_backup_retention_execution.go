//go:build unix

package runnerruntime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

var errStorageRetentionHeld = errors.New("backup retention requires exact-copy reconciliation")

func storageRetentionFailure(err error) string {
	if errors.Is(err, errStorageRetentionHeld) || errors.Is(err, domain.ErrBackupRemoteCredentials) {
		return "authority_denied"
	}
	if errors.Is(err, errBackupRetentionConditional) {
		return "conditional_unsupported"
	}
	if errors.Is(err, errBackupRetentionObjectChanged) || errors.Is(err, artifactfs.ErrIntegrity) {
		return "object_changed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "interrupted"
	}
	var remote backupRemoteError
	if errors.As(err, &remote) {
		if remote == "authority_denied" {
			return "authority_denied"
		}
		return "remote_unavailable"
	}
	return "storage_unavailable"
}
func storageRetentionRemote(ctx context.Context, c runnerclient.Client, config StorageArchiveExecutionConfig, p domain.HostingStorageBackupRetentionPlan, copy domain.StorageBackupRetentionCopy, a domain.HostingAuthority, before func(context.Context) error) (*backupRemoteS3, error) {
	if copy.Target == nil {
		return nil, errStorageRetentionHeld
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: attemptMutationKey("retentioncred", a.LeaseID, a.Attempt, copy.Item.CopyID), PlanHash: p.Hash, TargetID: copy.Target.Target.ID, TargetRevision: copy.Target.Target.Revision, DaemonID: copy.DaemonID, RetentionCopyID: copy.Item.CopyID}
	options := runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP}
	credentials, err := c.HostingBackupRetentionCredentials(ctx, p.RunnerID, request, options)
	if err != nil {
		return nil, err
	}
	defer credentials.Close()
	sequence := 0
	grant := backupRemoteGrant{useUntil: credentials.Metadata().UseUntil}
	grant.refresh = func(refreshCtx context.Context, access, secret []byte) (time.Time, error) {
		if err := before(refreshCtx); err != nil {
			return time.Time{}, err
		}
		sequence++
		next := request
		next.DeliveryID = attemptMutationKey("retentioncred", a.LeaseID, a.Attempt, copy.Item.CopyID+"_"+strconv.Itoa(sequence))
		fresh, err := c.HostingBackupRetentionCredentials(refreshCtx, p.RunnerID, next, options)
		if err != nil {
			return time.Time{}, err
		}
		defer fresh.Close()
		if err = fresh.Use(func(a, b []byte) error {
			if subtle.ConstantTimeCompare(access, a) != 1 || subtle.ConstantTimeCompare(secret, b) != 1 {
				return errStorageRetentionHeld
			}
			return nil
		}); err != nil {
			return time.Time{}, err
		}
		return fresh.Metadata().UseUntil, nil
	}
	policy, err := storageBackupRemotePolicy(config)
	if err != nil {
		return nil, err
	}
	var remote *backupRemoteS3
	err = credentials.Use(func(access, secret []byte) error {
		var e error
		remote, e = newBackupRemoteS3(ctx, copy.Target.Target, access, secret, policy, time.Duration(p.TimeoutSeconds)*time.Second, grant)
		return e
	})
	if err != nil {
		return nil, err
	}
	remote.transport.before = before
	return remote, nil
}
func storageRetentionBefore(c runnerclient.Client, e *storageArchiveEngine, p domain.HostingStorageBackupRetentionPlan, a domain.HostingAuthority) func(context.Context) error {
	return func(ctx context.Context) error {
		lease, err := c.Lease(ctx, runnerclient.Authority{LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence})
		if err != nil || lease.RunID != a.RunID || lease.RunnerID != p.RunnerID || lease.Status != domain.LeaseActive || lease.Attempt != a.Attempt || lease.Fence != a.Fence || !time.Now().Before(lease.ExpiresAt) {
			return errStorageRetentionHeld
		}
		current, err := c.HostingStorageBackupRetentionPlan(ctx, a)
		if err != nil || current.Hash != p.Hash {
			return errStorageRetentionHeld
		}
		for _, copy := range p.Copies {
			if e.image.verifyDaemon(ctx, copy.DaemonID) != nil {
				return errStorageRetentionHeld
			}
		}
		return nil
	}
}
func storageRetentionArtifactProof(c domain.StorageBackupRetentionCopy) artifactfs.Ownership {
	v := c.Local
	return artifactfs.Ownership{PoolID: v.PoolID, Device: v.Device, Inode: v.Inode, Bytes: v.Bytes, Created: v.Created}
}
func executeStorageBackupRetentionClaim(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *StorageArchiveExecutionConfig) error {
	if config == nil || !config.BackupRetention || domain.ValidateStorageBackupRetentionTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errStorageRetentionHeld
	}
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	p, err := c.HostingStorageBackupRetentionPlan(s.Context(), a)
	if err != nil {
		return err
	}
	if p.RunnerID != claim.Lease.RunnerID || len(p.Progress) != len(p.Copies) {
		return errStorageRetentionHeld
	}
	old := []runner.JournalStorageBackupRetention{}
	for _, v := range j.Snapshot().StorageBackupRetentions {
		if v.Request.OperationID != a.OperationID {
			continue
		}
		if v.Plan.Hash != p.Hash || v.Request.Attempt >= a.Attempt {
			return errStorageRetentionHeld
		}
		old = append(old, v)
	}
	ctx, cancel := context.WithTimeout(s.Context(), time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	e, err := openStorageRemoteEngine(ctx, *config, endpoint)
	if err != nil {
		return err
	}
	// Resource teardown must not reclassify a durably recorded operation.
	defer func() { _ = e.Close() }()
	before := storageRetentionBefore(c, e, p, a)
	if err = before(ctx); err != nil {
		return err
	}
	remotes := make([]*backupRemoteS3, len(p.Copies))
	defer func() {
		for _, v := range remotes {
			if v != nil {
				v.Close()
			}
		}
	}()
	record := func(i int, phase, failure, probeKey string, probe bool) (runner.JournalStorageBackupRetention, error) {
		mode := phase
		if probe {
			mode = "probe_" + phase
		}
		id := attemptMutationKey("retention", a.LeaseID, a.Attempt, p.Copies[i].Item.CopyID+"_"+mode)
		request := domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: id, PlanHash: p.Hash, CopyID: p.Copies[i].Item.CopyID, Phase: phase, FailureCode: failure, ProbeOnly: probe, ProbeKey: probeKey}
		return j.AppendStorageBackupRetention(runner.JournalStorageBackupRetention{ID: id, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, s), Plan: p, Request: request, ProbeKey: probeKey, CreatedAt: time.Now().UTC()})
	}
	send := func(entry runner.JournalStorageBackupRetention, last bool) error {
		transport, stop := context.WithTimeout(s.Context(), 5*time.Second)
		defer stop()
		if last {
			if err := drain(transport); err != nil {
				return err
			}
		}
		_, err := c.HostingStorageBackupRetentionMutation(transport, entry.Request)
		return err
	}
	retireOld := func(copyID string, probes bool) error {
		for _, v := range old {
			if v.Request.CopyID == copyID && v.Request.ProbeOnly == probes {
				if err := j.AckStorageBackupRetention(v.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	indices := []int{}
	for i, v := range p.Progress {
		if v.State == "deleted" || v.State == "absent" || v.State == "failed" {
			if err = retireOld(v.CopyID, false); err != nil {
				return err
			}
			continue
		}
		indices = append(indices, i)
	}
	if len(indices) == 0 {
		return errStorageRetentionHeld
	}
	// Reconcile saved completed effects before considering a new DELETE. A saved
	// completed response plus exact absence is materially different from an
	// unpaired intent whose provider request may still be running.
	completed := map[int]string{}
	unknown := map[int]bool{}
	for _, i := range indices {
		copy := p.Copies[i]
		var result *runner.JournalStorageBackupRetention
		hadIntent := false
		for k := range old {
			v := &old[k]
			if v.Request.CopyID != copy.Item.CopyID || v.Request.ProbeOnly {
				continue
			}
			if v.Request.Phase == "intent" {
				hadIntent = true
			} else if result == nil || v.Request.Attempt > result.Request.Attempt {
				result = v
			}
		}
		if copy.Target != nil {
			remotes[i], err = storageRetentionRemote(ctx, c, *config, p, copy, a, before)
			if err != nil {
				return err
			}
		}
		if result != nil && (result.Request.Phase == "deleted" || result.Request.Phase == "absent") {
			var absent bool
			if copy.Local != nil {
				absent, err = e.store.(*artifactfs.Store).ObserveOwned(ctx, strings.TrimPrefix(copy.Item.Digest, "sha256:"), storageRetentionArtifactProof(copy), before)
			} else {
				absent, err = remotes[i].retentionHead(ctx, copy.Target.Target.Bucket, copy.ObjectKey, *copy.Remote, domain.HostingDatabaseBackupBlob{Digest: copy.Item.Digest, Bytes: copy.Item.Bytes, Format: "postgresql_custom"})
			}
			if err == nil && absent {
				completed[i] = "absent"
			} else {
				unknown[i] = true
			}
			continue
		}
		if (p.Progress[i].State == "intent" || p.Progress[i].State == "uncertain" || hadIntent && p.Progress[i].State != "pending") && copy.Remote != nil && (copy.Remote.VersionID == "" || copy.Remote.VersionID == "null") {
			unknown[i] = true
		}
	}
	// Qualify every remote location that may receive a DELETE before deleting any
	// selected copy. Probe journal records are explicitly separate from per-copy
	// DELETE intents; untouched copies remain pending throughout qualification.
	for _, i := range indices {
		copy := p.Copies[i]
		if copy.Target == nil || completed[i] != "" || unknown[i] {
			continue
		}
		for _, v := range old {
			if v.Request.CopyID != copy.Item.CopyID || !v.Request.ProbeOnly || v.Request.Phase != "intent" {
				continue
			}
			paired := false
			for _, result := range old {
				if result.Request.HostingAuthority == v.Request.HostingAuthority && result.Request.CopyID == v.Request.CopyID && result.Request.ProbeOnly && (result.Request.Phase == "deleted" || result.Request.Phase == "absent") {
					paired = true
				}
			}
			if !paired {
				return errStorageRetentionHeld
			}
			absent, err := remotes[i].storageRetentionProbeAbsent(ctx, copy.Target.Target.Bucket, v.ProbeKey)
			if err != nil || !absent {
				return errStorageRetentionHeld
			}
			reconciled, err := record(i, "absent", "", v.ProbeKey, true)
			if err != nil {
				return err
			}
			if err = send(reconciled, false); err != nil {
				return err
			}
			if err = j.AckStorageBackupRetention(reconciled.ID); err != nil {
				return err
			}
			if err = retireOld(copy.Item.CopyID, true); err != nil {
				return err
			}
		}

		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		key := "nerocd/storage/retention-probes/" + p.Operation.ID + "/" + hex.EncodeToString(nonce[:])
		intent, err := record(i, "intent", "", key, true)
		if err != nil {
			return err
		}
		if err = send(intent, false); err != nil {
			return err
		}
		effectErr := remotes[i].qualifyStorageRetention(ctx, copy.Target.Target.Bucket, key)
		phase, failure := "deleted", ""
		if effectErr != nil {
			phase, failure = "uncertain", storageRetentionFailure(effectErr)
		}
		result, err := record(i, phase, failure, key, true)
		if err != nil {
			return err
		}
		if err = send(result, effectErr != nil); err != nil {
			return err
		}
		if effectErr != nil {
			return errStorageRetentionHeld
		}
		if err = j.AckStorageBackupRetention(intent.ID); err != nil {
			return err
		}
		if err = j.AckStorageBackupRetention(result.ID); err != nil {
			return err
		}
		if err = retireOld(copy.Item.CopyID, true); err != nil {
			return err
		}
	}
	for ordinal, i := range indices {
		copy := p.Copies[i]
		phase, failure := completed[i], ""
		var intent runner.JournalStorageBackupRetention
		if phase == "" && !unknown[i] {
			intent, err = record(i, "intent", "", "", false)
			if err != nil {
				return err
			}
			if err = send(intent, false); err != nil {
				return err
			}
			if err = before(ctx); err == nil {
				var absent bool
				if copy.Local != nil {
					absent, err = e.store.(*artifactfs.Store).RemoveOwnedWithAuthority(ctx, strings.TrimPrefix(copy.Item.Digest, "sha256:"), storageRetentionArtifactProof(copy), before)
				} else {
					absent, err = remotes[i].deleteStorageRetentionCopy(ctx, copy)
				}
				phase = "deleted"
				if absent {
					phase = "absent"
				}
			}
			if err != nil {
				phase, failure = "uncertain", storageRetentionFailure(err)
			}
		} else if unknown[i] {
			phase, failure = "uncertain", "interrupted"
		}
		result, err := record(i, phase, failure, "", false)
		if err != nil {
			return err
		}
		if err = send(result, ordinal == len(indices)-1); err != nil {
			return err
		}
		if intent.ID != "" {
			if err = j.AckStorageBackupRetention(intent.ID); err != nil {
				return err
			}
		}
		if err = j.AckStorageBackupRetention(result.ID); err != nil {
			return err
		}
		if err = retireOld(copy.Item.CopyID, false); err != nil {
			return err
		}
	}
	return nil
}

// Startup keeps the original journal. Only exact committed receipts retire
// immediately; an audited fresh same-batch attempt may carry unresolved records
// forward for the executor to reconcile under its NEW authority.
func reconcileStorageBackupRetentionJournal(ctx context.Context, config Config, j *runner.AttemptJournal) error {
	entries := j.Snapshot().StorageBackupRetentions
	if len(entries) == 0 {
		return nil
	}
	if config.StorageArchive == nil || !config.StorageArchive.BackupRetention {
		return errStorageRetentionHeld
	}
	ctx = runnerclient.WithStorageArchiveTransport(ctx, runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: config.StorageArchive.TrustedLoopbackHTTP})
	c := runnerclient.New(config.Server, config.Token)
	for _, entry := range entries {
		if entry.Request.Phase == "intent" {
			continue
		}
		if _, err := c.HostingStorageBackupRetentionMutation(ctx, entry.Request); err == nil {
			if entry.Request.ProbeOnly && entry.Request.Phase == "uncertain" {
				continue
			}
			for _, intent := range entries {
				if intent.Request.Phase == "intent" && intent.Request.HostingAuthority == entry.Request.HostingAuthority && intent.Request.CopyID == entry.Request.CopyID && intent.Request.ProbeOnly == entry.Request.ProbeOnly && intent.Plan.Hash == entry.Plan.Hash {
					if err = j.AckStorageBackupRetention(intent.ID); err != nil {
						return err
					}
				}
			}
			if err = j.AckStorageBackupRetention(entry.ID); err != nil {
				return err
			}
		}
	}
	for _, entry := range j.Snapshot().StorageBackupRetentions {
		_, err := c.HostingStorageBackupRetentionRetirement(ctx, domain.HostingStorageBackupRetentionRetirementInput{HostingAuthority: entry.Request.HostingAuthority, PlanHash: entry.Plan.Hash})
		if err != nil {
			return errStorageRetentionHeld
		}
	}
	return nil
}
