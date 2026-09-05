package secretcrypto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSealOpenRoundTripAndBounds(t *testing.T) {
	key := testKey(1)
	ring, err := New("active", map[string][]byte{"active": key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	binding := Binding{ProjectID: "project-1", SecretID: "secret-1", Version: 1}

	for _, size := range []int{1, maxPlaintextBytes} {
		t.Run(fmt.Sprintf("size-%d", size), func(t *testing.T) {
			plaintext := bytes.Repeat([]byte{0xa5}, size)
			envelope, err := ring.Seal(binding, plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if envelope.Version != envelopeVersion || envelope.KeyID != "active" {
				t.Fatalf("unexpected envelope metadata: %+v", envelope)
			}
			if got, want := len(envelope.Data), size+randomNonceOverhead; got != want {
				t.Fatalf("encrypted data length = %d, want %d", got, want)
			}

			opened, err := ring.Open(binding, envelope)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(opened, plaintext) {
				t.Fatal("opened plaintext differs")
			}
		})
	}

	first, err := ring.Seal(binding, []byte("same plaintext"))
	if err != nil {
		t.Fatalf("first Seal: %v", err)
	}
	second, err := ring.Seal(binding, []byte("same plaintext"))
	if err != nil {
		t.Fatalf("second Seal: %v", err)
	}
	if bytes.Equal(first.Data, second.Data) {
		t.Fatal("random-nonce encryptions unexpectedly produced identical data")
	}
}

func TestNewValidatesConfigurationAndCopiesKeys(t *testing.T) {
	validKey := testKey(2)
	tests := []struct {
		name   string
		active string
		keys   map[string][]byte
	}{
		{name: "no keys", active: "active", keys: nil},
		{name: "missing active", active: "missing", keys: map[string][]byte{"other": validKey}},
		{name: "empty active", active: "", keys: map[string][]byte{"active": validKey}},
		{name: "short key", active: "active", keys: map[string][]byte{"active": validKey[:31]}},
		{name: "long key", active: "active", keys: map[string][]byte{"active": append(append([]byte(nil), validKey...), 0)}},
		{name: "empty key id", active: "active", keys: map[string][]byte{"active": validKey, "": validKey}},
		{name: "space in key id", active: "bad id", keys: map[string][]byte{"bad id": validKey}},
		{name: "slash in key id", active: "bad/id", keys: map[string][]byte{"bad/id": validKey}},
		{name: "non ascii key id", active: "kulcs-á", keys: map[string][]byte{"kulcs-á": validKey}},
		{name: "oversized key id", active: strings.Repeat("a", maxKeyIDBytes+1), keys: map[string][]byte{strings.Repeat("a", maxKeyIDBytes+1): validKey}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ring, err := New(test.active, test.keys)
			if ring != nil || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("New = (%v, %v), want (nil, ErrInvalidInput)", ring, err)
			}
		})
	}

	tooMany := make(map[string][]byte, maxKeyCount+1)
	for index := 0; index <= maxKeyCount; index++ {
		tooMany[fmt.Sprintf("key-%02d", index)] = validKey
	}
	if ring, err := New("key-00", tooMany); ring != nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New with too many keys = (%v, %v), want (nil, ErrInvalidInput)", ring, err)
	}

	maximum := make(map[string][]byte, maxKeyCount)
	for index := 0; index < maxKeyCount; index++ {
		maximum[fmt.Sprintf("key-%02d", index)] = validKey
	}
	if _, err := New("key-00", maximum); err != nil {
		t.Fatalf("New with maximum key count: %v", err)
	}
	maximumID := strings.Repeat("a", maxKeyIDBytes)
	if _, err := New(maximumID, map[string][]byte{maximumID: validKey}); err != nil {
		t.Fatalf("New with maximum-length key ID: %v", err)
	}

	callerKey := testKey(3)
	callerKeys := map[string][]byte{"active": callerKey}
	ring, err := New("active", callerKeys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	callerKey[0] ^= 0xff
	callerKeys["active"] = testKey(9)
	delete(callerKeys, "active")
	binding := Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	envelope, err := ring.Seal(binding, []byte("value"))
	if err != nil {
		t.Fatalf("Seal after caller mutation: %v", err)
	}
	opened, err := ring.Open(binding, envelope)
	if err != nil || string(opened) != "value" {
		t.Fatalf("Open after caller mutation = (%q, %v)", opened, err)
	}
}

func TestSealRejectsInvalidBindingAndPlaintext(t *testing.T) {
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(4)})
	valid := Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	tests := []struct {
		name      string
		ring      *Keyring
		binding   Binding
		plaintext []byte
	}{
		{name: "nil ring", ring: nil, binding: valid, plaintext: []byte("x")},
		{name: "empty project", ring: ring, binding: Binding{SecretID: "secret", Version: 1}, plaintext: []byte("x")},
		{name: "empty secret", ring: ring, binding: Binding{ProjectID: "project", Version: 1}, plaintext: []byte("x")},
		{name: "long project", ring: ring, binding: Binding{ProjectID: strings.Repeat("p", maxBindingIDBytes+1), SecretID: "secret", Version: 1}, plaintext: []byte("x")},
		{name: "long secret", ring: ring, binding: Binding{ProjectID: "project", SecretID: strings.Repeat("s", maxBindingIDBytes+1), Version: 1}, plaintext: []byte("x")},
		{name: "zero version", ring: ring, binding: Binding{ProjectID: "project", SecretID: "secret"}, plaintext: []byte("x")},
		{name: "negative version", ring: ring, binding: Binding{ProjectID: "project", SecretID: "secret", Version: -1}, plaintext: []byte("x")},
		{name: "empty plaintext", ring: ring, binding: valid, plaintext: nil},
		{name: "long plaintext", ring: ring, binding: valid, plaintext: make([]byte, maxPlaintextBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope, err := test.ring.Seal(test.binding, test.plaintext)
			if envelope.Version != 0 || envelope.KeyID != "" || envelope.Data != nil || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Seal = (%+v, %v), want zero envelope and ErrInvalidInput", envelope, err)
			}
		})
	}
}

