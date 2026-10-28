package hostingbuild

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func readRegistryCAs(paths map[string]string) (map[string][]byte, error) {
	if len(paths) > 8 {
		return nil, errors.New("too many registry CA certificates")
	}
	result := map[string][]byte{}
	for host, path := range paths {
		if !canonicalRegistry(host) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("invalid registry CA configuration")
		}
		raw, err := readRegistryCAFile(path)
		if err != nil {
			return nil, err
		}
		rest := bytes.TrimSpace(raw)
		count := 0
		for len(rest) > 0 {
			if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
				return nil, errors.New("registry CA must contain only PEM certificates")
			}
			block, left := pem.Decode(rest)
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return nil, errors.New("invalid registry CA PEM")
			}
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
				return nil, errors.New("registry trust requires CA certificates")
			}
			rest = bytes.TrimSpace(left)
			count++
		}
		if count == 0 {
			return nil, errors.New("empty registry CA bundle")
		}
		result[host] = bytes.Clone(raw)
	}
	return result, nil
}

// RegistryCADigests validates trusted operator files and returns path-free identity.
func RegistryCADigests(paths map[string]string) (map[string]string, error) {
	contents, err := readRegistryCAs(paths)
	if err != nil {
		return nil, err
	}
	return registryCADigests(contents), nil
}
func registryCADigests(contents map[string][]byte) map[string]string {
	if len(contents) == 0 {
		return nil
	}
	result := map[string]string{}
	for host, raw := range contents {
		result[host] = sum(raw)
	}
	return result
}

// registryIdentityConfig substitutes CA digests only for request identity.
// Operational Config JSON retains its original operator paths.
func registryIdentityConfig(c Config) (Config, error) {
	identity := c.registryCADigests
	if identity == nil && len(c.RegistryCACertificates) > 0 {
		var err error
		identity, err = RegistryCADigests(c.RegistryCACertificates)
		if err != nil {
			return Config{}, err
		}
	}
	c.RegistryCACertificates = identity
	return c, nil
}
func (e *Engine) stageRegistryCAs(dir string) (string, []string, error) {
	var hosts []string
	for host := range e.registryCAs {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	if len(hosts) == 0 {
		return "", nil, nil
	}
	caDir := filepath.Join(dir, "registry-ca")
	if err := os.Mkdir(caDir, 0700); err != nil {
		return "", nil, err
	}
	var toml strings.Builder
	var mounts []string
	for i, host := range hosts {
		file := strconv.Itoa(i) + ".pem"
		local := filepath.Join(caDir, file)
		container := "/etc/nerocd-registry-ca/" + file
		if err := os.WriteFile(local, e.registryCAs[host], 0600); err != nil {
			return "", nil, err
		}
		toml.WriteString("\n[registry." + strconv.Quote(host) + "]\n  ca = [" + strconv.Quote(container) + "]\n")
		mounts = append(mounts, "--mount", "type=bind,src="+local+",dst="+container+",readonly")
	}
	return toml.String(), mounts, nil
}

// Each opened directory is compared to its no-follow observation before walking
// the next component. The final file is read through that same verified descriptor.
func readRegistryCAFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errors.New("registry CA root unavailable")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		before, e := root.Lstat(part)
		if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, errors.New("registry CA path must not contain symlinks")
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			_ = root.Close()
			return nil, errors.New("registry CA directory unavailable")
		}
		after, e := next.Stat(".")
		_ = root.Close()
		if e != nil || !os.SameFile(before, after) {
			_ = next.Close()
			return nil, errors.New("registry CA directory changed")
		}
		root = next
	}
	defer func() { _ = root.Close() }()
	name := parts[len(parts)-1]
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() > 256<<10 || before.Size() == 0 {
		return nil, errors.New("invalid registry CA file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, errors.New("registry CA unavailable")
	}
	after, statErr := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	closeErr := f.Close()
	if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(before, after) || len(raw) > 256<<10 {
		return nil, errors.New("registry CA changed or exceeds bounds")
	}
	return raw, nil
}
