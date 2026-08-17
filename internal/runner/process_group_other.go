//go:build !linux && !darwin

package runner

import (
	"errors"
	"os/exec"
)

type processGroupSignal uint8

const (
	processSignalTerm processGroupSignal = iota
	processSignalKill
)

func configureProcessGroup(*exec.Cmd) error {
	return errors.New("process-group cancellation is unsupported on this platform")
}

func signalProcessGroup(int, processGroupSignal) error {
	return errors.New("process-group cancellation is unsupported on this platform")
}

func processGroupAlive(int) (bool, error) {
	return false, errors.New("process-group cancellation is unsupported on this platform")
}
