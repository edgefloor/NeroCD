package hostingbuild

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingRequest(src string, strategy Strategy) Request {
	return Request{BuildID: "settings", Checkout: src, SourceID: "source", InputID: "input", DaemonID: "daemon", Strategy: strategy, Platform: "linux/arm64", Limits: testLimits()}
}
func makeSettingDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
}
func TestSettingPathsAndStrategyBounds(t *testing.T) {
	for _, p := range []string{".", "..", "../outside", "/absolute", "a/../b", "a//b", "a\\b", "a\nb", ".git", "a/.env", "C:/outside", strings.Repeat("a", 513)} {
		t.Run(p, func(t *testing.T) {
			r := settingRequest("/source", Dockerfile)
			r.ContextDir = p
			if r.validate(context.Background()) == nil {
				t.Fatal("unsafe context path admitted")
			}
			r.ContextDir = ""
			r.DockerfilePath = p
			if r.validate(context.Background()) == nil {
				t.Fatal("unsafe Dockerfile path admitted")
			}
			r.Strategy = Static
			r.DockerfilePath = ""
			r.PublishDir = p
			if r.validate(context.Background()) == nil {
				t.Fatal("unsafe publish path admitted")
			}
		})
	}
	for _, target := range []string{"--target", "space name", "x;touch", "$(command)", strings.Repeat("x", 129)} {
		r := settingRequest("/source", Dockerfile)
		r.BuildTarget = target
		if r.validate(context.Background()) == nil {
			t.Fatal("unsafe target admitted")
		}
	}
	for _, strategy := range []Strategy{Static, Railpack} {
		r := settingRequest("/source", strategy)
		r.BuildTarget = "release"
		if r.validate(context.Background()) == nil {
			t.Fatal("target outside Dockerfile strategy")
		}
	}
	for _, strategy := range []Strategy{Dockerfile, Railpack} {
		r := settingRequest("/source", strategy)
		r.SPAFallback = true
		if r.validate(context.Background()) == nil {
			t.Fatal("SPA outside static strategy")
		}
	}
}
func TestSettingsRootConfinement(t *testing.T) {
	src := t.TempDir()
	makeSettingDir(t, filepath.Join(src, "app", "build"))
	writeTestFile(t, filepath.Join(src, "app", "build", "Release.Dockerfile"), []byte("FROM scratch\n"))
	writeTestFile(t, filepath.Join(src, "outside"), []byte("not selected"))
	r := settingRequest(src, Dockerfile)
	r.ContextDir = "app"
	r.DockerfilePath = "build/Release.Dockerfile"
	r.BuildTarget = "release-v1"
	if err := r.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceSettings(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "context")
	if _, err := stageRequestContext(context.Background(), r, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "outside")); !os.IsNotExist(err) {
		t.Fatal("outside selected context copied")
	}
	if err := os.Symlink("app", filepath.Join(src, "linked")); err != nil {
		t.Fatal(err)
	}
	r.ContextDir = "linked"
	if validateSourceSettings(context.Background(), r) == nil {
		t.Fatal("linked context admitted")
	}
	r.ContextDir = "app"
	if err := os.Symlink("Release.Dockerfile", filepath.Join(src, "app", "build", "linked")); err != nil {
		t.Fatal(err)
	}
	r.DockerfilePath = "build/linked"
	if validateSourceSettings(context.Background(), r) == nil {
		t.Fatal("linked Dockerfile admitted")
	}
	r.DockerfilePath = "build"
	if validateSourceSettings(context.Background(), r) == nil {
		t.Fatal("directory as Dockerfile admitted")
	}
}
func TestStaticPublishAssetsAndGeneratedIsolation(t *testing.T) {
	src := t.TempDir()
	publish := filepath.Join(src, "app", "dist")
	makeSettingDir(t, publish)
	writeTestFile(t, filepath.Join(publish, "index.html"), []byte("prepared SPA"))
	writeTestFile(t, filepath.Join(src, "private.txt"), []byte("outside publish"))
	for _, name := range []string{"Dockerfile", "nginx.conf", ".dockerignore", "compose.yaml", ".env"} {
		writeTestFile(t, filepath.Join(publish, name), []byte("private instruction"))
	}
	r := settingRequest(src, Static)
	r.ContextDir = "app"
	r.PublishDir = "dist"
	r.SPAFallback = true
	if err := validateSourceSettings(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "context")
	digest, err := stageRequestContext(context.Background(), r, dst, staticContextPolicy)
	if err != nil || !digestPattern.MatchString(digest) {
		t.Fatalf("stage static: %v", err)
	}
	packaging, file, err := prepareStaticContext(dst, true)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadDir(filepath.Join(packaging, "public"))
	if err != nil || len(public) != 1 || public[0].Name() != "index.html" {
		t.Fatalf("unexpected public assets %v err=%v", public, err)
	}
	b, err := os.ReadFile(filepath.Join(packaging, file))
	if err != nil || !strings.Contains(string(b), "COPY --chmod=0755 public/") {
		t.Fatal("static COPY isolation")
	}
	conf, err := os.ReadFile(filepath.Join(packaging, ".nerocd-static.conf"))
	if err != nil || !strings.Contains(string(conf), "/index.html") || !strings.Contains(string(conf), "try_files $uri =404") {
		t.Fatal("SPA config lacks asset404 rule")
	}
	if b, err = os.ReadFile(filepath.Join(publish, "Dockerfile")); err != nil || string(b) != "private instruction" {
		t.Fatal("source instruction overwritten")
	}
	writeTestFile(t, filepath.Join(publish, ".nerocd-static.conf"), []byte("collision"))
	if _, err = stageRequestContext(context.Background(), r, filepath.Join(t.TempDir(), "context"), staticContextPolicy); err == nil {
		t.Fatal("reserved static filename admitted")
	}
}
func TestLegacyRequestIdentityAndPolicyReplay(t *testing.T) {
	r := settingRequest("/immutable/source", Static)
	c := Config{StateDir: "/state"}
	// This field order and shape is the accepted pre-settings serializer.
	type legacyRequest struct {
		BuildID, Checkout, SourceID, InputID, DaemonID string
		Strategy                                       Strategy
		Platform                                       string
		Limits                                         Limits
	}
	legacy := legacyRequest{r.BuildID, r.Checkout, r.SourceID, r.InputID, r.DaemonID, r.Strategy, r.Platform, r.Limits}
	b, err := json.Marshal(struct {
		Request legacyRequest
		Config  Config
	}{legacy, c})
	if err != nil {
		t.Fatal(err)
	}
	got, err := requestIdentity(r, c, "")
	if err != nil || got != sum(b) {
		t.Fatal("zero-setting legacy request bytes changed")
	}
	current, err := requestIdentity(r, c, staticContextPolicy)
	if err != nil || current == got {
		t.Fatal("static policy omitted from identity")
	}
	for _, change := range []func(*Request){func(r *Request) { r.ContextDir = "app" }, func(r *Request) { r.PublishDir = "dist" }, func(r *Request) { r.SPAFallback = true }} {
		changed := r
		change(&changed)
		if _, err = requestIdentity(changed, c, ""); err == nil {
			t.Fatal("changed settings authorized legacy replay")
		}
		next, err := requestIdentity(changed, c, staticContextPolicy)
		if err != nil || next == current {
			t.Fatal("effective static setting missing from identity")
		}
	}
	if _, err = requestIdentity(r, c, "unknown"); err == nil {
		t.Fatal("unknown receipt policy admitted")
	}
	r.Strategy = Dockerfile
	baseline, err := requestIdentity(r, c, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){func(r *Request) { r.ContextDir = "app" }, func(r *Request) { r.DockerfilePath = "Build.Dockerfile" }, func(r *Request) { r.BuildTarget = "release" }} {
		changed := r
		change(&changed)
		next, err := requestIdentity(changed, c, "")
		if err != nil || next == baseline {
			t.Fatal("Dockerfile setting missing from identity")
		}
	}
}

func TestStaticDigestIgnoresExcludedDirectoryMetadata(t *testing.T) {
	src := t.TempDir()
	makeSettingDir(t, filepath.Join(src, "assets"))
	writeTestFile(t, filepath.Join(src, "assets", "site.js"), []byte("asset"))
	r := settingRequest(src, Static)
	first, err := stageRequestContext(context.Background(), r, filepath.Join(t.TempDir(), "context"), staticContextPolicy)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "assets", "private.conf"), []byte("excluded config"))
	second, err := stageRequestContext(context.Background(), r, filepath.Join(t.TempDir(), "context"), staticContextPolicy)
	if err != nil || first != second {
		t.Fatal("excluded configuration affected public asset digest")
	}
}
