package runnerruntime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
)

const privateDockerfileMultiline = "first line\nsecond line"

// The first stage's served bytes and final stage's labels require independent
// ARG visibility. Values stay in build settings rather than generated source.
func privateDockerfileArgumentSource(declared, fail bool) string {
	declarations := ""
	if declared {
		declarations = "ARG BUILD_MARKER\nARG MULTILINE\nARG EMPTY=not-empty\n"
	}
	source := "FROM " + hostingbuild.StaticImage + " AS prepared\n" + declarations +
		"RUN test -z \"$EMPTY\" && printf '%s\\n%s\\n%s' \"$BUILD_MARKER\" \"$MULTILINE\" \"$EMPTY\" > /tmp/index.html\n" +
		"FROM " + hostingbuild.StaticImage + "\n" + declarations +
		"COPY --from=prepared --chmod=0644 /tmp/index.html /usr/share/nginx/html/index.html\n" +
		"LABEL nerocd.fixture.marker=\"${BUILD_MARKER}\" nerocd.fixture.multiline=\"${MULTILINE}\" nerocd.fixture.empty=\"${EMPTY}\"\n"
	if fail {
		source += "RUN exit 9\n"
	}
	return source
}

func privateDockerfileArgumentSettings(marker string, inject *bool) domain.HostingBuildSettings {
	return domain.HostingBuildSettings{Strategy: "dockerfile", BuildVariables: []domain.HostingBuildVariable{{Name: "BUILD_MARKER", Value: marker}, {Name: "MULTILINE", Value: privateDockerfileMultiline}, {Name: "EMPTY", Value: ""}}, InjectBuildArgsToDockerfile: inject}
}

func privateDockerfileArgumentBody(marker string) string {
	return marker + "\n" + privateDockerfileMultiline + "\n"
}

func TestPrivateDockerfileArgumentFixtureContracts(t *testing.T) {
	disabled := false
	for _, fixture := range []struct {
		name, marker string
		declared     bool
		inject       *bool
	}{{"default-A", "local-A", false, nil}, {"edited-B", "local-B", false, nil}, {"disabled-declared", "local-B", true, &disabled}} {
		t.Run(fixture.name, func(t *testing.T) {
			settings := privateDockerfileArgumentSettings(fixture.marker, fixture.inject)
			if err := domain.ValidateHostingBuildSettings(settings); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"source": map[string]any{"kind": "git", "git": map[string]any{"repository_url": "https://example.com:443/repo.git", "requested_ref": "main", "credential_kind": "https_basic", "settings": settings}}})
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Source domain.ResourceSourceInput `json:"source"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&request); err != nil {
				t.Fatal("production source decoder", err)
			}
			got := request.Source.Git.Settings
			if got.BuildVariables[1].Value != privateDockerfileMultiline || got.BuildVariables[2].Value != "" || (got.InjectBuildArgsToDockerfile == nil) != (fixture.inject == nil) || fixture.inject != nil && *got.InjectBuildArgsToDockerfile != *fixture.inject {
				t.Fatal("source decoder changed literal values or injection choice")
			}
			source := privateDockerfileArgumentSource(fixture.declared, false)
			if strings.Count(source, "FROM ") != 2 || strings.Count(source, "ARG BUILD_MARKER") != map[bool]int{false: 0, true: 2}[fixture.declared] || strings.Contains(source, fixture.marker) || strings.Contains(source, privateDockerfileMultiline) {
				t.Fatal("fixture does not require per-stage injection or leaked configured values into source")
			}
			if !strings.HasSuffix(privateDockerfileArgumentSource(fixture.declared, true), "RUN exit 9\n") {
				t.Fatal("genuine failure command changed")
			}
		})
	}
	if privateDockerfileArgumentBody("local-A") == privateDockerfileArgumentBody("local-B") {
		t.Fatal("edited variable must change served content")
	}
}
