package runnerruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
)

// readBuildCompose reads only the configured tracked file from the immutable
// checkout. Reject links component by component before the rooted open, even
// when a link would otherwise remain within the checkout.
func readBuildCompose(ctx context.Context, checkout string, settings domain.HostingBuildSettings) (domain.HostingStackSource, error) {
	var zero domain.HostingStackSource
	if ctx.Err() != nil || settings.Strategy != "compose" || domain.ValidateHostingBuildSettings(settings) != nil {
		return zero, errors.New("invalid Compose source settings")
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return zero, errors.New("compose checkout unavailable")
	}
	defer func() { _ = root.Close() }()
	name := path.Join(settings.ContextDir, settings.ComposeFile)
	current := ""
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		current = path.Join(current, part)
		info, e := root.Lstat(current)
		if e != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return zero, errors.New("compose file unavailable")
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return zero, errors.New("compose file unavailable")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 128<<10 {
		return zero, errors.New("compose file exceeds managed source limits")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
	if err != nil || len(raw) < 1 || len(raw) > 128<<10 {
		return zero, errors.New("compose file unavailable")
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return domain.HostingStackSource{Original: string(raw), PublicComponent: settings.PublicComponent, ContainerPort: settings.ContainerPort}, nil
}

// A closed checkout failure can settle only after the acquisition helper has
// confirmed cleanup. Receipt/authority uncertainty still requires replay.
func settledComposeSourceFailure(err error, checkout, state string) bool {
	var failure *SourceCheckoutError
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Code {
	case "fetch_failed", "dns_policy", "transport_unavailable", "credential_denied", "credential_required", "credential_expired", "invalid_credential", "unsupported_tree", "tree_limit", "workspace_limit", "workspace":
	default:
		return false
	}
	for _, name := range []string{checkout, filepath.Join(state, "cleanup_unconfirmed.json")} {
		if _, e := os.Lstat(name); !os.IsNotExist(e) {
			return false
		}
	}
	return true
}
