package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) ListRuns(ctx context.Context, projectID string) ([]domain.TaskRun, error) {
	result, err := s.ListRunsPage(ctx, projectID, store.Page{})
	return result.Items, err
}

func (s *Service) ListRunsPage(ctx context.Context, projectID string, page store.Page) (store.PageResult[domain.TaskRun], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.TaskRun]{}, err
		}
		return s.runs.ListRunsPage(ctx, projectID, page)
	}
	runs, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	filtered, err := s.filterRunsForPrincipal(ctx, principal, runs)
	if err != nil {
		return store.PageResult[domain.TaskRun]{}, err
	}
	return paginateForService(filtered, page), nil
}

func (s *Service) ListRunLogs(ctx context.Context, runID string) ([]domain.RunLog, error) {
	result, err := s.ListRunLogsPage(ctx, runID, store.Page{})
	return result.Items, err
}

func (s *Service) ListRunLogsPage(ctx context.Context, runID string, page store.Page) (store.PageResult[domain.RunLog], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, err := s.runByID(ctx, runID)
		if err != nil {
			return store.PageResult[domain.RunLog]{}, err
		}
		if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.RunLog]{}, err
		}
		return s.runs.ListRunLogsPage(ctx, runID, page)
	}
	logs, err := s.runs.ListRunLogs(ctx, "")
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	allRuns, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	runs, err := s.filterRunsForPrincipal(ctx, principal, allRuns)
	if err != nil {
		return store.PageResult[domain.RunLog]{}, err
	}
	allowedRuns := map[string]struct{}{}
	for _, run := range runs {
		allowedRuns[run.ID] = struct{}{}
	}
	out := make([]domain.RunLog, 0, len(logs))
	for _, log := range logs {
		if _, ok := allowedRuns[log.RunID]; ok {
			out = append(out, log)
		}
	}
	return paginateForService(out, page), nil
}

func (s *Service) ListArtifacts(ctx context.Context, runID string) ([]domain.ArtifactRecord, error) {
	result, err := s.ListArtifactsPage(ctx, runID, store.Page{})
	return result.Items, err
}

func (s *Service) ListArtifactsPage(ctx context.Context, runID string, page store.Page) (store.PageResult[domain.ArtifactRecord], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		run, err := s.runByID(ctx, runID)
		if err != nil {
			return store.PageResult[domain.ArtifactRecord]{}, err
		}
		if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.ArtifactRecord]{}, err
		}
		return s.runs.ListArtifactsPage(ctx, runID, page)
	}
	if isSystemAdmin(principal) {
		return s.runs.ListArtifactsPage(ctx, "", page)
	}
	runs, err := s.ListRuns(ctx, "")
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	allowed := map[string]bool{}
	for _, run := range runs {
		allowed[run.ID] = true
	}
	artifacts, err := s.runs.ListArtifacts(ctx, "")
	if err != nil {
		return store.PageResult[domain.ArtifactRecord]{}, err
	}
	out := make([]domain.ArtifactRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		if allowed[artifact.RunID] {
			out = append(out, artifact)
		}
	}
	return paginateForService(out, page), nil
}

type RunLogInput struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	EventKey string `json:"event_key"`
}

