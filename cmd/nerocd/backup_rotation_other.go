//go:build !unix

package main

import "errors"

func rotateSecureBackups(string, int) error {
	return errors.New("secure backup rotation is unsupported on this platform")
}
