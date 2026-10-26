package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestHostingStackReferenceAndIdentity(t *testing.T) {
	original, err := os.ReadFile("testdata/vikunja-with-postgresql.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := HostingStackSource{Original: string(original), PublicComponent: "vikunja", ContainerPort: 3456}
	stack, err := ParseHostingStack(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(stack.Services) != 2 || len(stack.Volumes) != 2 || !strings.Contains(stack.Services[1].Image, ":latest") {
		t.Fatalf("unexpected stack shape: %+v", stack)
	}
	if source.Original != string(original) || stack.OriginalHash != "sha256:4ae479af1c9ee76836315b952db8d177fb1b1426316ead2f0f5c09bfa4acdb54" {
		t.Fatal("original changed")
	}
	if stack.Services[1].Environment["VIKUNJA_DATABASE_PASSWORD"] != "${SERVICE_PASSWORD_POSTGRESQL}" {
		t.Fatal("reference expression lost")
	}
	if HostingStackObjectID("binding-a", "volume", "data") == HostingStackObjectID("binding-b", "volume", "data") {
		t.Fatal("volume ownership collision")
	}
}

func TestHostingStackPreflightRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	valid := "services:\n  app:\n    image: nginx:alpine\n"
	for name, definition := range map[string]string{
		"unknown service field":             valid + "    privileged: true\n",
		"unknown root field":                valid + "secrets: {}\n",
		"duplicate service":                 valid + "  app:\n    image: nginx:alpine\n",
		"duplicate environment":             valid + "    environment: [A=one, A=two]\n",
		"bind mount":                        valid + "    volumes: [/host:/data]\n",
		"external volume":                   valid + "volumes:\n  data:\n    external: true\n",
		"missing dependency":                valid + "    depends_on: [db]\n",
		"healthy dependency missing health": valid + "    depends_on:\n      db: {condition: service_healthy}\n  db:\n    image: postgres:16\n",
		"cycle":                             valid + "    depends_on: [db]\n  db:\n    image: postgres:16\n    depends_on: [app]\n",
		"multiple documents":                valid + "---\nservices: {}\n",
		"unsupported interpolation":         valid + "    environment: [A=${NAME/foo/bar}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHostingStack(HostingStackSource{Original: definition, PublicComponent: "app", ContainerPort: 80}); err == nil {
				t.Fatal("invalid stack accepted")
			}
		})
	}
}

func TestHostingStackInterpolation(t *testing.T) {
	values := map[string]string{"SET": "secret-value", "EMPTY": ""}
	for _, tc := range []struct{ expression, want string }{
		{"$SET", "secret-value"}, {"${SET}", "secret-value"}, {"$$SET", "$SET"},
		{"${MISSING:-fallback}", "fallback"}, {"${EMPTY:-fallback}", "fallback"}, {"${EMPTY-fallback}", ""},
		{"${MISSING+alternate}", ""}, {"${SET:+alternate}", "alternate"}, {"${EMPTY+alternate}", "alternate"}, {"${EMPTY:+alternate}", ""},
		{"a-$SET-b", "a-secret-value-b"},
	} {
		got, err := ExpandHostingStackValue(tc.expression, values, true)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q, %v", tc.expression, got, err)
		}
	}
	for _, expression := range []string{"${MISSING?do-not-log-this}", "${EMPTY:?secret-value}", "$MISSING"} {
		_, err := ExpandHostingStackValue(expression, values, true)
		if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "do-not-log-this") {
			t.Fatalf("missing variable error leaked or absent: %v", err)
		}
	}
}

func TestHostingStackAcceptsCompletedInitAndQuotedValues(t *testing.T) {
	text := `services:
  app:
    image: registry.example:5000/app:1
    command: ["/app/server", "--serve"]
    environment:
      ENABLED: true
      PORT: "3456"
      DESCRIPTION: >-
        hello world
    depends_on:
      init: {condition: service_completed_successfully}
    volumes: [data:/data:ro]
  init:
    image: busybox:1
    command: ["true"]
volumes:
  data: {}
`
	stack, err := ParseHostingStack(HostingStackSource{Original: text, PublicComponent: "app", ContainerPort: 3456})
	if err != nil {
		t.Fatal(err)
	}
	if stack.Services[0].Environment["DESCRIPTION"] != "hello world" || stack.Services[0].Environment["ENABLED"] != "true" {
		t.Fatal("YAML scalar semantics lost")
	}
}

func TestHostingStackSourceUnionRoundTrip(t *testing.T) {
	source := HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\n", PublicComponent: "app", ContainerPort: 80}
	value := ResourceSource{Kind: "compose", Compose: &source}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var input ResourceSourceInput
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if input.Source().Compose.Original != source.Original || ValidateResourceSource(input.Source()) != nil {
		t.Fatal("Compose source roundtrip lost exact source")
	}
	var persisted ResourceSource
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Kind != "compose" || persisted.Compose.PublicComponent != "app" {
		t.Fatal("persisted union lost selection")
	}
	for _, variant := range []string{`,"image":{"reference":"nginx:alpine"}`, `,"git":{}`, `,"image":null`} {
		mixed := append(append([]byte{}, raw[:len(raw)-1]...), []byte(variant+"}")...)
		if json.Unmarshal(mixed, &input) == nil {
			t.Fatal("mixed Compose source accepted")
		}
	}
}
