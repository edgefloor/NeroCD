//go:build !unix

package secretkeys

import "errors"

func readFile(string) ([]byte, error) {
	return nil, errors.New("secretkeys: keyring files unsupported on this platform")
}
