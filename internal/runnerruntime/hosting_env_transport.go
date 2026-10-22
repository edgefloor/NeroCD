package runnerruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/distribution/reference"
)

var hostingEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var hostingEnvironmentImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const hostingEnvironmentInputLimit = 256 << 10

func encodeHostingEnvironment(values map[string][]byte) ([]byte, error) {
	if len(values) > 64 {
		return nil, errors.New("invalid hosting runtime environment")
	}
	names := make([]string, 0, len(values))
	size := 0
	for name, value := range values {
		if !hostingEnvironmentName.MatchString(name) || strings.HasPrefix(name, "NEROCD_") || len(value) > 4096 || !utf8.Valid(value) || bytes.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid hosting runtime environment")
		}
		size += len(name) + len(value) + 2
		if size > hostingEnvironmentInputLimit {
			return nil, errors.New("invalid hosting runtime environment")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	body := make([]byte, 0, size)
	for _, name := range names {
		body = append(body, name...)
		body = append(body, '=')
		body = append(body, values[name]...)
		body = append(body, '\n')
	}
	return body, nil
}

func validHostingEnvironmentCreate(flags []string, image string) bool {
	if !hostingEnvironmentImageID.MatchString(image) {
		named, err := reference.ParseNormalizedNamed(image)
		if err != nil {
			return false
		}
		digested, ok := named.(reference.Digested)
		if !ok || !hostingEnvironmentImageID.MatchString(digested.Digest().String()) {
			return false
		}
	}
	if len(flags) > 128 {
		return false
	}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--pull=never":
		case "--name", "--restart", "--publish", "--label":
			i++
			if i == len(flags) || flags[i] == "" || len(flags[i]) > 4096 || strings.HasPrefix(flags[i], "-") || strings.ContainsAny(flags[i], "\x00\r\n") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

type hostingEnvironmentOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (b *hostingEnvironmentOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 128 {
		b.cancel()
		return 0, errors.New("hosting container output exceeded limit")
	}
	return b.Buffer.Write(p)
}

// createWithRuntimeEnvironment qualifies protected Docker stdin delivery. It is
// deliberately not wired into Apply until runtime value authority is available.
// A failure after launch can still have created a container. Callers must retain
// that uncertain outcome and must not retry the mutation automatically.
// Docker's trusted daemon administrator can inspect the resulting Config.Env.
func (e *imageEngine) createWithRuntimeEnvironment(ctx context.Context, daemonID string, createFlags []string, image string, values map[string][]byte) (string, error) {
	failed := errors.New("hosting runtime environment container create failed")
	if !validHostingEnvironmentCreate(createFlags, image) {
		return "", failed
	}
	body, err := encodeHostingEnvironment(values)
	if err != nil {
		return "", failed
	}
	// Clear only the transport-owned copy. Go and Docker can hold other copies;
	// this does not promise memory erasure or hide values from the daemon.
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if ctx.Err() != nil || e.verifyDaemon(ctx, daemonID) != nil {
		return "", failed
	}
	args := []string{"--host", e.endpoint, "--config", e.configDir, "container", "create"}
	args = append(args, createFlags...)
	args = append(args, "--env-file", "/dev/stdin", image)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Dir = e.configDir
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	command.Env = []string{"PATH=" + path, "HOME=" + e.configDir, "LANG=C", "LC_ALL=C", "DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT=", "DOCKER_CLI_HINTS=false"}
	command.Stdin = bytes.NewReader(body)
	output := &hostingEnvironmentOutput{cancel: cancel}
	command.Stdout, command.Stderr = output, io.Discard
	configureProvenanceProcess(command)
	command.WaitDelay = 2 * time.Second
	err = command.Run()
	if err != nil || ctx.Err() != nil {
		// WaitDelay can expire after the parent exits while a descendant keeps
		// an inherited pipe open. The context can still be live, so every
		// uncertain exit must terminate the owned group, not just cancellation.
		killProvenanceProcess(command)
		return "", failed
	}
	id := strings.TrimSuffix(output.String(), "\n")
	if !dockerObjectID.MatchString(id) {
		killProvenanceProcess(command)
		return "", failed
	}
	return id, nil
}
