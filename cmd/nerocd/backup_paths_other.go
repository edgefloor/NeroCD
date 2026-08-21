//go:build !unix

package main

import (
	"errors"
	"os"
)

func secureBackupDirectory(string) error {
	return errors.New("secure backup paths are unsupported on this platform")
}
func secureBackupRegular(string) error {
	return errors.New("secure backup paths are unsupported on this platform")
}
func openSecureBackupFile(string) (*os.File, error) {
	return nil, errors.New("secure backup paths are unsupported on this platform")
}
func readSecureBackupFile(string) ([]byte, error) {
	return nil, errors.New("secure backup paths are unsupported on this platform")
}
