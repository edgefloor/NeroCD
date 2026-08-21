//go:build !unix

package main

import "errors"

func readOwnerOnlyProductionSecret(string) ([]byte, error) {
	return nil, errors.New("production secret files are unsupported on this platform")
}
