//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func readOwnerOnlyProductionSecret(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("production database secret file is invalid")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("production database secret file cannot be read")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != uint32(syscall.Geteuid()) || stat.Mode&0077 != 0 || stat.Size <= 0 || stat.Size > 8192 {
		return nil, errors.New("production database secret file permissions are invalid")
	}
	f := os.NewFile(uintptr(fd), "production-secret")
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("production database secret file cannot be read")
	}
	defer f.Close()
	contents, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(contents) == 0 || len(contents) > 8192 {
		return nil, errors.New("production database secret file permissions are invalid")
	}
	return contents, nil
}
