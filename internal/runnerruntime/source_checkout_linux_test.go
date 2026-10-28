package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The scanner checks cancellation immediately after opening a directory. Remove
// that directory at this boundary to reproduce cleanup racing with ReadDir.
type removeOpenedSourceDirectoryContext struct {
	context.Context
	checks int
	remove func()
}

func (c *removeOpenedSourceDirectoryContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.remove()
	}
	return c.Context.Err()
}

func TestSourceScanDirectoryRemovedAfterOpen(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "workspace-monitor"
		if strict {
			name = "exported-tree"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "removed")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			removed := false
			ctx := &removeOpenedSourceDirectoryContext{Context: t.Context(), remove: func() {
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				removed = true
			}}
			_, _, err := scanSource(ctx, root, checkoutLimits(), strict)
			if !removed {
				t.Fatal("directory removal boundary was not reached")
			}
			if strict {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("exported tree must reject a vanished directory: %v", err)
				}
			} else if err != nil {
				t.Fatalf("workspace monitor rejected directory cleanup: %v", err)
			}
		})
	}
}