type RunEventInput struct {
	EventKey string `json:"event_key"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
}

type RunEventBatchInput struct {
	RunID   string          `json:"run_id"`
	LeaseID string          `json:"lease_id"`
	Attempt int             `json:"attempt"`
	Fence   string          `json:"fence"`
	Events  []RunEventInput `json:"events"`
}

type RunEventBatchAck struct {
	Events []domain.RunLog `json:"events"`
}

type ArtifactInput struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
}

func (s *Service) CreateArtifact(ctx context.Context, input ArtifactInput) (domain.ArtifactRecord, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.ArtifactRecord{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	leaseID := strings.TrimSpace(input.LeaseID)
	name := strings.TrimSpace(input.Name)
	path := strings.TrimSpace(input.Path)
	if runID == "" || leaseID == "" || name == "" || path == "" {
		return domain.ArtifactRecord{}, errors.New("run_id, lease_id, name, and path are required")
	}
	if input.Attempt <= 0 || strings.TrimSpace(input.Fence) == "" {
		return domain.ArtifactRecord{}, errors.New("attempt and fence are required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = domain.ArtifactFile
	}
	artifact := domain.ArtifactRecord{ID: mustPrefixedID("art"), RunID: runID, LeaseID: leaseID, Name: name, Path: path, Found: input.Found, Required: input.Required, Size: input.Size, Kind: kind, CreatedAt: time.Now().UTC()}
	if err := s.runs.CreateArtifactForLease(ctx, artifact, principal.ID, input.Attempt, input.Fence, artifact.CreatedAt); err != nil {
		return domain.ArtifactRecord{}, err
	}
	return artifact, nil
}

func (s *Service) AppendRunLog(ctx context.Context, input RunLogInput) (domain.RunLog, error) {
	result, err := s.AppendRunEvents(ctx, RunEventBatchInput{RunID: input.RunID, LeaseID: input.LeaseID, Attempt: input.Attempt, Fence: input.Fence, Events: []RunEventInput{{EventKey: input.EventKey, Sequence: input.Sequence, Stream: input.Stream, Message: input.Message}}})
	if err != nil {
		return domain.RunLog{}, err
	}
	return result.Events[0], nil
}

func (s *Service) AppendRunEvents(ctx context.Context, input RunEventBatchInput) (RunEventBatchAck, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return RunEventBatchAck{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	leaseID := strings.TrimSpace(input.LeaseID)
	if runID == "" || leaseID == "" {
		return RunEventBatchAck{}, errors.New("run_id and lease_id are required")
	}
	if input.Attempt <= 0 || strings.TrimSpace(input.Fence) == "" {
		return RunEventBatchAck{}, errors.New("attempt and fence are required")
	}
	if len(input.Events) == 0 || len(input.Events) > 64 {
		return RunEventBatchAck{}, errors.New("events batch must contain between 1 and 64 events")
	}
	totalBytes := 0
	logs := make([]domain.RunLog, 0, len(input.Events))
	seen := make(map[string]struct{}, len(input.Events))
	now := time.Now().UTC()
	for _, event := range input.Events {
		eventKey := strings.TrimSpace(event.EventKey)
		stream := strings.TrimSpace(event.Stream)
		if eventKey == "" || event.Sequence <= 0 {
			return RunEventBatchAck{}, errors.New("event_key and positive sequence are required")
		}
		if _, duplicate := seen[eventKey]; duplicate {
			return RunEventBatchAck{}, errors.New("event_key is duplicated within batch")
		}
		seen[eventKey] = struct{}{}
		if stream != domain.LogSystem && stream != domain.LogStdout && stream != domain.LogStderr {
			return RunEventBatchAck{}, errors.New("stream must be system, stdout, or stderr")
		}
		totalBytes += len(eventKey) + len(stream) + len(event.Message)
		if totalBytes > 256*1024 {
			return RunEventBatchAck{}, errors.New("events batch exceeds 256 KiB")
		}
		logID, err := prefixedID("log")
		if err != nil {
			return RunEventBatchAck{}, err
		}
		logs = append(logs, domain.RunLog{ID: logID, RunID: runID, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: stream, Message: event.Message, CreatedAt: now, EventKey: eventKey, LeaseID: leaseID, Attempt: input.Attempt})
	}
	persisted, err := s.runs.CreateRunLogsForLease(ctx, logs, runID, principal.ID, leaseID, input.Attempt, input.Fence, now)
	if err != nil {
		return RunEventBatchAck{}, err
	}
	return RunEventBatchAck{Events: persisted}, nil
}

func (s *Service) RequestRun(ctx context.Context, templateID string) (domain.TaskRun, error) {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return domain.TaskRun{}, errors.New("template_id is required")
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	template, err := s.templates.GetTemplate(ctx, templateID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if err := s.requireProjectRole(ctx, principal, template.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	status := domain.RunQueued
	if template.RequiresAck {
		status = domain.RunWaitingApproval
	}
	now := time.Now().UTC()
	runID, err := prefixedID("run")
	if err != nil {
		return domain.TaskRun{}, err
	}
	run := domain.TaskRun{ID: runID, ProjectID: template.ProjectID, TemplateID: &template.ID, RunSpec: template.RunSpec, Workflow: template.Workflow, WorkflowState: initialWorkflowState(template.Workflow), RunnerTags: template.RunnerTags, Status: status, RequestedBy: principal.ID, StartedAt: now}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	log := domain.RunLog{ID: logID, RunID: run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "Run requested", CreatedAt: now}
	var approval *domain.Approval
	if template.RequiresAck {
		approvalID, err := prefixedID("apr")
		if err != nil {
			return domain.TaskRun{}, err
		}
		approval = &domain.Approval{ID: approvalID, RunID: run.ID, Status: domain.ApprovalPending, RequestedBy: principal.ID, CreatedAt: now}
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.request", run.ID, map[string]any{"template_id": template.ID, "status": run.Status})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runs.CreateRunRequest(ctx, run, log, approval, audit)
}

type RunRequestInput struct {
	ProjectID   string          `json:"project_id"`
	TemplateID  string          `json:"template_id"`
	RunSpec     domain.RunSpec  `json:"run_spec"`
	Workflow    domain.Workflow `json:"workflow"`
	RunnerTags  []string        `json:"runner_tags"`
	RequiresAck bool            `json:"requires_ack"`
}

func (s *Service) RequestRunWithSpec(ctx context.Context, input RunRequestInput) (domain.TaskRun, error) {
	if strings.TrimSpace(input.TemplateID) != "" {
		return s.RequestRun(ctx, input.TemplateID)
	}
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		return domain.TaskRun{}, errors.New("project_id is required")
	}
	if err := s.requireProjectRole(ctx, principal, projectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	runSpec, err := s.normalizeRunSpec(input.RunSpec, "")
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := s.registry.BuildPlan(domain.TaskRun{ID: "validation", ProjectID: projectID, RunSpec: runSpec}); err != nil {
		return domain.TaskRun{}, err
	}
	status := domain.RunQueued
	if input.RequiresAck {
		status = domain.RunWaitingApproval
	}
	now := time.Now().UTC()
	runID, err := prefixedID("run")
	if err != nil {
		return domain.TaskRun{}, err
	}
	workflow, err := s.normalizeWorkflow(input.Workflow)
	if err != nil {
		return domain.TaskRun{}, err
	}
	run := domain.TaskRun{ID: runID, ProjectID: projectID, RunSpec: runSpec, Workflow: workflow, WorkflowState: initialWorkflowState(workflow), RunnerTags: normalizeTags(input.RunnerTags), Status: status, RequestedBy: principal.ID, StartedAt: now}
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	log := domain.RunLog{ID: logID, RunID: run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "Ad-hoc run requested", CreatedAt: now}
	var approval *domain.Approval
	if input.RequiresAck {
		approvalID, err := prefixedID("apr")
		if err != nil {
			return domain.TaskRun{}, err
		}
		approval = &domain.Approval{ID: approvalID, RunID: run.ID, Status: domain.ApprovalPending, RequestedBy: principal.ID, CreatedAt: now}
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.request", run.ID, map[string]any{"run_type": run.RunSpec.Type, "status": run.Status})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runs.CreateRunRequest(ctx, run, log, approval, audit)
}

func (s *Service) ApproveRun(ctx context.Context, runID string) (domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	run, err := s.runByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.Approval{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.approve", strings.TrimSpace(runID), nil)
	if err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.approvals.ApproveRun(ctx, strings.TrimSpace(runID), principal.ID, time.Now().UTC(), store.WithAudit(audit))
	if err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}

func (s *Service) RejectRun(ctx context.Context, runID string) (domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	run, err := s.runByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.Approval{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.reject", strings.TrimSpace(runID), nil)
	if err != nil {
		return domain.Approval{}, err
	}
	approval, err := s.approvals.RejectRun(ctx, strings.TrimSpace(runID), principal.ID, time.Now().UTC(), store.WithAudit(audit))
	if err != nil {
		return domain.Approval{}, err
	}
	_ = s.runs.CreateRunLog(ctx, domain.RunLog{ID: mustPrefixedID("log"), RunID: approval.RunID, Sequence: 2, Stream: domain.LogSystem, Message: "Run rejected by approver", CreatedAt: time.Now().UTC()})
	return approval, nil
}

func (s *Service) CancelRun(ctx context.Context, runID string) (domain.TaskRun, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	runID = strings.TrimSpace(runID)
	targetRun, err := s.runByID(ctx, runID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if err := s.requireProjectRole(ctx, principal, targetRun.ProjectID, domain.RoleMaintainer); err != nil {
		return domain.TaskRun{}, err
	}
	if domain.IsTerminalRunStatus(targetRun.Status) {
		return domain.TaskRun{}, store.ErrConflict
	}
	now := time.Now().UTC()
	logID, err := prefixedID("log")
	if err != nil {
		return domain.TaskRun{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run.cancel", runID, map[string]any{"status": domain.RunCanceled})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return s.runners.CancelRunRequest(ctx, runID, now, domain.RunLog{ID: logID, RunID: runID, Sequence: 2, Stream: domain.LogSystem, Message: "Run canceled by user", CreatedAt: now}, audit)
}

func (s *Service) RunnerPrimitivePlan(ctx context.Context, runID string) (domain.RunnerPrimitivePlan, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	if err := s.requireProjectRole(ctx, principal, run.ProjectID, domain.RoleViewer); err != nil {
		return domain.RunnerPrimitivePlan{}, err
	}
	return s.registry.BuildPlan(run)
}
