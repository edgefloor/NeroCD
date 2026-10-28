package hostingbuild

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRegistryCA(t *testing.T, root string) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "registry test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}
func TestRegistryConfigIsolatedAndScrubbed(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "build-test")
	if err := os.MkdirAll(filepath.Join(dir, "docker"), 0700); err != nil {
		t.Fatal(err)
	}
	path := registryConfigPath(dir)
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeBuildRegistryConfig(t.Context(), dir, func(context.Context) (RegistryCredential, error) {
		return RegistryCredential{Registry: "registry.example:5000", Username: "user", Password: "protected-password"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(raw, &config) != nil || len(config.Auths) != 1 || config.Auths["registry.example:5000"].Auth != base64.StdEncoding.EncodeToString([]byte("user:protected-password")) {
		t.Fatal("unexpected credential scope")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe mode", err)
	}
	if err = ScrubRegistryCredentials(state); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("crash-left config retained", err)
	}
	if err = ScrubRegistryCredentials(state); err != nil {
		t.Fatal("scrub not idempotent", err)
	}
}
func TestRegistryCAValidationIdentityAndMounts(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca := testRegistryCA(t, root)
	paths := map[string]string{"registry.example:5000": ca}
	digests, err := RegistryCADigests(paths)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Config{RegistryCACertificates: paths})
	if err != nil || strings.Contains(string(raw), root) || !strings.Contains(string(raw), digests["registry.example:5000"]) {
		t.Fatal("identity leaked path or omitted CA", err)
	}
	contents, err := readRegistryCAs(paths)
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{registryCAs: contents}
	toml, mounts, err := engine.stageRegistryCAs(filepath.Join(root, "build"))
	if !os.IsNotExist(err) {
		t.Fatal("invalid destination admitted", err)
	}
	if err = os.Mkdir(filepath.Join(root, "build"), 0700); err != nil {
		t.Fatal(err)
	}
	toml, mounts, err = engine.stageRegistryCAs(filepath.Join(root, "build"))
	if err != nil || !strings.Contains(toml, `[registry."registry.example:5000"]`) || !strings.Contains(toml, `ca = ["/etc/nerocd-registry-ca/0.pem"]`) || len(mounts) != 2 || !strings.HasSuffix(mounts[1], ",readonly") {
		t.Fatal("CA scope/mount incorrect", toml, mounts, err)
	}
	link := filepath.Join(root, "link.pem")
	if err = os.Symlink(ca, link); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{{"Registry.example": ca}, {"https://registry.example": ca}, {"registry.example": link}, {"registry.example": "relative.pem"}} {
		if _, err = RegistryCADigests(bad); err == nil {
			t.Fatal("unsafe CA accepted")
		}
	}
	if err = os.WriteFile(ca, []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = RegistryCADigests(paths); err == nil {
		t.Fatal("private key accepted")
	}
}
func TestBuildRegistryReferenceOwnedIdentity(t *testing.T) {
	original := settingRequest("/checkout", Dockerfile)
	legacy, _ := requestIdentity(original, Config{}, "")
	original.BuildRegistry = &RegistryReference{SecretID: "secret_build", Version: 1}
	owned := ownedBuildRequest(original)
	first, _ := requestIdentity(owned, Config{}, "")
	original.BuildRegistry.Version = 2
	second, _ := requestIdentity(owned, Config{}, "")
	changed, _ := requestIdentity(original, Config{}, "")
	if first == legacy || first != second || first == changed {
		t.Fatal("registry reference not immutable identity")
	}
	raw, _ := json.Marshal(RegistryCredential{Password: "secret"})
	if string(raw) != "{}" {
		t.Fatal("material serialization")
	}
}
