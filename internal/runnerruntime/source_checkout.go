//go:build unix

package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"nerocd/internal/source"
)

// SourceCheckoutLimits bounds acquisition and the exported tree. Disk usage is
// monitored, with possible overshoot between samples; it is not a filesystem quota.
type SourceCheckoutLimits struct {
	MaxTreeBytes, MaxWorkspaceBytes, MinFreeBytes int64
	MaxEntries, MaxDepth                          int
	PollInterval                                  time.Duration
}

// SourceCheckoutRequest names an immutable input and an exclusive, stable,
// operator-owned checkout path. Its parent must already exist with mode 0700.
type SourceCheckoutRequest struct {
	BuildID, SourceID, InputID                string
	RepositoryURL, RequestedRef, FrozenCommit string
	Policy                                    source.RepositoryPolicy
	CheckoutPath                              string
	Limits                                    SourceCheckoutLimits
}

// SourceCredential is a short-lived delivery lease, not revocation of a delivered
// PAT. The callback transfers ownership of Bytes to the adapter, which clears it.
type SourceCredential struct {
	Kind, Username string
	Bytes          []byte
	UseUntil       time.Time
}

// SourceReceipt contains no credentials or local workspace paths.
type SourceReceipt struct {
	BuildID, SourceID, InputID, Commit, PolicyHash, TreeDigest string
}

// SourceCheckoutError carries a fixed code suitable for durable metadata.
// Deliberately no underlying transport diagnostic is exposed.
type SourceCheckoutError struct{ Code string }

func (e *SourceCheckoutError) Error() string { return "source checkout: " + e.Code }
func sourceError(code string) error          { return &SourceCheckoutError{Code: code} }

