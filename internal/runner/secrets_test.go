package runner

import (
	"context"
	"testing"

	"nerocd/internal/domain"
)

func TestPrepareSecretsMapsAuthorizedDevelopmentEnvProvider(t *testing.T) {
	t.Setenv("RUNNER_SOURCE_SECRET", "super-secret")
	authorized := 0
	prepared, err := PrepareSecrets(t.Context(), []domain.SecretBinding{{
		Name: "api-token", Provider: domain.ProviderEnv, Reference: "RUNNER_SOURCE_SECRET", Target: "env:API_TOKEN",
		Required: true, Classification: SecretClassificationDevelopment, RedactEncodings: []string{"base64"},
	}}, "", func(context.Context, domain.SecretBinding) error {
		authorized++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized != 1 || prepared.Environment["API_TOKEN"] != "super-secret" || prepared.Count != 1 {
		t.Fatalf("prepared=%#v authorized=%d", prepared, authorized)
	}
	if got := prepared.Redactor.Redact("value=super-secret"); got != "value="+RedactionMarker {
		t.Fatalf("redacted=%q", got)
	}
}

func TestPrepareSecretsRejectsUnsupportedOrUnclassifiedProvider(t *testing.T) {
	for _, binding := range []domain.SecretBinding{
		{Name: "db-token", Provider: "database", Reference: "sec_token", Target: "env:TOKEN", Required: true},
		{Name: "env-token", Provider: domain.ProviderEnv, Reference: "RUNNER_SOURCE_SECRET", Target: "env:TOKEN", Required: true},
	} {
		if _, err := PrepareSecrets(t.Context(), []domain.SecretBinding{binding}, "", func(context.Context, domain.SecretBinding) error { return nil }); err == nil {
			t.Fatalf("expected provider rejection for %#v", binding)
		}
	}
}

func TestPrepareSecretsRejectsUnsafeTargetBeforeAuthorization(t *testing.T) {
	authorized := false
	_, err := PrepareSecrets(t.Context(), []domain.SecretBinding{{
		Name: "bad", Provider: domain.ProviderEnv, Reference: "RUNNER_SOURCE_SECRET", Target: "file:/tmp/token",
		Required: true, Classification: SecretClassificationDevelopment,
	}}, "", func(context.Context, domain.SecretBinding) error { authorized = true; return nil })
	if err == nil || authorized {
		t.Fatalf("err=%v authorized=%v", err, authorized)
	}
}

func TestPrepareSecretsAuthorizesBeforeMissingRequiredRead(t *testing.T) {
	authorized := false
	_, err := PrepareSecrets(t.Context(), []domain.SecretBinding{{
		Name: "missing", Provider: domain.ProviderEnv, Reference: "MISSING_RUNNER_SECRET", Target: "env:TOKEN",
		Required: true, Classification: SecretClassificationDevelopment,
	}}, "", func(context.Context, domain.SecretBinding) error { authorized = true; return nil })
	if err == nil || !authorized {
		t.Fatalf("err=%v authorized=%v", err, authorized)
	}
}