func TestBindingMaximumLengths(t *testing.T) {
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(14)})
	binding := Binding{
		ProjectID: strings.Repeat("p", maxBindingIDBytes),
		SecretID:  strings.Repeat("s", maxBindingIDBytes),
		Version:   1,
	}
	envelope, err := ring.Seal(binding, []byte("value"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := ring.Open(binding, envelope)
	if err != nil || string(opened) != "value" {
		t.Fatalf("Open = (%q, %v)", opened, err)
	}
}

func TestOpenRejectsMalformedEnvelopeBeforeDecryption(t *testing.T) {
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(5)})
	binding := Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	valid, err := ring.Seal(binding, []byte("value"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	tests := []struct {
		name     string
		ring     *Keyring
		binding  Binding
		envelope Envelope
	}{
		{name: "nil ring", ring: nil, binding: binding, envelope: valid},
		{name: "invalid binding", ring: ring, binding: Binding{}, envelope: valid},
		{name: "old envelope version", ring: ring, binding: binding, envelope: Envelope{Version: 0, KeyID: valid.KeyID, Data: valid.Data}},
		{name: "future envelope version", ring: ring, binding: binding, envelope: Envelope{Version: 2, KeyID: valid.KeyID, Data: valid.Data}},
		{name: "invalid key id", ring: ring, binding: binding, envelope: Envelope{Version: 1, KeyID: "bad key", Data: valid.Data}},
		{name: "empty data", ring: ring, binding: binding, envelope: Envelope{Version: 1, KeyID: valid.KeyID}},
		{name: "overhead only", ring: ring, binding: binding, envelope: Envelope{Version: 1, KeyID: valid.KeyID, Data: make([]byte, randomNonceOverhead)}},
		{name: "oversized data", ring: ring, binding: binding, envelope: Envelope{Version: 1, KeyID: valid.KeyID, Data: make([]byte, maxPlaintextBytes+randomNonceOverhead+1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plaintext, err := test.ring.Open(test.binding, test.envelope)
			if plaintext != nil || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Open = (%x, %v), want (nil, ErrInvalidInput)", plaintext, err)
			}
		})
	}
}

func TestOpenAuthenticatesAllMetadataAndCiphertext(t *testing.T) {
	sharedKey := testKey(6)
	ring := mustRing(t, "active", map[string][]byte{
		"active": sharedKey,
		"alias":  sharedKey,
	})
	binding := Binding{ProjectID: "a", SecretID: "bc", Version: 7}
	envelope, err := ring.Seal(binding, []byte("sensitive value"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tests := []struct {
		name     string
		binding  Binding
		envelope Envelope
	}{
		{name: "project", binding: Binding{ProjectID: "ab", SecretID: "bc", Version: 7}, envelope: envelope},
		{name: "secret", binding: Binding{ProjectID: "a", SecretID: "c", Version: 7}, envelope: envelope},
		{name: "field boundaries", binding: Binding{ProjectID: "ab", SecretID: "c", Version: 7}, envelope: envelope},
		{name: "version", binding: Binding{ProjectID: "a", SecretID: "bc", Version: 8}, envelope: envelope},
		{name: "key alias", binding: binding, envelope: Envelope{Version: 1, KeyID: "alias", Data: envelope.Data}},
		{name: "truncated ciphertext", binding: binding, envelope: Envelope{Version: 1, KeyID: "active", Data: envelope.Data[:len(envelope.Data)-1]}},
	}
	tampered := append([]byte(nil), envelope.Data...)
	tampered[len(tampered)-1] ^= 1
	tests = append(tests, struct {
		name     string
		binding  Binding
		envelope Envelope
	}{name: "tampered ciphertext", binding: binding, envelope: Envelope{Version: 1, KeyID: "active", Data: tampered}})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plaintext, err := ring.Open(test.binding, test.envelope)
			if plaintext != nil || !errors.Is(err, ErrDecrypt) {
				t.Fatalf("Open = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
			}
		})
	}

	unknown := envelope
	unknown.KeyID = "unknown"
	if plaintext, err := ring.Open(binding, unknown); plaintext != nil || !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open with unknown key = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
	}
	wrongRing := mustRing(t, "active", map[string][]byte{"active": testKey(7)})
	if plaintext, err := wrongRing.Open(binding, envelope); plaintext != nil || !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open with wrong key = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
	}

	nulBinding := Binding{ProjectID: "project\x00one", SecretID: "sëcret", Version: 1}
	nulEnvelope, err := ring.Seal(nulBinding, []byte("value"))
	if err != nil {
		t.Fatalf("Seal length-prefixed binding: %v", err)
	}
	if opened, err := ring.Open(nulBinding, nulEnvelope); err != nil || string(opened) != "value" {
		t.Fatalf("Open length-prefixed binding = (%q, %v)", opened, err)
	}
	collisionBinding := Binding{ProjectID: "project", SecretID: "\x00onesëcret", Version: 1}
	if plaintext, err := ring.Open(collisionBinding, nulEnvelope); plaintext != nil || !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open ambiguous-looking binding = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
	}
}

