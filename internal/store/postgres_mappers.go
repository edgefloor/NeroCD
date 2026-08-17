package store

import (
	"encoding/json"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func userFromSQLC(row sqlcgen.User) domain.User {
	return domain.User{ID: row.ID, Email: row.Email, Name: row.Name, Status: row.Status, GlobalRole: row.GlobalRole, PasswordHash: row.PasswordHash, CreatedAt: row.CreatedAt}
}

func apiTokenFromSQLC(row sqlcgen.ApiToken) domain.APIToken {
	return domain.APIToken{ID: row.ID, Name: row.Name, Kind: row.Kind, TokenHash: row.TokenHash, Roles: append([]string(nil), row.Roles...), Status: row.Status, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt}
}

func projectFromSQLC(row sqlcgen.Project) domain.Project {
	return domain.Project{ID: row.ID, Name: row.Name, Description: row.Description, CreatedAt: row.CreatedAt, ArchivedAt: row.ArchivedAt}
}

func projectMemberFromSQLC(row sqlcgen.ProjectMember, user sqlcgen.User) domain.ProjectMember {
	return domain.ProjectMember{ID: row.ID, ProjectID: row.ProjectID, UserID: row.UserID, Email: user.Email, Name: user.Name, Role: row.Role, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func projectMemberListRowFromSQLC(row sqlcgen.ListProjectMembersRow) domain.ProjectMember {
	return domain.ProjectMember{ID: row.ID, ProjectID: row.ProjectID, UserID: row.UserID, Email: row.Email, Name: row.Name, Role: row.Role, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func repositoryFromSQLC(row sqlcgen.Repository) domain.Repository {
	return domain.Repository{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, URL: row.Url, Provider: row.Provider, DefaultRef: row.DefaultRef, CreatedAt: row.CreatedAt}
}

func accessKeyFromSQLC(row sqlcgen.AccessKey) domain.AccessKey {
	return domain.AccessKey{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, Kind: row.Kind, Fingerprint: row.Fingerprint, CreatedAt: row.CreatedAt}
}

func inventoryFromSQLC(row sqlcgen.Inventory) domain.Inventory {
	return domain.Inventory{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, Kind: row.Kind, Source: row.Source, CreatedAt: row.CreatedAt}
}

func taskTemplateFromSQLC(row sqlcgen.TaskTemplate) (domain.TaskTemplate, error) {
	result := domain.TaskTemplate{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, Kind: row.Kind, RunnerTags: append([]string(nil), row.RunnerTags...), RequiresAck: row.RequiresAck}
	if err := decodeRunSpec(row.RunSpec, &result.RunSpec); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := decodeWorkflow(row.Workflow, &result.Workflow); err != nil {
		return domain.TaskTemplate{}, err
	}
	return result, nil
}

func templateJSON(template domain.TaskTemplate) (json.RawMessage, json.RawMessage, error) {
	runSpec, err := json.Marshal(template.RunSpec)
	if err != nil {
		return nil, nil, err
	}
	workflow, err := json.Marshal(template.Workflow)
	return runSpec, workflow, err
}

func taskRunFromSQLC(row sqlcgen.TaskRun) (domain.TaskRun, error) {
	result := domain.TaskRun{ID: row.ID, ProjectID: row.ProjectID, TemplateID: row.TemplateID, RunnerTags: append([]string(nil), row.RunnerTags...), Status: row.Status, RunnerID: row.RunnerID, RequestedBy: row.RequestedBy, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}
	if err := decodeRunSpec(row.RunSpec, &result.RunSpec); err != nil {
		return domain.TaskRun{}, err
	}
	if err := decodeWorkflow(row.Workflow, &result.Workflow); err != nil {
		return domain.TaskRun{}, err
	}
	if err := decodeWorkflowState(row.WorkflowState, &result.WorkflowState); err != nil {
		return domain.TaskRun{}, err
	}
	return result, nil
}

func runJSON(run domain.TaskRun) (json.RawMessage, json.RawMessage, json.RawMessage, error) {
	runSpec, err := json.Marshal(run.RunSpec)
	if err != nil {
		return nil, nil, nil, err
	}
	workflow, err := json.Marshal(run.Workflow)
	if err != nil {
		return nil, nil, nil, err
	}
	state, err := json.Marshal(run.WorkflowState)
	return runSpec, workflow, state, err
}

func runnerFromSQLC(row sqlcgen.Runner) domain.Runner {
	return domain.Runner{ID: row.ID, Name: row.Name, Tags: append([]string(nil), row.Tags...), Capabilities: append([]string(nil), row.Capabilities...), TokenHash: row.TokenHash, Status: row.Status, RegisteredAt: row.RegisteredAt, LastHeartbeatAt: row.LastHeartbeatAt}
}

func runnerEnrollmentFromSQLC(row sqlcgen.RunnerEnrollment) domain.RunnerEnrollment {
	return domain.RunnerEnrollment{
		ID: row.ID, TokenHash: row.TokenHash, RunnerID: row.RunnerID, RunnerName: row.RunnerName,
		Tags: row.Tags, Capabilities: row.Capabilities, CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt,
		UsedAt: row.UsedAt, ConsumeRequestID: row.ConsumeRequestID, CredentialHash: row.CredentialHash,
	}
}

func runLeaseFromSQLC(row sqlcgen.RunLease) domain.RunLease {
	lease := domain.RunLease{ID: row.ID, RunID: row.RunID, RunnerID: row.RunnerID, Status: row.Status, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt, Attempt: int(row.Attempt), Fence: row.Fence}
	if row.CompletionKey != nil {
		lease.CompletionKey = *row.CompletionKey
	}
	return lease
}

func runLogFromSQLC(row sqlcgen.RunLog) domain.RunLog {
	log := domain.RunLog{ID: row.ID, RunID: row.RunID, Sequence: int(row.Sequence), Stream: row.Stream, Message: row.Message, CreatedAt: row.CreatedAt}
	if row.EventKey != nil {
		log.EventKey = *row.EventKey
	}
	if row.LeaseID != nil {
		log.LeaseID = *row.LeaseID
	}
	if row.Attempt != nil {
		log.Attempt = int(*row.Attempt)
	}
	if row.RequestedSequence != nil {
		log.RequestedSequence = int(*row.RequestedSequence)
	}
	return log
}

func artifactFromSQLC(row sqlcgen.RunArtifact) domain.ArtifactRecord {
	return domain.ArtifactRecord{ID: row.ID, RunID: row.RunID, LeaseID: row.LeaseID, Name: row.Name, Path: row.Path, Found: row.Found, Required: row.Required, Size: row.Size, Kind: row.Kind, CreatedAt: row.CreatedAt}
}

func approvalFromSQLC(row sqlcgen.Approval) domain.Approval {
	return domain.Approval{ID: row.ID, RunID: row.RunID, Status: row.Status, RequestedBy: row.RequestedBy, ApprovedBy: row.ApprovedBy, CreatedAt: row.CreatedAt, ApprovedAt: row.ApprovedAt}
}

func auditEventFromSQLC(row sqlcgen.AuditEvent) (domain.AuditEvent, error) {
	result := domain.AuditEvent{ID: row.ID, ActorID: row.ActorID, Action: row.Action, TargetID: row.TargetID, CreatedAt: row.CreatedAt}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &result.Metadata); err != nil {
			return domain.AuditEvent{}, err
		}
	}
	return result, nil
}
