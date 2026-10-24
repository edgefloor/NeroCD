package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/runnerruntime"
)

type runnerStorageArchiveConfigFile struct {
	Version       int                                `json:"version"`
	ArtifactRoot  string                             `json:"artifact_root"`
	TemporaryRoot string                             `json:"temporary_root"`
	DockerBinary  string                             `json:"docker_binary"`
	Helper        domain.HostingStorageArchiveHelper `json:"helper"`
}

func loadRunnerStorageArchiveConfig(path string, execute bool, complete string) (*runnerruntime.StorageArchiveExecutionConfig, error) {
	if path == "" {
		return nil, nil
	}
	invalid := errors.New("storage archive requires explicit owned mode0600 bounded configuration")
	if !execute || complete != "" {
		return nil, errors.New("storage archive requires execution and forbids generic completion")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.TrimSpace(path) != path {
		return nil, invalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return nil, invalid
	}
	raw, err := readRunnerCredentialFile(path)
	if err != nil {
		return nil, invalid
	}
	var f runnerStorageArchiveConfigFile
	if domain.DecodeHostingStorageArchiveJSON([]byte(raw), &f) != nil || f.Version != 1 || f.Helper.Validate() != nil {
		return nil, invalid
	}
	for _, p := range []string{f.ArtifactRoot, f.TemporaryRoot, f.DockerBinary} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, ",\r\n\x00") {
			return nil, invalid
		}
	}
	if f.ArtifactRoot == f.TemporaryRoot || strings.HasPrefix(f.ArtifactRoot, f.TemporaryRoot+"/") || strings.HasPrefix(f.TemporaryRoot, f.ArtifactRoot+"/") {
		return nil, invalid
	}
	return &runnerruntime.StorageArchiveExecutionConfig{ArtifactRoot: f.ArtifactRoot, TemporaryRoot: f.TemporaryRoot, DockerBinary: f.DockerBinary, Helper: f.Helper}, nil
}

func runnerStorageArchiveCapabilities(enabled, specified bool, capabilities []string) ([]string, error) {
	has := func(name string) bool {
		for _, c := range capabilities {
			if c == name {
				return true
			}
		}
		return false
	}
	if !enabled {
		if has(domain.RunTypeStorageArchive) || has(domain.HostingStorageArchiveCapability) {
			return nil, errors.New("storage archive capabilities require explicit qualified local configuration")
		}
		return capabilities, nil
	}
	if !specified {
		capabilities = []string{domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.HostingOwnedStorageCapability}
	}
	if has("shell") {
		return nil, errors.New("shell and storage archive execution cannot share this runner")
	}
	for _, c := range []string{domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.HostingOwnedStorageCapability} {
		if !has(c) {
			capabilities = append(capabilities, c)
		}
	}
	return capabilities, nil
}
