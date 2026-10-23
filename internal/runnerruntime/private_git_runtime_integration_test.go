//go:build linux && nerocd_proxy_fixture

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/store"
)

// Source edits need the same operator-owned loopback policy as creation in this
// disposable fixture. Production public HTTPS policy remains unchanged. The
// service passes the current generation; the repository advances it after
// comparing configuration and still owns CAS, source revisions and audits.
func (h controllerFixtureHosting) UpdateResource(ctx context.Context, resource domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	previous, err := h.GetResource(ctx, resource.ID)
	if err != nil {
		return domain.Resource{}, err
	}
	if resource.Source.Kind == "git" && resource.Source.Git != nil && previous.Source.Git != nil && resource.Source.Git.SourceID == previous.Source.Git.SourceID && domain.HostingDocumentHash(resource.Source.Git.GitResourceConfiguration) != domain.HostingDocumentHash(previous.Source.Git.GitResourceConfiguration) {
		g := resource.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: resource.ID, ProjectID: resource.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: h.policy}))
	}
	return h.HostingRepository.UpdateResource(ctx, resource, expected, opts...)
}

// Preserve the actual repository's runtime-configuration interface through the
// fixture source-policy adapter. No document or authorization result is invented.
func (h controllerFixtureHosting) GetHostingRuntimeConfiguration(ctx context.Context, actor, resource string) (domain.HostingRuntimeConfiguration, error) {
	repository, ok := h.HostingRepository.(store.HostingRuntimeConfigurationRepository)
	if !ok {
		return domain.HostingRuntimeConfiguration{}, store.ErrHostingRuntimeConfigurationUnavailable
	}
	return repository.GetHostingRuntimeConfiguration(ctx, actor, resource)
}
func (h controllerFixtureHosting) PutHostingRuntimeConfiguration(ctx context.Context, actor, resource string, input domain.HostingRuntimeConfigurationInput, audit domain.AuditEvent) (domain.HostingRuntimeConfiguration, error) {
	repository, ok := h.HostingRepository.(store.HostingRuntimeConfigurationRepository)
	if !ok {
		return domain.HostingRuntimeConfiguration{}, store.ErrHostingRuntimeConfigurationUnavailable
	}
	return repository.PutHostingRuntimeConfiguration(ctx, actor, resource, input, audit)
}

// The embedded interface keeps this fake bounded to the two methods exercised.
type privateFixtureResourceRepository struct {
	store.HostingRepository
	previous, updated domain.Resource
	expected          int64
	options           int
}

func (r *privateFixtureResourceRepository) GetResource(context.Context, string) (domain.Resource, error) {
	return r.previous, nil
}
func (r *privateFixtureResourceRepository) UpdateResource(_ context.Context, value domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	r.updated, r.expected, r.options = value, expected, len(opts)
	return value, nil
}
func TestPrivateFixtureSourceRevisionPolicy(t *testing.T) {
	original := domain.Resource{ID: "resource_fixture", Generation: 1, Name: "before", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: "source_fixture", Generation: 1, GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://example.com/repo.git", RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: "railpack"}}}}}
	repository := &privateFixtureResourceRepository{previous: original}
	adapter := controllerFixtureHosting{HostingRepository: repository}
	named := original
	named.Name = "after"
	audit := store.WithAudit(domain.AuditEvent{})
	if _, err := adapter.UpdateResource(t.Context(), named, 1, audit); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(original.Source)
	after, _ := json.Marshal(repository.updated.Source)
	if string(before) != string(after) || repository.options != 1 || repository.expected != 1 || repository.updated.Name != "after" {
		t.Fatal("name-only fixture update rewrote source/options or lost CAS")
	}
	changed := named
	git := *named.Source.Git
	git.RequestedRef = "refs/heads/main"
	changed.Source.Git = &git
	if _, err := adapter.UpdateResource(t.Context(), changed, 1, audit); err != nil {
		t.Fatal(err)
	}
	if repository.options != 2 || repository.expected != 1 || repository.updated.Source.Git.Generation != 1 || repository.updated.Source.Git.RequestedRef != "refs/heads/main" {
		t.Fatal("source revision did not add exactly its fixture policy option while preserving CAS")
	}
	if _, err := adapter.GetHostingRuntimeConfiguration(t.Context(), "actor", original.ID); err != store.ErrHostingRuntimeConfigurationUnavailable {
		t.Fatal("missing runtime repository fabricated a GET")
	}
	if _, err := adapter.PutHostingRuntimeConfiguration(t.Context(), "actor", original.ID, domain.HostingRuntimeConfigurationInput{}, domain.AuditEvent{}); err != store.ErrHostingRuntimeConfigurationUnavailable {
		t.Fatal("missing runtime repository fabricated a PUT")
	}
}

func TestPrivateFixtureResourcePolicyPostgres(t *testing.T) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned local build fixture")
	}
	privateRuntimeDatabase(t)
	f := newControllerBuildFixture(t, domain.HostingPrivateGitCapability, domain.HostingBuildTaskType, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy)
	configuration := domain.GitResourceConfiguration{RepositoryURL: "https://example.com/fixture-policy.git", RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: "railpack"}}
	var resource domain.Resource
	f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": "policy probe", "kind": "application", "source": map[string]any{"kind": "git", "git": configuration}}, 201, &resource)
	before, err := f.pg.GetHostingBuildSource(t.Context(), "usr_bootstrap", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Policy.Mode != "internal" || !before.Policy.AllowInternal {
		t.Fatal("creation fixture policy missing")
	}
	configuration.RequestedRef = "refs/heads/main"
	f.request(t, "PATCH", "/api/v1/resources/"+resource.ID, f.admin, map[string]any{"expected_generation": resource.Generation, "source": map[string]any{"kind": "git", "git": configuration}}, 200, &resource)
	edited, err := f.pg.GetHostingBuildSource(t.Context(), "usr_bootstrap", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Generation != 2 || edited.Generation != 2 || edited.RequestedRef != configuration.RequestedRef || domain.HostingDocumentHash(edited.Policy) != domain.HostingDocumentHash(before.Policy) {
		t.Fatal("real source edit lost fixture policy or generation")
	}
	f.request(t, "PATCH", "/api/v1/resources/"+resource.ID, f.admin, map[string]any{"expected_generation": resource.Generation, "name": "renamed policy probe"}, 200, &resource)
	named, err := f.pg.GetHostingBuildSource(t.Context(), "usr_bootstrap", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Generation != 3 || named.Generation != 2 || domain.HostingDocumentHash(named) != domain.HostingDocumentHash(edited) {
		t.Fatal("real name-only edit rewrote source")
	}
	var runtime struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}
	runtimePath := "/api/v1/resources/" + resource.ID + "/runtime-configuration"
	f.request(t, "GET", runtimePath, "", nil, 401, nil)
	f.request(t, "GET", runtimePath, f.admin, nil, 200, &runtime)
	if runtime.Configuration.RuntimeGeneration != 0 || runtime.Configuration.ResourceID != resource.ID || runtime.Configuration.ProjectID != resource.ProjectID || runtime.Configuration.ResourceGeneration != resource.Generation {
		t.Fatal("real generation-zero runtime metadata mismatch")
	}
	var viewer struct {
		Token string `json:"token"`
	}
	f.request(t, "POST", "/api/v1/sessions", "", map[string]string{"email": "viewer@example.local", "password": "viewer"}, 201, &viewer)
	f.request(t, "GET", runtimePath, viewer.Token, nil, 403, nil)
	f.request(t, "POST", "/api/v1/project-members", f.admin, map[string]string{"project_id": resource.ProjectID, "email": "viewer@example.local", "role": "viewer"}, 200, nil)
	f.request(t, "GET", runtimePath, viewer.Token, nil, 200, &runtime)
	f.request(t, "PUT", runtimePath, viewer.Token, domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("a", 32), Entries: []domain.HostingRuntimeEntry{}}, 403, nil)
	t.Log("real API/service/PostgreSQL source edit preserves exact trusted fixture policy; name-only edit leaves complete source unchanged; generation-zero runtime GET, viewer read/write denial and anonymous denial passed")
}

// Each private case owns a separate database inside the disposable fixture's
// PostgreSQL container. Never reapply native migrations to a prior case's schema.
func privateRuntimeDatabase(t *testing.T) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" || cfg.ConnConfig.Database != "nerocd_build_controller" {
		t.Fatal("private database requires exact disposable fixture")
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "private_runtime_" + hex.EncodeToString(nonce)
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		defer admin.Close()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("owned private database cleanup", err)
		} else {
			t.Logf("owned private database removed: %s", name)
		}
	})
	u, err := url.Parse(os.Getenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL"))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("fixture requires PostgreSQL URL")
	}
	u.Path = "/" + name
	scoped := u.String()
	verified, err := pgxpool.ParseConfig(scoped)
	if err != nil || verified.ConnConfig.Database != name {
		t.Fatal("scoped fixture database mismatch")
	}
	t.Setenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL", scoped)

}

const compiledVitePackage = `{
  "name": "nerocd-compiled-vite-fixture",
  "version": "1.0.0",
  "private": true,
  "type": "module",
  "packageManager": "bun@1.3.6",
  "engines": {
    "node": "22.22.0"
  },
  "scripts": {
    "build": "node ./node_modules/vite/bin/vite.js build"
  },
  "devDependencies": {
    "vite": "8.2.2"
  }
}
`

