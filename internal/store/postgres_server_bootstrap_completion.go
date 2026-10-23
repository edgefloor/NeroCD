package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func bootstrapEvidence(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	if len(raw) > domain.ServerBootstrapMaxDocument {
		return false
	}
	var e struct {
		State                string `json:"state"`
		ArtifactMatches      bool   `json:"artifact_matches"`
		ServiceActive        bool   `json:"service_active"`
		ServiceJobPending    bool   `json:"service_job_pending"`
		EnrollmentFileExists bool   `json:"enrollment_file_exists"`
		CredentialFileExists bool   `json:"credential_file_exists"`
	}
	if domain.DecodeHostingRuntimeLogsJSON(raw, &e) != nil {
		return false
	}
	switch e.State {
	case "absent", "staged", "installed", "removed", "uncertain":
		return true
	default:
		return false
	}
}

// CompleteServerBootstrapStage retains immutable evidence and never clears a
// remote hold. Registration independently proves durable enrollment consumption.
func (s *PostgresStore) CompleteServerBootstrapStage(ctx context.Context, actor, id string, in domain.ServerBootstrapReceipt, event domain.AuditEvent) (domain.ServerBootstrapOperation, error) {
	var zero domain.ServerBootstrapOperation
	if event.ActorID != actor || !bootstrapEvidence(in.Evidence) {
		return zero, ErrConflict
	}
	// Normalize evidence JSON for exact semantic replay without normalizing manifests.
	if len(in.Evidence) > 0 {
		var value any
		if json.Unmarshal(in.Evidence, &value) != nil {
			return zero, ErrConflict
		}
		in.Evidence, _ = json.Marshal(value)
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapOperation, error) {
		op, err := bootstrapLock(ctx, tx, actor, id)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		row, err := q.BootstrapCommandLock(ctx, sqlcgen.BootstrapCommandLockParams{OperationID: id, ID: in.CommandID})
		if err != nil {
			return zero, hostingError(err)
		}
		if row.ActorID != actor {
			return zero, ErrConflict
		}
		if row.Receipt != nil {
			var stored struct {
				Receipt   domain.ServerBootstrapReceipt   `json:"receipt"`
				Operation domain.ServerBootstrapOperation `json:"operation"`
			}
			if json.Unmarshal(row.Receipt, &stored) != nil {
				return zero, ErrConflict
			}
			old := stored.Receipt
			if in.Probe != nil && old.Probe != nil && in.Probe.ObservedAt.IsZero() && in.Probe.ExpiresAt.IsZero() {
				in.Probe.ObservedAt, in.Probe.ExpiresAt = old.Probe.ObservedAt, old.Probe.ExpiresAt
			}
			if len(old.Evidence) > 0 {
				var value any
				_ = json.Unmarshal(old.Evidence, &value)
				old.Evidence, _ = json.Marshal(value)
			}
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(in) {
				return zero, ErrConflict
			}
			return stored.Operation, proxyCheckLegacyToken(ctx, tx, actor)
		}
		now, err := q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		stage := domain.ServerBootstrapStage{Command: domain.ServerBootstrapCommand{ID: in.CommandID}, LeaseID: in.LeaseID, Attempt: in.Attempt, Fence: in.Fence}
		if op.RestoreHeld || !bootstrapStageMatches(row, stage, now) {
			return zero, ErrConflict
		}
		if row.Action != "install" && (in.ManifestSHA256 != "" || in.RemoteOperationID != "") {
			return zero, ErrConflict
		}
		state := op.State
		if op.Input.ArtifactID != "" && row.Action == "probe" && in.Outcome == "observed" && in.Probe == nil {
			return zero, ErrConflict
		}
		if in.Probe != nil {
			if row.Action != "probe" || in.Outcome != "observed" || !in.Probe.ObservedAt.IsZero() || !in.Probe.ExpiresAt.IsZero() {
				return zero, ErrConflict
			}
			in.Probe.ObservedAt = now
			in.Probe.ExpiresAt = now.Add(domain.ServerBootstrapLeaseLifetime)
		}
		if in.Owned != nil && row.Action != "install" && row.Action != "inspect_owned" {
			return zero, ErrConflict
		}
		switch row.Action {
		case "probe", "inspect_owned":
			if in.Outcome != "observed" && in.Outcome != "refused" {
				return zero, ErrConflict
			}
			if row.Action == "inspect_owned" && in.Outcome == "observed" && in.Owned != nil && op.RemoteHold && (op.State == "running" || op.State == "uncertain") {
				if err = bootstrapReconcileOwned(ctx, tx, op, *in.Owned); err != nil {
					return zero, err
				}
				state = "installed_waiting_enrollment"
			}
		case "install":
			if !row.EffectIntent || !op.RemoteHold || in.RemoteOperationID != op.IDs.InstallOperationID {
				return zero, ErrConflict
			}
			// The original reviewed manifest remains the identity even after its probe expires.
			raw, e := q.BootstrapLatestReview(ctx, id)
			if e != nil {
				return zero, e
			}
			var command domain.ServerBootstrapCommand
			if json.Unmarshal(raw, &command) != nil || command.Review == nil || in.ManifestSHA256 != command.Review.ManifestSHA256 {
				return zero, ErrConflict
			}
			switch in.Outcome {
			case "installed":
				if op.Input.ArtifactID != "" && !bootstrapOwnedMatches(op, command.Review.ManifestSHA256, in.Owned) {
					return zero, ErrConflict
				}
				state = "installed_waiting_enrollment"
			case "uncertain":
				state = "uncertain"
			default:
				return zero, ErrConflict
			}
		case "register_server":
			if in.Outcome != "accepted" || op.State != "installed_waiting_enrollment" {
				return zero, ErrConflict
			}
			// Runner SHARE precedes enrollment SHARE/UPDATE, matching readiness admission.
			runner, e := q.LockReadinessRunner(ctx, op.IDs.RunnerID)
			if e != nil {
				return zero, hostingError(e)
			}
			enrollment, e := q.BootstrapEnrollmentLock(ctx, op.IDs.EnrollmentID)
			if e != nil {
				return zero, hostingError(e)
			}
			if enrollment.RunnerID != op.IDs.RunnerID || enrollment.UsedAt == nil || enrollment.ConsumeRequestID == nil || *enrollment.ConsumeRequestID == "" || enrollment.CredentialHash == nil || *enrollment.CredentialHash == "" || *enrollment.CredentialHash != runner.TokenHash || runner.Status != "active" {
				return zero, ErrConflict
			}
			serverEvent := event
			serverEvent.ID += ".server"
			if _, e = createServerDestinationTx(ctx, q, domain.Server{ID: op.IDs.ServerID, Name: op.Input.Name, RunnerID: op.IDs.RunnerID, CreatedBy: actor, CreatedAt: now}, domain.Destination{ID: op.IDs.DestinationID, ServerID: op.IDs.ServerID, Name: op.Input.Name, RuntimeKind: "docker_compose", CreatedAt: now}, serverEvent); e != nil {
				return zero, e
			}
			state = "registered"
		case "grant_destination":
			if in.Outcome != "accepted" || op.State != "registered" {
				return zero, ErrConflict
			}
			grantEvent := event
			grantEvent.ID += ".grant"
			if _, err = grantDestinationProjectTx(ctx, q, domain.DestinationProjectGrant{DestinationID: op.IDs.DestinationID, ProjectID: op.Input.ProjectID, GrantedBy: actor, CreatedAt: now}, grantEvent); err != nil {
				return zero, err
			}
			state = "completed"
		default:
			return zero, ErrConflict
		}
		updated, err := q.BootstrapUpdate(ctx, sqlcgen.BootstrapUpdateParams{ID: id, State: state})
		if err != nil {
			return zero, err
		}
		accepted, err := bootstrapOperation(updated)
		if err != nil {
			return zero, err
		}
		body, err := json.Marshal(struct {
			Receipt   domain.ServerBootstrapReceipt   `json:"receipt"`
			Operation domain.ServerBootstrapOperation `json:"operation"`
		}{in, accepted})
		if err != nil || len(body) > 32768 {
			return zero, ErrConflict
		}
		if err = q.BootstrapCommandReceipt(ctx, sqlcgen.BootstrapCommandReceiptParams{ID: row.ID, Receipt: body}); err != nil {
			return zero, err
		}
		if err = bootstrapAudit(ctx, q, event, actor, "stage_completed", id); err != nil {
			return zero, err
		}
		now, err = q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !bootstrapStageMatches(row, stage, now) {
			return zero, ErrConflict
		}
		out, err := bootstrapOperation(updated)
		if err != nil {
			return zero, err
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
