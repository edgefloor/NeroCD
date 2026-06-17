package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lib/pq"
	bobtypes "github.com/stephenafamo/bob/types"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
)

func taskRunSetter(run domain.TaskRun) (*bobmodels.TaskRunSetter, error) {
	runSpec, err := json.Marshal(run.RunSpec)
	if err != nil {
		return nil, err
	}
	workflow, err := json.Marshal(run.Workflow)
	if err != nil {
		return nil, err
	}
	workflowState, err := json.Marshal(run.WorkflowState)
	if err != nil {
		return nil, err
	}
	runSpecJSON := bobtypes.NewJSON(json.RawMessage(runSpec))
	workflowJSON := bobtypes.NewJSON(json.RawMessage(workflow))
	workflowStateJSON := bobtypes.NewJSON(json.RawMessage(workflowState))
	runnerTags := pq.StringArray(run.RunnerTags)
	templateID := nullString(run.TemplateID)
	finishedAt := nullTime(run.FinishedAt)
	createdAt := run.StartedAt
	return &bobmodels.TaskRunSetter{
		ID:            &run.ID,
		ProjectID:     &run.ProjectID,
		TemplateID:    &templateID,
		Status:        &run.Status,
		RequestedBy:   &run.RequestedBy,
		StartedAt:     &run.StartedAt,
		FinishedAt:    &finishedAt,
		CreatedAt:     &createdAt,
		RunSpec:       &runSpecJSON,
		Workflow:      &workflowJSON,
		WorkflowState: &workflowStateJSON,
		RunnerTags:    &runnerTags,
	}, nil
}

func taskRunFromGenerated(run *bobmodels.TaskRun) (domain.TaskRun, error) {
	result := domain.TaskRun{
		ID:          run.ID,
		ProjectID:   run.ProjectID,
		TemplateID:  stringPtr(run.TemplateID),
		RunnerTags:  append([]string(nil), run.RunnerTags...),
		Status:      run.Status,
		RunnerID:    stringPtr(run.RunnerID),
		RequestedBy: run.RequestedBy,
		StartedAt:   run.StartedAt,
		FinishedAt:  timePtr(run.FinishedAt),
	}
	if err := decodeRunSpec(run.RunSpec.Val, &result.RunSpec); err != nil {
		return domain.TaskRun{}, err
	}
	if err := decodeWorkflow(run.Workflow.Val, &result.Workflow); err != nil {
		return domain.TaskRun{}, err
	}
	if err := decodeWorkflowState(run.WorkflowState.Val, &result.WorkflowState); err != nil {
		return domain.TaskRun{}, err
	}
	return result, nil
}

func nullString(value *string) sql.Null[string] {
	if value == nil {
		return sql.Null[string]{}
	}
	return sql.Null[string]{V: *value, Valid: true}
}

func stringPtr(value sql.Null[string]) *string {
	if !value.Valid {
		return nil
	}
	return &value.V
}

func nullTimeValue(value time.Time) sql.Null[time.Time] {
	return sql.Null[time.Time]{V: value, Valid: true}
}