// Frozen with Bun 1.3.6 from exact vite@8.2.2. It is source, not generated output.
const compiledViteLock = `{
  "lockfileVersion": 1,
  "configVersion": 1,
  "workspaces": {
    "": {
      "name": "nerocd-compiled-vite-fixture",
      "devDependencies": {
        "vite": "8.2.2",
      },
    },
  },
  "packages": {
    "@oxc-project/types": ["@oxc-project/types@0.155.0", "", {}, "sha512-SeE8JPwW1xs1tHeNycjUtUBdhorRa8o4jlvjwk49R7/Z291i+4DQa7JJIgOm/TaYQZx+QrPQbVJ/9c8dMIZN8Q=="],

    "@rolldown/binding-android-arm-eabi": ["@rolldown/binding-android-arm-eabi@1.2.15", "", { "os": "android", "cpu": "arm" }, "sha512-aLcgK9gIJ3YRvrrtfSBbRa27F5WrpiTaPulnFpsHPxpUHgFlSgnU4854MHwoRGEBhfRJDWCS2WQ7lIjskE9Olw=="],

    "@rolldown/binding-android-arm64": ["@rolldown/binding-android-arm64@1.2.15", "", { "os": "android", "cpu": "arm64" }, "sha512-xD7DdZCrbThKuxNg7ooQmvp07ys4J/pnSEMTG3/OdOWl6daC67m/Q4fL78sRHMxTH/N4fGUbYhcPM0MF5WFHkg=="],

    "@rolldown/binding-darwin-arm64": ["@rolldown/binding-darwin-arm64@1.2.15", "", { "os": "darwin", "cpu": "arm64" }, "sha512-m2WuQ6r0B4OTYxyPpNLtXaMRbRDUbG8YQV2Q3KTdSSK8Th6KHCnC8jVCJt8bvYeSB5WivkkNbc85mKadaSdOxA=="],

    "@rolldown/binding-darwin-x64": ["@rolldown/binding-darwin-x64@1.2.15", "", { "os": "darwin", "cpu": "x64" }, "sha512-5RtnsHM36nodM9WbMy+zip0LGS/G+y5wro5RHrpOlr4ac24+1zSi9kiV2MP1myZeZ/ifEHfulQcWjnAXzc2C2A=="],

    "@rolldown/binding-freebsd-x64": ["@rolldown/binding-freebsd-x64@1.2.15", "", { "os": "freebsd", "cpu": "x64" }, "sha512-g3l09pyFqhu+135JKkbkwiKGQmwU5hUMUPJ4VdBADvuqR8d33L6H578rh8m7rtxbdp+VmX/+nzI7nvhSZlzpUQ=="],

    "@rolldown/binding-linux-arm-gnueabihf": ["@rolldown/binding-linux-arm-gnueabihf@1.2.15", "", { "os": "linux", "cpu": "arm" }, "sha512-nIe3VugI97nGQtspPxDqA4sXMUpeiocKIP/CqpY/u/vgbT+alX+cVxu1C/oVBPKg4feNTmYovE91v4DgXvXNwQ=="],

    "@rolldown/binding-linux-arm64-gnu": ["@rolldown/binding-linux-arm64-gnu@1.2.15", "", { "os": "linux", "cpu": "arm64" }, "sha512-iTBjXx+B5QSvgI89lJfmqh4wTh8NabR1YZIIN3hNx4zSG0EVfVqkOvLulNpG4251pbd6/IAmZ/LefVk1Y4rPhw=="],

    "@rolldown/binding-linux-arm64-musl": ["@rolldown/binding-linux-arm64-musl@1.2.15", "", { "os": "linux", "cpu": "arm64" }, "sha512-uN26XOnVR7rqQupG3V9oAG2EGGatWjcJHOeDOHUFCh4TBWmP2tHTFoVN13aYbwfm0aHqJAEmcwhAu6sooIjfqQ=="],

    "@rolldown/binding-linux-ppc64-gnu": ["@rolldown/binding-linux-ppc64-gnu@1.2.15", "", { "os": "linux", "cpu": "ppc64" }, "sha512-9nWkcdd+H+XhaA3fQIwpHnKIrfx/PQ6rO0pivr78Og07tXlUlctBBJoEYLPfhTmCR/QxEVdn6uPjHV5SymPhdw=="],

    "@rolldown/binding-linux-s390x-gnu": ["@rolldown/binding-linux-s390x-gnu@1.2.15", "", { "os": "linux", "cpu": "s390x" }, "sha512-Ggh0ciFjLwIusLSLz7LZYEaGdxOAUJXI+Qu5JCbhPJJHEt8vP+IjRATTJheiJsdzzTYgUiHUiw3X1Ea3ZpMBdA=="],

    "@rolldown/binding-linux-x64-gnu": ["@rolldown/binding-linux-x64-gnu@1.2.15", "", { "os": "linux", "cpu": "x64" }, "sha512-FHg60rS6DBePAYZ9X9F67x+JObFdmgn6tupA1C/a3B+CufFmTD4mX+e8M75tCiAO3foR/6xwVBI0KQiI7sn/rw=="],

    "@rolldown/binding-linux-x64-musl": ["@rolldown/binding-linux-x64-musl@1.2.15", "", { "os": "linux", "cpu": "x64" }, "sha512-/cvV/7v5VGz/moiz0XKRj2dE8DzsKyhIu1KpOvJGPjRywg3yscON889UGvAe8zfiJLHup7VLjBduoGWi3HusWQ=="],

    "@rolldown/binding-openharmony-arm64": ["@rolldown/binding-openharmony-arm64@1.2.15", "", { "os": "none", "cpu": "arm64" }, "sha512-MyzC5Hrv/sm+Z7TSGKP/j8001RO2xQm1zOpCNVL4j0cY/VhBZviludA2YFQ7i4lH6ZAXxQm0lcMdBzM+99mE7w=="],

    "@rolldown/binding-win32-arm64-msvc": ["@rolldown/binding-win32-arm64-msvc@1.2.15", "", { "os": "win32", "cpu": "arm64" }, "sha512-+PgYiS+AH5omy6L45ZLxybasauJaj3M8buoT83D+nzPmCayXlxUV9hydhciCKVyIg0B/v0BtXPDE1uGqNwCPpg=="],

    "@rolldown/binding-win32-x64-msvc": ["@rolldown/binding-win32-x64-msvc@1.2.15", "", { "os": "win32", "cpu": "x64" }, "sha512-HFZd2QNQYn1JbIi+Hpd7H+r/lkWxcfIu28nirZ6eCn2LafuvmExMlb/P96Sfx5tf7pPArw39IdhmCF8vmV+NhQ=="],

    "@rolldown/pluginutils": ["@rolldown/pluginutils@1.0.1", "", {}, "sha512-2j9bGt5Jh8hj+vPtgzPtl72j0yRxHAyumoo6TNfAjsLB04UtpSvPbPcDcBMxz7n+9CYB0c1GxQFxYRg2jimqGw=="],

    "detect-libc": ["detect-libc@2.1.2", "", {}, "sha512-Btj2BOOO83o3WyH59e8MgXsxEQVcarkUOpEYrubB0urwnN10yQ364rsiByU11nZlqWYZm05i/of7io4mzihBtQ=="],

    "fdir": ["fdir@6.5.0", "", { "peerDependencies": { "picomatch": "^3 || ^4" }, "optionalPeers": ["picomatch"] }, "sha512-tIbYtZbucOs0BRGqPJkshJUYdL+SDH7dVM8gjy+ERp3WAUjLEFJE+02kanyHtwjWOnwrKYBiwAmM0p4kLJAnXg=="],

    "fsevents": ["fsevents@2.3.3", "", { "os": "darwin" }, "sha512-5xoDfX+fL7faATnagmWPpbFtwh/R77WmMMqqHGS65C3vvB0YHrgF+B1YmZ3441tMj5n63k0212XNoJwzlhffQw=="],

    "lightningcss": ["lightningcss@1.33.0", "", { "dependencies": { "detect-libc": "^2.0.3" }, "optionalDependencies": { "lightningcss-android-arm64": "1.33.0", "lightningcss-darwin-arm64": "1.33.0", "lightningcss-darwin-x64": "1.33.0", "lightningcss-freebsd-x64": "1.33.0", "lightningcss-linux-arm-gnueabihf": "1.33.0", "lightningcss-linux-arm64-gnu": "1.33.0", "lightningcss-linux-arm64-musl": "1.33.0", "lightningcss-linux-x64-gnu": "1.33.0", "lightningcss-linux-x64-musl": "1.33.0", "lightningcss-win32-arm64-msvc": "1.33.0", "lightningcss-win32-x64-msvc": "1.33.0" } }, "sha512-WkUDrojuJs0xkgGf2udWxa3yGBRxPtxUkB79i6aCZLRgc7PM8fZe9TosfPDcvEpQZbuFASnHYmRLBLUbmLOIIA=="],

    "lightningcss-android-arm64": ["lightningcss-android-arm64@1.33.0", "", { "os": "android", "cpu": "arm64" }, "sha512-gEpRTalKdosp4Bb8qWtc2iOgE5SeIHlpS1up9bFq2wAyYhl1UdTObYiHe98zEM9SQvSoqQZ1IQD0JNpg3Ml5pg=="],

    "lightningcss-darwin-arm64": ["lightningcss-darwin-arm64@1.33.0", "", { "os": "darwin", "cpu": "arm64" }, "sha512-Sciaz8eenNTKn9b3t7+xr0ipTp9YxKQY4npwQ3mrRuL0BAVHBLyZxofhaKBAVtzmtRZ/zTyo0/to4B1uWG/Djg=="],

    "lightningcss-darwin-x64": ["lightningcss-darwin-x64@1.33.0", "", { "os": "darwin", "cpu": "x64" }, "sha512-Z5UPAxzrjlWNNyGy6i65cJzzvgJ5D3T6wMvs+gWpY9d7qRhANrxqAp6LhxIgZhWEw18RfJTGcRxjuLIBr+m8XQ=="],

    "lightningcss-freebsd-x64": ["lightningcss-freebsd-x64@1.33.0", "", { "os": "freebsd", "cpu": "x64" }, "sha512-QQM/Ti/hQajJwCY+RiWuCZ9sdtI/XQk7nDK5vC8kkdwixezOlDgvDx7+RT+QjK6FcFT4MpsuoBnHIo/O3StRRg=="],

    "lightningcss-linux-arm-gnueabihf": ["lightningcss-linux-arm-gnueabihf@1.33.0", "", { "os": "linux", "cpu": "arm" }, "sha512-N7FVBe6iS24MlM6R/4RBTxGhQheZGs7tiQ9U32UtF75NzP5Q7xWPRqLBCKxlRQRk3rY1jCIPLzx7WzOhuUIRLQ=="],

    "lightningcss-linux-arm64-gnu": ["lightningcss-linux-arm64-gnu@1.33.0", "", { "os": "linux", "cpu": "arm64" }, "sha512-j2v/itmy4HlNxlc6voKXYgBqNi0Ng2LShg4z7GufpEgs05P+2suBVyi9I6YHq5uoVFx9ETin3eCEhLVyXGQnKg=="],

    "lightningcss-linux-arm64-musl": ["lightningcss-linux-arm64-musl@1.33.0", "", { "os": "linux", "cpu": "arm64" }, "sha512-yiO5ROMuYQgXbC60yjZU5CYSFZGKXL0HFATXt9mHJn1+zW55oCtMI9NfcVhYLMFDL7gV7oBPon/EmMMGg2OvtQ=="],

    "lightningcss-linux-x64-gnu": ["lightningcss-linux-x64-gnu@1.33.0", "", { "os": "linux", "cpu": "x64" }, "sha512-ar+Ju7LmcN0Jo4FpL4hpFybwNG9/3A/Br5KW2n2jyODg3MEZXaDYADdemoNS+BDNfMgKvylJLj4S5tyRActuAg=="],

    "lightningcss-linux-x64-musl": ["lightningcss-linux-x64-musl@1.33.0", "", { "os": "linux", "cpu": "x64" }, "sha512-RYiYbkokw0trfKqqzfF55lginwEPrD3OJDfTuJzFs1MK6iFnDenaz1fqLLtX4ITG3OktJQXOeTaw1awrBAlZPw=="],

    "lightningcss-win32-arm64-msvc": ["lightningcss-win32-arm64-msvc@1.33.0", "", { "os": "win32", "cpu": "arm64" }, "sha512-1K+MPfLSFVpphzpdbfkhlWk6wBrTObBzS2T6db10PNOZgR9GoVsAWzwNyuhUYYbTp23j+4RrncfujZ4uAzXvwA=="],

    "lightningcss-win32-x64-msvc": ["lightningcss-win32-x64-msvc@1.33.0", "", { "os": "win32", "cpu": "x64" }, "sha512-OlEICDx/Xl0FqSp4bry8zFnCvGpig3Gl4gCquvYwHuqJKEC1+n9NgDniFvqHGmMv1ZkqDJrDqKKSykTDX+ehuA=="],

    "nanoid": ["nanoid@3.3.20", "", { "bin": { "nanoid": "bin/nanoid.cjs" } }, "sha512-uKdg2G3GNCKQn9byYOpxbGqrT2fGO5KRt5J/8b3pok8rT6qxGWF6hxMyJiEYtAf+FVyYuD9hRaDqX5uPFYJ4ZQ=="],

    "picocolors": ["picocolors@1.1.1", "", {}, "sha512-xceH2snhtb5M9liqDsmEw56le376mTZkEX/jEb/RxNFyegNul7eNslCXP9FDj/Lcu0X8KEyMceP2ntpaHrDEVA=="],

    "picomatch": ["picomatch@4.0.7", "", {}, "sha512-qcJu88Q2IWqJsDD529JKMdwGm/dvInW4HvQnRwiH9JtihJvzGOscDtHE3x1pBKeUOTysQ8kVmLnJ2kJu7yhcGA=="],

    "postcss": ["postcss@8.5.31", "", { "dependencies": { "nanoid": "^3.3.20", "picocolors": "^1.1.1", "source-map-js": "^1.2.2" } }, "sha512-YUbWYCgErwnMAVkjUnYa+KlMBzGpHwiIt/DE7f0G33AuX2LH9UcTk6KgWxd3zRU+pN3u/C+Xl3EnMLM4Hea7Sg=="],

    "rolldown": ["rolldown@1.2.15", "", { "dependencies": { "@oxc-project/types": "=0.155.0", "@rolldown/pluginutils": "^1.0.0" }, "optionalDependencies": { "@rolldown/binding-android-arm-eabi": "1.2.15", "@rolldown/binding-android-arm64": "1.2.15", "@rolldown/binding-darwin-arm64": "1.2.15", "@rolldown/binding-darwin-x64": "1.2.15", "@rolldown/binding-freebsd-x64": "1.2.15", "@rolldown/binding-linux-arm-gnueabihf": "1.2.15", "@rolldown/binding-linux-arm64-gnu": "1.2.15", "@rolldown/binding-linux-arm64-musl": "1.2.15", "@rolldown/binding-linux-ppc64-gnu": "1.2.15", "@rolldown/binding-linux-s390x-gnu": "1.2.15", "@rolldown/binding-linux-x64-gnu": "1.2.15", "@rolldown/binding-linux-x64-musl": "1.2.15", "@rolldown/binding-openharmony-arm64": "1.2.15", "@rolldown/binding-win32-arm64-msvc": "1.2.15", "@rolldown/binding-win32-x64-msvc": "1.2.15" }, "bin": { "rolldown": "./bin/cli.mjs" } }, "sha512-6odND+fel5zVw3I4Yc2e69gvguDnX0CAAGiNDZSq4ik5WUcaLrkD2GLuog0XcODbor31hZ5ayEMgVDno+KPV5g=="],

    "source-map-js": ["source-map-js@1.2.2", "", {}, "sha512-KGj/8Y43x35aZVDtt+J4mK1hoLGHULMYfSkODJNQjNDC3oW1PqPoxMwo0pLUsWM/UEGzON/NxeHywEfNXNP3Vw=="],

    "tinyglobby": ["tinyglobby@0.2.17", "", { "dependencies": { "fdir": "^6.5.0", "picomatch": "^4.0.4" } }, "sha512-wXR/dYpcqKmfWpEdZjiKJOwCNFndD0DMnrW/cYjVGttEkBfVgcLFHoNrlj47mjOVic9yyNu65alsgF4NQyTa2g=="],

    "vite": ["vite@8.2.2", "", { "dependencies": { "lightningcss": "^1.33.0", "picomatch": "^4.0.5", "postcss": "^8.5.26", "rolldown": "~1.2.4", "tinyglobby": "^0.2.17" }, "optionalDependencies": { "fsevents": "~2.3.3" }, "peerDependencies": { "@types/node": "^20.19.0 || >=22.12.0", "@vitejs/devtools": "^0.4.0 || ^0.5.0", "esbuild": "^0.27.0 || ^0.28.0", "jiti": ">=1.21.0", "less": "^4.0.0", "sass": "^1.70.0", "sass-embedded": "^1.70.0", "stylus": ">=0.54.8", "sugarss": "^5.0.0", "terser": "^5.16.0", "tsx": "^4.8.1", "yaml": "^2.4.2" }, "optionalPeers": ["@types/node", "@vitejs/devtools", "esbuild", "jiti", "less", "sass", "sass-embedded", "stylus", "sugarss", "terser", "tsx", "yaml"], "bin": { "vite": "bin/vite.js" } }, "sha512-cFKLV/PRgAUlIRm5WjMjJ86jrftzpqcgH+Us+DS8mI3CDNiH30Whrz8uHL3+MOLPAgqbMBAqWdAHAphOAM+z/Q=="],
  }
}
`

