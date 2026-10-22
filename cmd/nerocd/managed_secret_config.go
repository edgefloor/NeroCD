package main

import (
	"errors"
	"os"

	"nerocd/internal/secretcrypto"
	"nerocd/internal/secretkeys"
)

func loadManagedSecretKeyring() (*secretcrypto.Keyring, error) {
	path := os.Getenv("NEROCD_SECRET_KEYRING_FILE")
	if path == "" {
		return nil, nil
	}
	ring, err := secretkeys.Load(path)
	if err != nil {
		return nil, errors.New("NEROCD_SECRET_KEYRING_FILE cannot be loaded safely")
	}
	return ring, nil
}
