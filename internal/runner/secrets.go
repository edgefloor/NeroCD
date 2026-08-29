package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// PreparedComposeSecrets is an attempt-local Compose override. Its descriptor
// names validated operator-managed runner_file sources; Cleanup removes only
// generated override metadata and never copies or deletes source values.
type PreparedComposeSecrets struct {
	OverridePath      string
	DescriptorSources map[string]string
	Redactor          *Redactor
	Count             int
	Cleanup           func()
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
	if _, err := secretTarget(target); err != nil {
		return fmt.Errorf("secret binding %q: %w", name, err)
	}
	if strings.HasPrefix(target, "file:") && !binding.Required {
		return fmt.Errorf("secret binding %q compose file targets must be required", name)
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

// PrepareComposeSecrets authorizes runner_file bindings, validates each source,
// and writes a generated attempt-local Compose descriptor that refers directly
// to the validated operator-managed source. It never returns secret values or
// user-controlled filenames; cleanup removes only the generated metadata.
func PrepareComposeSecrets(ctx context.Context, bindings []domain.SecretBinding, secretRoot, workspace string, authorize SecretAuthorizer) (PreparedComposeSecrets, error) {
	if len(bindings) == 0 {
		return PreparedComposeSecrets{Redactor: NewRedactor(nil), Cleanup: func() {}}, nil
	}
	if authorize == nil {
		return PreparedComposeSecrets{}, errors.New("secret access authorizer is required")
	}
	if strings.TrimSpace(workspace) == "" {
		return PreparedComposeSecrets{}, errors.New("compose secret workspace is required")
	}
	for _, binding := range bindings {
		if err := ValidateSecretBinding(binding); err != nil {
			return PreparedComposeSecrets{}, err
		}
		if _, err := composeSecretTarget(binding.Target); err != nil {
			return PreparedComposeSecrets{}, fmt.Errorf("secret binding %q: %w", binding.Name, err)
		}
		if !strings.EqualFold(strings.TrimSpace(binding.Provider), domain.ProviderRunnerFile) {
			return PreparedComposeSecrets{}, fmt.Errorf("secret binding %q uses an unsupported compose provider", binding.Name)
		}
	}
	resolver, err := OpenFileSecretResolver(strings.TrimSpace(secretRoot))
	if err != nil {
		return PreparedComposeSecrets{}, fmt.Errorf("open runner secret root: %w", err)
	}
	defer func() { _ = resolver.Close() }()

	directory, err := os.MkdirTemp(workspace, ".nerocd-compose-secrets-")
	if err != nil {
		return PreparedComposeSecrets{}, fmt.Errorf("create compose secret directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0700); err != nil {
		cleanup()
		return PreparedComposeSecrets{}, fmt.Errorf("secure compose secret directory: %w", err)
	}
	seen := make(map[string]struct{}, len(bindings))
	materials := make([]SecretMaterial, 0, len(bindings))
	entries := make([]composeSecretEntry, 0, len(bindings))
	for _, binding := range bindings {
		if err := ctx.Err(); err != nil {
			cleanup()
			return PreparedComposeSecrets{}, err
		}
		name, _ := composeSecretTarget(binding.Target)
		if _, exists := seen[name]; exists {
			cleanup()
			return PreparedComposeSecrets{}, fmt.Errorf("secret binding %q reuses compose secret target", binding.Name)
		}
		seen[name] = struct{}{}
		if err := authorize(ctx, binding); err != nil {
			cleanup()
			return PreparedComposeSecrets{}, fmt.Errorf("authorize secret binding %q: %w", binding.Name, err)
		}
		value, readErr := resolver.ReadBytes(strings.TrimSpace(binding.Reference))
		if readErr != nil {
			if !binding.Required && errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			cleanup()
			return PreparedComposeSecrets{}, fmt.Errorf("resolve secret binding %q: %w", binding.Name, readErr)
		}
		source, sourceErr := resolver.CanonicalSourcePath(strings.TrimSpace(binding.Reference))
		if sourceErr != nil {
			cleanup()
			return PreparedComposeSecrets{}, fmt.Errorf("canonicalize secret binding %q source: %w", binding.Name, sourceErr)
		}
		entries = append(entries, composeSecretEntry{Name: name, Path: source})
		materials = append(materials, SecretMaterial{Value: string(value), Encodings: binding.RedactEncodings})
	}
	overridePath := filepath.Join(directory, "compose-secrets.yaml")
	if err := writeComposeSecretOverride(overridePath, entries); err != nil {
		cleanup()
		return PreparedComposeSecrets{}, err
	}
	sources := make(map[string]string, len(entries))
	for _, entry := range entries {
		sources[entry.Name] = entry.Path
	}
	return PreparedComposeSecrets{OverridePath: overridePath, DescriptorSources: sources, Redactor: NewRedactor(materials), Count: len(materials), Cleanup: cleanup}, nil
}

type composeSecretEntry struct{ Name, Path string }

func writeComposeSecretOverride(path string, entries []composeSecretEntry) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create compose secret override: %w", err)
	}
	if _, err := file.WriteString("secrets:\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write compose secret override: %w", err)
	}
	for _, entry := range entries {
		if _, err := fmt.Fprintf(file, "  %q:\n    file: %q\n", entry.Name, entry.Path); err != nil {
			_ = file.Close()
			return fmt.Errorf("write compose secret override: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close compose secret override: %w", err)
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

func secretTarget(target string) (string, error) {
	if value, err := secretEnvTarget(target); err == nil {
		return value, nil
	}
	return composeSecretTarget(target)
}

func composeSecretTarget(target string) (string, error) {
	const prefix = "file:"
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("target must use env:NAME or file:NAME")
	}
	name := strings.TrimSpace(strings.TrimPrefix(target, prefix))
	if !secretLogicalPattern.MatchString(name) || name == "." || name == ".." {
		return "", errors.New("target must use a valid Compose secret name")
	}
	return name, nil
}
