//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDatabaseCredentialsOwnAndEraseExactValues(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	ref := f.plan.Database.Credentials[0]
	input := []byte(strings.Repeat("z", 48))
	value, err := NewDatabaseCredentialValue(ref, input)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	clear(input)
	if len(value.value) != 48 || value.value[0] != 'z' {
		t.Fatal("credential borrowed caller delivery")
	}
	for _, secret := range []any{value, &value, credentials, f.config} {
		if strings.Contains(fmt.Sprintf("%+v %#v %s", secret, secret, secret), strings.Repeat("z", 32)) {
			t.Fatal("protected credential formatting leaked")
		}
		if _, err := json.Marshal(secret); err == nil {
			t.Fatal("protected value was exportable")
		}
	}
	owned := credentials.values[0].value
	credentials.Close()
	for _, b := range owned {
		if b != 0 {
			t.Fatal("owned credentials not erased")
		}
	}
	if err := credentials.use(context.Background(), f.plan, func(context.Context, []DatabaseCredentialValue) error { return nil }); err == nil {
		t.Fatal("closed credentials reused")
	}
}

func TestDatabaseCredentialsRejectWrongRefsAndUnsafeBytes(t *testing.T) {
	f, _ := databaseFixture(t, "postgresql")
	for _, value := range []string{"short", strings.Repeat("a", 32) + "\n", strings.Repeat("a", 32) + "'", strings.Repeat("a", 32) + "\x00"} {
		if _, err := NewDatabaseCredentialValue(f.plan.Database.Credentials[0], []byte(value)); err == nil {
			t.Fatal("unsafe generated password accepted")
		}
	}
	if _, err := NewDatabaseCredentials(f.plan, nil, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("missing credential roles accepted")
	}
	credentials := databaseFixtureCredentials(t, f.plan)
	credentials.planHash = "changed"
	if err := credentials.use(context.Background(), f.plan, func(context.Context, []DatabaseCredentialValue) error {
		t.Fatal("wrong plan credentials used")
		return nil
	}); err == nil {
		t.Fatal("wrong plan credential admission")
	}
}

func TestDatabaseSecretFilesPersistentPrivateAndImmutable(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	if result := f.apply(context.Background(), credentials); !result.Ready {
		t.Fatal(result)
	}
	path := filepath.Join(f.config.secretRoot, databaseGeneration(f.req.OriginOperationID, f.plan))
	if databaseFilePolicy(path, 0o700, os.Getuid(), os.Getgid(), true) != nil {
		t.Fatal("generation ownership/mode mismatch")
	}
	for _, name := range []string{"users.acl", "redis.conf", "identity.json"} {
		if databaseFilePolicy(filepath.Join(path, name), 0o600, os.Getuid(), os.Getgid(), false) != nil {
			t.Fatal("private file ownership/mode mismatch")
		}
	}
	acl, err := os.ReadFile(filepath.Join(path, "users.acl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(acl), "user default off\n") || strings.Contains(string(acl), strings.Repeat("k", 48)) {
		t.Fatal("ACL contains plaintext or default user")
	}
	config, err := os.ReadFile(filepath.Join(path, "redis.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"appendonly yes\n", "appendfsync everysec\n", "appenddirname appendonlydir\n", "no-appendfsync-on-rewrite no\n", "aof-load-truncated no\n"} {
		if !strings.Contains(string(config), setting) {
			t.Fatal("missing durable Redis setting", setting)
		}
	}
	credentials.Close()
	if err = f.e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(path, "users.acl")); err != nil {
		t.Fatal("attempt close removed persistent container credentials")
	}
	if err = os.WriteFile(filepath.Join(path, "users.acl"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	newCredentials := databaseFixtureCredentials(t, f.plan)
	err = newCredentials.use(context.Background(), f.plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
		_, err := f.config.secrets(ctx, f.req.OriginOperationID, f.plan, values, f.before)
		return err
	})
	if err == nil {
		t.Fatal("secret generation was silently overwritten")
	}
}

func TestDatabaseSecretFilesRejectSymlinksAndPublicPermissions(t *testing.T) {
	for _, kind := range []string{"directory_symlink", "file_symlink", "public_file"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "postgresql")
			path := filepath.Join(f.config.secretRoot, databaseGeneration(f.req.OriginOperationID, f.plan))
			if kind == "directory_symlink" {
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "file_symlink" {
					target := filepath.Join(t.TempDir(), "unchanged")
					if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, filepath.Join(path, "admin-password")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(path, "admin-password"), []byte(strings.Repeat("k", 48)), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := credentials.use(context.Background(), f.plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
				_, err := f.config.secrets(ctx, f.req.OriginOperationID, f.plan, values, f.before)
				return err
			}); err == nil {
				t.Fatal("unprotected credential path accepted")
			}
		})
	}
}

