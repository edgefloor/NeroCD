package runner

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"nerocd/internal/domain"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func PrepareSecrets(secrets []domain.SecretBinding, emit func(ProcessEvent)) (map[string]string, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	if emit == nil {
		emit = func(ProcessEvent) {}
	}
	environment := map[string]string{}
	for _, secret := range secrets {
		name := strings.TrimSpace(secret.Name)
		provider := strings.ToLower(strings.TrimSpace(secret.Provider))
		reference := strings.TrimSpace(secret.Reference)
		target := strings.TrimSpace(secret.Target)
		if name == "" || provider == "" || reference == "" || target == "" {
			return nil, fmt.Errorf("secret binding %q requires name, provider, reference, and target", name)
		}
		if provider != domain.ProviderEnv {
			return nil, fmt.Errorf("secret binding %q uses unsupported provider %q", name, secret.Provider)
		}
		targetName, err := secretEnvTarget(target)
		if err != nil {
			return nil, fmt.Errorf("secret binding %q: %w", name, err)
		}
		if !envNamePattern.MatchString(reference) {
			return nil, fmt.Errorf("secret binding %q reference must be a runner environment variable name", name)
		}
		value, ok := os.LookupEnv(reference)
		if !ok {
			return nil, fmt.Errorf("secret binding %q reference is not available in the runner environment", name)
		}
		environment[targetName] = value
		emit(ProcessEvent{Stream: domain.LogSystem, Message: fmt.Sprintf("Prepared secret binding %q for process environment", name)})
	}
	return environment, nil
}

func secretEnvTarget(target string) (string, error) {
	const prefix = "env:"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("target must use env:NAME")
	}
	name := strings.TrimSpace(strings.TrimPrefix(target, prefix))
	if !envNamePattern.MatchString(name) {
		return "", fmt.Errorf("target must use a valid environment variable name")
	}
	return name, nil
}
