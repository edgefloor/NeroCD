package hostingbuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func sum(b []byte) string { v := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(v[:]) }
func fileDigest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func privateDir(p string) error {
	// Existing ancestors may be shared, but must not redirect through symlinks.
	for q := p; ; q = filepath.Dir(q) {
		st, err := os.Lstat(q)
		if err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("unsafe directory: %s", q)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if q == filepath.Dir(q) {
			break
		}
	}
	if err := os.MkdirAll(p, 0700); err != nil {
		return err
	}
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must be owner-only")
	}
	return ownedByCurrentUser(st)
}

func excluded(name string) bool {
	n := strings.ToLower(name)
	return n == ".git-credentials" || n == ".config" || n == ".kube" || n == ".azure" || n == ".gnupg" || n == ".terraform" || n == "auth.json" || n == ".yarnrc" || n == ".yarnrc.yml" || n == ".git" || n == ".ssh" || n == ".aws" || n == ".docker" || n == ".npmrc" || n == ".netrc" || n == ".pypirc" || n == "credentials" || n == "credentials.json" || n == "id_rsa" || n == "id_ed25519" || n == ".env" || strings.HasPrefix(n, ".env.") || strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".key")
}

// stageContext rejects all links and special files, including internal symlinks.
// Root.Open keeps source reads beneath the checkout even if the caller violates
// its immutability promise while the copy is in progress.
func stageContext(ctx context.Context, src, dst string, l Limits) (string, error) {
	st, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("checkout must be a real directory")
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return stageRoot(ctx, root, dst, l, false)
}

func stageRoot(ctx context.Context, root *os.Root, dst string, l Limits, assets bool) (string, error) {
	if err := os.Mkdir(dst, 0700); err != nil {
		return "", err
	}
	h := sha256.New()
	var total int64
	count := 0
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if assets && strings.HasPrefix(strings.ToLower(d.Name()), ".nerocd-static.") {
			return errors.New("reserved static generated filename")
		}
		if assets && staticInstruction(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if excluded(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		count++
		if count > l.ContextFiles {
			return errors.New("context file count exceeded")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("context links/special files denied: %s", p)
		}
		if strings.ContainsAny(p, "\x00\n\r") || !fs.ValidPath(p) {
			return errors.New("invalid context path")
		}
		size := info.Size()
		if assets && info.IsDir() {
			size = 0
		}
		_, _ = fmt.Fprintf(h, "%s\x00%o\x00%d\x00", p, info.Mode().Perm()&0755, size)
		target := filepath.Join(dst, filepath.FromSlash(p))
		if info.IsDir() {
			return os.Mkdir(target, info.Mode().Perm()&0755)
		}
		if info.Size() > l.ContextBytes-total {
			return errors.New("context byte budget exceeded")
		}
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		actual, err := f.Stat()
		if err != nil {
			return err
		}
		if !actual.Mode().IsRegular() {
			return errors.New("source changed during staging")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0755)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(contextReader{ctx: ctx, reader: f}, l.ContextBytes-total+1))
		closeErr := out.Close()
		total += n
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() || total > l.ContextBytes {
			return errors.New("source changed or context byte budget exceeded")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// contextReader keeps long local copies and decompression inside the caller's
// cancellation boundary without starting an unbounded background operation.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

const (
	scanBatchSize    = 128
	scanWorkLimit    = 1_000_000
	scanDepthLimit   = 256
	scanFileLimit    = 1 << 20
	scanProcessLimit = 4096
)

// Each scan has finite work even when a build keeps adding entries. Cancellation
// is checked before and after each bounded ReadDir batch and before each entry.
// Local filesystem syscalls must return; no abandoned goroutine simulates a timeout.
type scanBudget struct {
	ctx       context.Context
	remaining int
}

func newScanBudget(ctx context.Context) *scanBudget {
	return &scanBudget{ctx: ctx, remaining: scanWorkLimit}
}
func (b *scanBudget) step() error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.remaining == 0 {
		return errors.New("filesystem scan work budget exceeded")
	}
	b.remaining--
	return nil
}
func scanDirectory(b *scanBudget, f *os.File, visit func(fs.DirEntry) error) error {
	for {
		if err := b.step(); err != nil {
			return err
		}
		entries, readErr := f.ReadDir(scanBatchSize)
		if err := b.ctx.Err(); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := b.step(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
func scanTree(b *scanBudget, rootPath string, visit func(string, fs.FileInfo) error) error {
	if err := b.step(); err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var walk func(string, int) error
	walk = func(relative string, depth int) error {
		if err := b.step(); err != nil {
			return err
		}
		if depth > scanDepthLimit {
			return errors.New("filesystem scan depth budget exceeded")
		}
		info, err := root.Lstat(relative)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = visit(relative, info); err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		if err = b.step(); err != nil {
			return err
		}
		dir, err := root.Open(relative)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = dir.Close() }()
		return scanDirectory(b, dir, func(entry fs.DirEntry) error { return walk(path.Join(relative, entry.Name()), depth+1) })
	}
	return walk(".", 0)
}
func scanReadFile(b *scanBudget, p string) ([]byte, error) {
	if err := b.step(); err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: b.ctx, reader: f}, scanFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > scanFileLimit {
		return nil, errors.New("process metadata scan byte budget exceeded")
	}
	if err = b.ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
func joinMonitor(stop context.CancelFunc, done <-chan error) error { stop(); return <-done }
