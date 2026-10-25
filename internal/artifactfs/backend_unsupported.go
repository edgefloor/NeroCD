//go:build !linux && !darwin

package artifactfs

import (
	"context"
	"os"
)

type directoryBackend struct{}

type fileMetadata struct {
	size int64
}

func openDirectoryBackend(string) (*directoryBackend, error)    { return nil, ErrUnsupported }
func (*directoryBackend) createTemp() (string, *os.File, error) { return "", nil, ErrUnsupported }
func (*directoryBackend) openRegular(string, int64) (*os.File, fileMetadata, error) {
	return nil, fileMetadata{}, ErrUnsupported
}
func (*directoryBackend) verifyOpenFile(*os.File, fileMetadata, int64) error { return ErrUnsupported }
func (*directoryBackend) remove(string) error                                { return ErrUnsupported }
func (*directoryBackend) sync() error                                        { return ErrUnsupported }
func (*directoryBackend) close() error                                       { return nil }
func (*directoryBackend) renameNoReplace(string, string) error               { return ErrUnsupported }

func (*directoryBackend) lock(context.Context) error { return ErrUnsupported }
func (*directoryBackend) unlock()                    {}
func (*directoryBackend) ownership(string, int64) (Ownership, error) {
	return Ownership{}, ErrUnsupported
}

func (*directoryBackend) poolID() (string, error) { return "", ErrUnsupported }
