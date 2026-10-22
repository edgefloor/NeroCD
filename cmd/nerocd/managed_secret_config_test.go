package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSecretKeyringConfiguration(t *testing.T) {
	t.Setenv("NEROCD_SECRET_KEYRING_FILE", "")
	if ring, err := loadManagedSecretKeyring(); err != nil || ring != nil {
		t.Fatal("absent keyring must permit startup")
	}
	path := filepath.Join(t.TempDir(), "keyring")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := os.WriteFile(path, []byte(`{"active_key_id":"test","keys":{"test":"`+key+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_SECRET_KEYRING_FILE", path)
	if ring, err := loadManagedSecretKeyring(); err != nil || ring == nil {
		t.Fatal("safe configured keyring rejected")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{path, "relative-keyring", " /private-missing ", " "} {
		t.Setenv("NEROCD_SECRET_KEYRING_FILE", value)
		ring, err := loadManagedSecretKeyring()
		if err == nil || ring != nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), key) {
			t.Fatal("unsafe configured keyring did not fail safely")
		}
	}
}