func compiledViteSource(fail bool) map[string]string {
	javascript := `const target = document.getElementById('app'); target.textContent = 'compiled-vite-A'; let count = 0; document.getElementById('increment').addEventListener('click', () => { target.textContent = 'compiled-vite-A clicks=' + (++count); });`
	if fail {
		javascript += "\nconst genuineSyntaxFailure = ;\n"
	}
	return map[string]string{
		"package.json":   compiledVitePackage,
		"bun.lock":       compiledViteLock,
		"railpack.json":  `{"provider":"node","packages":{"node":"22.22.0","bun":"1.3.6","caddy":"2.10.2"}}`,
		"index.html":     `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Compiled Vite fixture</title></head><body><main id="app">Waiting for compiled JavaScript</main><button id="increment">Increment compiled counter</button><script type="module" src="/src/main.js"></script></body></html>`,
		"src/main.js":    javascript,
		"vite.config.js": "export default { build: { outDir: 'dist' } };\n",
	}
}

func TestCompiledViteSourceContract(t *testing.T) {
	files := compiledViteSource(false)
	var manifest struct {
		PackageManager  string            `json:"packageManager"`
		Engines         map[string]string `json:"engines"`
		Scripts         map[string]string `json:"scripts"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(files["package.json"]), &manifest) != nil || manifest.PackageManager != "bun@1.3.6" || manifest.Engines["node"] != "22.22.0" || manifest.DevDependencies["vite"] != "8.2.2" || manifest.Scripts["build"] != "node ./node_modules/vite/bin/vite.js build" {
		t.Fatal("compiled package contract changed")
	}
	if !compiledViteBundlePath.MatchString("/assets/index-ABC.js") {
		t.Fatal("bundle matcher rejected generated path")
	}
	if files["vite.config.js"] != "export default { build: { outDir: 'dist' } };\n" {
		t.Fatal("Vite selector/config missing")
	}
	if len(files) != 6 || strings.Contains(files["index.html"], "compiled-vite-A") || !strings.Contains(files["bun.lock"], "vite@8.2.2") {
		t.Fatal("compiled source or lock contract changed")
	}
	failed := compiledViteSource(true)
	if failed["railpack.json"] != files["railpack.json"] || failed["bun.lock"] != files["bun.lock"] || !strings.Contains(failed["src/main.js"], "genuineSyntaxFailure = ;") {
		t.Fatal("failure must reach source compilation")
	}
}

func privateRuntimeGit(t *testing.T, strategy string, compiled ...bool) (string, func(string, bool), string, string) {
	t.Helper()
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	update := func(body string, fail bool) {
		t.Helper()
		dockerfile := "FROM " + hostingbuild.StaticImage + "\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\n"
		if fail {
			dockerfile += "RUN exit 9\n"
		}
		files := map[string]string{"Dockerfile": dockerfile, "index.html": body}
		if len(compiled) > 0 && compiled[0] {
			files = compiledViteSource(fail)
		} else if strategy == "railpack" {
			delete(files, "Dockerfile")
			files["Staticfile"] = "root: .\n"
			if fail {
				files["railpack.json"] = `{ "steps": `
			} else {
				if err := os.Remove(filepath.Join(work, "railpack.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
		for name, value := range files {
			if e := os.MkdirAll(filepath.Dir(filepath.Join(work, name)), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(work, name), []byte(value), 0600); e != nil {
				t.Fatal(e)
			}
		}
		checkoutCommand(t, work, "add", ".")
		checkoutCommand(t, work, "commit", "-qm", "fixture update")
	}
	update("local-A\n", false)
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	username := "fixture-user"
	password := fmt.Sprintf("fixture-pat-%x", time.Now().UnixNano())
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="private fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	for _, wrong := range []bool{false, true} {
		req, e := http.NewRequest(http.MethodGet, server.URL+"/repo.git/info/refs?service=git-upload-pack", nil)
		if e != nil {
			t.Fatal(e)
		}
		if wrong {
			req.SetBasicAuth(username, "wrong-password")
		}
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal("private fixture allowed anonymous/wrong auth")
		}
	}

	t.Cleanup(server.Close)
	cert := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n{ printf '%s\\n' \"$@\"; env; } >> "+shellQuote(filepath.Join(root, "transport.trace"))+"\nexec "+shellQuote(actual)+" -c http.sslCAInfo="+shellQuote(cert)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(root, "transport.trace"))
		if err != nil {
			t.Error(err)
			return
		}
		for _, pattern := range []string{password, username + ":" + password} {
			if strings.Contains(string(raw), pattern) {
				t.Error("credential entered Git argv/environment")
			}
		}
	})
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return strings.Replace(server.URL, "127.0.0.1", "example.com", 1) + "/repo.git", func(body string, fail bool) {
		update(body, fail)
		checkoutCommand(t, work, "push", filepath.Join(root, "repo.git"), "main")
	}, username, password
}

func testPrivateBuildProductionHTTPS(t *testing.T, strategy string) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-hosting-local-build/gate.sh")
	}
	privateRuntimeDatabase(t)
	f := newControllerBuildFixture(t, domain.HostingPrivateGitCapability, domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability)
	var lost atomic.Bool
	var deliveryMu sync.Mutex
	var deliveries []domain.HostingSourceCredentialRequest
	original := f.server.Config.Handler
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/builds/source-credential" {
			original.ServeHTTP(w, r)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		var in domain.HostingSourceCredentialRequest
		if json.Unmarshal(raw, &in) != nil {
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		deliveryMu.Lock()
		deliveries = append(deliveries, in)
		deliveryMu.Unlock()
		recorded := httptest.NewRecorder()
		original.ServeHTTP(recorded, r)
		defer clear(recorded.Body.Bytes())
		if recorded.Code == 200 && lost.CompareAndSwap(false, true) {
			w.WriteHeader(503)
			return
		}
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
	}))
	t.Cleanup(f.server.Close)
	root, err := os.MkdirTemp("/var/lib", "nerocd-local-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := controlTrust(t, root)
	pebbleID := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", "fixture-pebble")))
	t.Cleanup(func() {
		identity := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}} {{.Config.Image}}", pebbleID)))
		if identity != pebbleID+" "+controlPebbleImage {
			t.Error("owned Pebble identity changed")
			return
		}
		controlDocker(t, "rm", "--force", pebbleID)
	})
	proxyConfig := ProxyConfig{Directory: root, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		if err := os.MkdirAll(filepath.Join(settings.Directory, "certificates"), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(settings.Directory, "certificates/root.pem"), publicRoot, 0600); err != nil {
			return nil, err
		}
		return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
	}}
	extra := []string{"--local-build-deploy", "--proxy-state-root", root, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion}
	privateRuntimeFreshRegistration(t, f, extra)
	var cliOutput []byte
	cli := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		out, err := f.agentCommand(ctx, extra...).CombinedOutput()
		cliOutput = append(cliOutput, out...)
		if err != nil {
			t.Logf("bounded CLI diagnostic: %.1500s", out)
		}
		return err
	}
	url, _, username, password := privateRuntimeGit(t, strategy)
	var resource domain.Resource
	f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": "local-artifact", "kind": "application", "source": map[string]any{"kind": "git", "git": domain.GitResourceConfiguration{RepositoryURL: url, RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: strategy}}}}, 201, &resource)
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/binding", f.admin, map[string]string{"destination_id": f.target.Destination.ID}, 201, nil)
	secret, grant := privateRuntimeGrant(t, f, resource, username, password)
	awaitOperation := func(id string) domain.HostingOperation {
		deadline := time.Now().Add(15 * time.Second)
		for {
			op, e := f.pg.GetHostingOperation(t.Context(), id)
			if e != nil {
				t.Fatal(e)
			}
			if op.RunID != "" {
				return op
			}
			if time.Now().After(deadline) {
				t.Fatal("real River local dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	build := func(key string) domain.HostingBuild {
		b := privateRuntimeBuild(t, f, resource, grant.ID, key)
		if err := cli(); err != nil {
			// Retain bounded actual API diagnostics without printing known credentials.
			var page domain.HostingBuildLogPage
			f.request(t, "GET", "/api/v1/hosting-builds/"+b.ID+"/logs?limit=200", f.admin, nil, 200, &page)
			for _, item := range page.Items {
				message := item.Message
				for _, pattern := range []string{password, username + ":" + password, base64.StdEncoding.EncodeToString([]byte(username + ":" + password)), f.credential, f.admin} {
					message = strings.ReplaceAll(message, pattern, "[REDACTED fixture credential]")
				}
				if len(message) > 2000 {
					message = message[:2000]
				}
				t.Logf("typed private build diagnostic sequence=%d component=%s: %s", item.Sequence, item.Component, message)
			}
			t.Fatal(err)
		}
		full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
		if err != nil || full.State != "succeeded" || full.Artifact == nil {
			t.Fatal("actual public Git build did not persist artifact")
		}
		return full
	}
	deploy := func(b domain.HostingBuild, key string) domain.HostingOperation {
		var op domain.HostingOperation
		f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/deployments/from-build", f.admin, domain.HostingBuildDeploymentInput{BuildID: b.ID, ExpectedSourceGeneration: resource.Source.Git.Generation, ExpectedGeneration: resource.Generation, IdempotencyKey: key, ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 202, &op)
		return awaitOperation(op.ID)
	}
	firstBuild := build("local-build-A")
	first := deploy(firstBuild, "local-deploy-A")
	if err = cli(); err != nil {
		t.Fatal(err)
	}
	first, err = f.pg.GetHostingOperation(t.Context(), first.ID)
	if err != nil || first.State != "succeeded" || first.Result == nil || first.Release == nil || first.Release.LocalBuild != *firstBuild.Artifact {
		t.Fatal("CLI local deployment failed exact artifact contract")
	}
	owned := []string{first.Result.ContainerID}
	t.Cleanup(func() {
		for _, id := range owned {
			out := controlDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.binding"}}`, id)
			if strings.TrimSpace(string(out)) != first.BindingID {
				t.Error("owned local workload identity changed")
				continue
			}
			controlDocker(t, "rm", "--force", id)
		}
	})
	localBuildFixtureHTTP(t, first, "local-A\n")
	proxy := controlJSON[domain.HostingProxy](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: f.environment.ProjectID, IdempotencyKey: "local-enable-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	proxyRun := func() {
		deadline := time.Now().Add(15 * time.Second)
		for {
			p, e := f.pg.GetHostingProxy(t.Context(), proxy.ID)
			if e != nil {
				t.Fatal(e)
			}
			if p.ActiveOperationID != "" {
				op, e := f.pg.GetHostingProxyOperation(t.Context(), p.ActiveOperationID)
				if e == nil && op.Generation == p.DesiredGeneration {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("real River proxy dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if e := RunContext(t.Context(), Config{Build: &f.config, LocalBuildDeploy: true, Proxy: proxyConfig, Server: f.server.URL, Token: f.credential, RunnerID: f.config.Target.RunnerID, WorkDir: f.work, JournalDir: f.journal, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond}); e != nil {
			t.Fatal(e)
		}
	}
	proxyRun()
	proxyID := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", "nerocd-proxy-"+proxy.ID)))
	t.Cleanup(func() {
		if strings.TrimSpace(string(controlDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.proxy"}}`, proxyID))) != proxy.ID {
			t.Error("owned proxy identity changed")
			return
		}
		controlDocker(t, "rm", "--force", proxyID)
	})
	controlJSON[domain.HostingHostnameGrant](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: f.environment.ProjectID, Hostname: "local.example.test"}, 201)
	_ = controlJSON[domain.HostingRoute](t, f.server.URL, f.admin, "POST", "/api/v1/resources/"+resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "local-route-A", Hostname: "local.example.test", PathPrefix: "/", ExpectedHealthyOperationID: first.ID}, 202)
	proxyRun()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	probe := func(want string) {
		t.Helper()
		response, e := client.Get("https://local.example.test/")
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if e != nil || response.StatusCode != 200 || string(body) != want {
			t.Fatalf("managed HTTPS body=%q wanted%q err=%v", body, want, e)
		}
	}
	probe("local-A\n")
	deliveryMu.Lock()
	if len(deliveries) != 2 || deliveries[0] != deliveries[1] || !lost.Load() {
		t.Error("credential response-loss changed delivery identity")
	}
	deliveryMu.Unlock()
	publicDTO, err := json.Marshal(domain.PublicBuild(firstBuild))
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{password, username + ":" + password, secret.ID} {
		if strings.Contains(string(publicDTO), pattern) {
			t.Fatal("secret leaked into public build DTO")
		}
	}
	if strings.Contains(string(cliOutput), password) || strings.Contains(string(cliOutput), username+":"+password) {
		t.Fatal("credential leaked into compiled CLI output")
	}
	privateRuntimeNoSecrets(t, f, firstBuild.ID, password, username+":"+password)
	t.Logf("real private TLS Git %s -> compiled CLI -> same-host artifact -> trusted-test-CA HTTPS; anonymous/wrong auth denied", strategy)
}
func TestPrivateBuildProductionHTTPS(t *testing.T) {
	for _, strategy := range []string{"dockerfile", "railpack"} {
		t.Run(strategy, func(t *testing.T) { testPrivateBuildProductionHTTPS(t, strategy) })
	}
}

// Exercise the production CLI's default registration composition, rather than
// hiding missing capabilities behind the fixture's pre-enrolled build identity.
func privateRuntimeFreshRegistration(t *testing.T, f *controllerBuildFixture, extra []string) {
	t.Helper()
	before, err := f.pg.GetRunnerByID(t.Context(), f.config.Target.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	id := "private_fresh_" + hex.EncodeToString(nonce)
	raw, err := os.ReadFile(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("invalid fixture configuration")
	}
	document["target"].(map[string]any)["runner_id"] = id
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(f.work, "fresh-registration.json")
	if err = os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := f.agentCommand(ctx, extra...)
	for i := 1; i < len(command.Args)-1; i++ {
		switch command.Args[i] {
		case "--credential-file":
			command.Args[i] = "--token"
			command.Args[i+1] = f.admin
		case "--build-config-file":
			command.Args[i+1] = config
		case "--journal-dir":
			command.Args[i+1] = filepath.Join(f.work, "fresh-registration-journal")
		}
	}
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh private runner registration: %v %.500s", err, out)
	}
	if !strings.Contains(string(out), "no matching queued run") {
		t.Fatal("fresh registered runner did not finish empty claim")
	}
	registered, err := f.pg.GetRunnerByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{domain.HostingBuildTaskType: true, domain.HostingBuildTriggerRevisionCapability: true, domain.HostingPrivateGitCapability: true, domain.RunTypeLocalBuildDeploy: true, domain.RunTypeProxyApply: true}
	if len(registered.Capabilities) != len(expected) {
		t.Fatal("fresh registration capability count changed")
	}
	for _, capability := range registered.Capabilities {
		if !expected[capability] {
			t.Fatal("fresh registration capability changed")
		}
	}
	after, err := f.pg.GetRunnerByID(t.Context(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := json.Marshal(before.Capabilities)
	current, _ := json.Marshal(after.Capabilities)
	if string(prior) != string(current) {
		t.Fatal("registration mutated existing runner capabilities")
	}
	var leases int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM run_leases WHERE runner_id=$1`, id).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("fresh registration unexpectedly acquired work", err)
	}
	t.Log("fresh compiled CLI registration persisted exact private/build/proxy/local capabilities without shell or a claim")
}

func privateRuntimeSecret(t *testing.T, f *controllerBuildFixture, resource domain.Resource, username, password string) domain.ManagedSecret {
	t.Helper()
	value, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	var secret domain.ManagedSecret
	f.request(t, "POST", "/api/v1/managed-secrets", f.admin, app.ManagedSecretInput{ProjectID: resource.ProjectID, Name: "private-git", Value: string(value)}, 201, &secret)
	return secret
}
func privateRuntimeGrant(t *testing.T, f *controllerBuildFixture, resource domain.Resource, username, password string) (domain.ManagedSecret, domain.PublicHostingSourceGrant) {
	t.Helper()
	secret := privateRuntimeSecret(t, f, resource, username, password)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/source-grants", f.admin, domain.HostingSourceGrantRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, SecretID: secret.ID, SecretVersion: 1, RequestID: hex.EncodeToString(nonce)}, 201, &result)
	return secret, result.Grant
}
func privateRuntimeBuild(t *testing.T, f *controllerBuildFixture, r domain.Resource, grant, key string) domain.PublicHostingBuild {
	t.Helper()
	var out struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	f.request(t, "POST", "/api/v1/resources/"+r.ID+"/builds", f.admin, domain.HostingBuildRequest{ExpectedResourceGeneration: r.Generation, ExpectedSourceGeneration: r.Source.Git.Generation, IdempotencyKey: key, GrantID: grant}, 202, &out)
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", out.Build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.RunID != "" {
			return domain.PublicBuild(b)
		}
		if time.Now().After(deadline) {
			t.Fatal("private dispatch timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func privateRuntimeNoSecrets(t *testing.T, f *controllerBuildFixture, build string, patterns ...string) {
	t.Helper()
	rows, err := f.db.Query(t.Context(), `SELECT row_to_json(r)::text FROM run_logs r UNION ALL SELECT row_to_json(a)::text FROM audit_events a`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		if err = rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			if strings.Contains(row, pattern) {
				t.Fatal("credential persisted in logs/audit")
			}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(f.journal, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			for _, pattern := range patterns {
				if strings.Contains(string(raw), pattern) {
					t.Fatal("credential persisted in journal")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.config.SourceRoot, build)); !os.IsNotExist(err) {
		t.Fatal("private checkout cleanup unconfirmed")
	}
}

type compiledViteEvidence struct {
	HTMLSHA256         string `json:"html_sha256"`
	BundlePath         string `json:"bundle_path"`
	BundleSHA256       string `json:"bundle_sha256"`
	MissingAssetStatus int    `json:"missing_asset_status"`
}

var compiledViteBundlePath = regexp.MustCompile(`^/assets/[a-zA-Z0-9_-]+\.js$`)
var compiledViteScript = regexp.MustCompile(`src="(/assets/[a-zA-Z0-9_-]+\.js)"`)

func readCompiledVite(ctx context.Context, client *http.Client, path string) ([]byte, string, int, error) {
	if path != "/" && path != "/account/deep/link" && path != "/missing.js" && !compiledViteBundlePath.MatchString(path) {
		return nil, "", 0, fmt.Errorf("unsupported compiled fixture path")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://local.example.test"+path, nil)
	if err != nil {
		return nil, "", 0, err
	}
	// No caller headers, cookies or credentials cross this fixed upstream boundary.
	response, err := client.Do(request)
	if err != nil {
		return nil, "", 0, fmt.Errorf("compiled fixture HTTPS unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, "", 0, fmt.Errorf("compiled fixture response limit")
	}
	return body, response.Header.Get("Content-Type"), response.StatusCode, nil
}
func observeCompiledVite(ctx context.Context, client *http.Client) (compiledViteEvidence, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	html, kind, code, err := readCompiledVite(ctx, client, "/")
	if err != nil || code != 200 || !strings.Contains(kind, "text/html") || strings.Contains(string(html), "/src/main.js") {
		return compiledViteEvidence{}, fmt.Errorf("compiled index unavailable")
	}
	match := compiledViteScript.FindSubmatch(html)
	if len(match) != 2 {
		return compiledViteEvidence{}, fmt.Errorf("generated Vite bundle missing")
	}
	bundlePath := string(match[1])
	bundle, kind, code, err := readCompiledVite(ctx, client, bundlePath)
	if err != nil || code != 200 || !strings.Contains(kind, "javascript") || !bytes.Contains(bundle, []byte("compiled-vite-A")) {
		return compiledViteEvidence{}, fmt.Errorf("compiled marker or bundle unavailable")
	}
	for _, path := range []string{"/account/deep/link", "/missing.js"} {
		body, content, status, e := readCompiledVite(ctx, client, path)
		if e != nil || status != 200 || !strings.Contains(content, "text/html") || !bytes.Equal(body, html) {
			return compiledViteEvidence{}, fmt.Errorf("unexpected default SPA fallback")
		}
	}
	return compiledViteEvidence{HTMLSHA256: fmt.Sprintf("%x", sha256.Sum256(html)), BundlePath: bundlePath, BundleSHA256: fmt.Sprintf("%x", sha256.Sum256(bundle)), MissingAssetStatus: 200}, nil
}
func compiledViteReadBridge(client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" || len(r.URL.Path) > 256 {
			http.Error(w, "unsupported path", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		body, kind, code, err := readCompiledVite(ctx, client, r.URL.Path)
		if err != nil {
			http.Error(w, "compiled fixture unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", kind)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}
func TestCompiledViteBridgeRejectsMutations(t *testing.T) {
	handler := compiledViteReadBridge(&http.Client{Timeout: time.Second})
	for _, request := range []*http.Request{httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil), httptest.NewRequest(http.MethodGet, "http://127.0.0.1/?upstream=bad", nil)} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusBadRequest {
			t.Fatal("unsafe bridge request accepted")
		}
	}
	for _, path := range []string{"https://untrusted.invalid/", "//untrusted.invalid/", "/api/v1/me", "/assets/../../secret.js"} {
		if _, _, _, err := readCompiledVite(t.Context(), &http.Client{Timeout: time.Second}, path); err == nil {
			t.Fatal("upstream path escaped")
		}
	}
}

func TestCompiledViteBridgeDropsPrivateHeaders(t *testing.T) {
	var reads atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		for _, name := range []string{"Cookie", "Authorization", "Origin", "X-Caller-Header"} {
			if r.Header.Get(name) != "" {
				t.Errorf("private header forwarded: %s", name)
			}
		}
		if r.Host != "local.example.test" {
			t.Error("upstream Host changed")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Location", "https://untrusted.invalid/")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("fixed upstream"))
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "https://"))
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	request.Header.Set("Cookie", "nerocd_session=test-only")
	request.Header.Set("Authorization", "Bearer test-only")
	request.Header.Set("Origin", "https://caller.invalid")
	request.Header.Set("X-Caller-Header", "test-only")
	response := httptest.NewRecorder()
	compiledViteReadBridge(client).ServeHTTP(response, request)
	if reads.Load() != 1 || response.Code != http.StatusFound || response.Header().Get("Location") != "" || response.Body.String() != "fixed upstream" {
		t.Fatal("bridge redirected or altered fixed upstream")
	}
}

// Pinned Node Caddyfile.template listens on PORT and falls back to 80.
func compiledVitePort(environment string) (int, error) {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return 80, nil
	}
	if !strings.HasPrefix(environment, "PORT=") || strings.ContainsAny(environment, "\r\n") {
		return 0, fmt.Errorf("invalid fixed image port")
	}
	port, err := strconv.Atoi(strings.TrimPrefix(environment, "PORT="))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid fixed image port")
	}
	return port, nil
}
func TestCompiledVitePortFromImage(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{{"", 80}, {"PORT=8080", 8080}, {"PORT=80", 80}} {
		got, err := compiledVitePort(tc.input)
		if err != nil || got != tc.want {
			t.Fatal("Caddy effective port", got, err)
		}
	}
	for _, input := range []string{"PORT=0", "PORT=65536", "PORT=80\nPORT=90", "OTHER=8080"} {
		if _, err := compiledVitePort(input); err == nil {
			t.Fatal("unsafe port accepted")
		}
	}
}

// Normalize only terminal colors in already-redacted typed fixture output.
// Preserve all production log bytes and require evidence from the real parser.
var compiledViteSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func compiledViteSyntaxDiagnostic(text string) bool {
	plain := compiledViteSGR.ReplaceAllString(text, "")
	return strings.Contains(plain, "[PARSE_ERROR]") && strings.Contains(plain, "src/main.js:2") && strings.Contains(plain, "genuineSyntaxFailure")
}

func TestCompiledViteSourceSyntaxDiagnostic(t *testing.T) {
	// Bounded actual Vite 8.2.2/Rolldown output: every source character is colored.
	colored := "\u001b[31m[PARSE_ERROR] \u001b[0mUnexpected token\n   \u001b[38;5;246m\u256d\u001b[0m\u001b[38;5;246m\u2500\u001b[0m\u001b[38;5;246m[\u001b[0m ../../../../../private/var/folders/vn/83xc0ss13rsg8dq6f3gng0hr0000gn/T/nerocd-compiled-spa-parser-rp2qe7nt/src/main.js:2:30 \u001b[38;5;246m]\u001b[0m\n   \u001b[38;5;246m\u2502\u001b[0m\n \u001b[38;5;246m2 \u2502\u001b[0m \u001b[38;5;249mc\u001b[0m\u001b[38;5;249mo\u001b[0m\u001b[38;5;249mn\u001b[0m\u001b[38;5;249ms\u001b[0m\u001b[38;5;249mt\u001b[0m\u001b[38;5;249m \u001b[0m\u001b[38;5;249mg\u001b[0m\u001b[38;5;249me\u001b[0m\u001b[38;5;249mn\u001b[0m\u001b[38;5;249mu\u001b[0m\u001b[38;5;249mi\u001b[0m\u001b[38;5;249mn\u001b[0m\u001b[38;5;249me\u001b[0m\u001b[38;5;249mS\u001b[0m\u001b[38;5;249my\u001b[0m\u001b[38;5;249mn\u001b[0m\u001b[38;5;249mt\u001b[0m\u001b[38;5;249ma\u001b[0m\u001b[38;5;249mx\u001b[0m\u001b[38;5;249mF\u001b[0m\u001b[38;5;249ma\u001b[0m\u001b[38;5;249mi\u001b[0m\u001b[38;5;249ml\u001b[0m\u001b[38;5;249mu\u001b[0m\u001b[38;5;249mr\u001b[0m\u001b[38;5;249me\u001b[0m\u001b[38;5;249m \u001b[0m\u001b[38;5;249m=\u001b[0m\u001b[38;5;249m \u001b[0m;\n"
	if strings.Contains(colored, "genuineSyntaxFailure") || !compiledViteSyntaxDiagnostic(colored) {
		t.Fatal("colored compiler diagnostic not recognized")
	}
	for _, message := range []string{"build_failed", "network error: install failed", "error: timeout", "genuineSyntaxFailure source uploaded", "[PARSE_ERROR] src/other.js:2 genuineSyntaxFailure"} {
		if compiledViteSyntaxDiagnostic(message) {
			t.Fatal("non-compiler failure accepted")
		}
	}
}

// Diagnostic-only observer reads bounded counters and owned-container state.
// It never changes a limit or substitutes for compiled CLI qualification.
func compiledResourceObserver(parent context.Context, config hostingbuild.Config) func() string {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	evidence := "resource counters unavailable"
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				raw, err := os.ReadFile(filepath.Join(config.StateDir, "cleanup_unconfirmed.json"))
				if err != nil || len(raw) > 2048 {
					continue
				}
				var marker struct{ Group, Builder, Token string }
				if json.Unmarshal(raw, &marker) != nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(marker.Token) || marker.Group != filepath.Join(config.CgroupRoot, "build-"+marker.Token) || marker.Builder != "buildx_buildkit_nerocd-"+marker.Token+"0" {
					continue
				}
				counters := make([]string, 0, 3)
				for _, name := range []string{"memory.events", "memory.peak", "pids.events"} {
					value, e := os.ReadFile(filepath.Join(marker.Group, name))
					if e == nil && len(value) < 1024 && regexp.MustCompile(`^[a-z0-9_\s]+$`).Match(value) {
						counters = append(counters, name+"="+strings.Join(strings.Fields(string(value)), " "))
					}
				}
				inspect, stop := context.WithTimeout(ctx, 2*time.Second)
				state, e := exec.CommandContext(inspect, "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "inspect", "--format", `{{index .Config.Labels "io.nerocd.hostingbuild.owner"}} {{.State.Running}} {{.State.ExitCode}} {{.State.OOMKilled}}`, marker.Builder).Output()
				stop()
				if e == nil && len(state) < 256 && strings.HasPrefix(string(state), marker.Token+" ") {
					counters = append(counters, "owned_builder_state="+strings.TrimSpace(strings.TrimPrefix(string(state), marker.Token)))
				}
				if len(counters) > 0 {
					evidence = strings.Join(counters, "; ")
				}
			}
		}
	}()
	return func() string { cancel(); <-done; return evidence }
}
func compiledEngineDiagnostic(t *testing.T, ctx context.Context, f *controllerBuildFixture, snapshot domain.HostingBuildSnapshot, patterns []string) {
	t.Helper()
	root := t.TempDir()
	for name, contents := range compiledViteSource(false) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Error("diagnostic source directory")
			return
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Error("diagnostic source file")
			return
		}
	}
	engine, err := hostingbuild.New(f.config.Engine)
	if err != nil {
		t.Error("diagnostic engine configuration")
		return
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Error("diagnostic identity")
		return
	}
	request := buildEngineRequest(snapshot, root, snapshot.ExpectedDaemonID)
	request.BuildID = "build_diagnostic_" + hex.EncodeToString(nonce[:])
	if request.DaemonID == "" {
		request.DaemonID = strings.TrimSpace(string(controlDocker(t, "info", "--format", "{{.ID}}")))
	}
	stop := compiledResourceObserver(ctx, f.config.Engine)
	_, err = engine.Build(ctx, request)
	counters := stop()
	message := "success (diagnostic only)"
	if err != nil {
		message = err.Error()
	}
	for _, pattern := range patterns {
		message = strings.ReplaceAll(message, pattern, "[REDACTED fixture credential]")
	}
	t.Logf("diagnostic-only direct real engine same pins/limits error=%.1500s counters=%s", message, counters)
}

func TestPrivateGitBrowser(t *testing.T) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_PRIVATE_GIT_BROWSER") != "1" {
		t.Skip("requires opt-in local build browser gate")
	}
	strategy := os.Getenv("NEROCD_PRIVATE_GIT_STRATEGY")
	if strategy == "" {
		strategy = "dockerfile"
	}
	if strategy != "dockerfile" && strategy != "railpack" {
		t.Fatal("unsupported fixed private browser strategy")
	}
	compiledVite := os.Getenv("NEROCD_PRIVATE_GIT_COMPILED_VITE") == "1"
	if compiledVite && strategy != "railpack" {
		t.Fatal("compiled Vite requires Railpack")
	}
	privateRuntimeDatabase(t)
	f := newControllerBuildFixture(t, domain.HostingPrivateGitCapability, domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability)
	root, err := os.MkdirTemp("/var/lib", "nerocd-browser-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := controlTrust(t, root)
	proxyConfig := ProxyConfig{Directory: root, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		if err := os.MkdirAll(filepath.Join(settings.Directory, "certificates"), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(settings.Directory, "certificates/root.pem"), publicRoot, 0600); err != nil {
			return nil, err
		}
		return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
	}}
	repositoryURL, updateGit, username, password := privateRuntimeGit(t, strategy, compiledVite)
	secret := privateRuntimeSecret(t, f, domain.Resource{ProjectID: f.environment.ProjectID}, username, password)
	var ownerID string
	if err := f.db.QueryRow(t.Context(), `SELECT id FROM users WHERE email='browser-bootstrap@example.local'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	patterns := []string{password, username + ":" + password, base64.StdEncoding.EncodeToString([]byte(username + ":" + password)), url.QueryEscape(password), url.PathEscape(password), f.credential, f.admin}
	var failureEnabled atomic.Bool
	var diagnosticOnce sync.Once
	var recordsMu sync.Mutex
	var buildIDs []string
	var deploymentIDs []string
	var effectivePort atomic.Int32
	var cliOutput []byte
	f.request(t, "POST", "/api/v1/project-members", f.admin, map[string]string{"project_id": f.environment.ProjectID, "email": "viewer@example.local", "role": "viewer"}, 200, nil)
	// Infrastructure setup is separate from the browser's build/deploy/route journey.
	proxy := controlJSON[domain.HostingProxy](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: f.environment.ProjectID, IdempotencyKey: "browser-enable-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	controlJSON[domain.HostingHostnameGrant](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: f.environment.ProjectID, Hostname: "local.example.test"}, 201)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	runProxy := func() error {
		return RunContext(ctx, Config{Build: &f.config, LocalBuildDeploy: true, Proxy: proxyConfig, Server: f.server.URL, Token: f.credential, RunnerID: f.config.Target.RunnerID, WorkDir: f.work, JournalDir: f.journal, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond})
	}
	// Wait for real River dispatch before asking the authenticated runner to claim.
	waitDispatch := func(kind, id string) error {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			switch kind {
			case "build":
				b, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", id)
				if err != nil {
					return err
				}
				if b.RunID != "" {
					return nil
				}
			case "deploy":
				op, err := f.pg.GetHostingOperation(ctx, id)
				if err != nil {
					return err
				}
				if op.RunID != "" {
					return nil
				}
			default:
				if id == "" {
					current, err := f.pg.GetHostingProxy(ctx, proxy.ID)
					if err != nil {
						return err
					}
					id = current.ActiveOperationID
				}
				op, err := f.pg.GetHostingProxyOperation(ctx, id)
				if err == nil && op.RunID != "" {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("browser fixture dispatch timeout for %s", kind)
			case <-tick.C:
			}
		}
	}
	// River makes the initial proxy operation active when its run is dispatched.
	initialProxy, err := f.pg.GetHostingProxy(ctx, proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = waitDispatch("proxy", initialProxy.ActiveOperationID); err != nil {
		t.Fatal(err)
	}
	if err = runProxy(); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	defer transport.CloseIdleConnections()
	compiledClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var compiledExpected *compiledViteEvidence
	if compiledVite {
		transport.TLSClientConfig.ServerName = "local.example.test"
	}
	probe := func() {
		if compiledVite {
			evidence, e := observeCompiledVite(t.Context(), compiledClient)
			if e != nil {
				t.Fatal(e)
			}
			if compiledExpected != nil && *compiledExpected != evidence {
				t.Fatal("failed source replaced compiled HTTPS artifact")
			}
			compiledExpected = &evidence
			t.Logf("verified fixture-CA HTTPS compiled bundle=%s sha256=%s deep_link=200 missing.js=200 HTML", evidence.BundlePath, evidence.BundleSHA256)
			return
		}
		response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://local.example.test/")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil || response.StatusCode != 200 || string(body) != "local-A\n" {
			t.Fatalf("browser HTTPS actual response %d %q err=%v", response.StatusCode, body, err)
		}
	}
	// Record only fixed planner container names, never argv, environment or output.
	// This invocation is scoped to the immutable planner image in this owned DinD.
	eventsCtx, stopEvents := context.WithCancel(ctx)
	var plannerEvents bytes.Buffer
	events := exec.CommandContext(eventsCtx, "/usr/local/bin/docker", "events", "--filter", "image="+f.config.Engine.PlannerImage, "--filter", "event=start", "--format", "{{.Actor.Attributes.name}}")
	events.Stdout, events.Stderr = &plannerEvents, io.Discard
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	eventsDone := make(chan struct{})
	go func() { defer close(eventsDone); _ = events.Wait() }()
	defer func() { stopEvents(); <-eventsDone }()
	type work struct{ kind, id string }
	queue := make(chan work, 1)
	results := make(chan error, 1)
	var seen sync.Map
	done := make(chan struct{})
	var finish sync.Once
	nonce := make([]byte, 24)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	completePath := "/api/v1/fixture/private-browser/" + hex.EncodeToString(nonce) + "/complete"
	failurePath := strings.TrimSuffix(completePath, "/complete") + "/fail-source"
	compiledProbePath := strings.TrimSuffix(completePath, "/complete") + "/compiled-probe"
	compiledPortPath := strings.TrimSuffix(completePath, "/complete") + "/compiled-port"
	appOrigin := ""
	if compiledVite {
		appOrigin = os.Getenv("NEROCD_PRIVATE_GIT_APP_ORIGIN")
		parsed, e := url.Parse(appOrigin)
		if e != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatal("compiled bridge origin must be owned loopback")
		}
		appListener, e := net.Listen("tcp4", "0.0.0.0:18081")
		if e != nil {
			t.Fatal(e)
		}
		bridge := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 7 * time.Second, Handler: compiledViteReadBridge(compiledClient)}
		bridgeDone := make(chan error, 1)
		go func() { bridgeDone <- bridge.Serve(appListener) }()
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_ = bridge.Shutdown(cleanup)
			if e := <-bridgeDone; e != nil && e != http.ErrServerClosed {
				t.Error(e)
			}
		})
	}
	stages := make(chan chan struct{})
	origin := os.Getenv("NEROCD_LOCAL_BUILD_BROWSER_ORIGIN")
	handler := f.server.Config.Handler
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == completePath || r.URL.Path == failurePath || compiledVite && (r.URL.Path == compiledProbePath || r.URL.Path == compiledPortPath) {
			cookie, cookieErr := r.Cookie("nerocd_session")
			authorized := false
			if cookieErr == nil && r.Header.Get("Authorization") == "" {
				check := httptest.NewRequestWithContext(r.Context(), "GET", "/api/v1/me", nil)
				check.AddCookie(cookie)
				recorded := httptest.NewRecorder()
				handler.ServeHTTP(recorded, check)
				var principal auth.Principal
				authorized = recorded.Code == 200 && json.Unmarshal(recorded.Body.Bytes(), &principal) == nil && principal.ID == ownerID
			}
			if r.Method != "POST" || r.Header.Get("Origin") != origin || !authorized {
				w.WriteHeader(http.StatusForbidden)
				return
			}

			if compiledVite && r.URL.Path == compiledPortPath {
				if r.Header.Get("X-NeroCD-CSRF") != "1" || r.ContentLength != 0 {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				recordsMu.Lock()
				ids := append([]string(nil), buildIDs...)
				recordsMu.Unlock()
				if len(ids) != 1 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				full, e := f.pg.GetHostingBuild(r.Context(), "usr_bootstrap", ids[0])
				if e != nil || full.Artifact == nil || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(full.Artifact.ImageID) {
					w.WriteHeader(http.StatusConflict)
					return
				}
				plan, e := os.ReadFile(filepath.Join(f.config.Engine.StateDir, ids[0], "context", "railpack-plan.json"))
				if e != nil || len(plan) > 1<<20 || !bytes.Contains(plan, []byte(":{$PORT:80}")) {
					w.WriteHeader(http.StatusConflict)
					return
				}
				inspect, stop := context.WithTimeout(r.Context(), 3*time.Second)
				defer stop()
				raw, e := exec.CommandContext(inspect, "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "image", "inspect", "--format", `{{range .Config.Env}}{{if eq (index (split . "=") 0) "PORT"}}{{println .}}{{end}}{{end}}`, full.Artifact.ImageID).Output()
				if e != nil || len(raw) > 128 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				port, e := compiledVitePort(string(raw))
				if e != nil {
					w.WriteHeader(http.StatusConflict)
					return
				}
				effectivePort.Store(int32(port))
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]int{"application_port": port})
				return
			}
			if compiledVite && r.URL.Path == compiledProbePath {
				if r.Header.Get("X-NeroCD-CSRF") != "1" || r.ContentLength != 0 {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				evidence, e := observeCompiledVite(r.Context(), compiledClient)
				if e != nil {
					http.Error(w, "compiled fixture unavailable", http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(evidence)
				return
			}
			if r.URL.Path == failurePath {
				acknowledgement := make(chan struct{})
				select {
				case stages <- acknowledgement:
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				}
				select {
				case <-acknowledgement:
					w.WriteHeader(http.StatusNoContent)
				case <-ctx.Done():
				case <-r.Context().Done():
				}
				return
			}
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/builds") && !strings.HasSuffix(r.URL.Path, "/deployments/from-build") && !strings.HasSuffix(r.URL.Path, "/routes") {
			handler.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, r)
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
		if recorded.Code != http.StatusAccepted {
			return
		}
		item := work{}
		if strings.HasSuffix(r.URL.Path, "/builds") {
			var response struct {
				Build domain.PublicHostingBuild `json:"build"`
			}
			if json.Unmarshal(recorded.Body.Bytes(), &response) != nil {
				return
			}
			item = work{"build", response.Build.ID}
			recordsMu.Lock()
			buildIDs = append(buildIDs, response.Build.ID)
			recordsMu.Unlock()
		} else if strings.HasSuffix(r.URL.Path, "/from-build") {
			var op domain.HostingOperation
			if json.Unmarshal(recorded.Body.Bytes(), &op) != nil {
				return
			}
			item = work{"deploy", op.ID}
			recordsMu.Lock()
			deploymentIDs = append(deploymentIDs, op.ID)
			recordsMu.Unlock()
		} else {
			var route domain.HostingRoute
			if json.Unmarshal(recorded.Body.Bytes(), &route) != nil {
				return
			}
			item = work{"proxy", route.OperationID}
		}
		if _, duplicate := seen.LoadOrStore(item.id, true); duplicate {
			return
		}
		select {
		case queue <- item:
		case <-ctx.Done():
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-queue:
				err := waitDispatch(item.kind, item.id)
				if err == nil && item.kind == "build" {
					full, readErr := f.pg.GetHostingBuild(ctx, "usr_bootstrap", item.id)
					if readErr != nil {
						err = readErr
					} else {
						policy := full.Snapshot.Source.Policy
						if policy.Mode != "internal" || !policy.AllowInternal || len(policy.AllowedHosts) != 1 || policy.AllowedHosts[0] != "example.com" || len(policy.AllowedCIDRs) != 1 || policy.AllowedCIDRs[0] != "127.0.0.0/8" {
							err = fmt.Errorf("fixture edited source policy mismatch before compiled build")
						}
					}
				}
				if err == nil && item.kind == "proxy" {
					err = runProxy()
				} else if err == nil {
					// Build and local workload execution use the compiled production CLI.
					command := f.agentCommand(ctx, "--local-build-deploy", "--proxy-state-root", root, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion)
					var stopResource func() string
					if compiledVite && os.Getenv("NEROCD_PRIVATE_GIT_COMPILED_DIAGNOSTIC") == "1" {
						stopResource = compiledResourceObserver(ctx, f.config.Engine)
					}
					out, runErr := command.CombinedOutput()
					resourceEvidence := ""
					if stopResource != nil {
						resourceEvidence = stopResource()
					}
					recordsMu.Lock()
					cliOutput = append(cliOutput, out...)
					recordsMu.Unlock()
					credentialLeak := false
					for _, pattern := range patterns {
						if strings.Contains(string(out), pattern) {
							credentialLeak = true
							runErr = fmt.Errorf("known credential in CLI output")
							out = nil
						}
					}
					err = runErr
					if err != nil && !credentialLeak && item.kind == "build" && failureEnabled.Load() {
						full, readErr := f.pg.GetHostingBuild(ctx, "usr_bootstrap", item.id)
						if readErr == nil && full.State == "failed" && full.Artifact == nil && full.FailureCode == "build_failed" {
							err = nil
						}
					}
					if err != nil {
						if item.kind == "build" {
							if compiledVite && os.Getenv("NEROCD_PRIVATE_GIT_COMPILED_DIAGNOSTIC") == "1" {
								t.Logf("unexpected compiled CLI failure resource evidence: %s", resourceEvidence)
								diagnosticOnce.Do(func() {
									full, e := f.pg.GetHostingBuild(ctx, "usr_bootstrap", item.id)
									if e == nil {
										compiledEngineDiagnostic(t, ctx, f, full.Snapshot, patterns)
									}
								})
							}
							if compiledVite {
								planFile, openErr := os.Open(filepath.Join(f.config.Engine.StateDir, item.id, "context", "railpack-plan.json"))
								if openErr == nil {
									raw, readErr := io.ReadAll(io.LimitReader(planFile, (1<<20)+1))
									_ = planFile.Close()
									if readErr == nil && len(raw) <= 1<<20 {
										var summary struct {
											Steps []struct {
												Name string `json:"name"`
											} `json:"steps"`
										}
										_ = json.Unmarshal(raw, &summary)
										var names []string
										for _, step := range summary.Steps {
											if len(names) < 20 && regexp.MustCompile(`^[a-z0-9_:-]{1,64}$`).MatchString(step.Name) {
												names = append(names, step.Name)
											}
										}
										t.Logf("bounded compiled plan diagnostic caddy=%t dist=%t node_pin=%t bun_pin=%t caddy_pin=%t steps=%v", bytes.Contains(raw, []byte("/Caddyfile")), bytes.Contains(raw, []byte("dist")), bytes.Contains(raw, []byte("22.22.0")), bytes.Contains(raw, []byte("1.3.6")), bytes.Contains(raw, []byte("2.10.2")), names)
									}
								}
								if info, e := os.Stat(filepath.Join(f.config.Engine.StateDir, item.id, "image.oci.tar")); e == nil {
									t.Logf("bounded compiled export bytes=%d limit=%d", info.Size(), f.config.Policy.Limits.ExportBytes)
								}
							}
							diagnosticsCtx, stop := context.WithTimeout(ctx, 5*time.Second)
							var tail []domain.HostingBuildLogItem
							cursor := 0
							for page := 0; page < 10; page++ {
								output, readErr := f.pg.ListHostingBuildLogs(diagnosticsCtx, "usr_bootstrap", item.id, cursor, 200)
								if readErr != nil {
									break
								}
								tail = append(tail, output.Items...)
								if len(tail) > 20 {
									tail = tail[len(tail)-20:]
								}
								if output.NextSequence <= cursor {
									break
								}
								cursor = output.NextSequence
							}
							stop()
							for _, row := range tail {
								message := row.Message
								for _, pattern := range patterns {
									message = strings.ReplaceAll(message, pattern, "[REDACTED fixture credential]")
								}
								t.Logf("bounded typed build diagnostic sequence=%d component=%s: %.2000s", row.Sequence, row.Component, message)
							}
						}
						t.Logf("bounded browser CLI diagnostic: %.1500s", out)
					}
				}
				if err != nil {
					select {
					case results <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-workerDone })
	metadata, err := json.Marshal(map[string]any{"origin": origin, "project_id": f.environment.ProjectID, "environment": f.environment.Name, "complete_path": completePath, "secret_id": secret.ID, "secret_version": int64(1), "source_repository_url": repositoryURL, "strategy": strategy, "destination_id": f.target.Destination.ID, "failure_path": failurePath, "compiled_vite": compiledVite, "compiled_diagnostic": compiledVite && os.Getenv("NEROCD_PRIVATE_GIT_COMPILED_DIAGNOSTIC") == "1", "compiled_probe_path": compiledProbePath, "application_origin": appOrigin, "compiled_port_path": compiledPortPath})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PRIVATE_GIT_BROWSER_READY %s\n", metadata)
journey:
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case err = <-results:
			t.Fatal(err)
		case ack := <-stages:
			recordsMu.Lock()
			count := len(buildIDs)
			recordsMu.Unlock()
			if failureEnabled.Load() || count != 1 {
				t.Fatal("unexpected private fixture stage order")
			}
			probe()
			updateGit("local-B\n", true)
			failureEnabled.Store(true)
			close(ack)
		case <-done:
			break journey
		}
	}
	cancel()
	<-workerDone
	<-eventsDone
	recordsMu.Lock()
	ids := append([]string(nil), buildIDs...)
	output := append([]byte(nil), cliOutput...)
	recordsMu.Unlock()
	if len(ids) != 2 || !failureEnabled.Load() {
		t.Fatal("browser did not complete success and genuine failure builds")
	}
	for i, id := range ids {
		full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && compiledVite {
			if len(deploymentIDs) != 1 || full.Artifact == nil {
				t.Fatal("compiled deployment missing")
			}
			operation, e := f.pg.GetHostingOperation(t.Context(), deploymentIDs[0])
			if e != nil || operation.State != "succeeded" || operation.Release == nil || operation.Release.LocalBuild != *full.Artifact || operation.Snapshot.LocalBuild.Artifact != *full.Artifact || operation.Snapshot.ContainerPort != int(effectivePort.Load()) || effectivePort.Load() == 0 || full.Artifact.DaemonID != strings.TrimSpace(string(controlDocker(t, "info", "--format", "{{.ID}}"))) || full.Artifact.DaemonID != full.DaemonID || full.Artifact.Platform != f.config.Policy.Platform {
				t.Fatal("compiled deployment changed artifact daemon/platform")
			}
			t.Logf("compiled artifact image=%s daemon=%s platform=%s exact_release=true application_port=%d", full.Artifact.ImageID, full.Artifact.DaemonID, full.Artifact.Platform, effectivePort.Load())
		}
		if i == 0 && (full.State != "succeeded" || full.Artifact == nil) {
			t.Fatal("browser success artifact missing")
		}
		if i == 1 && (full.State != "failed" || full.Artifact != nil || full.FailureCode != "build_failed") {
			t.Fatal("genuine failed build published an artifact or unexpected status")
		}
		privateRuntimeNoSecrets(t, f, id, patterns...)
		if i == 1 && compiledVite {
			// A generic build_failed status also covers transport/resource errors.
			// Require the source-owned invalid syntax to reach the real compiler.
			var compilerOutput strings.Builder
			cursor := 0
			for page := 0; page < 10; page++ {
				logs, e := f.pg.ListHostingBuildLogs(t.Context(), "usr_bootstrap", id, cursor, 200)
				if e != nil {
					t.Fatal("compiled syntax failure output unavailable")
				}
				for _, row := range logs.Items {
					compilerOutput.WriteString(row.Message)
				}
				if logs.NextSequence <= cursor {
					break
				}
				cursor = logs.NextSequence
			}
			text := compilerOutput.String()
			if !compiledViteSyntaxDiagnostic(text) {
				t.Fatal("failed compiled build lacks genuine source syntax diagnostic")
			}
			t.Log("compiled failure proved by typed compiler error at genuineSyntaxFailure; no artifact published")
		}
		if i == 0 && strategy == "railpack" {
			plan, err := os.ReadFile(filepath.Join(f.config.Engine.StateDir, id, "context", "railpack-plan.json"))
			if err != nil || !json.Valid(plan) {
				t.Fatal("actual pinned planner output plan missing or invalid")
			}
			if compiledVite {
				for _, required := range []string{"22.22.0", "1.3.6", "2.10.2", "bun install --frozen-lockfile", "bun run build", "dist", "/Caddyfile"} {
					if !bytes.Contains(plan, []byte(required)) {
						t.Fatalf("resolved compiled plan missing %q", required)
					}
				}
				t.Logf("compiled resolved plan Node=22.22.0 Bun=1.3.6 Caddy=2.10.2 sourceVite=8.2.2 lock_sha256=%x", sha256.Sum256([]byte(compiledViteLock)))
			}
			starts := strings.Fields(plannerEvents.String())
			if len(starts) != 2 {
				t.Fatal("pinned planner success/failure invocations missing")
			}
			for _, name := range starts {
				if !strings.HasPrefix(name, "nerocd-planner-") || len(name) != len("nerocd-planner-")+32 {
					t.Fatal("unexpected planner event identity")
				}
			}
			for _, pattern := range patterns {
				if strings.Contains(string(plan), pattern) {
					t.Fatal("known credential in planner output plan")
				}
			}
			t.Logf("actual pinned planner image=%s toolchain=%s starts=%d valid_plan_sha256=%x", f.config.Engine.PlannerImage, f.config.Policy.ToolchainIdentity, len(starts), sha256.Sum256(plan))
		}
		var typed domain.HostingBuildLogPage
		f.request(t, "GET", "/api/v1/hosting-builds/"+id+"/logs?limit=200", f.admin, nil, 200, &typed)
		raw, err := json.Marshal(typed)
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			if strings.Contains(string(raw), pattern) || strings.Contains(string(output), pattern) {
				t.Fatal("known credential reached typed output or compiled CLI")
			}
		}
	}
	probe()
	t.Logf("private %s browser: actual successful artifact/HTTPS, genuine failed build without artifact preserves old HTTPS bytes; known credentials absent logs/audit/journal/typed output/CLI; source cleanup passed", strategy)
}
