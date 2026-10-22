package hostingbuild

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type cappedWriter struct {
	mu        sync.Mutex
	b         bytes.Buffer
	remaining int64
	cancel    context.CancelFunc
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(p)) > w.remaining {
		w.cancel()
		return 0, errors.New("command output budget exceeded")
	}
	w.remaining -= int64(len(p))
	return w.b.Write(p)
}
func (e *Engine) environment(dir string) []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "DOCKER_CONFIG=" + filepath.Join(dir, "docker"), "DOCKER_HOST=unix://" + e.config.Socket, "BUILDX_CONFIG=" + filepath.Join(dir, "buildx"), "BUILDX_NO_DEFAULT_ATTESTATIONS=1", "NO_COLOR=1"}
}
func (e *Engine) command(ctx context.Context, dir string, output io.Writer, binary string, args ...string) error {
	return e.commandStreams(ctx, dir, output, output, binary, args...)
}
func (e *Engine) commandStreams(ctx context.Context, dir string, stdout, stderr io.Writer, binary string, args ...string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = e.environment(dir)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		err = errors.Join(err, ctx.Err())
		if writer, ok := stdout.(*cappedWriter); ok {
			writer.mu.Lock()
			tail := writer.b.Bytes()
			if len(tail) > 8192 {
				tail = tail[len(tail)-8192:]
			}
			detail := string(tail)
			writer.mu.Unlock()
			return fmt.Errorf("%s: %w: %s", filepath.Base(binary), err, detail)
		}
		return fmt.Errorf("%s: %w", filepath.Base(binary), err)
	}
	return nil
}
func (e *Engine) docker(ctx context.Context, dir string, args ...string) ([]byte, error) {
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &cappedWriter{remaining: 1 << 20, cancel: cancel}
	args = append([]string{"--host", "unix://" + e.config.Socket, "--config", filepath.Join(dir, "docker")}, args...)
	err := e.command(run, dir, out, e.config.DockerBinary, args...)
	return out.b.Bytes(), err
}
