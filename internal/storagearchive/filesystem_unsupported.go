//go:build !linux

package storagearchive

import (
	"context"
	"io"
	"os"
)

// Write is unavailable on platforms without descriptor-relative no-follow IO.
func Write(context.Context, *os.Root, io.Writer, Limits) (Summary, error) {
	return Summary{}, ErrUnsupported
}

// Extract is unavailable on platforms without descriptor-relative no-follow IO.
func Extract(context.Context, *os.File, *os.Root, Limits) (Summary, error) {
	return Summary{}, ErrUnsupported
}
