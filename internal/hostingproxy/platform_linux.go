//go:build linux

package hostingproxy

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

var errBusy = errors.New("proxy lock busy")

func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errBusy
	}
	return err
}
func owned(st os.FileInfo) error {
	v, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(v.Uid) != os.Geteuid() {
		return errors.New("state must belong to effective user")
	}
	return nil
}
func platform() error { return nil }

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
