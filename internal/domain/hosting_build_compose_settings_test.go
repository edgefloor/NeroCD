package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestHostingBuildComposeSettingsDecoderAndIsolation(t *testing.T) {
	valid := `{"kind":"git","git":{"repository_url":"https://github.com/team/app.git","requested_ref":"release/v1","credential_kind":"https_basic","settings":{"strategy":"compose","context_dir":"apps/web","compose_file":"deploy/compose.yaml","public_component":"web","container_port":8080}}}`
	var in domain.ResourceSourceInput
	if err := json.Unmarshal([]byte(valid), &in); err != nil || in.Git == nil || domain.ValidateHostingBuildSettings(in.Git.Settings) != nil {
		t.Fatal("valid Git Compose configuration rejected", err)
	}
	for _, change := range []struct{ old, new string }{
		{`"compose_file":"deploy/compose.yaml",`, ""},
		{`deploy/compose.yaml`, `../escape.yaml`}, {`deploy/compose.yaml`, `/compose.yaml`}, {`deploy/compose.yaml`, `.env`},
		{`"public_component":"web"`, `"public_component":"Web"`}, {`"container_port":8080`, `"container_port":0`},
		{`"container_port":8080`, `"container_port":65536`}, {`"strategy":"compose"`, `"strategy":"dockerfile"`},
		{`"container_port":8080`, `"container_port":8080,"dockerfile_path":"Dockerfile"`},
		{`"container_port":8080`, `"container_port":8080,"build_target":"release"`},
		{`"container_port":8080`, `"container_port":8080,"publish_dir":"dist"`},
		{`"container_port":8080`, `"container_port":8080,"spa_fallback":true`},
	} {
		var bad domain.ResourceSourceInput
		err := json.Unmarshal([]byte(strings.Replace(valid, change.old, change.new, 1)), &bad)
		if err == nil && bad.Git != nil && domain.ValidateHostingBuildSettings(bad.Git.Settings) == nil {
			t.Fatalf("invalid settings accepted: %+v", change)
		}
	}
}

func TestHostingBuildSettingsHistoricalJSONUnchanged(t *testing.T) {
	for _, tc := range []struct {
		settings domain.HostingBuildSettings
		raw      string
	}{
		{domain.HostingBuildSettings{Strategy: "dockerfile", ContextDir: "web", DockerfilePath: "Dockerfile"}, `{"strategy":"dockerfile","context_dir":"web","dockerfile_path":"Dockerfile"}`},
		{domain.HostingBuildSettings{Strategy: "railpack"}, `{"strategy":"railpack"}`},
		{domain.HostingBuildSettings{Strategy: "static", PublishDir: "dist", SPAFallback: true}, `{"strategy":"static","publish_dir":"dist","spa_fallback":true}`},
	} {
		actual, err := json.Marshal(tc.settings)
		if err != nil || string(actual) != tc.raw {
			t.Fatalf("historical settings changed: %s %v", actual, err)
		}
		// Exact bytes preserve the previously persisted typed document hash.
		if strings.Contains(string(actual), "compose_file") || strings.Contains(string(actual), "public_component") || strings.Contains(string(actual), "container_port") {
			t.Fatal("new fields entered historical settings")
		}
	}
}
