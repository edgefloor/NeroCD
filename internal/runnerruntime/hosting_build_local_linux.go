//go:build linux

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func validateBuildSourceRoot(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("build source root has unsafe ancestors")
		}
		if p == path {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
				return errors.New("build source root must be owned mode 0700")
			}
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func lockBuildSource(root, id string) (func(), error) {
	if err := validateBuildSourceRoot(root); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(root, ".build-"+id+".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("build source reservation unavailable")
	}
	f := os.NewFile(uintptr(fd), "build reservation")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = f.Close()
		return nil, errors.New("unsafe build reservation")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		_ = f.Close()
		return nil, errors.New("unowned build reservation")
	}
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		_ = f.Close()
		return nil, errors.New("build already reserved")
	}
	return func() { _ = f.Close() }, nil
}
func checkoutPublicBuild(ctx context.Context, s domain.HostingBuildSnapshot, path, frozen string, accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
	return checkoutBuildWithCredential(ctx, s, path, frozen, nil, accept, use)
}
func checkoutBuildWithCredential(ctx context.Context, s domain.HostingBuildSnapshot, path, frozen string, credential func(context.Context) (SourceCredential, error), accept func(context.Context, domain.HostingBuildSourceReceipt) error, use func(context.Context, string) error) error {
	l := s.Policy.SourceLimits
	var useErr error
	_, err := WithSourceCheckout(ctx, SourceCheckoutRequest{BuildID: s.BuildID, SourceID: s.Source.ID, InputID: s.InputID, RepositoryURL: s.Source.RepositoryURL, RequestedRef: s.Source.RequestedRef, FrozenCommit: frozen, Policy: s.Source.Policy, CheckoutPath: path, Limits: SourceCheckoutLimits{MaxTreeBytes: l.MaxTreeBytes, MaxWorkspaceBytes: l.MaxWorkspaceBytes, MinFreeBytes: l.MinFreeBytes, MaxEntries: l.MaxEntries, MaxDepth: l.MaxDepth, PollInterval: l.PollInterval}}, credential, func(c context.Context, r SourceReceipt) error {
		return accept(c, domain.HostingBuildSourceReceipt{BuildID: r.BuildID, SourceID: r.SourceID, InputID: r.InputID, Commit: r.Commit, PolicyHash: r.PolicyHash, TreeDigest: r.TreeDigest})
	}, func(c context.Context, p string, _ SourceReceipt) error {
		// Preserve the private runtime outcome only after source monitoring and
		// cleanup succeed. WithSourceCheckout otherwise redacts use errors.
		useErr = use(c, p)
		return nil
	})
	if err != nil {
		return err
	}
	return useErr
}

type buildObserverOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	cancel    context.CancelFunc
	remaining int
}

func (w *buildObserverOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > w.remaining {
		w.cancel()
		return 0, errors.New("daemon output exceeds bound")
	}
	w.remaining -= len(p)
	return w.data.Write(p)
}
func observeBuildDaemon(ctx context.Context, c BuildConfig) (id string, result error) {
	run, cancel := context.WithTimeout(ctx, c.RequestTimeout)
	defer cancel()
	dir, err := os.MkdirTemp(c.SourceRoot, ".build-observer-")
	if err != nil {
		return "", errors.New("daemon observer workspace unavailable")
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			id = ""
			result = errors.New("daemon observer cleanup unconfirmed")
		}
	}()
	if os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0600) != nil {
		return "", errors.New("daemon observer config unavailable")
	}
	cmd := exec.CommandContext(run, c.Engine.DockerBinary, "--host", "unix://"+c.Engine.Socket, "--config", dir, "info", "--format", "{{json .}}")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "DOCKER_CONFIG=" + dir, "DOCKER_HOST=unix://" + c.Engine.Socket, "NO_COLOR=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	out := &buildObserverOutput{remaining: 64 << 10, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = out
	if cmd.Run() != nil || run.Err() != nil {
		return "", errors.New("fixed daemon observation failed")
	}
	var info struct {
		ID, OSType, CgroupVersion, DockerRootDir string
		SecurityOptions                          []string
	}
	if json.Unmarshal(out.data.Bytes(), &info) != nil || info.OSType != "linux" || info.CgroupVersion != "2" || !filepath.IsAbs(info.DockerRootDir) {
		return "", errors.New("unsupported local build daemon")
	}
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "rootless") {
			return "", errors.New("rootful build daemon required")
		}
	}
	if _, err = os.Stat(info.DockerRootDir); err != nil {
		return "", errors.New("daemon storage visibility required")
	}
	if (hostingbuildprotocol.PrepareRequest{Authority: domain.HostingBuildAuthority{BuildID: "observer", RunID: "build_run_observer", LeaseID: "probe", Attempt: 1, Fence: "probe"}, ExpectedRevision: 1, DaemonID: info.ID}).Validate() != nil {
		return "", errors.New("invalid observed daemon identity")
	}
	return info.ID, nil
}
