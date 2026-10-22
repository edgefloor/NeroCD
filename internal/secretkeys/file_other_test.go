//go:build !unix

package secretkeys

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-keyring.json")
	ring, err := Load(path)
	if ring != nil || err == nil || strings.Contains(err.Error(), path) {
		t.Fatal("unsupported platform did not fail closed")
	}
}
