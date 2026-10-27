//go:build linux && nerocd_proxy_fixture

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingfixture"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/source"
	"nerocd/internal/store"
)

// This adapter supplies only the operator-owned disposable source network
// policy. Resource validation, grants, build receipts and admission remain real.
type gitComposeRuntimeHosting struct{ *store.PostgresStore }

func (h gitComposeRuntimeHosting) CreateResource(ctx context.Context, r domain.Resource, opts ...store.MutationOption) (domain.Resource, error) {
	if r.Source.Git != nil {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: gitComposeRuntimePolicy()}))
	}
	return h.PostgresStore.CreateResource(ctx, r, opts...)
}
func (h gitComposeRuntimeHosting) UpdateResource(ctx context.Context, r domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	previous, err := h.GetResource(ctx, r.ID)
	if err != nil {
		return domain.Resource{}, err
	}
	if r.Source.Git != nil && domain.HostingDocumentHash(previous.Source) != domain.HostingDocumentHash(r.Source) {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: gitComposeRuntimePolicy()}))
	}
	return h.PostgresStore.UpdateResource(ctx, r, expected, opts...)
}
func gitComposeRuntimePolicy() source.RepositoryPolicy {
	return source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}, AllowedCIDRs: []string{"127.0.0.0/8"}}
}
func configureGitComposeRuntime(t *testing.T, f *storageRuntimeFixture, pg *store.PostgresStore) {
	t.Helper()
	sources := filepath.Join(f.root, "sources")
	if err := os.Mkdir(sources, 0700); err != nil {
		t.Fatal(err)
	}
	engine := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE"), StateDir: filepath.Join(f.root, "build-state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}
	identity, err := runnerruntime.ComputeBuildToolchainIdentity(t.Context(), engine, "linux/"+runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.HostingBuildPolicy{Identity: "sha256:" + strings.Repeat("a", 64), ToolchainIdentity: identity, Protocol: domain.HostingBuildProtocol, Platform: "linux/" + runtime.GOARCH, GitCompose: true, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 1 << 30, Deadline: 4 * time.Minute, ContextBytes: 8 << 20, ContextFiles: 1000, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 512 << 20, UnpackedBytes: 1 << 30, UsageBytes: 3 << 30, ReserveBytes: 256 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 8 << 20, MaxWorkspaceBytes: 32 << 20, MinFreeBytes: 256 << 20, MaxEntries: 1000, MaxDepth: 32, PollInterval: 100 * time.Millisecond}}
	if err = pg.ConfigureHostingBuild(&policy); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"version": 1, "compose": true, "private_git": runnerruntime.PrivateGitConfig{}, "source_root": sources, "engine": map[string]string{"docker_binary": engine.DockerBinary, "socket": engine.Socket, "buildx_binary": engine.BuildxBinary, "buildx_sha256": engine.BuildxSHA256, "planner_image": engine.PlannerImage, "state_dir": engine.StateDir, "cgroup_root": engine.CgroupRoot}, "target": map[string]string{"runner_id": "runner_storage_runtime_fixture", "server_id": f.serverID, "destination_id": f.destinationID}, "policy": policy, "status_interval_ns": 200 * time.Millisecond, "request_timeout_ns": 5 * time.Second}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.buildConfig = filepath.Join(f.root, "build.json")
	if err = os.WriteFile(f.buildConfig, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func gitComposeRuntimeRepository(t *testing.T, raw, base, password string) (string, func(string, bool) string, func(bool), string) {
	t.Helper()
	files := func(marker string, fail bool) map[string]string {
		return gitComposeFixtureFiles(raw, base, marker, fail)
	}
	url, update, unavailable := hostingfixture.GitHTTPS(t, files("A", false), "fixture-user", password)
	return strings.Replace(url, "127.0.0.1", "example.com", 1), func(marker string, fail bool) string { return update(files(marker, fail)) }, unavailable, update(nil)
}

func gitComposeRuntimeInstall(f *storageRuntimeFixture, resource domain.Resource) {
	t := f.t
	t.Helper()
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resource.ID+"/binding", map[string]string{"destination_id": f.destinationID}, 201)
	entries := []domain.HostingRuntimeEntry{}
	for name, value := range map[string]string{"SERVICE_URL_VIKUNJA": "https://catalog-runtime.example.test:25443", "SERVICE_USER_POSTGRESQL": "vikunja", "POSTGRESQL_DATABASE": "vikunja"} {
		v := value
		entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "literal", Value: &v})
	}
	for _, name := range []string{"SERVICE_PASSWORD_POSTGRESQL", "SERVICE_PASSWORD_JWTSECRET"} {
		secret := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", map[string]string{"project_id": resource.ProjectID, "name": "Git Compose " + name, "value": hostingStorageRemoteRuntimeRandom(t)}, 201)
		entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: secret.ID, Version: 1}})
	}
	current := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resource.ID+"/runtime-configuration", nil, 200)
	storageRuntimeJSON[json.RawMessage](f, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: current.Configuration.RuntimeGeneration, RequestID: strings.Repeat("c", 32), Entries: entries}, 200)
}
func gitComposeRuntimeBuild(f *storageRuntimeFixture, resource domain.Resource, grant, key, want string) domain.PublicHostingBuild {
	f.t.Helper()
	result := storageRuntimeJSON[struct {
		Build domain.PublicHostingBuild `json:"build"`
	}](f, "POST", "/api/v1/resources/"+resource.ID+"/builds", domain.HostingBuildRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, GrantID: grant, IdempotencyKey: key}, 202)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		result = storageRuntimeJSON[struct {
			Build domain.PublicHostingBuild `json:"build"`
		}](f, "GET", "/api/v1/hosting-builds/"+result.Build.ID, nil, 200)
		if result.Build.State == want {
			return result.Build
		}
		if result.Build.State == "failed" || result.Build.State == "succeeded" || result.Build.State == "canceled" {
			f.t.Fatal("Git Compose build settled unexpectedly")
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("Git Compose build did not settle")
	return domain.PublicHostingBuild{}
}
