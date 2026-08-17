//go:build linux || darwin

package runner

import (
	"errors"
	"os/exec"
	"syscall"
)

type processGroupSignal syscall.Signal

const (
	processSignalTerm processGroupSignal = processGroupSignal(syscall.SIGTERM)
	processSignalKill processGroupSignal = processGroupSignal(syscall.SIGKILL)
)

func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func signalProcessGroup(pid int, signal processGroupSignal) error {
	err := syscall.Kill(-pid, syscall.Signal(signal))
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
