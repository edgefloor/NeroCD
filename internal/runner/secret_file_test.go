//go:build linux || darwin

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestFileSecretResolverSecurityAndRotation(t *testing.T) {
	root := filepath.Join(physicalTempDir(t), "secrets")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecret := func(name, value string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeSecret("service-token", "version-one\n", 0o400)
	resolver, err := OpenFileSecretResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	if value, err := resolver.Read("service-token"); err != nil || value != "version-one" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	temporary := filepath.Join(root, ".rotation")
	if err := os.WriteFile(temporary, []byte("version-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, filepath.Join(root, "service-token")); err != nil {
		t.Fatal(err)
	}
	if value, err := resolver.Read("service-token"); err != nil || value != "version-two" {
		t.Fatalf("rotated value=%q err=%v", value, err)
	}
}

func TestFileSecretResolverRejectsUnsafeFilesAndReferences(t *testing.T) {
	root := filepath.Join(physicalTempDir(t), "secrets")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, contents []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), contents, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("wide", []byte("secret"), 0o644)
	write("empty", nil, 0o600)
	write("nul", []byte("unsafe\x00value"), 0o600)
	write("control", []byte("unsafe\ninside"), 0o600)
	write("oversize", []byte(strings.Repeat("x", runnerSecretMaxBytes+1)), 0o600)
	write("target", []byte("secret"), 0o600)
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	resolver, err := OpenFileSecretResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	for _, reference := range []string{"../target", "nested/target", `nested\target`, ".", "..", "wide", "empty", "nul", "control", "oversize", "link"} {
		if _, err := resolver.Read(reference); err == nil {
			t.Fatalf("reference %q unexpectedly succeeded", reference)
		}
	}
}

func TestFileSecretResolverRejectsUnsafeRoot(t *testing.T) {
	parent := physicalTempDir(t)
	wide := filepath.Join(parent, "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSecretResolver(wide); err == nil {
		t.Fatal("accepted permissive root")
	}
	secure := filepath.Join(parent, "secure")
	if err := os.Mkdir(secure, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(secure, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSecretResolver(link); err == nil {
		t.Fatal("accepted symlink root")
	}
}

func TestFileSecretResolverRejectsWrongOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("wrong-owner setup requires root; exercised in supported Linux container")
	}
	root := filepath.Join(physicalTempDir(t), "secrets")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "owned-elsewhere")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := OpenFileSecretResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	if err := os.Chown(secret, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Read("owned-elsewhere"); err == nil {
		t.Fatal("accepted wrong-owner secret")
	}
}

func TestPrepareRunnerFileSecretAuthorizesBeforeRead(t *testing.T) {
	root := filepath.Join(physicalTempDir(t), "secrets")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(root, "logical-id")
	if err := os.WriteFile(secretPath, []byte("secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorized := false
	prepared, err := PrepareSecrets(t.Context(), []domain.SecretBinding{{
		Name: "binding", Provider: domain.ProviderRunnerFile, Reference: "logical-id", Target: "env:TOKEN", Required: true, Version: "v1",
	}}, root, func(context.Context, domain.SecretBinding) error {
		authorized = true
		return os.Remove(secretPath)
	})
	if err == nil || !authorized || prepared.Count != 0 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared=%#v authorized=%v err=%v", prepared, authorized, err)
	}
}

func physicalTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
