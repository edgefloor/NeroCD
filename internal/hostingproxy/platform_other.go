//go:build !linux

package hostingproxy

import (
	"errors"
	"os"
	"os/exec"
)

var errBusy = errors.New("proxy lock busy")

func platform() error         { return errors.New("hosting proxy requires Linux") }
func tryLock(*os.File) error  { return platform() }
func owned(os.FileInfo) error { return platform() }

func configureCommand(*exec.Cmd) {}
