package hostingbuild

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
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
		// Trusted operator paths still reject symlink components before opening.
		current := string(filepath.Separator)
		for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
			current = filepath.Join(current, part)
			st, err := os.Lstat(current)
			if err != nil || st.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("registry CA path must not contain symlinks")
			}
		}
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() > 256<<10 || before.Size() == 0 {
			return nil, errors.New("invalid registry CA file")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, errors.New("registry CA unavailable")
		}
		after, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, (256<<10)+1))
		closeErr := f.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(before, after) || len(raw) > 256<<10 {
			return nil, errors.New("registry CA changed or exceeds bounds")
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

// MarshalJSON replaces operator CA paths with content digests in immutable engine identity.
func (c Config) MarshalJSON() ([]byte, error) {
	type wire Config
	identity := c.registryCADigests
	if identity == nil && len(c.RegistryCACertificates) > 0 {
		var err error
		identity, err = RegistryCADigests(c.RegistryCACertificates)
		if err != nil {
			return nil, err
		}
	}
	value := wire(c)
	value.RegistryCACertificates = identity
	return json.Marshal(value)
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
