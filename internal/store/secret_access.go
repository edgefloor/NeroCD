package store

import (
	"encoding/json"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const secretAccessAuditAction = "secret.access"

func runSpecForSecretAccess(run domain.TaskRun) (domain.RunSpec, bool) {
	currentStepID := strings.TrimSpace(run.WorkflowState.CurrentStepID)
	if len(run.Workflow.Steps) == 0 {
		return run.RunSpec, true
	}
	if currentStepID == "" {
		return domain.RunSpec{}, false
	}
	for _, step := range run.Workflow.Steps {
		if strings.TrimSpace(step.ID) == currentStepID {
			return step.RunSpec, true
		}
	}
	return domain.RunSpec{}, false
}

func runAuthorizesSecretAccess(run domain.TaskRun, request domain.SecretAccessRequest) bool {
	runSpec, ok := runSpecForSecretAccess(run)
	if !ok {
		return false
	}
	matches := 0
	for _, binding := range runSpec.Secrets {
		if strings.TrimSpace(binding.Name) != request.Binding {
			continue
		}
		matches++
		if strings.ToLower(strings.TrimSpace(binding.Provider)) != request.Provider || strings.TrimSpace(binding.Version) != request.Version {
			return false
		}
	}
	return matches == 1
}

func secretAccessAudit(request domain.SecretAccessRequest, createdAt time.Time) domain.AuditEvent {
	return domain.AuditEvent{
		ID:        request.AccessID,
		ActorID:   request.RunnerID,
		Action:    secretAccessAuditAction,
		TargetID:  request.RunID,
		CreatedAt: createdAt,
		Metadata: map[string]any{
			"lease_id": request.LeaseID,
			"attempt":  request.Attempt,
			"binding":  request.Binding,
			"provider": request.Provider,
			"version":  request.Version,
		},
	}
}

func secretAccessAuditsEqual(existing, expected domain.AuditEvent) bool {
	if existing.ID != expected.ID || existing.ActorID != expected.ActorID || existing.Action != expected.Action || existing.TargetID != expected.TargetID {
		return false
	}
	existingMetadata, existingErr := json.Marshal(existing.Metadata)
	expectedMetadata, expectedErr := json.Marshal(expected.Metadata)
	return existingErr == nil && expectedErr == nil && string(existingMetadata) == string(expectedMetadata)
}

func secretAccessGrant(request domain.SecretAccessRequest, authorizedAt time.Time) domain.SecretAccessGrant {
	return domain.SecretAccessGrant{
		AccessID: request.AccessID, RunID: request.RunID, LeaseID: request.LeaseID,
		Attempt: request.Attempt, Binding: request.Binding, Provider: request.Provider,
		Version: request.Version, AuthorizedAt: authorizedAt,
	}
}