// WithSourceCheckout acquires source under policy, asks accept to durably record
// its receipt, destroys transport material, then calls use. It removes the
// checkout before returning, including when a callback fails. Callbacks must
// obey ctx and must not retain or mutate the checkout after use returns.
// The policy and callbacks are trusted operator inputs; this is not hostile-code
// isolation. A deadline on ctx is mandatory.
func WithSourceCheckout(ctx context.Context, req SourceCheckoutRequest, credential func(context.Context) (SourceCredential, error), accept func(context.Context, SourceReceipt) error, use func(context.Context, string, SourceReceipt) error) (receipt SourceReceipt, result error) {
	u, err := validateSourceCheckout(ctx, req, accept != nil && use != nil)
	if err != nil {
		return receipt, err
	}
	ips, err := lookupRepositoryIP(ctx, "ip", u.Hostname())
	if err != nil || req.Policy.ValidateResolvedHost(u.Hostname(), netIPAddrs(ips)) != nil {
		return receipt, sourceError("dns_policy")
	}
	ip, err := selectPinnedAddress(req.Policy, ips)
	if err != nil {
		return receipt, sourceError("dns_policy")
	}
	if u.Scheme != "ssh" && !gitCurlResolveAvailable(ctx) {
		return receipt, sourceError("transport_unavailable")
	}
	if err = ctx.Err(); err != nil {
		return receipt, sourceError("canceled")
	}
	// Mkdir is exclusive; an interrupted acquisition is never silently reused.
	if err = os.Mkdir(req.CheckoutPath, 0700); err != nil {
		return receipt, sourceError("checkout_unavailable")
	}
	defer func() {
		if err := os.RemoveAll(req.CheckoutPath); err != nil {
			result = sourceError("cleanup_unconfirmed")
		}
	}()
	material, err := os.MkdirTemp(filepath.Dir(req.CheckoutPath), ".nerocd-source-transport-")
	if err != nil {
		return receipt, sourceError("workspace")
	}
	defer func() {
		if err := os.RemoveAll(material); err != nil {
			result = sourceError("cleanup_unconfirmed")
		}
	}()
	if err = checkSourceWorkspace(ctx, req.CheckoutPath, material, req.Limits); err != nil {
		return receipt, err
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	monitorDone := make(chan error, 1)
	go func() {
		monitorDone <- monitorSourceWorkspace(operation, req.CheckoutPath, material, req.Limits, cancel)
	}()
	defer func() {
		cancel()
		if err := <-monitorDone; err != nil {
			result = err
		}
	}()
	var cred SourceCredential
	if credential != nil {
		cred, err = credential(operation)
		defer clear(cred.Bytes)
		if err != nil {
			return receipt, sourceError("credential_denied")
		}
	}
	if req.Policy.CredentialReferenceID != "" && credential == nil {
		return receipt, sourceError("credential_required")
	}
	checkTransport := func() error {
		if operation.Err() != nil {
			return sourceError("canceled")
		}
		if credential != nil && !time.Now().Before(cred.UseUntil) {
			return sourceError("credential_expired")
		}
		return nil
	}
	if err = checkTransport(); err != nil {
		return receipt, err
	}
	// The lease also cancels an in-flight fetch, rather than checking only between commands.
	transportCtx := operation
	if credential != nil {
		var stop context.CancelFunc
		transportCtx, stop = context.WithDeadline(operation, cred.UseUntil)
		defer stop()
	}
	env, err := prepareSourceTransport(transportCtx, material, u, ip.String(), req.Policy, cred, credential != nil)
	if err != nil {
		return receipt, err
	}
	ref := req.RequestedRef
	if req.FrozenCommit != "" {
		ref = req.FrozenCommit
	}
	command := sourceCheckoutCommand{}
	commit, err := controlledGitCheckout(transportCtx, command, req.CheckoutPath, u, ip, ref, env)
	if err != nil {
		return receipt, sourceError("fetch_failed")
	}
	if req.FrozenCommit != "" && commit != req.FrozenCommit {
		return receipt, sourceError("commit_mismatch")
	}
	if err = checkTransport(); err != nil {
		return receipt, err
	}
	tracked, trackErr := command.RunEnvironment(transportCtx, "git", []string{"ls-files", "--stage", "-z"}, req.CheckoutPath, env)
	if trackErr != nil {
		return receipt, sourceError("fetch_failed")
	}
	for _, record := range strings.Split(string(tracked), "\x00") {
		if strings.HasPrefix(record, "160000 ") {
			return receipt, sourceError("unsupported_tree")
		}
	}
	// Git metadata is not an input to the builder and may contain transport routing.
	if err = os.RemoveAll(filepath.Join(req.CheckoutPath, ".git")); err != nil {
		return receipt, sourceError("cleanup_unconfirmed")
	}
	digest, err := sourceTreeDigest(operation, req.CheckoutPath, req.Limits)
	if err != nil {
		return receipt, err
	}
	policyJSON, _ := json.Marshal(req.Policy)
	policySum := sha256.Sum256(policyJSON)
	receipt = SourceReceipt{req.BuildID, req.SourceID, req.InputID, commit, "sha256:" + hex.EncodeToString(policySum[:]), digest}
	if err = checkTransport(); err != nil {
		return receipt, err
	}
	if err = accept(operation, receipt); err != nil {
		return receipt, sourceError("receipt_rejected")
	}
	if err = os.RemoveAll(material); err != nil {
		return receipt, sourceError("cleanup_unconfirmed")
	}
	clear(cred.Bytes)
	if err = checkTransport(); err != nil {
		return receipt, err
	}
	// Credential authority ends only after confirmed transport removal and byte
	// clearing. The use callback remains bounded by operation, not the retired
	// transport credential's delivery window.
	if err = use(operation, req.CheckoutPath, receipt); err != nil {
		return receipt, sourceError("use_failed")
	}
	if operation.Err() != nil {
		return receipt, sourceError("canceled")
	}
	return receipt, nil
}

func validateSourceCheckout(ctx context.Context, r SourceCheckoutRequest, callbacks bool) (*url.URL, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, sourceError("deadline_required")
	}
	if ctx.Err() != nil {
		return nil, sourceError("canceled")
	}
	for _, id := range []string{r.BuildID, r.SourceID, r.InputID} {
		if id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
			return nil, sourceError("invalid_input")
		}
	}
	l := r.Limits
	if !callbacks || len(r.RepositoryURL) > 8192 || !requestedGitRef(r.RequestedRef) || strings.ContainsAny(r.RequestedRef, ":*?[]\\~^") || strings.Contains(r.RequestedRef, "..") || strings.Contains(r.RequestedRef, "@{") || (r.FrozenCommit != "" && !isHexCommit(r.FrozenCommit)) || l.MaxTreeBytes <= 0 || l.MaxWorkspaceBytes < l.MaxTreeBytes || l.MinFreeBytes <= 0 || l.MaxEntries <= 0 || l.MaxEntries > 1000000 || l.MaxDepth <= 0 || l.MaxDepth > 256 || l.PollInterval < 10*time.Millisecond || l.PollInterval > time.Second {
		return nil, sourceError("invalid_input")
	}
	p := r.CheckoutPath
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return nil, sourceError("invalid_path")
	}
	for parent := filepath.Dir(p); ; parent = filepath.Dir(parent) {
		st, err := os.Lstat(parent)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, sourceError("invalid_path")
		}
		if parent == filepath.Dir(p) {
			stat, ok := st.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != os.Geteuid() || st.Mode().Perm()&0077 != 0 {
				return nil, sourceError("invalid_path")
			}
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		return nil, sourceError("checkout_unavailable")
	}
	u, err := r.Policy.ValidateURL(r.RepositoryURL, false)
	if err != nil {
		return nil, sourceError("url_policy")
	}
	if u.Port() != "" {
		n, e := strconv.Atoi(u.Port())
		if e != nil || n < 1 || n > 65535 {
			return nil, sourceError("url_policy")
		}
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return nil, sourceError("url_policy")
	}
	return u, nil
}

