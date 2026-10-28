package runner

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/source"
)

type checkoutTransport struct {
	lookup    func(context.Context, string, string) ([]netip.Addr, error)
	available func(context.Context) bool
}

// ExecuteCheckout performs plan in a fresh directory beneath workRoot. Existing
// destinations are never removed or reused. The caller owns attempt cleanup.
func ExecuteCheckout(ctx context.Context, plan domain.CheckoutPlan, workRoot string, emit func(ProcessEvent)) (string, error) {
	return executeCheckout(ctx, plan, workRoot, emit, checkoutTransport{net.DefaultResolver.LookupNetIP, source.GitCurlResolveAvailable})
}

func executeCheckout(ctx context.Context, plan domain.CheckoutPlan, workRoot string, emit func(ProcessEvent), transport checkoutTransport) (string, error) {
	repoURL := strings.TrimSpace(plan.Repository.URL)
	if err := source.ValidateRepositoryURL(repoURL); err != nil {
		return "", err
	}
	u, err := url.Parse(repoURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("checkout requires a credential-free HTTP(S) repository URL")
	}
	ref := strings.TrimSpace(plan.Repository.Ref)
	if ref == "" {
		ref = "HEAD"
	}
	if len(ref) > 512 || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n\t ") {
		return "", errors.New("checkout repository ref is invalid")
	}
	dest := strings.TrimSpace(plan.DestPath)
	if dest == "" {
		dest = "workspace"
	}
	dest, err = cleanRelativePath(dest, "checkout dest_path")
	if err != nil {
		return "", err
	}
	ips, err := transport.lookup(ctx, "ip", u.Hostname())
	if err != nil || len(ips) == 0 {
		return "", errors.New("repository DNS policy rejected source")
	}
	// Admit every answer, then pass one literal address to the shared Git
	// transport. Git cannot resolve the hostname again or follow redirects.
	for _, ip := range ips {
		if !ip.IsValid() || (source.RepositoryPolicy{}).ValidateAddress(ip) != nil {
			return "", errors.New("repository DNS policy rejected source")
		}
	}
	if _, err := source.GitCurlResolveOption(u, ips[0]); err != nil {
		return "", err
	}
	if !transport.available(ctx) {
		return "", errors.New("installed Git lacks http.curloptResolve; refusing uncontrolled checkout")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	workRoot = strings.TrimSpace(workRoot)
	if workRoot == "" {
		return "", errors.New("checkout workspace is required")
	}
	if err := os.MkdirAll(workRoot, 0700); err != nil {
		return "", err
	}
	workRoot, err = filepath.Abs(workRoot)
	if err != nil {
		return "", err
	}
	workRoot, err = filepath.EvalSymlinks(workRoot)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(workRoot)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := createCheckoutDestination(root, dest); err != nil {
		return "", err
	}
	destPath := filepath.Join(workRoot, dest)
	complete := false
	defer func() {
		if !complete {
			_ = root.RemoveAll(dest)
		}
	}()
	configDir, err := os.MkdirTemp(workRoot, "nerocd-checkout-config-")
	if err != nil {
		return "", err
	}
	defer func() { _ = root.RemoveAll(filepath.Base(configDir)) }()
	environment := map[string]string{
		"HOME": configDir, "XDG_CONFIG_HOME": configDir,
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_GLOBAL": "/dev/null",
		"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "/bin/false", "GIT_ALLOW_PROTOCOL": "https:http",
	}
	if emit != nil {
		emit(ProcessEvent{Stream: domain.LogSystem, Message: "Runner checking out repository"})
	}
	_, err = source.ControlledGitCheckout(ctx, destPath, u, ips[0].Unmap(), ref, func(ctx context.Context, stage string, args []string, dir string) ([]byte, error) {
		if dir == "" {
			dir = configDir
		}
		var head strings.Builder
		// Git diagnostics may contain credentials. Only the bounded commit ID
		// reaches the caller; no transport output becomes a runner event.
		result, err := ExecuteProcess(ctx, domain.ProcessSpec{Command: append([]string{"git"}, args...), WorkingDir: dir, Environment: environment}, func(event ProcessEvent) {
			if stage == "git rev-parse" && event.Stream == domain.LogStdout && head.Len()+len(event.Message) <= 256 {
				head.WriteString(event.Message)
			}
		})
		if err != nil || result.ExitCode != 0 || result.Canceled || result.TimedOut {
			return nil, errors.New("repository checkout failed")
		}
		return []byte(head.String()), nil
	})
	if err != nil {
		return "", err
	}
	complete = true
	return destPath, nil
}

func createCheckoutDestination(root *os.Root, dest string) error {
	parts := strings.Split(dest, string(filepath.Separator))
	parent := ""
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		if err := root.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkout destination has an unsafe ancestor")
		}
	}
	return root.Mkdir(dest, 0700)
}
