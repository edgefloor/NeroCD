//go:build !linux

package runnerruntime

import (
	"context"
	"errors"
	"nerocd/internal/domain"
)

func validateBuildSourceRoot(string) error {
	return errors.New("hosting build executor requires Linux")
}
func lockBuildSource(string, string) (func(), error) {
	return nil, errors.New("hosting build executor requires Linux")
}
func observeBuildDaemon(context.Context, BuildConfig) (string, error) {
	return "", errors.New("hosting build executor requires Linux")
}
func checkoutPublicBuild(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
	return errors.New("hosting build executor requires Linux")
}

func checkoutBuildWithCredential(context.Context, domain.HostingBuildSnapshot, string, string, func(context.Context) (SourceCredential, error), func(context.Context, domain.HostingBuildSourceReceipt) error, func(context.Context, string) error) error {
	return errors.New("hosting build executor requires Linux")
}
