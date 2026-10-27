package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const observationOutputLimit = 8 << 10

// This projection intentionally excludes Config.Env and unselected labels.
const observationProjection = `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},"status":{{json .State.Status}},"running":{{json .State.Running}},"paused":{{json .State.Paused}},"restarting":{{json .State.Restarting}},"dead":{{json .State.Dead}},"exit_code":{{json .State.ExitCode}},"binding":{{json (index .Config.Labels "io.nerocd.binding")}},"operation":{{json (index .Config.Labels "io.nerocd.operation")}},"release":{{json (index .Config.Labels "io.nerocd.release")}},"plan":{{json (index .Config.Labels "io.nerocd.plan")}},"ports":{{json .HostConfig.PortBindings}},"host_policy":{"Privileged":{{json .HostConfig.Privileged}},"PublishAllPorts":{{json .HostConfig.PublishAllPorts}},"NetworkMode":{{json .HostConfig.NetworkMode}},"PidMode":{{json .HostConfig.PidMode}},"IpcMode":{{json .HostConfig.IpcMode}},"UsernsMode":{{json .HostConfig.UsernsMode}},"Binds":{{json .HostConfig.Binds}},"CapAdd":{{json .HostConfig.CapAdd}},"VolumesFrom":{{json .HostConfig.VolumesFrom}},"SecurityOpt":{{json .HostConfig.SecurityOpt}},"Devices":{{json .HostConfig.Devices}},"DeviceRequests":{{json .HostConfig.DeviceRequests}}}}`

type imageObservation struct{ engine *imageEngine }

func newImageObservation(endpoint string) (*imageObservation, error) {
	e, err := newImageEngine(endpoint)
	if err != nil {
		return nil, err
	}
	e.command = observationCommand{}
	return &imageObservation{e}, nil
}
func (s *imageObservation) Close() error { return s.engine.Close() }

type observationContainer struct {
	HostPolicy imageHostConfig `json:"host_policy"`
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Image      string          `json:"image"`
	Status     string          `json:"status"`
	Running    bool            `json:"running"`
	Paused     bool            `json:"paused"`
	Restarting bool            `json:"restarting"`
	Dead       bool            `json:"dead"`
	ExitCode   int             `json:"exit_code"`
	Binding    string          `json:"binding"`
	Operation  string          `json:"operation"`
	Release    string          `json:"release"`
	Plan       string          `json:"plan"`
	Ports      map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	} `json:"ports"`
}

