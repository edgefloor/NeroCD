package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestGitComposeStackUsesExactLocalArtifactsAndImageAliases(t *testing.T) {
	for _, mode := range []string{"success", "local_missing", "daemon_changed", "missing_authority", "missing_owner", "registry_pull_failed"} {
		t.Run(mode, func(t *testing.T) {
			e, client, a, at := composeBuildFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := e.Execute(ctx, a, at); err != nil {
				t.Fatal(err)
			}
			composed := client.build.ComposeResult
			engine := testImageEngine(t)
			engine.stackCommandReady = true
			pulls := 0
			localMissing := mode == "local_missing"
			engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				switch {
				case args[4] == "info":
					daemon := "daemon"
					if mode == "daemon_changed" {
						daemon = "changed"
					}
					return json.Marshal(daemon)
				case args[4] == "image" && args[5] == "pull":
					pulls++
					if mode == "registry_pull_failed" {
						return nil, errors.New("registry unavailable")
					}
					if args[6] != "docker.io/library/postgres:17" {
						t.Error("local artifact used a tag or pull fallback")
					}
					return []byte("Digest: sha256:" + strings.Repeat("a", 64) + "\n"), nil
				case args[4] == "image" && args[5] == "inspect":
					id := args[6]
					digests := []string{}
					if !strings.HasPrefix(id, "sha256:") {
						id = "sha256:" + strings.Repeat("b", 64)
						digests = []string{args[6]}
					} else if localMissing {
						return nil, errors.New("missing local artifact")
					}
					return json.Marshal(map[string]any{"Id": id, "RepoDigests": digests, "Os": "linux", "Architecture": "amd64"})
				default:
					t.Error("preparation attempted workload mutation")
					return nil, errors.New("unexpected command")
				}
			})
			if mode == "missing_owner" {
				composed.Services = nil
				composed.Hash = ""
				composed.Hash = domain.HostingDocumentHash(*composed)
			}
			authority := composed
			if mode == "missing_authority" {
				authority = nil
			}
			release, images, err := engine.prepareStack(ctx, composed.Definition, nil, authority)
			if mode != "success" {
				if err == nil {
					t.Fatal("unavailable artifact accepted")
				}
				if pulls > 1 {
					t.Fatal("local image was pulled")
				}
				return
			}
			if err != nil || pulls != 1 || len(images) != len(composed.Definition.Services) {
				t.Fatalf("mixed preparation failed %v", err)
			}
			var local string
			for _, image := range images {
				if image.Component == "web" {
					local = image.Release.ImageID
				}
				if image.Component != "database" && image.Release.LocalBuild == (domain.HostingBuildArtifact{}) {
					t.Fatal("built service or alias lost local authority")
				}
			}
			for _, image := range images {
				if image.Component == "worker" && image.Release.ImageID != local {
					t.Fatal("shared-image alias changed local image")
				}
			}
			plan := domain.HostingApplyPlan{Release: release, Stack: &domain.HostingStackPlan{Definition: composed.Definition, Images: images}}
			if err = engine.ensureStackImages(ctx, plan); err != nil || pulls != 1 {
				t.Fatal("prepared local images were fetched again", err)
			}
			localMissing = true
			if err = engine.ensureStackImages(ctx, plan); err == nil || pulls != 1 {
				t.Fatal("collected local artifact fell back to registry")
			}
			snapshot := imageTestPlan(t).Snapshot
			snapshot.ImageReference = ""
			snapshot.PolicyVersion = domain.HostingStackPolicyVersion
			snapshot.ContainerPort = composed.Definition.ContainerPort
			snapshot.GitCompose = domain.HostingGitComposeRef{BuildID: client.build.ID, SourceGeneration: client.build.Snapshot.Source.Generation, ResultHash: composed.Hash}
			snapshot.Stack = domain.HostingStackRef{Generation: snapshot.ResourceGeneration, Hash: domain.HostingDocumentHash(composed.Definition)}
			stack := *plan.Stack
			stack.NetworkName = domain.HostingStackObjectID(snapshot.BindingID, "network", "private")
			stack.Volumes = []domain.HostingStackVolume{{Name: "uploads", VolumeName: domain.HostingStackObjectID(snapshot.BindingID, "volume", "uploads"), CreationOperationID: "origin", CreationNonce: strings.Repeat("a", 32)}}
			plan, err = domain.CompileHostingStackPlan(snapshot, release, nil, stack)
			if err != nil {
				t.Fatal("mixed plan compilation", err)
			}
			body, err := renderStack("hop", plan, nil)
			if err != nil {
				t.Fatal(err)
			}
			var rendered struct{ Services map[string]map[string]any }
			if json.Unmarshal(body, &rendered) != nil {
				t.Fatal("invalid rendered Compose")
			}
			for _, component := range []string{"web", "worker", "jobs"} {
				if rendered.Services[component]["image"] != local || rendered.Services[component]["build"] != nil {
					t.Fatal("rendered local service used mutable tag or rebuilt")
				}
			}
		})
	}
}
