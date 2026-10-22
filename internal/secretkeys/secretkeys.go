// Package secretkeys loads operator-supplied encryption keys from a bounded,
// owner-only file. It does not generate keys or configure runtime services.
package secretkeys

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"nerocd/internal/secretcrypto"
)

const (
	maxFileBytes = 8192
	maxKeys      = 32
)

var errInvalidFile = errors.New("secretkeys: invalid keyring file")

// Load reads an absolute path to a regular file owned by the effective user,
// with mode 0400 or 0600. The final path component must not be a symlink;
// operators must trust ancestor directories. Unsupported platforms fail closed.
// The JSON object must contain exactly active_key_id and keys, where keys maps
// unique key IDs to base64-encoded 32-byte keys. Errors contain no input values.
func Load(path string) (*secretcrypto.Keyring, error) {
	if !filepath.IsAbs(path) {
		return nil, errInvalidFile
	}
	contents, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return parse(contents)
}

func parse(contents []byte) (*secretcrypto.Keyring, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if !delimiter(decoder, '{') {
		return nil, errInvalidFile
	}
	seen := make(map[string]bool, 2)
	var active string
	var keys map[string][]byte
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || seen[field] {
			return nil, errInvalidFile
		}
		seen[field] = true
		switch field {
		case "active_key_id":
			if err := decoder.Decode(&active); err != nil {
				return nil, errInvalidFile
			}
		case "keys":
			keys, err = decodeKeys(decoder)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errInvalidFile
		}
	}
	if !delimiter(decoder, '}') || !seen["active_key_id"] || !seen["keys"] {
		return nil, errInvalidFile
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errInvalidFile
	}
	ring, err := secretcrypto.New(active, keys)
	if err != nil {
		return nil, errInvalidFile
	}
	return ring, nil
}

func decodeKeys(decoder *json.Decoder) (map[string][]byte, error) {
	if !delimiter(decoder, '{') {
		return nil, errInvalidFile
	}
	keys := make(map[string][]byte)
	for decoder.More() {
		token, err := decoder.Token()
		id, ok := token.(string)
		if err != nil || !ok || len(keys) >= maxKeys {
			return nil, errInvalidFile
		}
		if _, exists := keys[id]; exists {
			return nil, errInvalidFile
		}
		var encoded string
		if err := decoder.Decode(&encoded); err != nil {
			return nil, errInvalidFile
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, errInvalidFile
		}
		keys[id] = key
	}
	if !delimiter(decoder, '}') {
		return nil, errInvalidFile
	}
	return keys, nil
}

func delimiter(decoder *json.Decoder, want json.Delim) bool {
	token, err := decoder.Token()
	return err == nil && token == want
}
