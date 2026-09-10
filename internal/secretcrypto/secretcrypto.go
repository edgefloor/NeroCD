// Package secretcrypto provides bounded, versioned authenticated encryption
// for project-managed secret values. It performs no I/O, configuration loading,
// authorization, or key generation.
//
// Operators must supply cryptographically random 32-byte AES keys and rotate a
// key before it is used for 2^32 total encryptions across all processes. Go does
// not provide reliable cryptographic memory erasure, and Keyring makes no such
// claim.
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

const (
	associatedDataDomain = "nerocd/secretcrypto/aes-256-gcm"
	envelopeVersion      = 1
	maxKeyCount          = 32
	maxKeyIDBytes        = 128
	maxBindingIDBytes    = 256
	maxPlaintextBytes    = 65_536
	randomNonceOverhead  = 12 + 16
)

var (
	// ErrInvalidInput reports malformed configuration, binding, plaintext, or
	// envelope structure without including caller-controlled values.
	ErrInvalidInput = errors.New("secretcrypto: invalid input")
	// ErrDecrypt reports an unknown key or failed authenticated decryption
	// without distinguishing the cause.
	ErrDecrypt = errors.New("secretcrypto: decryption failed")
)

// Binding identifies the immutable domain to which ciphertext is bound.
type Binding struct {
	ProjectID string
	SecretID  string
	Version   int64
}

// Envelope is the durable encrypted representation. Data contains the random
// nonce prefix and authenticated ciphertext produced by Go's AES-GCM helper.
type Envelope struct {
	Version int
	KeyID   string
	Data    []byte
}

// Keyring is an immutable, concurrency-safe collection of AES-256-GCM keys.
// Construct a new ring to change the active key or remove historical keys.
type Keyring struct {
	activeKeyID string
	keys        map[string]cipher.AEAD
}

// New validates and copies the caller's key configuration into an immutable
// keyring. It does not generate keys or assess their entropy.
func New(activeKeyID string, keys map[string][]byte) (*Keyring, error) {
	if !validKeyID(activeKeyID) || len(keys) == 0 || len(keys) > maxKeyCount {
		return nil, ErrInvalidInput
	}
	ring := &Keyring{activeKeyID: activeKeyID, keys: make(map[string]cipher.AEAD, len(keys))}
	for keyID, callerKey := range keys {
		if !validKeyID(keyID) || len(callerKey) != 32 {
			return nil, ErrInvalidInput
		}
		keyCopy := append([]byte(nil), callerKey...)
		block, err := aes.NewCipher(keyCopy)
		if err != nil {
			return nil, ErrInvalidInput
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, ErrInvalidInput
		}
		if aead.NonceSize() != 0 || aead.Overhead() != randomNonceOverhead {
			return nil, ErrInvalidInput
		}
		ring.keys[keyID] = aead
	}
	if _, ok := ring.keys[activeKeyID]; !ok {
		return nil, ErrInvalidInput
	}
	return ring, nil
}

// Seal encrypts a bounded nonempty plaintext using the active key and binds it
// to the envelope format, key identity, project, stable secret ID, and positive
// secret version. The returned Data never aliases plaintext.
func (k *Keyring) Seal(binding Binding, plaintext []byte) (Envelope, error) {
	if k == nil || !validBinding(binding) || len(plaintext) == 0 || len(plaintext) > maxPlaintextBytes {
		return Envelope{}, ErrInvalidInput
	}
	aead, ok := k.keys[k.activeKeyID]
	if !ok {
		return Envelope{}, ErrInvalidInput
	}
	aad := associatedData(envelopeVersion, k.activeKeyID, binding)
	data := aead.Seal(nil, nil, plaintext, aad)
	return Envelope{Version: envelopeVersion, KeyID: k.activeKeyID, Data: data}, nil
}

// Open validates all bounded structure before decrypting. Unknown keys,
// authentication failures, and key aliases fail with ErrDecrypt. It returns no
// partial plaintext, and successful output never aliases Envelope.Data.
func (k *Keyring) Open(binding Binding, envelope Envelope) ([]byte, error) {
	if k == nil || !validBinding(binding) || envelope.Version != envelopeVersion || !validKeyID(envelope.KeyID) || len(envelope.Data) < randomNonceOverhead+1 || len(envelope.Data) > maxPlaintextBytes+randomNonceOverhead {
		return nil, ErrInvalidInput
	}
	aead, ok := k.keys[envelope.KeyID]
	if !ok {
		return nil, ErrDecrypt
	}
	aad := associatedData(envelope.Version, envelope.KeyID, binding)
	plaintext, err := aead.Open(nil, nil, envelope.Data, aad)
	if err != nil || len(plaintext) == 0 || len(plaintext) > maxPlaintextBytes {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func validBinding(binding Binding) bool {
	return validBindingID(binding.ProjectID) && validBindingID(binding.SecretID) && binding.Version > 0
}

func validBindingID(value string) bool {
	return len(value) > 0 && len(value) <= maxBindingIDBytes
}

func validKeyID(value string) bool {
	if len(value) == 0 || len(value) > maxKeyIDBytes {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func associatedData(version int, keyID string, binding Binding) []byte {
	capacity := len(associatedDataDomain) + 1 + 4 + 4 + len(keyID) + 4 + len(binding.ProjectID) + 4 + len(binding.SecretID) + 8
	aad := make([]byte, 0, capacity)
	aad = append(aad, associatedDataDomain...)
	aad = append(aad, 0)
	aad = binary.BigEndian.AppendUint32(aad, uint32(version))
	aad = appendLengthPrefixed(aad, keyID)
	aad = appendLengthPrefixed(aad, binding.ProjectID)
	aad = appendLengthPrefixed(aad, binding.SecretID)
	aad = binary.BigEndian.AppendUint64(aad, uint64(binding.Version))
	return aad
}

func appendLengthPrefixed(destination []byte, value string) []byte {
	destination = binary.BigEndian.AppendUint32(destination, uint32(len(value)))
	return append(destination, value...)
}
