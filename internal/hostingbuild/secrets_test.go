package hostingbuild

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileSecretMountSyntax(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"heredoc and multistage", "FROM alpine AS build\nRUN <<'EOF'\nRUN not-an-instruction\nEOF\nFROM build\nrun echo $TOKEN\n", "FROM alpine AS build\nRUN --mount=type=secret,id=TOKEN,env=TOKEN <<'EOF'\nRUN not-an-instruction\nEOF\nFROM build\nrun --mount=type=secret,id=TOKEN,env=TOKEN echo $TOKEN\n"},
		{"continuation", "# escape=`\nFROM alpine\n RUN `\n echo $TOKEN\n", "# escape=`\nFROM alpine\n RUN --mount=type=secret,id=TOKEN,env=TOKEN `\n echo $TOKEN\n"},
		{"explicit mount", "FROM alpine\nRUN --mount=id=USER,type=secret,target=/tmp/user cat /tmp/user\nRUN --mount=type=cache,target=/tmp/cache echo $TOKEN\n", "FROM alpine\nRUN --mount=id=USER,type=secret,target=/tmp/user cat /tmp/user\nRUN --mount=type=secret,id=TOKEN,env=TOKEN --mount=type=cache,target=/tmp/cache echo $TOKEN\n"},
		{"json and crlf", "FROM alpine\r\nRUN [\"echo\",\"TOKEN\"]\r\n", "FROM alpine\r\nRUN --mount=type=secret,id=TOKEN,env=TOKEN [\"echo\",\"TOKEN\"]\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dockerfileSecretMounts(t.Context(), []byte(tc.input), []SecretReference{{Name: "TOKEN", SecretID: "secret_token", Version: 1}}, 1<<20)
			if err != nil || string(got) != tc.want {
				t.Fatalf("transformed Dockerfile=%q want%q error=%v", got, tc.want, err)
			}
		})
	}
}

func TestBuildSecretStagingIdentityAndScrub(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "build-secrets-test")
	if err := os.MkdirAll(filepath.Join(dir, "docker"), 0700); err != nil {
		t.Fatal(err)
	}
	refs := []SecretReference{{Name: "TOKEN", SecretID: "secret_token", Version: 1}, {Name: "EMPTY", SecretID: "secret_empty", Version: 1}}
	value := "protected-token\nsecond-line"
	err := writeBuildSecrets(t.Context(), dir, refs, func(context.Context) ([]SecretValue, error) {
		return []SecretValue{{Name: "TOKEN", Value: value}, {Name: "EMPTY", Value: ""}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "build-secrets", "TOKEN")
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != value {
		t.Fatal("exact secret staging", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe staged mode", err)
	}
	r := settingRequest("/checkout", Dockerfile)
	r.BuildSecrets = refs
	identity, _ := requestIdentity(r, Config{}, "")
	owned := ownedBuildRequest(r)
	refs[0].Version = 2
	next, _ := requestIdentity(r, Config{}, "")
	if identity == next || owned.BuildSecrets[0].Version != 1 {
		t.Fatal("secret references lack owned identity")
	}
	args := r.buildxArguments("builder", DockerfileFrontend, filepath.Join(dir, "context"), "Dockerfile", "export")
	wire, _ := json.Marshal(r)
	protected, _ := json.Marshal(SecretValue{Name: "TOKEN", Value: value})
	if strings.Contains(strings.Join(args, " "), value) || strings.Contains(string(wire), value) || string(protected) != "{}" || !strings.Contains(strings.Join(args, " "), "--secret id=TOKEN,src="+path) || !strings.Contains(strings.Join(args, " "), "--no-cache") {
		t.Fatal("secret argv/serialization contract changed")
	}
	if err = ScrubRegistryCredentials(state); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(dir, "build-secrets")); !os.IsNotExist(err) {
		t.Fatal("crash-left build secrets retained", err)
	}
}

func TestBuildSecretsRejectMaterialAndCleanupObstruction(t *testing.T) {
	for _, mode := range []string{"cancel", "missing", "duplicate", "nul", "provider"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := writeBuildSecrets(ctx, dir, []SecretReference{{Name: "TOKEN", SecretID: "secret_token", Version: 1}}, func(context.Context) ([]SecretValue, error) {
				switch mode {
				case "cancel":
					cancel()
				case "missing":
					return nil, nil
				case "duplicate":
					return []SecretValue{{Name: "TOKEN", Value: "one"}, {Name: "TOKEN", Value: "two"}}, nil
				case "nul":
					return []SecretValue{{Name: "TOKEN", Value: "one\x00two"}}, nil
				case "provider":
					return nil, errors.New("protected-error")
				}
				return []SecretValue{{Name: "TOKEN", Value: "protected"}}, nil
			})
			if err == nil || strings.Contains(err.Error(), "protected-error") {
				t.Fatal("invalid secret provider admitted or leaked")
			}
			if err = removeBuildSecrets(dir); err != nil {
				t.Fatal("failed provider cleanup", err)
			}
		})
	}
	state := t.TempDir()
	dir := filepath.Join(state, "build-held")
	if err := os.MkdirAll(filepath.Join(dir, "build-secrets", "TOKEN"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ScrubRegistryCredentials(state); !errors.Is(err, ErrCleanupUnconfirmed) {
		t.Fatal("obstructed secret scrub did not hold recovery", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build-secrets", "TOKEN")); err != nil {
		t.Fatal("obstruction was deleted", err)
	}
}

func TestBuildSecretTransformBudgetsAndOriginalSource(t *testing.T) {
	checkout := t.TempDir()
	original := "FROM alpine\nRUN echo $TOKEN\n"
	writeTestFile(t, filepath.Join(checkout, "Dockerfile"), []byte(original))
	r := settingRequest(checkout, Dockerfile)
	r.BuildSecrets = []SecretReference{{Name: "TOKEN", SecretID: "secret_token", Version: 1}}
	stage := filepath.Join(t.TempDir(), "context")
	if _, err := stageRequestContext(t.Context(), r, stage, ""); err != nil {
		t.Fatal(err)
	}
	transform := func(ctx context.Context, raw []byte, limit int64) ([]byte, error) {
		return dockerfileSecretMounts(ctx, raw, r.BuildSecrets, limit)
	}
	r.Limits.PlanBytes = int64(len(original))
	if err := transformPreparedDockerfile(t.Context(), r, stage, transform); err == nil {
		t.Fatal("transform exceeded plan budget")
	}
	raw, err := os.ReadFile(filepath.Join(stage, "Dockerfile"))
	if err != nil || string(raw) != original {
		t.Fatal("failed transform changed staged source", err)
	}
	r.Limits.PlanBytes = 1 << 20
	if err = transformPreparedDockerfile(t.Context(), r, stage, transform); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(checkout, "Dockerfile"))
	if err != nil || string(raw) != original {
		t.Fatal("transform changed source checkout", err)
	}
	r.BuildArgs = map[string]string{"TOKEN": "public"}
	if r.validateSettings() == nil {
		t.Fatal("engine accepted public/secret collision")
	}
}

func TestBuildSecretArgumentPathCannotSelectHostEnvironment(t *testing.T) {
	path := "/private/state,env=UNSELECTED/\"quoted\"/build-secrets/TOKEN"
	fields, err := csv.NewReader(strings.NewReader(buildSecretArgument("TOKEN", path))).Read()
	if err != nil || len(fields) != 2 || fields[0] != "id=TOKEN" || fields[1] != "src="+path {
		t.Fatal("Buildx path escaped its CSV field", err)
	}
}
