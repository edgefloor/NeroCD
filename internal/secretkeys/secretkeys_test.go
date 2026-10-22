//go:build unix

package secretkeys

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"nerocd/internal/secretcrypto"
)

func keyText(value byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
}

func document(active string, keys map[string]string) string {
	contents, err := json.Marshal(struct {
		Active string            `json:"active_key_id"`
		Keys   map[string]string `json:"keys"`
	}{active, keys})
	if err != nil {
		panic(err) // Test fixture contains only JSON strings.
	}
	return string(contents)
}

func writeKeyFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-keyring.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireRejected(t *testing.T, path string, private ...string) {
	t.Helper()
	ring, err := Load(path)
	if err == nil || ring != nil {
		t.Fatal("expected nil keyring and error")
	}
	if err.Error() != errInvalidFile.Error() {
		t.Fatalf("unexpected error category: %v", err)
	}
	for _, value := range append(private, path) {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("error contains private input")
		}
	}
}

func TestLoadRoundTripAndRotation(t *testing.T) {
	binding := secretcrypto.Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	old, err := Load(writeKeyFile(t, document("old", map[string]string{"old": keyText(1)})))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := old.Seal(binding, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	ring, err := Load(writeKeyFile(t, document("new", map[string]string{"old": keyText(1), "new": keyText(2)})))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := ring.Open(binding, envelope)
	if err != nil || string(plaintext) != "value" {
		t.Fatalf("old envelope roundtrip failed: %v", err)
	}
	envelope, err = ring.Seal(binding, []byte("new value"))
	if err != nil || envelope.KeyID != "new" {
		t.Fatalf("active key selection failed: %v", err)
	}
	plaintext, err = ring.Open(binding, envelope)
	if err != nil || string(plaintext) != "new value" {
		t.Fatalf("new envelope roundtrip failed: %v", err)
	}
	for name, keys := range map[string]map[string]string{
		"missing": {"other": keyText(2)},
		"wrong":   {"new": keyText(3)},
	} {
		t.Run(name, func(t *testing.T) {
			var active string
			for id := range keys {
				active = id
			}
			other, err := Load(writeKeyFile(t, document(active, keys)))
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := other.Open(binding, envelope)
			if !errors.Is(err, secretcrypto.ErrDecrypt) || plaintext != nil {
				t.Fatal("incorrect or missing decryption key accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	key := keyText(1)
	valid := document("key", map[string]string{"key": key})
	cases := map[string]string{
		"empty": "", "not object": "[]", "null": "null", "truncated": "{",
		"missing active":           `{"keys":{"key":"` + key + `"}}`,
		"missing keys":             `{"active_key_id":"key"}`,
		"unknown field":            strings.TrimSuffix(valid, "}") + `,"private-unknown":"private-value"}`,
		"wrong case":               strings.Replace(valid, "active_key_id", "Active_key_id", 1),
		"duplicate active":         strings.TrimSuffix(valid, "}") + `,"active_key_id":"key"}`,
		"escaped duplicate active": strings.TrimSuffix(valid, "}") + `,"active_key_\u0069d":"key"}`,
		"duplicate keys":           strings.TrimSuffix(valid, "}") + `,"keys":{}}`,
		"duplicate id":             `{"active_key_id":"key","keys":{"key":"` + key + `","key":"` + key + `"}}`,
		"escaped duplicate id":     `{"active_key_id":"key","keys":{"key":"` + key + `","k\u0065y":"` + key + `"}}`,
		"trailing object":          valid + "{}", "trailing malformed": valid + "private-data",
		"keys null":           `{"active_key_id":"key","keys":null}`,
		"keys array":          `{"active_key_id":"key","keys":[]}`,
		"active number":       strings.Replace(valid, `"active_key_id":"key"`, `"active_key_id":1`, 1),
		"key number":          `{"active_key_id":"key","keys":{"key":1}}`,
		"key null":            `{"active_key_id":"key","keys":{"key":null}}`,
		"active null":         strings.Replace(valid, `"active_key_id":"key"`, `"active_key_id":null`, 1),
		"invalid base64":      document("key", map[string]string{"key": "private-value!"}),
		"short key":           document("key", map[string]string{"key": base64.StdEncoding.EncodeToString(make([]byte, 31))}),
		"long key":            document("key", map[string]string{"key": base64.StdEncoding.EncodeToString(make([]byte, 33))}),
		"empty keys":          document("key", map[string]string{}),
		"missing active key":  document("missing", map[string]string{"key": key}),
		"empty active":        document("", map[string]string{"key": key}),
		"invalid id":          document("private/id", map[string]string{"private/id": key}),
		"long id":             document(strings.Repeat("x", 129), map[string]string{strings.Repeat("x", 129): key}),
		"invalid inactive id": document("key", map[string]string{"key": key, "private/id": key}),
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			requireRejected(t, writeKeyFile(t, contents), key, "private-value", "private/id", "private-unknown")
		})
	}
}

func TestLoadBounds(t *testing.T) {
	keys := make(map[string]string)
	for index := range 33 {
		keys[fmt.Sprintf("key%d", index)] = keyText(byte(index))
	}
	requireRejected(t, writeKeyFile(t, document("key0", keys)))
	delete(keys, "key32")
	if _, err := Load(writeKeyFile(t, document("key0", keys))); err != nil {
		t.Fatalf("32 keys rejected: %v", err)
	}
	valid := document("key", map[string]string{"key": keyText(1)})
	exact := valid + strings.Repeat(" ", maxFileBytes-len(valid))
	if _, err := Load(writeKeyFile(t, exact)); err != nil {
		t.Fatalf("exact size limit rejected: %v", err)
	}
	requireRejected(t, writeKeyFile(t, exact+" "))
	requireRejected(t, writeKeyFile(t, strings.Repeat(" ", maxFileBytes)))
}

func TestLoadFileSafety(t *testing.T) {
	valid := document("key", map[string]string{"key": keyText(1)})
	for _, mode := range []os.FileMode{0o400, 0o600} {
		t.Run(fmt.Sprintf("allowed %o", mode), func(t *testing.T) {
			path := writeKeyFile(t, valid)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, mode := range []os.FileMode{0, 0o200, 0o640, 0o604, 0o660, 0o666, 0o700, os.ModeSetuid | 0o600, os.ModeSetgid | 0o600, os.ModeSticky | 0o600} {
		t.Run(fmt.Sprintf("rejected %o", mode), func(t *testing.T) {
			path := writeKeyFile(t, valid)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			requireRejected(t, path)
		})
	}
	t.Run("symlink", func(t *testing.T) {
		path := writeKeyFile(t, valid)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		requireRejected(t, link)
	})
	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fifo")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		requireRejected(t, path)
	})
	requireRejected(t, t.TempDir())
	requireRejected(t, filepath.Join(t.TempDir(), "missing-private-path"))
	requireRejected(t, "relative-private-path")
	requireRejected(t, "")
	requireRejected(t, "/private-path\x00")
}

func TestMetadataRejectsOtherOwner(t *testing.T) {
	stat := syscall.Stat_t{Mode: syscall.S_IFREG | 0o600, Uid: 123, Size: 1}
	if !safeMetadata(&stat, 123) {
		t.Fatal("valid metadata rejected")
	}
	if safeMetadata(&stat, 456) {
		t.Fatal("other owner accepted")
	}
}

func TestInvalidMetadataClosesDescriptor(t *testing.T) {
	// With no parallel tests, the next opened descriptor must stay available
	// across repeated metadata rejection rather than leaking each rejected file.
	path := writeKeyFile(t, "")
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatal(err)
	}
	for range 256 {
		requireRejected(t, path)
	}
	after, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(after) }()
	if after != fd {
		t.Fatalf("metadata rejection leaked descriptors: before %d, after %d", fd, after)
	}
}
