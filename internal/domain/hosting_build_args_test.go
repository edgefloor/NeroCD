package domain_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestHostingComposeBuildArguments(t *testing.T) {
	t.Setenv("AMBIENT_ARG", "must-not-leak")
	for _, tc := range []struct {
		name, yaml string
		variables  []domain.HostingBuildVariable
		want       map[string]string
		bad        bool
	}{
		{name: "map", yaml: "{ONE: literal, EMPTY: '', BARE: null, AMBIENT_ARG: null}", want: map[string]string{"ONE": "literal", "EMPTY": ""}},
		{name: "scalars", yaml: "{NUMBER: 20, FLAG: true, HEX: 0x20, FRACTION: 1.25}", want: map[string]string{"NUMBER": "20", "FLAG": "true", "HEX": "32", "FRACTION": "1.25"}},
		{name: "list", yaml: "[ONE=literal, EMPTY=, BARE, AMBIENT_ARG]", want: map[string]string{"ONE": "literal", "EMPTY": ""}},
		{name: "multiline", yaml: "{ONE: '${TEXT}'}", variables: []domain.HostingBuildVariable{{Name: "TEXT", Value: "first\nsecond\r\nthird"}}, want: map[string]string{"ONE": "first\nsecond\r\nthird", "TEXT": "first\nsecond\r\nthird"}},
		{name: "override", yaml: "{ONE: source, BARE: null, FROM: '${OTHER:?required}'}", variables: []domain.HostingBuildVariable{{Name: "ONE", Value: "configured"}, {Name: "BARE", Value: ""}, {Name: "OTHER", Value: "$UNCHANGED"}}, want: map[string]string{"ONE": "configured", "BARE": "", "OTHER": "$UNCHANGED", "FROM": "$UNCHANGED"}},
		{name: "operators", yaml: "{DEFAULT: '${MISSING:-fallback}', EMPTY: '${SET-fallback}', REQUIRED: '${SET?required}', ALT: '${SET:+yes}', ESCAPE: '$$VALUE'}", variables: []domain.HostingBuildVariable{{Name: "SET", Value: ""}}, want: map[string]string{"DEFAULT": "fallback", "EMPTY": "", "REQUIRED": "", "ALT": "", "ESCAPE": "$VALUE", "SET": ""}},
		{name: "empty required", yaml: "{ONE: '${SET:?no}'}", variables: []domain.HostingBuildVariable{{Name: "SET", Value: ""}}, bad: true},
		{name: "missing required", yaml: "{ONE: '${MISSING?no}'}", bad: true},
		{name: "nested", yaml: "{ONE: '${MISSING:-${OTHER}}'}", bad: true},
		{name: "duplicate map", yaml: "{ONE: a, ONE: b}", bad: true},
		{name: "duplicate list", yaml: "[ONE=a, ONE=b]", bad: true},
		{name: "reserved", yaml: "{BUILDKIT_SYNTAX: arbitrary}", bad: true},
		{name: "wrong type", yaml: "[{}]", bad: true},
		{name: "null args", yaml: "null", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, result := composeResultFixture(t)
			settings := b.Snapshot.Source.Settings
			settings.BuildVariables = tc.variables
			source := result.Source
			source.Original = strings.Replace(source.Original, "      target: release\n", "      target: release\n      args: "+tc.yaml+"\n", 1)
			definition, err := domain.ParseHostingGitCompose(source, settings)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid args accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, service := range definition.Services {
				if service.Name == "web" && !reflect.DeepEqual(service.Build.Args, tc.want) {
					t.Fatalf("args %v want %v", service.Build.Args, tc.want)
				}
			}
			if _, err = domain.ParseHostingStack(source); err == nil {
				t.Fatal("pasted source accepted build args")
			}
		})
	}
}

func TestHostingBuildVariablesBoundsAndIdentity(t *testing.T) {
	b, _ := composeResultFixture(t)
	settings := b.Snapshot.Source.Settings
	baseline, _ := json.Marshal(settings)
	settings.BuildVariables = []domain.HostingBuildVariable{}
	empty, _ := json.Marshal(settings)
	if string(baseline) != string(empty) {
		t.Fatal("empty variables changed historical bytes")
	}
	for _, vars := range [][]domain.HostingBuildVariable{
		{{Name: "BAD-NAME", Value: "x"}}, {{Name: "ARG", Value: "x"}, {Name: "ARG", Value: "y"}}, {{Name: "ARG", Value: strings.Repeat("x", 4097)}}, {{Name: "ARG", Value: string([]byte{0xff})}}, {{Name: "ARG", Value: "nul\x00"}}, {{Name: "BUILDKIT_SYNTAX", Value: "x"}}, {{Name: "http_proxy", Value: "x"}},
	} {
		settings.BuildVariables = vars
		if domain.ValidateHostingBuildSettings(settings) == nil {
			t.Fatal("invalid build variables accepted")
		}
	}
	settings.BuildVariables = []domain.HostingBuildVariable{{Name: "ARG", Value: "configured"}}
	if domain.ValidateHostingBuildSettings(settings) != nil {
		t.Fatal("valid build variables denied")
	}
	clone := settings.Clone()
	clone.BuildVariables[0].Value = "changed"
	if settings.BuildVariables[0].Value != "configured" || domain.HostingDocumentHash(settings) == domain.HostingDocumentHash(clone) {
		t.Fatal("variables borrowed or missing from identity")
	}
	settings.Strategy = "dockerfile"
	settings.ComposeFile = ""
	settings.PublicComponent = ""
	settings.ContainerPort = 0
	if domain.ValidateHostingBuildSettings(settings) == nil {
		t.Fatal("non-Compose variables accepted")
	}
}
