//go:build unix

package runnerruntime

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"nerocd/internal/domain"
)

// materializeStoredDockerfile uses the same exclusive source reservation and
// accepted-source callback as checkout, without Git transport or ambient files.
func materializeStoredDockerfile(ctx context.Context, s domain.HostingBuildSnapshot, path, frozen string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) (result error) {
	if s.Source.Kind != "dockerfile" || domain.ValidateHostingGitSource(s.Source) != nil || domain.ValidateHostingBuildTriggerRevision(s) != nil || frozen != "" || accept == nil || use == nil {
		return sourceError("invalid_source")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filepath.Base(path) != s.BuildID || !filepath.IsAbs(path) {
		return sourceError("workspace")
	}
	// The build executor holds the per-build reservation. Ancestors are checked
	// separately so this helper also rejects symlink roots when called directly.
	rootInfo, rootErr := os.Lstat(filepath.Dir(path))
	if rootErr != nil {
		return sourceError("workspace")
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootStat.Uid != uint32(os.Geteuid()) || rootInfo.Mode().Perm() != 0700 {
		return sourceError("workspace")
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return sourceError("workspace")
		}
		if p == "/" {
			break
		}
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return sourceError("workspace")
	}
	defer func() {
		if os.RemoveAll(path) != nil {
			result = sourceError("cleanup_unconfirmed")
		}
	}()
	l := s.Policy.SourceLimits
	limits := SourceCheckoutLimits{MaxTreeBytes: l.MaxTreeBytes, MaxWorkspaceBytes: l.MaxWorkspaceBytes, MinFreeBytes: l.MinFreeBytes, MaxEntries: l.MaxEntries, MaxDepth: l.MaxDepth, PollInterval: l.PollInterval}
	if err := checkSourceWorkspace(ctx, path, filepath.Join(path, ".transport-unused"), limits); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(path, "Dockerfile"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return sourceError("workspace")
	}
	_, writeErr := file.WriteString(s.Source.Content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return sourceError("workspace")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	digest, err := sourceTreeDigest(ctx, path, limits)
	if err != nil {
		return err
	}
	receipt := domain.HostingBuildSourceReceipt{Kind: "dockerfile", ContentHash: s.Source.ContentHash, BuildID: s.BuildID, SourceID: s.Source.ID, InputID: s.InputID, TreeDigest: digest}
	if err := accept(ctx, receipt); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return use(ctx, path)
}
