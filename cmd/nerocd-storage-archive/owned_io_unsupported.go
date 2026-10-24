//go:build !linux && !darwin

package main

import (
	"context"
	"os"
)

func runOwned(context.Context, []string, *os.File, *os.File, func(string)) error {
	return errUnsupportedStream
}

func writeDiagnostic(*os.File, string) error {
	return errUnsupportedStream
}
