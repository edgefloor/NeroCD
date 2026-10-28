package hostingbuild

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/distribution/reference"
)

// RegistryReference is nonsecret immutable input. Material never enters Request.
type RegistryReference struct {
	SecretID string
	Version  int64
}

// RegistryCredential is ephemeral authentication supplied only for a fresh build.
type RegistryCredential struct{ Registry, Username, Password string }

// Format redacts credential material for every formatting verb.
func (RegistryCredential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted build registry credential]")
}

// MarshalJSON prevents accidental protected serialization.
func (RegistryCredential) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Close releases references to credential material. Go cannot guarantee string erasure.
func (c *RegistryCredential) Close() { *c = RegistryCredential{} }

// RegistryCredentialProvider authorizes fresh material under the caller's attempt.
type RegistryCredentialProvider func(context.Context) (RegistryCredential, error)

func canonicalRegistry(host string) bool {
	if host == "" || len(host) > 255 || host != strings.ToLower(host) || strings.ContainsAny(host, "/@?#\\") {
		return false
	}
	n, err := reference.ParseNormalizedNamed(host + "/nerocd/probe:v1")
	return err == nil && reference.Domain(n) == host && !strings.ContainsFunc(host, unicode.IsControl)
}
func writeBuildRegistryConfig(ctx context.Context, dir string, provider RegistryCredentialProvider) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	credential, err := provider(ctx)
	if err != nil {
		return errors.New("build registry credential unavailable")
	}
	defer credential.Close()
	if !canonicalRegistry(credential.Registry) || credential.Username == "" || len(credential.Username) > 255 || strings.Contains(credential.Username, ":") || credential.Password == "" || len(credential.Password) > 20000 || strings.ContainsFunc(credential.Username+credential.Password, unicode.IsControl) || ctx.Err() != nil {
		return errors.New("invalid build registry credential")
	}
	raw, err := json.Marshal(struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}{map[string]struct {
		Auth string `json:"auth"`
	}{credential.Registry: {Auth: base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))}}})
	defer clear(raw)
	if err != nil {
		return errors.New("invalid build registry config")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat("docker/config.json")
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("unsafe build registry config")
	}
	f, err := root.OpenFile("docker/config.json", os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	after, statErr := f.Stat()
	if statErr != nil || !os.SameFile(before, after) {
		_ = f.Close()
		return errors.New("build registry config changed")
	}
	if err = f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, ctx.Err())
}
func removeBuildRegistryConfig(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	err = root.Remove("docker/config.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Join(dir, "docker"))
}

// ScrubRegistryCredentials removes only fixed credential files from private engine
// build directories. It runs under the engine lock before replay or new effects.
func ScrubRegistryCredentials(state string) error {
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := os.ReadDir(state)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !idPattern.MatchString(entry.Name()) || !entry.IsDir() {
			continue
		}
		build, err := root.OpenRoot(entry.Name())
		if err != nil {
			return ErrCleanupUnconfirmed
		}
		docker, err := build.OpenRoot("docker")
		if os.IsNotExist(err) {
			_ = build.Close()
			continue
		}
		if err != nil {
			_ = build.Close()
			return ErrCleanupUnconfirmed
		}
		err = docker.Remove("config.json")
		if err == nil {
			err = syncDir(filepath.Join(state, entry.Name(), "docker"))
		}
		_ = docker.Close()
		if caErr := build.RemoveAll("registry-ca"); caErr != nil && !os.IsNotExist(caErr) {
			_ = docker.Close()
			_ = build.Close()
			return ErrCleanupUnconfirmed
		}
		_ = build.Close()
		if err != nil && !os.IsNotExist(err) {
			return ErrCleanupUnconfirmed
		}
	}
	return nil
}

func registryConfigPath(dir string) string { return filepath.Join(dir, "docker", "config.json") }

// ScrubRegistryCredentials removes crash-left credential files under exclusive engine admission.
func (e *Engine) ScrubRegistryCredentials() error {
	host, err := acquireHostLock()
	if err != nil {
		return err
	}
	defer func() { _ = host.Close() }()
	lock, err := acquireLock(e.config.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return ScrubRegistryCredentials(e.config.StateDir)
}