// sourceCheckoutCommand retains the shared process-group cancellation and fixed
// output ceiling. The explicit workspace ceiling also bounds Git's acquisition.
type sourceCheckoutCommand struct{}

func (c sourceCheckoutCommand) Run(ctx context.Context, name string, args []string, dir string) ([]byte, error) {
	return c.RunEnvironment(ctx, name, args, dir, nil)
}
func (c sourceCheckoutCommand) RunEnvironment(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return osProvenanceCommand{}.RunEnvironment(ctx, name, args, dir, env)
}

func prepareSourceTransport(ctx context.Context, dir string, u *url.URL, address string, p source.RepositoryPolicy, c SourceCredential, has bool) ([]string, error) {
	if !has {
		if u.Scheme == "ssh" {
			return nil, sourceError("credential_required")
		}
		return nil, nil
	}
	if len(c.Bytes) == 0 || len(c.Bytes) > 1<<20 {
		return nil, sourceError("invalid_credential")
	}
	if u.Scheme == "https" {
		if c.Kind != "https_basic" || c.Username == "" || len(c.Username) > 256 || strings.ContainsAny(c.Username, "\x00\r\n") || strings.ContainsAny(string(c.Bytes), "\x00\r\n") {
			return nil, sourceError("invalid_credential")
		}
		// Askpass reads owner-only files. Neither the secret nor username appears in argv/env.
		if os.WriteFile(filepath.Join(dir, "username"), []byte(c.Username), 0600) != nil || os.WriteFile(filepath.Join(dir, "password"), c.Bytes, 0600) != nil {
			return nil, sourceError("workspace")
		}
		script := "#!/bin/sh\ncase \"$1\" in *Username*) cat " + shellQuote(filepath.Join(dir, "username")) + ";; *Password*) cat " + shellQuote(filepath.Join(dir, "password")) + ";; *) exit 1;; esac\n"
		ask := filepath.Join(dir, "askpass")
		if os.WriteFile(ask, []byte(script), 0700) != nil {
			return nil, sourceError("workspace")
		}
		return []string{"GIT_ASKPASS=" + ask}, nil
	}
	if c.Kind != "ssh_private_key" || !strings.Contains(string(c.Bytes), "PRIVATE KEY-----") || strings.Contains(string(c.Bytes), "ENCRYPTED") {
		return nil, sourceError("invalid_credential")
	}
	username := c.Username
	if u.User != nil {
		if username != "" && username != u.User.Username() {
			return nil, sourceError("invalid_credential")
		}
		username = u.User.Username()
	}
	if username == "" {
		return nil, sourceError("invalid_credential")
	}
	userURL := *u
	userURL.User = url.User(username)
	if _, err := p.ValidateURL(userURL.String(), false); err != nil {
		return nil, sourceError("invalid_credential")
	}
	key := filepath.Join(dir, "identity")
	if os.WriteFile(key, c.Bytes, 0600) != nil {
		return nil, sourceError("workspace")
	}
	if _, err := (osProvenanceCommand{}).RunEnvironment(ctx, "ssh-keygen", []string{"-y", "-P", "", "-f", key}, dir, nil); err != nil {
		return nil, sourceError("invalid_credential")
	}
	ip, err := selectPinnedAddress(p, []netip.Addr{netip.MustParseAddr(address)})
	if err != nil {
		return nil, sourceError("dns_policy")
	}
	known, err := pinnedKnownHosts(ctx, osProvenanceCommand{}, u, ip, p.SSHHostFingerprints)
	if err != nil {
		return nil, sourceError("host_key")
	}
	hosts := filepath.Join(dir, "known_hosts")
	if os.WriteFile(hosts, known, 0600) != nil {
		return nil, sourceError("workspace")
	}
	environment, err := sshTransportEnvironment(u, ip, key, hosts, username)
	if err != nil {
		return nil, sourceError("invalid_credential")
	}
	return environment, nil
}

