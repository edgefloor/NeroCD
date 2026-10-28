package runnerruntime

import (
	"nerocd/internal/domain"
	"testing"
)

func TestDockerfileBuildRequestArgumentInjectionChoice(t *testing.T) {
	for _, choice := range []string{"default", "enabled", "disabled", "empty"} {
		t.Run(choice, func(t *testing.T) {
			settings := domain.HostingBuildSettings{Strategy: "dockerfile", BuildVariables: []domain.HostingBuildVariable{{Name: "APP", Value: "first\nsecond"}}}
			if choice == "enabled" || choice == "disabled" {
				enabled := choice == "enabled"
				settings.InjectBuildArgsToDockerfile = &enabled
			}
			if choice == "empty" {
				settings.BuildVariables = nil
			}
			snapshot := domain.HostingBuildSnapshot{Source: domain.HostingGitSource{Settings: settings}}
			request := buildEngineRequest(snapshot, "/checkout", "daemon")
			if request.InjectBuildArgs != (choice == "default" || choice == "enabled") {
				t.Fatal("effective injection choice changed")
			}
			if choice != "empty" && request.BuildArgs["APP"] != "first\nsecond" {
				t.Fatal("public build variable not passed")
			}
			if choice != "empty" {
				settings.BuildVariables[0].Value = "changed"
				if request.BuildArgs["APP"] != "first\nsecond" {
					t.Fatal("request borrowed source variable storage")
				}
			}
		})
	}
}
