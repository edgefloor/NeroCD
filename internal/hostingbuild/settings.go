package hostingbuild

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const staticContextPolicy = "static-assets-v1"

var buildArgNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var targetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (r Request) validateSettings() error {
	if r.InjectBuildArgs && r.Strategy != Dockerfile {
		return errors.New("ARG injection requires Dockerfile strategy")
	}
	if len(r.BuildArgs) > 64 || len(r.BuildArgs) > 0 && r.Strategy != Dockerfile {
		return errors.New("public build arguments require bounded Dockerfile strategy")
	}
	for name, value := range r.BuildArgs {
		upper := strings.ToUpper(name)
		if !buildArgNamePattern.MatchString(name) || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("invalid public build argument")
		}
		for _, prefix := range []string{"NEROCD_", "BUILDKIT_", "BUILDX_"} {
			if strings.HasPrefix(upper, prefix) {
				return errors.New("reserved build argument")
			}
		}
		switch upper {
		case "HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY", "BUILDPLATFORM", "BUILDOS", "BUILDARCH", "BUILDVARIANT", "TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT":
			return errors.New("reserved build argument")
		}
	}

	for _, p := range []string{r.ContextDir, r.DockerfilePath, r.PublishDir} {
		if !relativeSetting(p) {
			return errors.New("settings require canonical relative paths within checkout")
		}
	}
	if r.BuildTarget != "" && !targetPattern.MatchString(r.BuildTarget) {
		return errors.New("invalid bounded build target")
	}
	if r.Strategy != Dockerfile && (r.DockerfilePath != "" || r.BuildTarget != "") {
		return errors.New("dockerfile path and target require Dockerfile strategy")
	}
	if r.Strategy != Static && (r.PublishDir != "" || r.SPAFallback) {
		return errors.New("publish directory and SPA fallback require static strategy")
	}
	return nil
}

func requestRoot(r Request, assets bool) (*os.Root, error) {
	st, err := os.Lstat(r.Checkout)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("checkout must be a real directory")
	}
	checkout, err := os.OpenRoot(r.Checkout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = checkout.Close() }()
	selected, err := openSettingRoot(checkout, r.ContextDir)
	if err != nil {
		return nil, err
	}
	if assets {
		defer func() { _ = selected.Close() }()
		return openSettingRoot(selected, r.PublishDir)
	}
	return selected, nil
}
func validateSourceSettings(ctx context.Context, r Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := requestRoot(r, r.Strategy == Static)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if r.Strategy == Dockerfile {
		file := r.DockerfilePath
		if file == "" {
			file = "Dockerfile"
		}
		return regularSetting(root, file)
	}
	if r.Strategy == Static && r.SPAFallback {
		return regularSetting(root, "index.html")
	}
	return nil
}
func currentContextPolicy(r Request) string {
	if r.Strategy == Static {
		return staticContextPolicy
	}
	return ""
}
func requestIdentity(r Request, c Config, policy string) (string, error) {
	var err error
	c, err = registryIdentityConfig(c)
	if err != nil {
		return "", err
	}
	if policy != "" && (policy != staticContextPolicy || r.Strategy != Static) {
		return "", errors.New("unsupported context policy")
	}
	if policy == "" && r.Strategy == Static && (r.ContextDir != "" || r.PublishDir != "" || r.SPAFallback) {
		return "", errors.New("legacy static replay does not support changed settings")
	}
	b, err := json.Marshal(struct {
		Request       Request
		Config        Config
		ContextPolicy string `json:",omitempty"`
	}{r, c, policy})
	if err != nil {
		return "", err
	}
	return sum(b), nil
}
func stageRequestContext(ctx context.Context, r Request, dst, policy string) (string, error) {
	if policy != "" && (policy != staticContextPolicy || r.Strategy != Static) {
		return "", errors.New("unsupported context policy")
	}
	root, err := requestRoot(r, policy == staticContextPolicy)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return stageRoot(ctx, root, dst, r.Limits, policy == staticContextPolicy)
}
func staticInstruction(name string) bool {
	n := strings.ToLower(name)
	return n == "dockerfile" || strings.HasPrefix(n, "dockerfile.") || strings.HasSuffix(n, ".dockerfile") || n == ".dockerignore" || strings.HasSuffix(n, ".conf") || n == "compose.yaml" || n == "compose.yml" || n == "docker-compose.yaml" || n == "docker-compose.yml"
}
func prepareStaticContext(source string, spa bool) (string, string, error) {
	packaging := filepath.Join(filepath.Dir(source), "static-context")
	if err := os.Mkdir(packaging, 0700); err != nil {
		return "", "", err
	}
	if err := os.Rename(source, filepath.Join(packaging, "public")); err != nil {
		return "", "", err
	}
	config := "server {\n listen 80;\n server_name _;\n root /usr/share/nginx/html;\n index index.html;\n"
	if spa {
		config += " location ~ /[^/]*\\.[^/]*$ { try_files $uri =404; }\n location / { try_files $uri $uri/ /index.html; }\n"
	} else {
		config += " location / { try_files $uri $uri/ =404; }\n"
	}
	config += "}\n"
	filename := ".nerocd-static.Dockerfile"
	content := "FROM " + StaticImage + "\nRUN rm -rf /usr/share/nginx/html && mkdir -p /usr/share/nginx/html\nCOPY --chmod=0755 public/ /usr/share/nginx/html/\nCOPY .nerocd-static.conf /etc/nginx/conf.d/default.conf\nEXPOSE 80\n"
	if err := os.WriteFile(filepath.Join(packaging, filename), []byte(content), 0600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(packaging, ".nerocd-static.conf"), []byte(config), 0600); err != nil {
		return "", "", err
	}
	return packaging, filename, nil
}
