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

func openSecureBackupDirectory(string) (*os.File, error) { return nil, secureBackupDirectory("") }
func createBackupDirectoryAt(*os.File, string) (*os.File, error) {
	return nil, secureBackupDirectory("")
}
func createBackupFileAt(*os.File, string) (*os.File, error) { return nil, secureBackupDirectory("") }
func openSecureBackupFileAt(*os.File, string) (*os.File, error) {
	return nil, secureBackupDirectory("")
}
func publishBackupDirectoryAt(*os.File, string, string) error { return secureBackupDirectory("") }
func removeBackupStagingAt(*os.File, *os.File, string)        {}
