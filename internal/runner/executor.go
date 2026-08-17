package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"nerocd/internal/domain"
)

type ProcessEvent struct {
	Stream  string
	Message string
}

type ProcessResult struct {
	ExitCode  int
	TimedOut  bool
	Canceled  bool
	StartedAt time.Time
	EndedAt   time.Time
}

type processWaitOutcome struct {
	err      error
	received bool
}

const processTerminationGrace = 500 * time.Millisecond

const (
	processGroupPollInterval   = 10 * time.Millisecond
	processGroupCleanupTimeout = time.Second
	processOutputDrainTimeout  = time.Second
	processOutputCloseTimeout  = time.Second
)

func ExecuteProcess(ctx context.Context, spec domain.ProcessSpec, emit func(ProcessEvent)) (ProcessResult, error) {
	if len(spec.Command) == 0 {
		return ProcessResult{ExitCode: -1}, errors.New("process command is required")
	}
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// CommandContext kills only the immediate child. Runner processes may spawn
	// helpers that retain credentials, filesystem handles, or output pipes, so an
	// attempt owns a dedicated process group and tears down that whole group.
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	if err := configureProcessGroup(cmd); err != nil {
		return ProcessResult{ExitCode: -1, EndedAt: time.Now().UTC()}, err
	}
	if spec.WorkingDir != "" {
		cmd.Dir = spec.WorkingDir
	}
	if len(spec.Environment) > 0 {
		cmd.Env = cmd.Environ()
		for key, value := range spec.Environment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return ProcessResult{ExitCode: -1, EndedAt: time.Now().UTC()}, err
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return ProcessResult{ExitCode: -1, EndedAt: time.Now().UTC()}, err
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	result := ProcessResult{ExitCode: -1, StartedAt: time.Now().UTC()}
	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		result.EndedAt = time.Now().UTC()
		return result, err
	}
	// The child received duplicated descriptors at Start. Keeping these parent
	// writers open would prevent scanners from ever observing EOF.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	var wg sync.WaitGroup
	scan := func(stream string, scanner *bufio.Scanner) {
		defer wg.Done()
		scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
		for scanner.Scan() {
			emit(ProcessEvent{Stream: stream, Message: scanner.Text()})
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			emit(ProcessEvent{Stream: domain.LogSystem, Message: fmt.Sprintf("%s stream read failed: %v", stream, err)})
		}
	}
	wg.Add(2)
	go scan(domain.LogStdout, bufio.NewScanner(stdoutReader))
	go scan(domain.LogStderr, bufio.NewScanner(stderrReader))

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()
	var canceled bool
	var cleanupErr error
	waitOutcome := processWaitOutcome{}
	select {
	case err = <-waitErr:
		waitOutcome = processWaitOutcome{err: err, received: true}
		cleanupErr = cleanRemainingProcessGroup(cmd.Process.Pid, waitErr, waitOutcome)
	case <-ctx.Done():
		canceled = true
		waitOutcome, cleanupErr = terminateProcessGroupAndWait(cmd.Process.Pid, waitErr, waitOutcome)
	}
	drainProcessScanners(&wg, stdoutReader, stderrReader)
	result.EndedAt = time.Now().UTC()
	if waitOutcome.received && cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if canceled && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	if canceled && errors.Is(ctx.Err(), context.Canceled) && !result.TimedOut {
		result.Canceled = true
	}
	if cleanupErr != nil {
		return result, cleanupErr
	}
	if waitOutcome.err != nil {
		var exitErr *exec.ExitError
		if errors.As(waitOutcome.err, &exitErr) {
			return result, nil
		}
		if result.TimedOut || result.Canceled {
			return result, nil
		}
		return result, waitOutcome.err
	}
	return result, nil
}

func drainProcessScanners(wg *sync.WaitGroup, stdoutReader, stderrReader *os.File) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(processOutputDrainTimeout):
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
		// A callback may itself be blocked. Return after one further bounded
		// interval rather than letting an arbitrary emit implementation hang the
		// runner forever. The callback goroutine can finish once it is released;
		// unread output after this forced close is intentionally discarded.
		select {
		case <-done:
		case <-time.After(processOutputCloseTimeout):
		}
	}
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
}

// cleanRemainingProcessGroup preserves ordinary leader exit semantics while
// ensuring a detached descendant cannot survive the attempt.
func cleanRemainingProcessGroup(pid int, waitCh <-chan error, waitOutcome processWaitOutcome) error {
	alive, err := processGroupAlive(pid)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	_, err = terminateProcessGroupAndWait(pid, waitCh, waitOutcome)
	return err
}

// terminateProcessGroupAndWait does not let an exited leader hide a surviving
// descendant. It observes the process group through the TERM grace, escalates
// any remaining member to KILL, then consumes the direct child's Wait result
// unless its caller already did so.
func terminateProcessGroupAndWait(pid int, waitCh <-chan error, waitOutcome processWaitOutcome) (processWaitOutcome, error) {
	var terminationErr error
	if err := signalProcessGroup(pid, processSignalTerm); err != nil {
		terminationErr = err
	}

	ticker := time.NewTicker(processGroupPollInterval)
	defer ticker.Stop()
	graceDeadline := time.Now().Add(processTerminationGrace)
	var cleanupDeadline time.Time
	killed := false
	leaderWait := waitCh

	for {
		alive, err := processGroupAlive(pid)
		if err != nil && terminationErr == nil {
			terminationErr = err
		}
		if !alive {
			if !waitOutcome.received {
				var received bool
				waitOutcome, received = receiveProcessWait(leaderWait, processGroupCleanupTimeout)
				if !received {
					return waitOutcome, fmt.Errorf("process leader wait did not complete after group %d exited", pid)
				}
			}
			return waitOutcome, terminationErr
		}

		now := time.Now()
		if !killed && !now.Before(graceDeadline) {
			if err := signalProcessGroup(pid, processSignalKill); err != nil && terminationErr == nil {
				terminationErr = err
			}
			killed = true
			cleanupDeadline = now.Add(processGroupCleanupTimeout)
		}
		if killed && !now.Before(cleanupDeadline) {
			return waitOutcome, fmt.Errorf("process group %d survived SIGKILL", pid)
		}

		select {
		case waitOutcome.err = <-leaderWait:
			waitOutcome.received = true
			leaderWait = nil
		case <-ticker.C:
		}
	}
}

func receiveProcessWait(waitCh <-chan error, timeout time.Duration) (processWaitOutcome, bool) {
	select {
	case err := <-waitCh:
		return processWaitOutcome{err: err, received: true}, true
	case <-time.After(timeout):
		return processWaitOutcome{}, false
	}
}
