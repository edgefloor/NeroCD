package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"nerocd/internal/domain"
)

func archivePolicyFixture(t *testing.T) (hostingStorageArchivePolicy, []byte) {
	t.Helper()
	h := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: "sha256:" + strings.Repeat("a", 64), ExecutableSHA256: "sha256:" + strings.Repeat("b", 64), SourceCommit: strings.Repeat("c", 40), OS: "linux", Architecture: "arm64", DaemonID: "fixture-daemon"}
	h.Hash = domain.HostingDocumentHash(h)
	p := hostingStorageArchivePolicy{Helper: &h, Limits: &domain.HostingStorageArchiveLimits{ArchiveBytes: 1048576, DataBytes: 524288, FileBytes: 262144, Members: 100, Depth: 8, PathBytes: 255}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, raw
}

func TestHostingStorageArchivePolicyLoadAndStrictness(t *testing.T) {
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", "")
	if p, err := loadHostingStorageArchivePolicy(); p != nil || err != nil {
		t.Fatalf("off policy=%v err=%v", p, err)
	}
	want, valid := archivePolicyFixture(t)
	path := filepath.Join(t.TempDir(), "private-policy-path.json")
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", path)
	write := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(append(append([]byte{}, valid...), []byte(strings.Repeat(" ", 65536-len(valid)))...))
	if got, err := loadHostingStorageArchivePolicy(); err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("valid explicit policy not loaded: %v", err)
	}
	cases := map[string]string{
		"null": "null", "empty": "{}", "missing helper": `{"limits":{}}`, "missing limits": `{"helper":{}}`,
		"duplicate root":   string(valid[:len(valid)-1]) + `,"helper":null}`,
		"duplicate nested": strings.Replace(string(valid), `"mode":"local_image"`, `"mode":"local_image","mode":"local_image"`, 1),
		"case alias":       strings.Replace(string(valid), `"helper":`, `"Helper":`, 1),
		"unknown root":     string(valid[:len(valid)-1]) + `,"secret":"path-and-secret-canary"}`,
		"unknown nested":   strings.Replace(string(valid), `"mode":"local_image"`, `"mode":"local_image","secret":"path-and-secret-canary"`, 1),
		"trailing":         string(valid) + `{}`, "oversized": string(valid) + strings.Repeat(" ", 65537),
		"bad hash": strings.Replace(string(valid), `"hash":"sha256:`, `"hash":"sha257:`, 1),
		"tag":      strings.Replace(string(valid), want.Helper.ImageID, "mutable:tag", 1),
	}
	for _, field := range []string{"archive_bytes", "data_bytes", "file_bytes", "members", "depth", "path_bytes"} {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(valid, &body); err != nil {
			t.Fatal(err)
		}
		var limits map[string]any
		if err := json.Unmarshal(body["limits"], &limits); err != nil {
			t.Fatal(err)
		}
		delete(limits, field)
		encodedLimits, err := json.Marshal(limits)
		if err != nil {
			t.Fatal(err)
		}
		body["limits"] = encodedLimits
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		cases["missing "+field] = string(raw)
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			write([]byte(body))
			got, err := loadHostingStorageArchivePolicy()
			if got != nil || err == nil {
				t.Fatal("invalid policy accepted")
			}
			if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "path-and-secret-canary") {
				t.Fatal("policy error disclosed input")
			}
		})
	}
}

func TestHostingStorageArchivePolicyInvalidValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", path)
	cases := map[string]func(*hostingStorageArchivePolicy){
		"null helper":        func(p *hostingStorageArchivePolicy) { p.Helper = nil },
		"null limits":        func(p *hostingStorageArchivePolicy) { p.Limits = nil },
		"archive minimum":    func(p *hostingStorageArchivePolicy) { p.Limits.ArchiveBytes = 1535 },
		"archive maximum":    func(p *hostingStorageArchivePolicy) { p.Limits.ArchiveBytes = domain.HostingStorageArchiveMaxBytes + 1 },
		"data maximum":       func(p *hostingStorageArchivePolicy) { p.Limits.DataBytes = p.Limits.ArchiveBytes + 1 },
		"file maximum":       func(p *hostingStorageArchivePolicy) { p.Limits.FileBytes = p.Limits.DataBytes + 1 },
		"members maximum":    func(p *hostingStorageArchivePolicy) { p.Limits.Members = 1000001 },
		"depth maximum":      func(p *hostingStorageArchivePolicy) { p.Limits.Depth = 129 },
		"path maximum":       func(p *hostingStorageArchivePolicy) { p.Limits.PathBytes = 256 },
		"wrong platform":     func(p *hostingStorageArchivePolicy) { p.Helper.Architecture = "amd64" },
		"missing daemon":     func(p *hostingStorageArchivePolicy) { p.Helper.DaemonID = "" },
		"missing executable": func(p *hostingStorageArchivePolicy) { p.Helper.ExecutableSHA256 = "" },
		"missing source":     func(p *hostingStorageArchivePolicy) { p.Helper.SourceCommit = "" },
		"wrong recipe":       func(p *hostingStorageArchivePolicy) { p.Helper.RecipeVersion = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := archivePolicyFixture(t)
			mutate(&p)
			if p.Helper != nil {
				p.Helper.Hash = ""
				p.Helper.Hash = domain.HostingDocumentHash(*p.Helper)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadHostingStorageArchivePolicy(); err == nil {
				t.Fatal("invalid values accepted")
			}
		})
	}
}