func TestSealAndOpenDoNotAliasCallerBuffers(t *testing.T) {
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(8)})
	binding := Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	plaintext := []byte("original")
	envelope, err := ring.Seal(binding, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	plaintext[0] = 'X'
	opened, err := ring.Open(binding, envelope)
	if err != nil || string(opened) != "original" {
		t.Fatalf("Open after plaintext mutation = (%q, %v)", opened, err)
	}

	envelopeSnapshot := append([]byte(nil), envelope.Data...)
	opened[0] = 'Y'
	if !bytes.Equal(envelope.Data, envelopeSnapshot) {
		t.Fatal("opened plaintext aliases envelope data")
	}
	secondOpen, err := ring.Open(binding, envelope)
	if err != nil || string(secondOpen) != "original" {
		t.Fatalf("second Open = (%q, %v)", secondOpen, err)
	}
	envelope.Data[0] ^= 1
	if string(secondOpen) != "original" {
		t.Fatal("previous plaintext aliases subsequently mutated envelope data")
	}
}

func TestHistoricalKeyRotation(t *testing.T) {
	oldKey := testKey(10)
	newKey := testKey(11)
	binding := Binding{ProjectID: "project", SecretID: "secret", Version: 1}
	oldRing := mustRing(t, "old", map[string][]byte{"old": oldKey, "new": newKey})
	oldEnvelope, err := oldRing.Seal(binding, []byte("old value"))
	if err != nil {
		t.Fatalf("old Seal: %v", err)
	}

	rotatedRing := mustRing(t, "new", map[string][]byte{"old": oldKey, "new": newKey})
	if opened, err := rotatedRing.Open(binding, oldEnvelope); err != nil || string(opened) != "old value" {
		t.Fatalf("rotated ring Open old envelope = (%q, %v)", opened, err)
	}
	newEnvelope, err := rotatedRing.Seal(binding, []byte("new value"))
	if err != nil || newEnvelope.KeyID != "new" {
		t.Fatalf("rotated ring Seal = (%+v, %v)", newEnvelope, err)
	}

	removedRing := mustRing(t, "new", map[string][]byte{"new": newKey})
	if plaintext, err := removedRing.Open(binding, oldEnvelope); plaintext != nil || !errors.Is(err, ErrDecrypt) {
		t.Fatalf("removed-key Open = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
	}
}

func TestKeyringConcurrentUse(t *testing.T) {
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(12)})
	const goroutines = 24
	const iterations = 40
	errorsChannel := make(chan error, goroutines)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				binding := Binding{ProjectID: fmt.Sprintf("project-%d", worker), SecretID: "secret", Version: int64(iteration + 1)}
				plaintext := []byte(fmt.Sprintf("value-%d-%d", worker, iteration))
				envelope, err := ring.Seal(binding, plaintext)
				if err != nil {
					errorsChannel <- fmt.Errorf("Seal: %w", err)
					return
				}
				opened, err := ring.Open(binding, envelope)
				if err != nil {
					errorsChannel <- fmt.Errorf("Open: %w", err)
					return
				}
				if !bytes.Equal(opened, plaintext) {
					errorsChannel <- errors.New("opened plaintext differs")
					return
				}
			}
		}(worker)
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func TestErrorsAreGeneric(t *testing.T) {
	if got, want := ErrInvalidInput.Error(), "secretcrypto: invalid input"; got != want {
		t.Fatalf("ErrInvalidInput = %q, want %q", got, want)
	}
	if got, want := ErrDecrypt.Error(), "secretcrypto: decryption failed"; got != want {
		t.Fatalf("ErrDecrypt = %q, want %q", got, want)
	}
	ring := mustRing(t, "active", map[string][]byte{"active": testKey(13)})
	plaintext, err := ring.Open(
		Binding{ProjectID: "private-project", SecretID: "private-secret", Version: 1},
		Envelope{Version: 1, KeyID: "missing-private-key", Data: make([]byte, randomNonceOverhead+1)},
	)
	if plaintext != nil || err != ErrDecrypt {
		t.Fatalf("Open = (%x, %v), want (nil, ErrDecrypt)", plaintext, err)
	}
	for _, privateValue := range []string{"private-project", "private-secret", "missing-private-key"} {
		if strings.Contains(err.Error(), privateValue) {
			t.Fatalf("error exposes caller-controlled value %q", privateValue)
		}
	}
}

func mustRing(t *testing.T, activeKeyID string, keys map[string][]byte) *Keyring {
	t.Helper()
	ring, err := New(activeKeyID, keys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ring
}

func testKey(seed byte) []byte {
	key := make([]byte, 32)
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}
