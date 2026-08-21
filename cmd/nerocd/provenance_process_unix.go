//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureProvenanceProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext's default cancellation kills only its immediate
	// child. Compose and shell wrappers can leave descendants behind, so an
	// attempt owns a process group and terminates the whole group explicitly.
	// Cancel is intentionally bounded: a cooperative group gets TERM first;
	// a TERM-ignoring descendant gets KILL before Wait may return.
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		pid := command.Process.Pid
		termErr := syscall.Kill(-pid, syscall.SIGTERM)
		if errors.Is(termErr, syscall.ESRCH) {
			return nil
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		<-timer.C
		if err := syscall.Kill(-pid, 0); err == nil {
			if killErr := syscall.Kill(-pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				return killErr
			}
		}
		return termErr
	}
}

func killProvenanceProcess(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
