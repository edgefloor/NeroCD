//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"nerocd/internal/domain"
)

var errDatabaseSecret = errors.New("database protected credentials unavailable or expired")

// DatabaseCredentialValue owns a credential delivery value. Formatting and JSON
// never expose its bytes. The factory copies its input; callers must erase their
// original delivery buffer separately.
type DatabaseCredentialValue struct {
	reference domain.HostingDatabaseCredentialRef
	value     []byte
}

// NewDatabaseCredentialValue copies a bounded, generated ASCII password. This
// private recipe accepts no line delimiters, NUL, or configuration syntax.
func NewDatabaseCredentialValue(ref domain.HostingDatabaseCredentialRef, value []byte) (DatabaseCredentialValue, error) {
	if len(value) < 32 || len(value) > 256 {
		return DatabaseCredentialValue{}, errDatabaseSecret
	}
	for _, b := range value {
		valid := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
		if !valid {
			return DatabaseCredentialValue{}, errDatabaseSecret
		}
	}
	return DatabaseCredentialValue{reference: ref, value: bytes.Clone(value)}, nil
}

// Close erases the owned delivery buffer. Do not copy a value after creation.
func (v *DatabaseCredentialValue) Close() { clear(v.value); v.value = nil }

// Format emits a fixed redaction for every formatting verb.
func (DatabaseCredentialValue) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[protected database credential]")
}

// MarshalJSON refuses ordinary credential serialization.
func (DatabaseCredentialValue) MarshalJSON() ([]byte, error) { return nil, errDatabaseSecret }

// DatabaseCredentials owns exact plan credential values until Close or UseUntil.
// A busy bundle rejects concurrent borrows immediately rather than waiting past
// their context deadlines. Close waits for active use before erasing its bytes.
type DatabaseCredentials struct {
	mu       sync.Mutex
	planHash string
	until    time.Time
	values   []DatabaseCredentialValue
}

// NewDatabaseCredentials copies the complete exact role/ref/version set. It does
// not prove delivery authorization; the typed controller protocol owns that.
func NewDatabaseCredentials(plan domain.HostingApplyPlan, values []DatabaseCredentialValue, useUntil time.Time) (*DatabaseCredentials, error) {
	if domain.ValidateHostingDatabasePlan(plan) != nil || !useUntil.After(time.Now()) || useUntil.After(time.Now().Add(15*time.Minute)) || len(values) != len(plan.Database.Credentials) {
		return nil, errDatabaseSecret
	}
	c := &DatabaseCredentials{planHash: plan.Hash, until: useUntil}
	for _, ref := range plan.Database.Credentials {
		found := 0
		for _, supplied := range values {
			if supplied.reference != ref {
				continue
			}
			value, err := NewDatabaseCredentialValue(ref, supplied.value)
			if err != nil {
				c.Close()
				return nil, errDatabaseSecret
			}
			c.values = append(c.values, value)
			found++
		}
		if found != 1 {
			c.Close()
			return nil, errDatabaseSecret
		}
	}
	return c, nil
}

// Close erases all owned values and prevents subsequent use. Persistent protected
// engine files remain: a stopped/restarting container still needs those files.
func (c *DatabaseCredentials) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.values {
		c.values[i].Close()
	}
	c.values = nil
}

// Format emits a fixed redaction without inspecting protected values.
func (*DatabaseCredentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[protected database credentials]")
}

// MarshalJSON refuses ordinary credential serialization.
func (*DatabaseCredentials) MarshalJSON() ([]byte, error) { return nil, errDatabaseSecret }

func (c *DatabaseCredentials) use(ctx context.Context, plan domain.HostingApplyPlan, fn func(context.Context, []DatabaseCredentialValue) error) error {
	if c == nil || ctx.Err() != nil || !c.mu.TryLock() {
		return errDatabaseSecret
	}
	defer c.mu.Unlock()
	if ctx.Err() != nil || plan.Database == nil || c.planHash != plan.Hash || len(c.values) != len(plan.Database.Credentials) || !time.Now().Before(c.until) {
		return errDatabaseSecret
	}
	bounded, cancel := context.WithDeadline(ctx, c.until)
	defer cancel()
	err := fn(bounded, c.values)
	if err == nil && bounded.Err() != nil {
		return errDatabaseSecret
	}
	return err
}