func TestDatabaseCredentialsBusyBorrowDoesNotWaitPastContext(t *testing.T) {
	for _, kind := range []string{"canceled", "short_deadline"} {
		t.Run(kind, func(t *testing.T) {
			f, credentials := databaseFixture(t, "redis")
			entered := make(chan struct{})
			release := make(chan struct{})
			firstDone := make(chan error, 1)
			secondResult := make(chan error, 1)
			secondDone := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				unblock()
				select {
				case err := <-firstDone:
					if err != nil {
						t.Error("active credential borrower failed")
					}
				case <-time.After(time.Second):
					t.Error("active credential borrower did not join")
				}
			})
			go func() {
				firstDone <- credentials.use(context.Background(), f.plan, func(context.Context, []DatabaseCredentialValue) error { close(entered); <-release; return nil })
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("active borrower did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			t.Cleanup(func() {
				unblock()
				select {
				case <-secondDone:
				case <-time.After(time.Second):
					t.Error("rejected credential borrower did not join")
				}
			})
			go func() {
				defer close(secondDone)
				secondResult <- credentials.use(ctx, f.plan, func(context.Context, []DatabaseCredentialValue) error {
					t.Error("busy credential callback ran")
					return nil
				})
			}()
			select {
			case err := <-secondResult:
				if err == nil {
					t.Fatal("busy credential borrow succeeded")
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("canceled/deadline borrower waited behind active use")
			}
		})
	}
}

func TestDatabaseCredentialsCloseWaitsForBorrowThenErases(t *testing.T) {
	f, credentials := databaseFixture(t, "redis")
	borrowed := make(chan []byte, 1)
	release := make(chan struct{})
	borrowDone := make(chan error, 1)
	closeDone := make(chan struct{})
	closeStarted := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case err := <-borrowDone:
			if err != nil {
				t.Error("active credential borrower failed during Close")
			}
		case <-time.After(time.Second):
			t.Error("credential borrower did not join")
		}
		select {
		case <-closeDone:
		case <-time.After(time.Second):
			t.Error("credential close did not join")
		}
	})
	go func() {
		borrowDone <- credentials.use(context.Background(), f.plan, func(_ context.Context, values []DatabaseCredentialValue) error {
			borrowed <- values[0].value
			<-release
			return nil
		})
	}()
	var owned []byte
	select {
	case owned = <-borrowed:
	case <-time.After(time.Second):
		t.Fatal("credential borrower did not start")
	}
	go func() { close(closeStarted); credentials.Close(); close(closeDone) }()
	<-closeStarted
	select {
	case <-closeDone:
		t.Fatal("Close returned while credential bytes were borrowed")
	case <-time.After(20 * time.Millisecond):
	}
	if len(owned) < 32 || owned[0] == 0 {
		t.Fatal("Close erased active borrower bytes")
	}
	unblock()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after borrow")
	}
	for _, b := range owned {
		if b != 0 {
			t.Fatal("Close did not erase owned bytes")
		}
	}
	if err := credentials.use(context.Background(), f.plan, func(context.Context, []DatabaseCredentialValue) error {
		t.Error("closed credentials callback ran")
		return nil
	}); err == nil {
		t.Fatal("closed credentials reused")
	}
}
