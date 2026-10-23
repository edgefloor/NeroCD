package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const readinessDockerBodyLimit = 64 << 10

type hostReadinessSampler struct {
	http                  *http.Client
	transport             *http.Transport
	workRoot, journalRoot string
}

func newReadinessSampler(endpoint, workRoot, journalRoot string) (*hostReadinessSampler, error) {
	if endpoint == "" {
		endpoint = defaultImageDockerEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.User != nil || !filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(parsed.Path, "\x00\r\n") {
		return nil, errors.New("readiness requires an explicit local Docker Unix socket")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8 << 10, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", parsed.Path)
	}}
	return &hostReadinessSampler{http: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport: transport, workRoot: workRoot, journalRoot: journalRoot}, nil
}
func (s *hostReadinessSampler) Close() { s.transport.CloseIdleConnections() }

func readinessOS(value string) string {
	if value == "linux" {
		return "linux"
	}
	return "unsupported"
}
func readinessArchitecture(value string) string {
	switch value {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return "unsupported"
	}
}
func (s *hostReadinessSampler) get(ctx context.Context, path string, out any) string {
	if ctx.Err() != nil {
		return "unreachable"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return "invalid_response"
	}
	response, err := s.http.Do(request)
	if err != nil {
		return "unreachable"
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "invalid_response"
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, readinessDockerBodyLimit+1))
	defer clear(raw)
	if err != nil {
		return "unreachable"
	}
	if len(raw) > readinessDockerBodyLimit || json.Unmarshal(raw, out) != nil {
		return "invalid_response"
	}
	return "available"
}

type readinessDockerVersion struct {
	API     string `json:"ApiVersion"`
	Minimum string `json:"MinAPIVersion"`
}
type readinessDockerInfo struct {
	ID           string `json:"ID"`
	OS           string `json:"OSType"`
	Architecture string `json:"Architecture"`
}

var readinessNumericVersion = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,3})$`)
var readinessDaemonID = regexp.MustCompile(`^[ -~]{1,128}$`)

func readinessAPIVersion(value string) (int, bool) {
	parts := readinessNumericVersion.FindStringSubmatch(value)
	if parts == nil {
		return 0, false
	}
	major, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	minor, err := strconv.Atoi(parts[2])
	return major*1000 + minor, err == nil
}
func validReadinessDockerInfo(info readinessDockerInfo) bool {
	return readinessDaemonID.MatchString(info.ID) && strings.TrimSpace(info.ID) != "" && len(info.OS) > 0 && len(info.OS) <= 32 && len(info.Architecture) > 0 && len(info.Architecture) <= 32
}

func (s *hostReadinessSampler) Sample(ctx context.Context) domain.ServerReadinessReport {
	out := domain.ServerReadinessReport{Profile: domain.ServerReadinessProfile, OS: readinessOS(runtime.GOOS), Architecture: readinessArchitecture(runtime.GOARCH), Execution: "enabled", Configuration: "matched", Docker: "unreachable", DaemonOS: "unknown", DaemonArchitecture: "unknown", CLI: "unknown", WorkRoot: "unknown", JournalRoot: "unknown"}
	if ctx.Err() != nil {
		out.Error = "timeout"
		return out
	}
	out.CLI = readinessCLI()
	if ctx.Err() != nil {
		out.Error = "timeout"
		return out
	}
	out.WorkRoot, out.WorkFreeBytes = readinessRoot(s.workRoot, false)
	if ctx.Err() != nil {
		out.Error = "timeout"
		return out
	}
	out.JournalRoot, out.JournalFreeBytes = readinessRoot(s.journalRoot, true)
	if ctx.Err() != nil {
		out.Error = "timeout"
		return out
	}
	var version readinessDockerVersion
	out.Docker = s.get(ctx, "/version", &version)
	if out.Docker != "available" {
		return out
	}
	maximum, valid := readinessAPIVersion(version.API)
	minimum, minValid := readinessAPIVersion(version.Minimum)
	if !valid || !minValid {
		out.Docker = "invalid_response"
		return out
	}
	if maximum < 1054 || minimum > 1054 {
		out.Docker = "unsupported_api"
		out.DockerAPIVersion = version.API
		return out
	}
	out.DockerAPIVersion = "1.54"
	var before readinessDockerInfo
	out.Docker = s.get(ctx, "/v1.54/info", &before)
	if out.Docker != "available" {
		return out
	}
	if !validReadinessDockerInfo(before) {
		out.Docker = "invalid_response"
		return out
	}
	var after readinessDockerInfo
	out.Docker = s.get(ctx, "/v1.54/info", &after)
	if out.Docker != "available" {
		return out
	}
	if !validReadinessDockerInfo(after) {
		out.Docker = "invalid_response"
		return out
	}
	if before != after {
		out.Error = "daemon_changed"
		out.Docker = "invalid_response"
		return out
	}
	out.DaemonID = before.ID
	out.DaemonOS = readinessOS(before.OS)
	out.DaemonArchitecture = readinessArchitecture(before.Architecture)
	if ctx.Err() != nil {
		out.Error = "timeout"
	}
	return out
}
