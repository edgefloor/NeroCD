//go:build !linux

package serverbootstrap

import (
	"context"
	"io"
)

// ServeOwnedHelper is supported only on prepared Linux targets.
func ServeOwnedHelper(context.Context, string, io.Reader, io.Writer) error { return RemoteFailure }

// RunOwnedAgent is supported only on prepared Linux targets.
func RunOwnedAgent(string, string) error { return RemoteFailure }
