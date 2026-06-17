package runner

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestPrepareSecretsMapsEnvProviderToTarget(t *testing.T) {
	t.Setenv("RUNNER_SOURCE_SECRET", "super-secret")
	var events []ProcessEvent
	env, err := PrepareSecrets([]domain.SecretBinding{
		{Name: "api-token", Provider: "env", Reference: "RUNNER_SOURCE_SECRET", Target: "env:API_TOKEN"},
	}, func(event ProcessEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["API_TOKEN"] != "super-secret" {
		t.Fatalf("secret was not mapped to target env: %#v", env)
	}
	joined := artifactEvents(events)
	if !strings.Contains(joined, `Prepared secret binding "api-token"`) {
		t.Fatalf("expected secret preparation event, got:\n%s", joined)
	}
	if strings.Contains(joined, "super-secret") || strings.Contains(joined, "RUNNER_SOURCE_SECRET") || strings.Contains(joined, "API_TOKEN") {
		t.Fatalf("secret event leaked value, reference, or target:\n%s", joined)
	}
}

func TestPrepareSecretsRejectsUnsupportedProvider(t *testing.T) {
	_, err := PrepareSecrets([]domain.SecretBinding{
		{Name: "db-token", Provider: "database", Reference: "sec_token", Target: "env:TOKEN"},
	}, func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestPrepareSecretsRejectsUnsafeTarget(t *testing.T) {
	t.Setenv("RUNNER_SOURCE_SECRET", "super-secret")
	_, err := PrepareSecrets([]domain.SecretBinding{
		{Name: "bad", Provider: "env", Reference: "RUNNER_SOURCE_SECRET", Target: "file:/tmp/token"},
	}, func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsafe target error")
	}
}

func TestPrepareSecretsRequiresAvailableReference(t *testing.T) {
	_, err := PrepareSecrets([]domain.SecretBinding{
		{Name: "missing", Provider: "env", Reference: "MISSING_RUNNER_SECRET", Target: "env:TOKEN"},
	}, func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected missing environment reference error")
	}
}
