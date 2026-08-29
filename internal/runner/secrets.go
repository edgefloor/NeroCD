package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"nerocd/internal/domain"
)

// SecretClassificationDevelopment marks a development-only environment secret.
const SecretClassificationDevelopment = "development"

var (
	envNamePattern           = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretLogicalPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	secretVersionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	secretFingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	secretAccessIDPattern    = regexp.MustCompile(`^secret_access_[0-9a-f]{32}$`)
)

// SecretAuthorizer verifies access to one secret binding.
type SecretAuthorizer func(context.Context, domain.SecretBinding) error

// PreparedSecrets contains process environment and output redaction material.
type PreparedSecrets struct {
	Environment map[string]string
	Redactor    *Redactor
	Count       int
}

// ValidateSecretBinding verifies binding syntax and provider constraints.
func ValidateSecretBinding(binding domain.SecretBinding) error {
	name := strings.TrimSpace(binding.Name)
	provider := strings.ToLower(strings.TrimSpace(binding.Provider))
	reference := strings.TrimSpace(binding.Reference)
	target := strings.TrimSpace(binding.Target)
	if name == "" || provider == "" || reference == "" || target == "" {
		return fmt.Errorf("secret binding %q requires name, provider, reference, and target", name)
	}
	if !secretLogicalPattern.MatchString(name) || name == "." || name == ".." {
		return errors.New("secret binding name is invalid")
	}
	if _, err := secretEnvTarget(target); err != nil {
		return fmt.Errorf("secret binding %q: %w", name, err)
	}
	switch provider {
	case domain.ProviderRunnerFile:
		if !validSecretLogicalReference(reference) {
			return fmt.Errorf("secret binding %q reference must be a logical runner file id", name)
		}
		if !secretVersionPattern.MatchString(strings.TrimSpace(binding.Version)) {
			return fmt.Errorf("secret binding %q version is required and invalid", name)
		}
	case domain.ProviderEnv:
		if strings.ToLower(strings.TrimSpace(binding.Classification)) != SecretClassificationDevelopment {
			return fmt.Errorf("secret binding %q env provider requires development classification", name)
		}
		if !envNamePattern.MatchString(reference) {
			return fmt.Errorf("secret binding %q reference must be a runner environment variable name", name)
		}
	default:
		return fmt.Errorf("secret binding %q uses unsupported provider %q", name, binding.Provider)
	}
	if fingerprint := strings.TrimSpace(binding.Fingerprint); fingerprint != "" && !secretFingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("secret binding %q fingerprint must use sha256:<64 lowercase hex>", name)
	}
	seenEncoding := map[string]struct{}{}
	for _, raw := range binding.RedactEncodings {
		encoding := strings.ToLower(strings.TrimSpace(raw))
		switch encoding {
		case "base64", "base64url", "hex":
		default:
			return fmt.Errorf("secret binding %q redaction encoding %q is unsupported", name, raw)
		}
		if _, exists := seenEncoding[encoding]; exists {
			return fmt.Errorf("secret binding %q redaction encodings must be unique", name)
		}
		seenEncoding[encoding] = struct{}{}
	}
	return nil
}

// ValidateSecretAccessMetadata verifies recorded secret access metadata.
func ValidateSecretAccessMetadata(accessID, binding, provider, version string) error {
	if !secretAccessIDPattern.MatchString(strings.TrimSpace(accessID)) {
		return errors.New("secret access id is invalid")
	}
	if !secretLogicalPattern.MatchString(strings.TrimSpace(binding)) || binding == "." || binding == ".." {
		return errors.New("secret binding metadata is invalid")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != domain.ProviderRunnerFile && provider != domain.ProviderEnv {
		return errors.New("secret provider metadata is invalid")
	}
	version = strings.TrimSpace(version)
	if provider == domain.ProviderRunnerFile && !secretVersionPattern.MatchString(version) {
		return errors.New("runner_file secret version metadata is invalid")
	}
	if provider == domain.ProviderEnv && version != "" && !secretVersionPattern.MatchString(version) {
		return errors.New("env secret version metadata is invalid")
	}
	return nil
}

// PrepareSecrets authorizes and resolves bindings for a runner process.
func PrepareSecrets(ctx context.Context, bindings []domain.SecretBinding, secretRoot string, authorize SecretAuthorizer) (PreparedSecrets, error) {
	if len(bindings) == 0 {
		return PreparedSecrets{Redactor: NewRedactor(nil)}, nil
	}
	if authorize == nil {
		return PreparedSecrets{}, errors.New("secret access authorizer is required")
	}
	for _, binding := range bindings {
		if err := ValidateSecretBinding(binding); err != nil {
			return PreparedSecrets{}, err
		}
	}
	needsFileRoot := false
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Provider), domain.ProviderRunnerFile) {
			needsFileRoot = true
			break
		}
	}
	var resolver *FileSecretResolver
	if needsFileRoot {
		var err error
		resolver, err = OpenFileSecretResolver(strings.TrimSpace(secretRoot))
		if err != nil {
			return PreparedSecrets{}, fmt.Errorf("open runner secret root: %w", err)
		}
		defer func() { _ = resolver.Close() }()
	}
	environment := make(map[string]string, len(bindings))
	materials := make([]SecretMaterial, 0, len(bindings))
	for _, binding := range bindings {
		if err := ctx.Err(); err != nil {
			return PreparedSecrets{}, err
		}
		if err := authorize(ctx, binding); err != nil {
			return PreparedSecrets{}, fmt.Errorf("authorize secret binding %q: %w", binding.Name, err)
		}
		var value string
		var err error
		switch strings.ToLower(strings.TrimSpace(binding.Provider)) {
		case domain.ProviderRunnerFile:
			value, err = resolver.Read(strings.TrimSpace(binding.Reference))
		case domain.ProviderEnv:
			var ok bool
			value, ok = os.LookupEnv(strings.TrimSpace(binding.Reference))
			if !ok {
				err = os.ErrNotExist
			}
		}
		if err != nil {
			if !binding.Required && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return PreparedSecrets{}, fmt.Errorf("resolve secret binding %q: %w", binding.Name, err)
		}
		targetName, _ := secretEnvTarget(strings.TrimSpace(binding.Target))
		if _, exists := environment[targetName]; exists {
			return PreparedSecrets{}, fmt.Errorf("secret binding %q reuses process environment target", binding.Name)
		}
		environment[targetName] = value
		materials = append(materials, SecretMaterial{Value: value, Encodings: binding.RedactEncodings})
	}
	return PreparedSecrets{Environment: environment, Redactor: NewRedactor(materials), Count: len(materials)}, nil
}

func validSecretLogicalReference(reference string) bool {
	return secretLogicalPattern.MatchString(reference) && reference != "." && reference != ".." && !strings.ContainsAny(reference, `/\`)
}

func secretEnvTarget(target string) (string, error) {
	const prefix = "env:"
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("target must use env:NAME")
	}
	name := strings.TrimSpace(strings.TrimPrefix(target, prefix))
	if !envNamePattern.MatchString(name) {
		return "", errors.New("target must use a valid environment variable name")
	}
	return name, nil
}
