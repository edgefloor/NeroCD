package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// VerifyDummyPassword deliberately performs an Argon2id verification for an
// unknown, disabled, malformed, or empty account. It prevents existence from
// being inferred from a cheaper failure path. It performs exactly one KDF,
// matching normal verification work without creating a first-request timing
// outlier. The fixed salt is neither a credential nor persisted state.
func VerifyDummyPassword(password string) {
	_ = argon2.IDKey([]byte(password), []byte("nerocd-login-dummy-salt-v1"), argonTime, argonMemory, argonThreads, argonKeyLen)
}

// Argon2id parameters are encoded with every hash so later revisions can
// verify existing credentials and decide whether a rehash is appropriate.
const (
	argonVersion = 19
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrUnsupportedPasswordHash indicates an unsupported persisted hash format.
var ErrUnsupportedPasswordHash = errors.New("unsupported password hash")

// HashPassword returns a password hash suitable for persistent storage.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password is required")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argonVersion, argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword returns whether the credential is valid and whether it used
// the retired development-only SHA-256 format. Both comparisons are constant
// time; callers decide whether legacy verification is allowed in their mode.
func VerifyPassword(password, encoded string) (valid, legacy bool, err error) {
	if strings.HasPrefix(encoded, "sha256:") {
		sum := sha256.Sum256([]byte(password))
		want, decodeErr := hexDigest(encoded[len("sha256:"):])
		if decodeErr != nil {
			return false, true, ErrUnsupportedPasswordHash
		}
		return subtle.ConstantTimeCompare(sum[:], want) == 1, true, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false, ErrUnsupportedPasswordHash
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil || memory < 8*1024 || memory > 512*1024 || iterations == 0 || iterations > 10 || threads == 0 || threads > 16 {
		return false, false, ErrUnsupportedPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, false, ErrUnsupportedPasswordHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != argonKeyLen {
		return false, false, ErrUnsupportedPasswordHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, false, nil
}

func hexDigest(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 {
		return nil, errors.New("invalid digest")
	}
	decoded := make([]byte, sha256.Size)
	for i := range decoded {
		var v byte
		for _, c := range []byte{value[i*2], value[i*2+1]} {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= c - '0'
			case c >= 'a' && c <= 'f':
				v |= c - 'a' + 10
			default:
				return nil, errors.New("invalid digest")
			}
		}
		decoded[i] = v
	}
	return decoded, nil
}
