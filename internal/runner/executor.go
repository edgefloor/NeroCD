package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

func ExecuteProcess(ctx context.Context, spec domain.ProcessSpec, emit func(ProcessEvent)) (ProcessResult, error) {
	if len(spec.Command) == 0 {
		return ProcessResult{ExitCode: -1}, errors.New("process command is required")
	}
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	if spec.WorkingDir != "" {
		cmd.Dir = spec.WorkingDir
	}
	if len(spec.Environment) > 0 {
		cmd.Env = cmd.Environ()
		for key, value := range spec.Environment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}

	stdoutReader, err := cmd.StdoutPipe()
	if err != nil {
		return ProcessResult{ExitCode: -1, EndedAt: time.Now().UTC()}, err
	}
	stderrReader, err := cmd.StderrPipe()
	if err != nil {
		return ProcessResult{ExitCode: -1, EndedAt: time.Now().UTC()}, err
	}

	result := ProcessResult{ExitCode: -1, StartedAt: time.Now().UTC()}
	if err := cmd.Start(); err != nil {
		result.EndedAt = time.Now().UTC()
		return result, err
	}

	var wg sync.WaitGroup
	scan := func(stream string, scanner *bufio.Scanner) {
		defer wg.Done()
		scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
		for scanner.Scan() {
			emit(ProcessEvent{Stream: stream, Message: scanner.Text()})
		}
		if err := scanner.Err(); err != nil {
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
	err = <-waitErr
	wg.Wait()
	result.EndedAt = time.Now().UTC()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	if errors.Is(ctx.Err(), context.Canceled) && !result.TimedOut {
		result.Canceled = true
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, nil
		}
		if result.TimedOut || result.Canceled {
			return result, nil
		}
		return result, err
	}
	return result, nil
}
