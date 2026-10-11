package store

import (
	"slices"
	"testing"

	"nerocd/internal/store/sqlcgen"
)

func TestRunMappersPreserveTagArrays(t *testing.T) {
	for _, tags := range [][]string{nil, {}, {"shell", "linux"}} {
		template, err := taskTemplateFromSQLC(sqlcgen.TaskTemplate{RunnerTags: tags, RunSpec: []byte(`{}`), Workflow: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		run, err := taskRunFromSQLC(sqlcgen.TaskRun{RunnerTags: tags, RunSpec: []byte(`{}`), Workflow: []byte(`{}`), WorkflowState: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		for name, got := range map[string][]string{"template": template.RunnerTags, "run": run.RunnerTags} {
			if got == nil {
				t.Errorf("%s mapped tags to nil; want an array", name)
			}
			if !slices.Equal(got, tags) {
				t.Errorf("%s tags = %v, want %v", name, got, tags)
			}
			if len(got) > 0 {
				got[0] = "changed"
				if tags[0] == "changed" {
					t.Errorf("%s tags alias the database row", name)
				}
			}
		}
	}
}
