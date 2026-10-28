package hostingbuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileArgumentDeclarationSyntax(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"stages and heredoc", "# syntax=docker/dockerfile:1.28\n# check=skip=JSONArgsRecommended\nARG BASE=alpine\nFROM \\\n  ${BASE} AS build\nRUN <<'SCRIPT'\nFROM not-a-stage\nSCRIPT\nFROM build AS final\nARG APP=existing\n", "# syntax=docker/dockerfile:1.28\n# check=skip=JSONArgsRecommended\nARG BASE=alpine\nFROM \\\n  ${BASE} AS build\nARG APP\nARG Z\nRUN <<'SCRIPT'\nFROM not-a-stage\nSCRIPT\nFROM build AS final\nARG APP\nARG Z\nARG APP=existing\n"},
		{"escape directive", "# escape=`\nFROM `\n  alpine AS build\nRUN echo done\n", "# escape=`\nFROM `\n  alpine AS build\nARG APP\nARG Z\nRUN echo done\n"},
		{"crlf", "# syntax=docker/dockerfile:1.28\r\nFROM --platform=$BUILDPLATFORM \\\r\n alpine AS stage\r\nRUN echo done\r\n", "# syntax=docker/dockerfile:1.28\r\nFROM --platform=$BUILDPLATFORM \\\r\n alpine AS stage\r\nARG APP\r\nARG Z\r\nRUN echo done\r\n"},
		{"no final newline", "FROM alpine", "FROM alpine\nARG APP\nARG Z\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := dockerfileArgumentDeclarations(t.Context(), []byte(tc.input), map[string]string{"Z": "never-written\nvalue", "APP": "private-arg-value"}, 1<<20)
			if err != nil || string(output) != tc.want {
				t.Fatalf("injection altered Dockerfile structure:\n%s\n%v", output, err)
			}
		})
	}
}

func TestDockerfileArgumentsPreparedContextAndReplay(t *testing.T) {
	checkout := t.TempDir()
	raw := "FROM alpine AS first\nRUN echo '$APP'\nFROM first\nRUN echo done\n"
	writeTestFile(t, filepath.Join(checkout, "Dockerfile"), []byte(raw))
	r := settingRequest(checkout, Dockerfile)
	r.BuildArgs = map[string]string{"APP": "value-not-in-source"}
	r.InjectBuildArgs = true
	stage := filepath.Join(t.TempDir(), "context")
	digest, err := stageRequestContext(t.Context(), r, stage, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = injectDockerfileBuildArgs(t.Context(), r, stage); err != nil {
		t.Fatal(err)
	}
	transformed, err := os.ReadFile(filepath.Join(stage, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(transformed), r.BuildArgs["APP"]) || strings.Count(string(transformed), "ARG APP\n") != 2 {
		t.Fatal("injection exposed values or missed a stage")
	}
	original, err := os.ReadFile(filepath.Join(checkout, "Dockerfile"))
	if err != nil || string(original) != raw {
		t.Fatal("source checkout changed", err)
	}
	again := filepath.Join(t.TempDir(), "context")
	nextDigest, err := stageRequestContext(t.Context(), r, again, "")
	if err != nil || nextDigest != digest {
		t.Fatal("original context identity changed", err)
	}
	if err = injectDockerfileBuildArgs(t.Context(), r, again); err != nil {
		t.Fatal(err)
	}
	repeated, err := os.ReadFile(filepath.Join(again, "Dockerfile"))
	if err != nil || string(repeated) != string(transformed) {
		t.Fatal("fresh retry accumulated declarations", err)
	}
	enabled, _ := requestIdentity(ownedBuildRequest(r), Config{}, "")
	r.InjectBuildArgs = false
	disabled, _ := requestIdentity(ownedBuildRequest(r), Config{}, "")
	if enabled == disabled {
		t.Fatal("injection choice absent from identity")
	}
	r.BuildArgs = nil
	r.InjectBuildArgs = true
	empty := ownedBuildRequest(r)
	if empty.InjectBuildArgs {
		t.Fatal("no-argument request generated declarations")
	}
}

func TestDockerfileArgumentsFailBeforePreparedWrite(t *testing.T) {
	for _, mode := range []string{"cancel", "context_budget", "plan_budget", "symlink", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			stage := t.TempDir()
			raw := "FROM alpine\nRUN echo done\n"
			writeTestFile(t, filepath.Join(stage, "Dockerfile"), []byte(raw))
			r := settingRequest("/checkout", Dockerfile)
			r.BuildArgs = map[string]string{"APP": "literal"}
			r.InjectBuildArgs = true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "cancel":
				cancel()
			case "context_budget":
				writeTestFile(t, filepath.Join(stage, "asset"), []byte("asset"))
				r.Limits.ContextBytes = int64(len(raw) + 12)
			case "plan_budget":
				r.Limits.PlanBytes = int64(len(raw))
			case "symlink":
				if err := os.Symlink("Dockerfile", filepath.Join(stage, "linked")); err != nil {
					t.Fatal(err)
				}
				r.DockerfilePath = "linked"
			case "invalid":
				raw = "RUN <<EOF\nnot terminated\n"
				writeTestFile(t, filepath.Join(stage, "Dockerfile"), []byte(raw))
			}
			if err := injectDockerfileBuildArgs(ctx, r, stage); err == nil {
				t.Fatal("unsafe prepared transformation accepted")
			}
			current, err := os.ReadFile(filepath.Join(stage, "Dockerfile"))
			if err != nil || string(current) != raw {
				t.Fatal("denied transform changed prepared file", err)
			}
		})
	}
}
