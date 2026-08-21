//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionSecretReaderRejectsUnsafeFilesWithoutDisclosingContents(t *testing.T) {
	dir := t.TempDir()
	secret := "postgres://user:top-secret-value@db:5432/nerocd?sslmode=disable"
	write := func(name string, value []byte, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, value, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := write("good", []byte(secret), 0o600)
	if got, err := readOwnerOnlyProductionSecret(good); err != nil || string(got) != secret {
		t.Fatalf("good secret err=%v got=%q", err, got)
	}
	wide := write("wide", []byte(secret), 0o640)
	empty := write("empty", nil, 0o600)
	oversize := write("oversize", []byte(strings.Repeat("x", 8193)), 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{wide, empty, oversize, link} {
		if _, err := readOwnerOnlyProductionSecret(path); err == nil {
			t.Fatalf("unsafe secret %q was accepted", path)
		} else if strings.Contains(err.Error(), "top-secret-value") {
			t.Fatalf("secret leaked in error %q", err)
		}
	}
}

func TestProductionSecretReaderRejectsWrongOwnerWhenTestable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("wrong-owner mutation requires root; covered in the Linux production profile")
	}
	path := filepath.Join(t.TempDir(), "wrong-owner")
	if err := os.WriteFile(path, []byte("postgres://user:secret@db:5432/nerocd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerOnlyProductionSecret(path); err == nil {
		t.Fatal("wrong-owner production secret was accepted")
	}
}