func databasePassword(values []DatabaseCredentialValue, role string) []byte {
	for _, value := range values {
		if value.reference.Role == role {
			return value.value
		}
	}
	return nil
}

const databaseSecretMount = "/run/nerocd-db"

// DatabaseEngineConfig selects persistent daemon-host roots. Only the constructor
// exposes configuration; host/ownership test seams are package-private.
type DatabaseEngineConfig struct {
	stateRoot, secretRoot string
	uid, gid              int
	hostCheck             func(context.Context, *imageEngine) error
	readiness             func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error
}

// NewDatabaseEngineConfig requires pre-provisioned root-owned0700 persistent
// directories on the same Linux host as the trusted rootful Docker daemon.
// Home and temporary directories are deliberately unsupported.
func NewDatabaseEngineConfig(stateRoot, secretRoot string) (DatabaseEngineConfig, error) {
	zero := DatabaseEngineConfig{}
	for _, path := range []string{stateRoot, secretRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, ",\r\n\x00") {
			return zero, errors.New("invalid persistent database root")
		}
		for _, excluded := range []string{"/tmp", "/var/tmp", "/home", "/root", "/Users"} {
			if path == excluded || strings.HasPrefix(path, excluded+"/") {
				return zero, errors.New("database roots require persistent system storage")
			}
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path || databaseFilePolicy(path, 0o700, 0, 0, true) != nil || databaseProtectedParents(path) != nil {
			return zero, errors.New("database roots require protected real directories")
		}
	}
	if stateRoot == secretRoot || strings.HasPrefix(stateRoot, secretRoot+"/") || strings.HasPrefix(secretRoot, stateRoot+"/") {
		return zero, errors.New("database roots must be separate")
	}
	return DatabaseEngineConfig{stateRoot: stateRoot, secretRoot: secretRoot, uid: 999, gid: 999}, nil
}

// Format keeps protected host paths out of ordinary diagnostics.
func (c DatabaseEngineConfig) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[database engine protected roots]")
}

// MarshalJSON refuses exporting protected host configuration.
func (DatabaseEngineConfig) MarshalJSON() ([]byte, error) {
	return nil, errors.New("database engine configuration is not exportable")
}

func databaseFilePolicy(path string, mode os.FileMode, uid, gid int, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("database protected filesystem unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != mode || info.IsDir() != directory || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || int(stat.Uid) != uid || int(stat.Gid) != gid || !directory && (!info.Mode().IsRegular() || stat.Nlink != 1) {
		return errors.New("database protected filesystem policy mismatch")
	}
	return nil
}

func databaseProtectedParents(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return errors.New("database protected parent unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || stat.Uid != 0 {
			return errors.New("database persistent parents require root ownership")
		}
		if parent == "/" {
			return nil
		}
	}
}

func databaseCheckFile(root *os.Root, name string, data []byte, uid, gid int) error {
	path := filepath.Join(root.Name(), name)
	if databaseFilePolicy(path, 0o600, uid, gid, false) != nil {
		return errors.New("database protected file changed")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("database protected file unavailable")
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 16<<10))
	defer clear(raw)
	if err != nil || !bytes.Equal(raw, data) {
		return errors.New("database protected file contents changed")
	}
	return nil
}

func databaseFile(root *os.Root, name string, data []byte, uid, gid int) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		return databaseCheckFile(root, name, data, uid, gid)
	}
	if err != nil {
		return errors.New("database protected file creation failed")
	}
	defer func() { _ = file.Close() }()
	if file.Chown(uid, gid) != nil {
		return errors.New("database runtime credential ownership unavailable")
	}
	if _, err = file.Write(data); err != nil {
		return errors.New("database protected file write failed")
	}
	if file.Sync() != nil {
		return errors.New("database protected file synchronization failed")
	}
	return nil
}

