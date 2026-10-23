//go:build linux

package hostingproxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReceiptPersistsStrictIntentWithoutChangingLegacyShape(t *testing.T) {
	s, err := openStorage(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	legacy := receipt{Identity: testIdentity(), Generation: 1, Hash: digest("legacy"), Phase: "committed"}
	if err = s.save(legacy); err != nil {
		t.Fatal(err)
	}
	b, err := s.read("receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["Verification"]; present {
		t.Fatal("legacy receipt shape changed")
	}
	strict := legacy
	strict.Phase = "intent"
	strict.Verification = &verificationPolicy{Version: 1, RequiredRouteIDs: []string{"a", "b"}}
	if err = s.save(strict); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.load()
	if err != nil || !reflect.DeepEqual(reloaded, strict) {
		t.Fatalf("strict intent round trip: %+v %v", reloaded, err)
	}
}

func TestStorageSafety(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := openStorage(context.Background(), dir); err == nil {
			t.Fatal("public state directory accepted")
		}
	})
	t.Run("symlink directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := openStorage(context.Background(), link); err == nil {
			t.Fatal("symlink directory accepted")
		}
	})
	t.Run("nonempty Docker config", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		s, err := openStorage(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		s.close()
		if err = os.WriteFile(filepath.Join(dir, "docker-config", "config.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = openStorage(context.Background(), dir); err == nil {
			t.Fatal("ambient credentials accepted")
		}
	})
	t.Run("symlink file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		s, err := openStorage(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		if err = s.root.Symlink("outside", "receipt.json"); err != nil {
			t.Fatal(err)
		}
		if err = s.atomic("receipt.json", []byte("{}")); err == nil {
			t.Fatal("symlink replaced")
		}
	})
}
func TestStorageLockCancellationAndAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := openStorage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if second, err := openStorage(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second.close()
		}
		t.Fatalf("lock wait %v", err)
	}
	if err = s.atomic("dynamic/routes.yml", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err = s.atomic("dynamic/routes.yml", []byte("second")); err != nil {
		t.Fatal(err)
	}
	b, err := s.read("dynamic/routes.yml")
	if err != nil || string(b) != "second" {
		t.Fatalf("atomic read %s %v", b, err)
	}
	if _, err = s.root.Stat("candidate"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate remained in staging")
	}
}
