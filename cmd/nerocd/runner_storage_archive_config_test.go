package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func runnerArchiveFile(t *testing.T) (string, []byte) {
	t.Helper()
	h := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: "sha256:" + strings.Repeat("a", 64), ExecutableSHA256: "sha256:" + strings.Repeat("b", 64), SourceCommit: strings.Repeat("c", 40), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	h.Hash = domain.HostingDocumentHash(h)
	f := runnerStorageArchiveConfigFile{Version: 1, ArtifactRoot: "/srv/nerocd/archives", TemporaryRoot: "/srv/nerocd/archive-tmp", DockerBinary: "/usr/bin/docker", Helper: h}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func TestRunnerStorageArchiveConfigurationIsSeparateAndClosed(t *testing.T) {
	if c, err := loadRunnerStorageArchiveConfig("", true, ""); err != nil || c != nil {
		t.Fatal("default archive enabled")
	}
	path, raw := runnerArchiveFile(t)
	c, err := loadRunnerStorageArchiveConfig(path, true, "")
	if err != nil || c == nil || c.TrustedLoopbackHTTP {
		t.Fatalf("explicit candidate config %v", err)
	}
	for _, tc := range []struct {
		execute  bool
		complete string
	}{{false, ""}, {true, "succeeded"}} {
		if _, err = loadRunnerStorageArchiveConfig(path, tc.execute, tc.complete); err == nil {
			t.Fatal("generic completion admitted archive")
		}
	}
	for name, body := range map[string][]byte{
		"unknown":   append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"trusted_loopback_http":true}`)...),
		"duplicate": append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"version":1}`)...),
		"alias":     append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"Version":1}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRunnerStorageArchiveConfig(path, true, ""); err == nil {
				t.Fatal("open configuration accepted")
			}
		})
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRunnerStorageArchiveConfig(path, true, ""); err == nil {
		t.Fatal("world readable config admitted")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = loadRunnerStorageArchiveConfig(link, true, ""); err == nil {
		t.Fatal("symlink config admitted")
	}
}

func TestRunnerStorageArchiveCapabilityNeverFollowsDatabaseFlag(t *testing.T) {
	for _, caps := range [][]string{{domain.RunTypeStorageArchive}, {domain.HostingStorageArchiveCapability}, {domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeStorageArchive}} {
		if _, err := runnerStorageArchiveCapabilities(false, true, caps); err == nil {
			t.Fatal("archive capability accepted without archive configuration")
		}
	}
	caps, err := runnerStorageArchiveCapabilities(true, false, []string{"shell"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(caps, "shell") || slices.Contains(caps, domain.HostingDatabaseCapability) || !slices.Contains(caps, domain.RunTypeStorageArchive) || !slices.Contains(caps, domain.HostingOwnedStorageCapability) {
		t.Fatalf("capability family %v", caps)
	}
	if _, err = runnerStorageArchiveCapabilities(true, true, []string{"shell"}); err == nil {
		t.Fatal("explicit shell admitted")
	}
	legacy := []string{domain.RunTypeImageDeploy}
	got, err := runnerStorageArchiveCapabilities(false, true, legacy)
	if err != nil || !slices.Equal(got, legacy) {
		t.Fatal("legacy capability changed")
	}
}
