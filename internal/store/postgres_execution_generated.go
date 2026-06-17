package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"
	"github.com/stephenafamo/bob"
	bobtypes "github.com/stephenafamo/bob/types"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
	bobqueries "nerocd/internal/store/bobgen/queries"
)

type generatedExecutor = bob.Executor

func runnerSetter(runner domain.Runner) *bobmodels.RunnerSetter {
	tags := pq.StringArray(runner.Tags)
	capabilities := pq.StringArray(runner.Capabilities)
	return &bobmodels.RunnerSetter{
		ID:              &runner.ID,
		Name:            &runner.Name,
		Tags:            &tags,
		Capabilities:    &capabilities,
		Status:          &runner.Status,
		RegisteredAt:    &runner.RegisteredAt,
		LastHeartbeatAt: &runner.LastHeartbeatAt,
		TokenHash:       &runner.TokenHash,
	}
}

func runnerFromGenerated(runner *bobmodels.Runner) domain.Runner {
	return domain.Runner{
		ID:              runner.ID,
		Name:            runner.Name,
		Tags:            append([]string(nil), runner.Tags...),
		Capabilities:    append([]string(nil), runner.Capabilities...),
		TokenHash:       runner.TokenHash,
		Status:          runner.Status,
		RegisteredAt:    runner.RegisteredAt,
		LastHeartbeatAt: runner.LastHeartbeatAt,
	}
}

func leaseSetter(lease domain.RunLease) *bobmodels.RunLeaseSetter {
	completedAt := nullTime(lease.CompletedAt)
	return &bobmodels.RunLeaseSetter{
		ID:          &lease.ID,
		RunID:       &lease.RunID,
		RunnerID:    &lease.RunnerID,
		Status:      &lease.Status,
		ExpiresAt:   &lease.ExpiresAt,
		CreatedAt:   &lease.CreatedAt,
		CompletedAt: &completedAt,
	}
}

func leaseFromGenerated(lease *bobmodels.RunLease) domain.RunLease {
	return domain.RunLease{
		ID:          lease.ID,
		RunID:       lease.RunID,
		RunnerID:    lease.RunnerID,
		Status:      lease.Status,
		ExpiresAt:   lease.ExpiresAt,
		CreatedAt:   lease.CreatedAt,
		CompletedAt: timePtr(lease.CompletedAt),
	}
}

func runLogSetter(log domain.RunLog) *bobmodels.RunLogSetter {
	sequence := int32(log.Sequence)
	return &bobmodels.RunLogSetter{
		ID:        &log.ID,
		RunID:     &log.RunID,
		Sequence:  &sequence,
		Stream:    &log.Stream,
		Message:   &log.Message,
		CreatedAt: &log.CreatedAt,
	}
}

func runLogFromGenerated(log *bobmodels.RunLog) domain.RunLog {
	return domain.RunLog{
		ID:        log.ID,
		RunID:     log.RunID,
		Sequence:  int(log.Sequence),
		Stream:    log.Stream,
		Message:   log.Message,
		CreatedAt: log.CreatedAt,
	}
}

func insertRunLogWithSequence(ctx context.Context, exec generatedExecutor, log domain.RunLog) (domain.RunLog, error) {
	row, err := bobqueries.InsertRunLogWithSequence(
		[]bobqueries.InsertRunLogWithSequence_Group80{{
			ID:        log.ID,
			RunID:     log.RunID,
			RunID2:    log.RunID,
			Sequence:  int32(log.Sequence),
			Arg5:      sql.Null[int32]{V: int32(log.Sequence), Valid: true},
			Stream:    log.Stream,
			Message:   log.Message,
			CreatedAt: log.CreatedAt,
		}},
	).One(ctx, exec)
	if err != nil {
		return domain.RunLog{}, err
	}
	return domain.RunLog{
		ID:        row.ID,
		RunID:     row.RunID,
		Sequence:  int(row.Sequence),
		Stream:    row.Stream,
		Message:   row.Message,
		CreatedAt: row.CreatedAt,
	}, nil
}

func artifactSetter(artifact domain.ArtifactRecord) *bobmodels.RunArtifactSetter {
	return &bobmodels.RunArtifactSetter{
		ID:        &artifact.ID,
		RunID:     &artifact.RunID,
		LeaseID:   &artifact.LeaseID,
		Name:      &artifact.Name,
		Path:      &artifact.Path,
		Found:     &artifact.Found,
		Required:  &artifact.Required,
		Size:      &artifact.Size,
		Kind:      &artifact.Kind,
		CreatedAt: &artifact.CreatedAt,
	}
}

func artifactFromGenerated(artifact *bobmodels.RunArtifact) domain.ArtifactRecord {
	return domain.ArtifactRecord{
		ID:        artifact.ID,
		RunID:     artifact.RunID,
		LeaseID:   artifact.LeaseID,
		Name:      artifact.Name,
		Path:      artifact.Path,
		Found:     artifact.Found,
		Required:  artifact.Required,
		Size:      artifact.Size,
		Kind:      artifact.Kind,
		CreatedAt: artifact.CreatedAt,
	}
}

func approvalSetter(approval domain.Approval) *bobmodels.ApprovalSetter {
	approvedBy := nullString(approval.ApprovedBy)
	approvedAt := nullTime(approval.ApprovedAt)
	return &bobmodels.ApprovalSetter{
		ID:          &approval.ID,
		RunID:       &approval.RunID,
		Status:      &approval.Status,
		RequestedBy: &approval.RequestedBy,
		ApprovedBy:  &approvedBy,
		CreatedAt:   &approval.CreatedAt,
		ApprovedAt:  &approvedAt,
	}
}

func approvalFromGenerated(approval *bobmodels.Approval) domain.Approval {
	return domain.Approval{
		ID:          approval.ID,
		RunID:       approval.RunID,
		Status:      approval.Status,
		RequestedBy: approval.RequestedBy,
		ApprovedBy:  stringPtr(approval.ApprovedBy),
		CreatedAt:   approval.CreatedAt,
		ApprovedAt:  timePtr(approval.ApprovedAt),
	}
}

func auditEventSetter(event domain.AuditEvent) (*bobmodels.AuditEventSetter, error) {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return nil, err
	}
	metadataJSON := bobtypes.NewJSON(json.RawMessage(metadata))
	return &bobmodels.AuditEventSetter{
		ID:        &event.ID,
		ActorID:   &event.ActorID,
		Action:    &event.Action,
		TargetID:  &event.TargetID,
		Metadata:  &metadataJSON,
		CreatedAt: &event.CreatedAt,
	}, nil
}

func auditEventFromGenerated(event *bobmodels.AuditEvent) (domain.AuditEvent, error) {
	result := domain.AuditEvent{
		ID:        event.ID,
		ActorID:   event.ActorID,
		Action:    event.Action,
		TargetID:  event.TargetID,
		CreatedAt: event.CreatedAt,
	}
	if len(event.Metadata.Val) > 0 {
		if err := json.Unmarshal(event.Metadata.Val, &result.Metadata); err != nil {
			return domain.AuditEvent{}, err
		}
	}
	return result, nil
}