func databaseGeneration(operation string, plan domain.HostingApplyPlan) string {
	return "generation_" + domain.HostingDocumentHash(struct{ Operation, Plan string }{operation, plan.Hash})[7:39]
}

func databaseSecretFiles(operation string, plan domain.HostingApplyPlan, values []DatabaseCredentialValue) map[string][]byte {
	files := map[string][]byte{}
	if plan.Database.Reference.Engine == "postgresql" {
		files["admin-password"] = bytes.Clone(databasePassword(values, "admin"))
	} else {
		app := sha256.Sum256(databasePassword(values, "app"))
		probe := sha256.Sum256(databasePassword(values, "probe"))
		files["users.acl"] = []byte("user default off\nuser ncd_app on #" + hex.EncodeToString(app[:]) + " ~* -@all +@read +@write +ping -@dangerous\nuser ncd_probe on #" + hex.EncodeToString(probe[:]) + " -@all +ping\n")
		files["redis.conf"] = []byte("bind 0.0.0.0\nprotected-mode yes\nport 6379\ndir /data\naclfile /run/nerocd-db/users.acl\nappendonly yes\nappendfsync everysec\nappenddirname appendonlydir\nno-appendfsync-on-rewrite no\naof-load-truncated no\nsave \"\"\nlogfile \"\"\n")
	}
	identity, _ := json.Marshal(struct{ Operation, Plan string }{operation, plan.Hash})
	files["identity.json"] = identity
	return files
}

// Retirement replay must reject changed protected files before readiness can
// create a Docker exec for process-ownership inspection. This never repairs files.
func (c DatabaseEngineConfig) checkSecretFiles(ctx context.Context, operation string, plan domain.HostingApplyPlan, values []DatabaseCredentialValue) error {
	path := filepath.Join(c.secretRoot, databaseGeneration(operation, plan))
	if ctx.Err() != nil || databaseFilePolicy(path, 0o700, c.uid, c.gid, true) != nil {
		return errDatabaseSecret
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return errDatabaseSecret
	}
	defer func() { _ = root.Close() }()
	files := databaseSecretFiles(operation, plan, values)
	defer func() {
		for _, raw := range files {
			clear(raw)
		}
	}()
	for name, data := range files {
		if ctx.Err() != nil {
			return errDatabaseSecret
		}
		if err := databaseCheckFile(root, name, data, c.uid, c.gid); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c DatabaseEngineConfig) secrets(ctx context.Context, operation string, plan domain.HostingApplyPlan, values []DatabaseCredentialValue, before func(context.Context) error) (string, error) {
	name := databaseGeneration(operation, plan)
	path := filepath.Join(c.secretRoot, name)
	if err := databaseAuthorize(ctx, before); err != nil {
		return "", err
	}
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return "", errors.New("database credential generation unavailable")
	}
	// Only root controls the parent; the runtime account owns this generation.
	if err == nil {
		if err = os.Lchown(path, c.uid, c.gid); err != nil {
			return "", errors.New("database runtime credential ownership unavailable")
		}
	}
	if databaseFilePolicy(path, 0o700, c.uid, c.gid, true) != nil {
		return "", errors.New("database runtime credential ownership unavailable")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", errors.New("database credential generation unavailable")
	}
	defer func() { _ = root.Close() }()
	files := databaseSecretFiles(operation, plan, values)
	defer func() {
		for _, raw := range files {
			clear(raw)
		}
	}()
	for name, data := range files {
		if err := databaseAuthorize(ctx, before); err != nil {
			return "", err
		}
		if err := databaseFile(root, name, data, c.uid, c.gid); err != nil {
			return "", err
		}
	}
	dir, err := os.Open(path)
	if err != nil {
		return "", errors.New("database credential directory unavailable")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return "", errors.New("database credential directory synchronization failed")
	}
	parent, err := os.Open(c.secretRoot)
	if err != nil {
		return "", errors.New("database secret root unavailable")
	}
	defer func() { _ = parent.Close() }()
	if parent.Sync() != nil || ctx.Err() != nil {
		return "", errors.New("database secret root synchronization not confirmed")
	}
	return path, nil
}
