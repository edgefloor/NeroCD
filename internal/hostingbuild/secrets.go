package hostingbuild

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
)

// SecretReference binds a RUN environment name to one immutable managed version.
// Protected values never enter the request or its retained identity.
type SecretReference struct {
	Name     string
	SecretID string
	Version  int64
}

// SecretValue is ephemeral protected build material.
type SecretValue struct{ Name, Value string }

// Format redacts protected material for every formatting verb.
func (SecretValue) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[redacted build secret]") }

// MarshalJSON prevents accidental material serialization.
func (SecretValue) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// SecretProvider authorizes fresh managed values for the current build attempt.
type SecretProvider func(context.Context) ([]SecretValue, error)

// BuildMaterialProviders share the existing fresh-only material delivery boundary.
type BuildMaterialProviders struct {
	Registry RegistryCredentialProvider
	Secrets  SecretProvider
}

func validateBuildSecretReferences(r Request) error {
	if len(r.BuildSecrets) > 64 || len(r.BuildSecrets) > 0 && r.Strategy != Dockerfile {
		return errors.New("build secrets require bounded Dockerfile strategy")
	}
	settings := domain.HostingBuildSettings{Strategy: "dockerfile"}
	for _, ref := range r.BuildSecrets {
		if _, exists := r.BuildArgs[ref.Name]; exists {
			return errors.New("public argument collides with build secret")
		}
		settings.BuildSecrets = append(settings.BuildSecrets, domain.HostingBuildSecretReference{Name: ref.Name, Secret: domain.HostingRegistryCredentialRef{SecretID: ref.SecretID, Version: ref.Version}})
	}
	return domain.ValidateHostingBuildSettings(settings)
}

func writeBuildSecrets(ctx context.Context, dir string, refs []SecretReference, provider SecretProvider) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	values, err := provider(ctx)
	defer clear(values)
	if err != nil {
		return errors.Join(errors.New("build secrets unavailable"), ctx.Err())
	}
	if len(values) != len(refs) || len(values) == 0 || len(values) > 64 {
		return errors.New("build secret selection changed")
	}
	selected := map[string]bool{}
	for _, ref := range refs {
		selected[ref.Name] = true
	}
	for _, value := range values {
		if !selected[value.Name] || !domain.ValidHostingBuildSecretValue(value.Value) {
			return errors.New("invalid build secret material")
		}
		delete(selected, value.Name)
	}
	if len(selected) != 0 || ctx.Err() != nil {
		return errors.Join(errors.New("build secret selection changed"), ctx.Err())
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// Exclusive mkdir and O_EXCL files create owner-only material beneath the
	// already locked engine directory. Existing paths are never followed or reused.
	if err = root.Mkdir("build-secrets", 0700); err != nil {
		return err
	}
	secrets, err := root.OpenRoot("build-secrets")
	if err != nil {
		return err
	}
	defer func() { _ = secrets.Close() }()
	for _, value := range values {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, err := secrets.OpenFile(value.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(f, strings.NewReader(value.Value))
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(writeErr, syncErr, closeErr, ctx.Err()); err != nil {
			return err
		}
	}
	return errors.Join(syncDir(filepath.Join(dir, "build-secrets")), syncDir(dir), ctx.Err())
}

func removeBuildSecrets(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("build-secrets")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("unsafe build secret directory")
	}
	secrets, err := root.OpenRoot("build-secrets")
	if err != nil {
		return err
	}
	defer func() { _ = secrets.Close() }()
	entries, err := fs.ReadDir(secrets.FS(), ".")
	if err != nil || len(entries) > 64 {
		return errors.New("unsafe build secret entries")
	}
	for _, entry := range entries {
		info, err := secrets.Lstat(entry.Name())
		if err != nil || !buildArgNamePattern.MatchString(entry.Name()) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("unsafe retained build secret")
		}
		if err = secrets.Remove(entry.Name()); err != nil {
			return err
		}
	}
	if err = syncDir(filepath.Join(dir, "build-secrets")); err != nil {
		return err
	}
	if err = root.Remove("build-secrets"); err != nil {
		return err
	}
	return syncDir(dir)
}

// buildSecretArgument preserves source paths as CSV data even when the operator's
// state path contains commas or quotes. No value enters the Buildx argument.
func buildSecretArgument(name, path string) string {
	var b bytes.Buffer
	writer := csv.NewWriter(&b)
	// bytes.Buffer writes cannot fail, and both fields are ordinary CSV strings.
	_ = writer.Write([]string{"id=" + name, "src=" + path})
	writer.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}
