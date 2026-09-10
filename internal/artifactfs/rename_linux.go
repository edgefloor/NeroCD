//go:build linux

package artifactfs

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func (d *directoryBackend) renameNoReplace(oldName, newName string) error {
	err := unix.Renameat2(d.fd, oldName, d.fd, newName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return os.ErrExist
	}
	if err != nil {
		return ioError("publish object", err)
	}
	return nil
}
