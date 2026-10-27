package runnerruntime

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

const runtimeLogDockerAPI = "1.54"

var errRuntimeLogSource = errors.New("runtime log source unavailable")
var errRuntimeLogDriver = errors.New("unsupported runtime log driver")

type runtimeLogSourceIdentity struct {
	TTY    bool
	Driver string
}
type runtimeLogSource interface {
	Verify(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error)
	Open(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error)
	Close()
}
type dockerRuntimeLogs struct {
	client    *http.Client
	transport *http.Transport
}

func newDockerRuntimeLogs(endpoint string) (*dockerRuntimeLogs, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path {
		return nil, errRuntimeLogSource
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 2 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
	}}
	return &dockerRuntimeLogs{transport: transport, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *dockerRuntimeLogs) Close() { s.transport.CloseIdleConnections() }
func (s *dockerRuntimeLogs) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, errRuntimeLogSource
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, errRuntimeLogSource
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, errRuntimeLogSource
	}
	return response, nil
}
func (s *dockerRuntimeLogs) document(ctx context.Context, path string, out any) error {
	response, err := s.get(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	defer clear(raw)
	if err != nil || len(raw) > 256<<10 || json.Unmarshal(raw, out) != nil {
		return errRuntimeLogSource
	}
	return nil
}
func runtimeLogAPINumber(v string) int {
	major, minor, ok := strings.Cut(v, ".")
	if !ok || major != "1" {
		return -1
	}
	n, err := strconv.Atoi(minor)
	if err != nil {
		return -1
	}
	return n
}
func (s *dockerRuntimeLogs) Verify(ctx context.Context, t domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
	var identity runtimeLogSourceIdentity
	if !validRuntimeLogTarget(t, t.RunnerID) {
		return identity, errRuntimeLogSource
	}
	var version struct {
		API string `json:"ApiVersion"`
		Min string `json:"MinAPIVersion"`
	}
	if s.document(ctx, "/version", &version) != nil || runtimeLogAPINumber(version.Min) < 0 || runtimeLogAPINumber(version.Min) > 54 || runtimeLogAPINumber(version.API) < 54 {
		return identity, errRuntimeLogSource
	}
	var info struct {
		ID string `json:"ID"`
	}
	if s.document(ctx, "/v"+runtimeLogDockerAPI+"/info", &info) != nil || info.ID != t.DaemonID {
		return identity, errRuntimeLogSource
	}
	// Decode selected fields only. Env, arbitrary labels and configuration are
	// skipped; the bounded raw document is cleared before returning.
	var c struct {
		ID     string `json:"Id"`
		Name   string
		Image  string
		Config struct {
			TTY    *bool `json:"Tty"`
			Labels struct {
				Binding   string `json:"io.nerocd.binding"`
				Operation string `json:"io.nerocd.operation"`
				Release   string `json:"io.nerocd.release"`
				Plan      string `json:"io.nerocd.plan"`
				Component string `json:"io.nerocd.stack.component"`
			}
		}
		HostConfig struct{ LogConfig struct{ Type string } }
	}
	if s.document(ctx, "/v"+runtimeLogDockerAPI+"/containers/"+t.ContainerID+"/json", &c) != nil {
		return identity, errRuntimeLogSource
	}
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(t.OperationID), "sha256:")[:16]
	name := t.RuntimeNamespace + "_" + suffix
	if t.Component != "" {
		name = t.ContainerName
	}
	if c.ID != t.ContainerID || c.Name != "/"+name || c.Image != t.ImageID || c.Config.TTY == nil || c.Config.Labels.Binding != t.BindingID || c.Config.Labels.Operation != t.OperationID || c.Config.Labels.Release != t.ReleaseHash || c.Config.Labels.Plan != t.PlanHash || c.Config.Labels.Component != t.Component {
		return identity, errRuntimeLogSource
	}
	identity = runtimeLogSourceIdentity{TTY: *c.Config.TTY, Driver: c.HostConfig.LogConfig.Type}
	if identity.Driver != "json-file" && identity.Driver != "local" {
		return identity, errRuntimeLogDriver
	}
	return identity, nil
}
func (s *dockerRuntimeLogs) Open(ctx context.Context, t domain.HostingRuntimeLogTarget) (io.ReadCloser, error) {
	if !validRuntimeLogTarget(t, t.RunnerID) {
		return nil, errRuntimeLogSource
	}
	response, err := s.get(ctx, "/v"+runtimeLogDockerAPI+"/containers/"+t.ContainerID+"/logs?stdout=1&stderr=1&timestamps=1&tail=200&follow=1")
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// runtimeLogFramer redacts before retaining output. Only bounded redacted line
// fragments and at most three incomplete UTF-8 bytes survive an input callback.
type runtimeLogFramer struct {
	redactor  *runner.Redactor
	lines     map[string]string
	utf8tails map[string]string
	emit      func(domain.HostingRuntimeLogEvent) error
}

func newRuntimeLogFramer(token string, emit func(domain.HostingRuntimeLogEvent) error) *runtimeLogFramer {
	return &runtimeLogFramer{redactor: runner.NewRedactor([]runner.SecretMaterial{{Value: token, Encodings: []string{"base64", "base64url", "hex"}}}), lines: map[string]string{}, utf8tails: map[string]string{}, emit: emit}
}
func (f *runtimeLogFramer) chunk(stream string, raw []byte) error {
	safe := f.utf8tails[stream] + f.redactor.RedactChunk(stream, string(raw))
	delete(f.utf8tails, stream)
	var text strings.Builder
	for len(safe) > 0 {
		if !utf8.FullRuneInString(safe) {
			f.utf8tails[stream] = safe
			break
		}
		r, n := utf8.DecodeRuneInString(safe)
		if r == 0 {
			r = utf8.RuneError
		}
		text.WriteRune(r)
		safe = safe[n:]
	}
	line := f.lines[stream] + text.String()
	delete(f.lines, stream)
	for len(line) > 0 {
		n := strings.IndexByte(line, '\n')
		clipped := false
		if n >= 0 {
			n++
		}
		if n < 0 && len(line) <= domain.HostingRuntimeLogMessageLimit {
			f.lines[stream] = line
			return nil
		}
		if n < 0 || n > domain.HostingRuntimeLogMessageLimit {
			n = domain.HostingRuntimeLogMessageLimit
			for n > 0 && !utf8.RuneStart(line[n]) {
				n--
			}
			clipped = true
		}
		message := line[:n]
		line = line[n:]
		event := domain.HostingRuntimeLogEvent{Component: "application", Stream: stream, Message: message, MessageTruncated: clipped}
		if stamp, remainder, ok := strings.Cut(message, " "); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				event.SourceTimestamp = &parsed
				event.Message = remainder
			}
		}
		if err := f.emit(event); err != nil {
			return err
		}
	}
	return nil
}

