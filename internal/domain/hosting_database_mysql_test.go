package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMySQLPinnedRecipePlanAndBinding(t *testing.T) {
	recipe, err := HostingDatabaseRecipeFor("mysql", HostingDatabaseMySQLRecipe)
	if err != nil {
		t.Fatal(err)
	}
	if recipe.ContainerPort != 3306 || recipe.MountPath != "/var/lib/mysql" || recipe.DatabaseName != "app" || len(recipe.Platforms) != 2 || recipe.Credentials[0].Username != "root" || recipe.Credentials[1].Username != "ncd_app" {
		t.Fatal("MySQL recipe differs")
	}
	for _, platform := range []int{0, 1} {
		s, r, storage, db := databaseExecutionFixture(t, "mysql", HostingDatabaseMySQLRecipe, platform)
		plan, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
		if err != nil || ValidateHostingDatabasePlan(plan) != nil {
			t.Fatal("MySQL plan invalid", err)
		}
		raw, err := json.Marshal(plan)
		var decoded HostingApplyPlan
		if err != nil || json.Unmarshal(raw, &decoded) != nil || ValidateHostingDatabasePlan(decoded) != nil {
			t.Fatal("MySQL plan codec failed")
		}
	}
	binding, h, db, endpoint := databaseBindingFixture(t, "mysql")
	if binding.ValidateTargets("project", h.Snapshot, "project", db) != nil || endpoint.ValidateApplication(binding, h) != nil {
		t.Fatal("MySQL binding rejected")
	}
	if !slices.Equal(binding.EnvironmentNames, []string{"MYSQL_DATABASE", "MYSQL_HOST", "MYSQL_PASSWORD", "MYSQL_PORT", "MYSQL_USER"}) {
		t.Fatal("MySQL binding profile changed")
	}
	input := `{"request_id":"` + strings.Repeat("a", 32) + `","project_id":"project","project_environment_id":"environment","name":"mysql","destination_id":"destination","engine":"mysql","recipe_version":"` + HostingDatabaseMySQLRecipe + `"}`
	var in HostingDatabaseCreateInput
	if json.Unmarshal([]byte(input), &in) != nil {
		t.Fatal("real MySQL decoder rejected request")
	}
	for _, bad := range []string{strings.Replace(input, HostingDatabaseMySQLRecipe, HostingDatabasePostgresRecipe, 1), strings.TrimSuffix(input, "}") + `,"password":"caller"}`, strings.TrimSuffix(input, "}") + `,"engine":"mysql"}`} {
		if json.Unmarshal([]byte(bad), &in) == nil {
			t.Fatal("invalid MySQL creation accepted")
		}
	}
}
