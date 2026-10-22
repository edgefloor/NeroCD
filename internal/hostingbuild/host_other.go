//go:build !linux

package hostingbuild

import (
	"context"
	"errors"
	"os"
)

func ownedByCurrentUser(os.FileInfo) error            { return errors.New("linux required") }
func acquireLock(string) (*os.File, error)            { return nil, errors.New("linux required") }
func freeBytes(string) (uint64, error)                { return 0, errors.New("linux required") }
func containment(int, Limits, string) (string, error) { return "", errors.New("linux required") }
func verifyDescendants(context.Context, int, string, Limits) error {
	return errors.New("linux required")
}

func inspectDescendants(context.Context, int, string, Limits) (int, error) {
	return 0, errors.New("linux required")
}

func acquireHostLock() (*os.File, error)    { return nil, errors.New("linux required") }
func groupRelative(string) string           { return "" }
func validateDelegation(string) error       { return errors.New("linux required") }
func createBuildGroup(string, Limits) error { return errors.New("linux required") }
func (e *Engine) cleanupGroup(context.Context, ownedResources) error {
	return errors.New("linux required")
}

func checkGroup(string, Limits) error { return errors.New("linux required") }

func processDescendants(context.Context, int) (map[int]bool, error) {
	return nil, errors.New("linux required")
}