func (s *imageObservation) inspect(ctx context.Context, t domain.HostingObservationTarget) (observationContainer, error) {
	var got observationContainer
	raw, err := s.engine.docker(ctx, "container", "inspect", "--format", observationProjection, t.ContainerID)
	if err != nil || len(raw) > observationOutputLimit {
		return got, errors.New("observation inspect unavailable")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 15 {
		return got, errors.New("invalid observation projection")
	}
	for _, key := range []string{"id", "name", "image", "status", "running", "paused", "restarting", "dead", "exit_code", "binding", "operation", "release", "plan", "ports", "host_policy"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return got, errors.New("incomplete observation projection")
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&got) != nil {
		return observationContainer{}, errors.New("invalid observation projection")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return observationContainer{}, errors.New("invalid observation projection")
	}
	return got, nil
}
func observationIdentityMatches(c observationContainer, t domain.HostingObservationTarget) bool {
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(t.OperationID), "sha256:")[:16]
	ports := c.Ports[strconv.Itoa(t.ContainerPort)+"/tcp"]
	return imageHostPolicyMatches(c.HostPolicy) && c.ID == t.ContainerID && c.Name == "/"+t.RuntimeNamespace+"_"+suffix && c.Image == t.ImageID && c.Binding == t.BindingID && c.Operation == t.OperationID && c.Release == t.ReleaseHash && c.Plan == t.PlanHash && len(c.Ports) == 1 && len(ports) == 1 && ports[0].HostIP == "127.0.0.1" && ports[0].HostPort == strconv.Itoa(t.HostPort)
}
func observationRunning(c observationContainer) bool {
	return c.Status == "running" && c.Running && !c.Paused && !c.Restarting && !c.Dead
}
func unknownObservation(t domain.HostingObservationTarget, reason string) domain.HostingRuntimeObservation {
	return domain.HostingRuntimeObservation{BindingID: t.BindingID, SampleID: t.SampleID, ContainerState: "unknown", Readiness: "unknown", CollectionError: reason}
}
func (s *imageObservation) Sample(parent context.Context, t domain.HostingObservationTarget) domain.HostingRuntimeObservation {
	result := unknownObservation(t, "inspect_failed")
	if validateObservationTarget(t, t.RunnerID, time.Now()) != nil {
		return unknownObservation(t, "target_mismatch")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	// Leave a small cleanup reserve inside the declared five-second sample.
	deadline, _ := ctx.Deadline()
	work, stop := context.WithDeadline(ctx, deadline.Add(-150*time.Millisecond))
	defer stop()
	daemon, err := s.engine.daemonID(work)
	if err != nil {
		return unknownObservation(t, "daemon_unreachable")
	}
	if daemon != t.DaemonID {
		return unknownObservation(t, "daemon_mismatch")
	}
	container, err := s.inspect(work, t)
	if err != nil {
		// Only a successful exact-ID query proves absence. Diagnostics never do.
		ids, listErr := s.engine.docker(work, "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", "id="+t.ContainerID)
		if listErr == nil && len(ids) <= observationOutputLimit && len(strings.Fields(string(ids))) == 0 && s.engine.verifyDaemon(work, t.DaemonID) == nil && work.Err() == nil {
			result.ContainerState, result.Readiness, result.CollectionError = "missing", "not_checked", ""
		}
		return result
	}
	if !observationIdentityMatches(container, t) {
		return unknownObservation(t, "target_mismatch")
	}
	if container.Status == "exited" && !container.Running && !container.Paused && !container.Restarting && !container.Dead && container.ExitCode >= 0 && container.ExitCode <= 255 {
		afterDaemon, daemonErr := s.engine.daemonID(work)
		if daemonErr != nil {
			return unknownObservation(t, "daemon_unreachable")
		}
		if afterDaemon != t.DaemonID {
			return unknownObservation(t, "daemon_mismatch")
		}
		result.ContainerState, result.Readiness, result.CollectionError = "exited", "not_checked", ""
		exit := container.ExitCode
		result.ExitCode = &exit
		return result
	}
	if !observationRunning(container) {
		return unknownObservation(t, "")
	}
	probeDeadline := deadline.Add(-400 * time.Millisecond)
	probe, finish := context.WithDeadline(work, probeDeadline)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{}).DialContext}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(probe, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(t.HostPort)+t.ReadinessPath, nil)
	ready, probeFailed := false, true
	if err == nil {
		response, probeErr := client.Do(request)
		if probeErr == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
			_ = response.Body.Close()
			probeFailed = readErr != nil || len(body) > 4096
			clear(body)
			ready = !probeFailed && response.StatusCode >= 200 && response.StatusCode <= 399
		}
	}
	finish()
	// A probe is evidence about this exact running container only after recheck.
	afterDaemon, daemonErr := s.engine.daemonID(work)
	if daemonErr != nil {
		return unknownObservation(t, "daemon_unreachable")
	}
	if afterDaemon != t.DaemonID {
		return unknownObservation(t, "daemon_mismatch")
	}
	after, err := s.inspect(work, t)
	if err != nil {
		return unknownObservation(t, "inspect_failed")
	}
	if !observationIdentityMatches(after, t) {
		return unknownObservation(t, "target_mismatch")
	}
	if !observationRunning(after) {
		return unknownObservation(t, "")
	}
	result.ContainerState, result.CollectionError = "running", ""
	if probeFailed {
		result.Readiness, result.CollectionError = "unknown", "readiness_timeout"
	} else if ready {
		result.Readiness = "ready"
	} else {
		result.Readiness = "not_ready"
	}
	return result
}

type observationCommand struct{}
type observationOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (b *observationOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > observationOutputLimit {
		b.cancel()
		return 0, errors.New("observation output exceeds limit")
	}
	return b.buffer.Write(p)
}
func observationCommandAllowed(args []string) bool {
	if len(args) == 3 && args[0] == "info" && args[1] == "--format" && args[2] == "{{json .ID}}" {
		return true
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" && args[3] == observationProjection && dockerObjectID.MatchString(args[4]) {
		return true
	}
	return len(args) == 7 && args[0] == "container" && args[1] == "ls" && args[2] == "--all" && args[3] == "--no-trunc" && args[4] == "--quiet" && args[5] == "--filter" && strings.HasPrefix(args[6], "id=") && dockerObjectID.MatchString(strings.TrimPrefix(args[6], "id="))
}
func (observationCommand) RunEnvironment(parent context.Context, name string, args []string, dir string, _ []string) ([]byte, error) {
	failed := errors.New("hosting observation command unavailable")
	if name != "docker" || len(args) < 5 || args[0] != "--host" || args[2] != "--config" || args[3] != dir || !observationCommandAllowed(args[4:]) {
		return nil, failed
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	command.Env = []string{"PATH=" + path, "HOME=" + dir, "LANG=C", "LC_ALL=C", "DOCKER_HOST=" + args[1], "DOCKER_CONFIG=" + dir, "DOCKER_CONTEXT=", "DOCKER_CLI_HINTS=false"}
	configureProvenanceProcess(command)
	command.Cancel = func() error { killProvenanceProcess(command); return nil }
	command.WaitDelay = 50 * time.Millisecond
	output := &observationOutput{cancel: cancel}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil || ctx.Err() != nil {
		killProvenanceProcess(command)
		return nil, failed
	}
	return append([]byte(nil), output.buffer.Bytes()...), nil
}
