package store

import (
	"encoding/json"

	"github.com/lib/pq"
	bobtypes "github.com/stephenafamo/bob/types"

	"nerocd/internal/domain"
	bobmodels "nerocd/internal/store/bobgen/models"
)

func taskTemplateSetter(template domain.TaskTemplate) (*bobmodels.TaskTemplateSetter, error) {
	runSpec, err := json.Marshal(template.RunSpec)
	if err != nil {
		return nil, err
	}
	workflow, err := json.Marshal(template.Workflow)
	if err != nil {
		return nil, err
	}
	tags := pq.StringArray(template.RunnerTags)
	runSpecJSON := bobtypes.NewJSON(json.RawMessage(runSpec))
	workflowJSON := bobtypes.NewJSON(json.RawMessage(workflow))
	return &bobmodels.TaskTemplateSetter{
		ID:          &template.ID,
		ProjectID:   &template.ProjectID,
		Name:        &template.Name,
		Kind:        &template.Kind,
		RunnerTags:  &tags,
		RequiresAck: &template.RequiresAck,
		RunSpec:     &runSpecJSON,
		Workflow:    &workflowJSON,
	}, nil
}

func taskTemplateFromGenerated(template *bobmodels.TaskTemplate) (domain.TaskTemplate, error) {
	result := domain.TaskTemplate{
		ID:          template.ID,
		ProjectID:   template.ProjectID,
		Name:        template.Name,
		Kind:        template.Kind,
		RunnerTags:  append([]string(nil), template.RunnerTags...),
		RequiresAck: template.RequiresAck,
	}
	if err := decodeRunSpec(template.RunSpec.Val, &result.RunSpec); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err := decodeWorkflow(template.Workflow.Val, &result.Workflow); err != nil {
		return domain.TaskTemplate{}, err
	}
	return result, nil
}
