package auth

import (
	"strings"
	"testing"
)

func TestArgon2idPasswordHashRoundTripAndMalformedValues(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil || !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
	if valid, legacy, err := VerifyPassword("correct horse battery staple", hash); err != nil || !valid || legacy {
		t.Fatalf("valid=%v legacy=%v err=%v", valid, legacy, err)
	}
	if valid, _, err := VerifyPassword("wrong", hash); err != nil || valid {
		t.Fatalf("wrong password valid=%v err=%v", valid, err)
	}
	if _, _, err := VerifyPassword("x", "$argon2id$v=19$m=9999999,t=99,p=99$bad$bad"); err == nil {
		t.Fatal("accepted unsafe malformed hash")
	}
}

func TestLegacySHA256VerificationIsMarkedForCallerPolicy(t *testing.T) {
	valid, legacy, err := VerifyPassword("admin", "sha256:8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918")
	if err != nil || !valid || !legacy {
		t.Fatalf("valid=%v legacy=%v err=%v", valid, legacy, err)
	}
}
