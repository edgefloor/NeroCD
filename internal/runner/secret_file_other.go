//go:build !linux && !darwin

package runner

import "errors"

type FileSecretResolver struct{}

func OpenFileSecretResolver(string) (*FileSecretResolver, error) {
	return nil, errors.New("runner_file secrets are supported only on Linux and Darwin")
}

func (*FileSecretResolver) Read(string) (string, error) {
	return "", errors.New("runner_file secrets are unsupported on this platform")
}

func (*FileSecretResolver) Close() error { return nil }