func monitorSourceWorkspace(ctx context.Context, checkout, material string, l SourceCheckoutLimits, cancel context.CancelFunc) error {
	tick := time.NewTicker(l.PollInterval)
	defer tick.Stop()
	for {
		if err := checkSourceWorkspace(ctx, checkout, material, l); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func checkSourceWorkspace(ctx context.Context, checkout, material string, l SourceCheckoutLimits) error {
	total := int64(0)
	for _, p := range []string{checkout, material} {
		_, n, err := scanSource(ctx, p, l, false)
		if err != nil {
			return sourceError("workspace_limit")
		}
		if n > l.MaxWorkspaceBytes-total {
			return sourceError("workspace_limit")
		}
		total += n
	}
	var stat unix.Statfs_t
	if unix.Statfs(filepath.Dir(checkout), &stat) != nil || uint64(stat.Bavail)*uint64(stat.Bsize) < uint64(l.MinFreeBytes) {
		return sourceError("workspace_limit")
	}
	return nil
}

type sourceTreeEntry struct {
	path string
	mode os.FileMode
	size int64
}

// scanSource reads bounded directory batches and checks cancellation before every
// entry. It never follows links; acquisition may contain Git's own link metadata
// but the exported tree admits only regular files and directories.
func scanSource(ctx context.Context, root string, l SourceCheckoutLimits, strict bool) ([]sourceTreeEntry, int64, error) {
	entries := []sourceTreeEntry{}
	total := int64(0)
	var visit func(string, int) error
	visit = func(rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > l.MaxDepth {
			return sourceError("tree_limit")
		}
		dir, err := os.Open(filepath.Join(root, rel))
		if os.IsNotExist(err) && !strict {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = dir.Close() }()
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			batch, readErr := dir.ReadDir(64)
			for _, entry := range batch {
				if err = ctx.Err(); err != nil {
					return err
				}
				p := filepath.Join(rel, entry.Name())
				st, err := entry.Info()
				if os.IsNotExist(err) && !strict {
					continue
				}
				if err != nil {
					return err
				}
				if len(entries) >= l.MaxEntries {
					return sourceError("tree_limit")
				}
				if strict && (!st.IsDir() && !st.Mode().IsRegular() || entry.Name() == ".gitmodules" || entry.Name() == ".lfsconfig") {
					return sourceError("unsupported_tree")
				}
				size := st.Size()
				if st.IsDir() {
					size = 0
				}
				entries = append(entries, sourceTreeEntry{filepath.ToSlash(p), st.Mode(), size})
				if !st.IsDir() {
					if st.Size() < 0 || st.Size() > l.MaxWorkspaceBytes-total {
						return sourceError("tree_limit")
					}
					total += st.Size()
				}
				if st.IsDir() {
					if err = visit(p, depth+1); err != nil {
						return err
					}
				}
			}
			// Git and transport cleanup can remove an already-open directory.
			// Only the live workspace scan tolerates that deletion race.
			if os.IsNotExist(readErr) && !strict {
				return nil
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	err := visit("", 0)
	return entries, total, err
}

func sourceTreeDigest(ctx context.Context, path string, l SourceCheckoutLimits) (string, error) {
	entries, total, err := scanSource(ctx, path, l, true)
	if err != nil {
		if ctx.Err() != nil {
			return "", sourceError("canceled")
		}
		var coded *SourceCheckoutError
		if errors.As(err, &coded) {
			return "", coded
		}
		return "", sourceError("unsupported_tree")
	}
	if total > l.MaxTreeBytes {
		return "", sourceError("tree_limit")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", sourceError("workspace")
	}
	defer func() { _ = root.Close() }()
	h := sha256.New()
	buffer := make([]byte, 32<<10)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return "", sourceError("canceled")
		}
		// Length-delimited names and fixed file size separate every record. Only the
		// executable bit is source-significant, independent of the host's umask.
		_, _ = fmt.Fprintf(h, "%d:%s:%t:%t:%d\n", len(entry.path), entry.path, entry.mode.IsDir(), entry.mode&0111 != 0, entry.size)
		if entry.mode.IsDir() {
			continue
		}
		f, err := root.Open(entry.path)
		if err != nil {
			return "", sourceError("workspace")
		}
		remaining := entry.size
		first := true
		for remaining > 0 {
			if ctx.Err() != nil {
				_ = f.Close()
				return "", sourceError("canceled")
			}
			n, readErr := f.Read(buffer[:min(int64(len(buffer)), remaining)])
			if first && strings.HasPrefix(string(buffer[:n]), "version https://git-lfs.github.com/spec/v1") {
				_ = f.Close()
				return "", sourceError("unsupported_tree")
			}
			first = false
			_, _ = h.Write(buffer[:n])
			remaining -= int64(n)
			if readErr != nil || n == 0 {
				_ = f.Close()
				return "", sourceError("workspace")
			}
		}
		var extra [1]byte
		n, readErr := f.Read(extra[:])
		_ = f.Close()
		if n != 0 || !errors.Is(readErr, io.EOF) {
			return "", sourceError("workspace")
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