// readRuntimeDockerLogs never flushes partial tails at disconnect or authority loss.
func readRuntimeDockerLogs(ctx context.Context, body io.Reader, tty bool, token string, emit func(domain.HostingRuntimeLogEvent) error) error {
	framer := newRuntimeLogFramer(token, emit)
	input := bufio.NewReaderSize(body, 4096)
	buffer := make([]byte, 4096)
	defer clear(buffer)
	for ctx.Err() == nil {
		stream := "combined"
		remaining := uint32(len(buffer))
		if !tty {
			var header [8]byte
			if _, err := io.ReadFull(input, header[:]); err != nil {
				return errRuntimeLogSource
			}
			if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
				return errRuntimeLogSource
			}
			remaining = binary.BigEndian.Uint32(header[4:])
			if remaining > 64<<10 {
				return errRuntimeLogSource
			}
			stream = "stdout"
			if header[0] == 2 {
				stream = "stderr"
			}
		}
		for remaining > 0 {
			n := int(remaining)
			if n > len(buffer) {
				n = len(buffer)
			}
			var got int
			var err error
			if tty {
				got, err = input.Read(buffer[:n])
			} else {
				got, err = io.ReadFull(input, buffer[:n])
			}
			if got > 0 {
				if e := framer.chunk(stream, buffer[:got]); e != nil {
					return e
				}
			}
			if err != nil {
				return errRuntimeLogSource
			}
			remaining -= uint32(got)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if tty {
				break
			}
		}
	}
	return ctx.Err()
}