func TestHostingStorageArchivePolicyRejectsNonRegularAndUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	for _, p := range []string{path, t.TempDir(), " " + path} {
		t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", p)
		if got, err := loadHostingStorageArchivePolicy(); got != nil || err == nil || strings.Contains(err.Error(), path) {
			t.Fatal("unavailable policy accepted or disclosed")
		}
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", path)
	if _, err := loadHostingStorageArchivePolicy(); err == nil {
		t.Fatal("FIFO accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", link)
	if _, err := loadHostingStorageArchivePolicy(); err == nil {
		t.Fatal("symlink accepted")
	}
}

type archiveStartupProbe struct {
	calls        []string
	helper       *domain.HostingStorageArchiveHelper
	limits       domain.HostingStorageArchiveLimits
	configureErr error
	startErr     error
}

func (p *archiveStartupProbe) ConfigureStorageArchive(h *domain.HostingStorageArchiveHelper, l domain.HostingStorageArchiveLimits) error {
	p.calls = append(p.calls, "configure")
	p.helper, p.limits = h, l
	return p.configureErr
}
func (p *archiveStartupProbe) StartHostingExecution(context.Context) (func(context.Context) error, error) {
	p.calls = append(p.calls, "start")
	return func(context.Context) error { p.calls = append(p.calls, "stop"); return nil }, p.startErr
}

func TestHostingStorageArchiveStartupOrdering(t *testing.T) {
	policy, _ := archivePolicyFixture(t)
	for _, enabled := range []bool{false, true} {
		var cfg *hostingStorageArchivePolicy
		if enabled {
			cfg = &policy
		}
		p := &archiveStartupProbe{}
		stop, err := startConfiguredHostingExecution(t.Context(), p, cfg)
		if err != nil || !reflect.DeepEqual(p.calls, []string{"configure", "start"}) {
			t.Fatalf("startup order=%v err=%v", p.calls, err)
		}
		if enabled && (!reflect.DeepEqual(p.helper, policy.Helper) || p.limits != *policy.Limits) {
			t.Fatal("policy changed at startup")
		}
		if !enabled && (p.helper != nil || p.limits != (domain.HostingStorageArchiveLimits{})) {
			t.Fatal("off inferred configuration")
		}
		if err := stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("fixture configuration failed")
	p := &archiveStartupProbe{configureErr: failure}
	if stop, err := startConfiguredHostingExecution(t.Context(), p, &policy); !errors.Is(err, failure) || stop != nil || !reflect.DeepEqual(p.calls, []string{"configure"}) {
		t.Fatal("configuration failure started jobs or lost error")
	}
	p = &archiveStartupProbe{startErr: failure}
	if _, err := startConfiguredHostingExecution(t.Context(), p, &policy); !errors.Is(err, failure) {
		t.Fatal("startup error lost")
	}
	p = &archiveStartupProbe{}
	if stop, err := startConfiguredHostingExecution(t.Context(), p, &hostingStorageArchivePolicy{}); err == nil || stop != nil || len(p.calls) != 0 {
		t.Fatal("invalid direct policy reached startup")
	}
}

func TestHostingStorageArchiveRuntimeConfigRequiresPostgres(t *testing.T) {
	policy, raw := archivePolicyFixture(t)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"NEROCD_REQUIRE_DATABASE", "NEROCD_HOSTING_BUILD_POLICY_FILE", "NEROCD_SERVER_BOOTSTRAP_CONFIG_FILE", "NEROCD_OIDC_ISSUER_URL", "NEROCD_SECRET_KEYRING_FILE", "NEROCD_HOSTING_GIT_ALLOWED_HOSTS"} {
		t.Setenv(key, "")
	}
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_DATABASE_URL", "postgres://fixture:fixture@127.0.0.1:1/unused")
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", path)
	cfg, err := loadRuntimeConfig(":8080")
	if err != nil || !reflect.DeepEqual(cfg.hostingStorageArchivePolicy, &policy) {
		t.Fatalf("runtime policy missing: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"secret":"private-config-canary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntimeConfig(":8080"); err == nil || strings.Contains(err.Error(), "private-config-canary") || strings.Contains(err.Error(), path) {
		t.Fatal("runtime configuration failed open or disclosed invalid policy")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", "")
	if cfg, err := loadRuntimeConfig(":8080"); err != nil || cfg.hostingStorageArchivePolicy != nil {
		t.Fatalf("runtime default enabled archive: %v", err)
	}
	t.Setenv("NEROCD_HOSTING_STORAGE_ARCHIVE_POLICY_FILE", path)
	t.Setenv("NEROCD_DATABASE_URL", "")
	t.Setenv("NEROCD_DEV_MEMORY", "true")
	t.Setenv("NEROCD_DEV_BOOTSTRAP_EMAIL", "fixture@example.invalid")
	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("fixture-password"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE", password)
	if _, err := loadRuntimeConfig(":8080"); err == nil || !strings.Contains(err.Error(), "archive controller requires PostgreSQL") {
		t.Fatalf("memory archive admitted: %v", err)
	}
	if _, _, err := newService(t.Context(), runtimeConfig{developmentMemory: true, hostingStorageArchivePolicy: &policy}); err == nil || !strings.Contains(err.Error(), "archive controller requires PostgreSQL") {
		t.Fatalf("direct memory archive admitted: %v", err)
	}
}
