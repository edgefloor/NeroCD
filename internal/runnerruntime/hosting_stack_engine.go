package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

const stackComponentLabel = "io.nerocd.stack.component"
const stackVolumeLabel = "io.nerocd.stack.volume"
const stackOriginLabel = "io.nerocd.stack.origin"
const stackNonceLabel = "io.nerocd.stack.nonce"

type protectedComposeCommand interface {
	RunProtectedInput(context.Context, string, []string, string, []string, []byte) ([]byte, error)
}

// prepareStack resolves every image before the caller can request mutation authority.
func (e *imageEngine) prepareStack(ctx context.Context, definition domain.HostingStackDefinition) (domain.HostingRelease, []domain.HostingStackImage, error) {
	var zero domain.HostingRelease
	if err := e.prepareCompose(ctx); err != nil {
		return zero, nil, errors.New("managed stack requires Docker Compose v2")
	}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return zero, nil, err
	}
	images := make([]domain.HostingStackImage, 0, len(definition.Services))
	for _, service := range definition.Services {
		release, err := e.Prepare(ctx, domain.HostingSnapshot{ImageReference: service.Image})
		if err != nil {
			return zero, nil, errors.New("stack image preparation failed; prior writers were not changed")
		}
		images = append(images, domain.HostingStackImage{Component: service.Name, Release: release})
	}
	if err = domain.ValidateHostingStackImages(definition, daemon, images); err != nil {
		return zero, nil, err
	}
	release := domain.HostingRelease{DaemonID: daemon, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	return release, images, nil
}

func (e *imageEngine) ensureStackImages(ctx context.Context, plan domain.HostingApplyPlan) error {
	if plan.Stack == nil || domain.ValidateHostingStackImages(plan.Stack.Definition, plan.Release.DaemonID, plan.Stack.Images) != nil {
		return errors.New("stack image set changed")
	}
	for _, image := range plan.Stack.Images {
		if e.EnsurePreparedRelease(ctx, image.Release) != nil {
			return errors.New("pinned stack image unavailable")
		}
	}
	return nil
}

// renderStack borrows protected values and returns a caller-owned stdin document.
// Every value expands once from the supplied map, then escapes dollars for Compose.
// Neither nested protected values nor the agent's environment participate in expansion.
func renderStack(operation string, plan domain.HostingApplyPlan, environment *hostingruntimeprotocol.Environment) ([]byte, error) {
	if plan.Stack == nil || environment == nil && plan.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) || !domain.HostingPlanFamilyMatches(plan.Snapshot, plan) {
		return nil, errors.New("invalid stack rendering plan")
	}
	var body []byte
	build := func(entries []hostingruntimeprotocol.Entry) error {
		values := make(map[string]string, len(entries))
		for _, entry := range entries {
			values[entry.Name] = string(entry.Value)
		}
		defer clear(values)
		expand := func(expression string) (string, error) {
			value, err := domain.ExpandHostingStackValue(expression, values, true)
			if err != nil {
				return "", err
			}
			return strings.ReplaceAll(value, "$", "$$"), nil
		}
		services := map[string]any{}
		for i, service := range plan.Stack.Definition.Services {
			item := map[string]any{"image": plan.Stack.Images[i].Release.CanonicalImage, "container_name": domain.HostingStackObjectID(plan.Snapshot.BindingID, "component", service.Name), "labels": map[string]string{imageBindingLabel: plan.Snapshot.BindingID, imageOperationLabel: operation, imageReleaseLabel: plan.Release.Hash, imagePlanLabel: plan.Hash, stackComponentLabel: service.Name}}
			if service.Command != nil {
				command := make([]string, len(service.Command))
				for n, value := range service.Command {
					var err error
					command[n], err = expand(value)
					if err != nil {
						return err
					}
				}
				item["command"] = command
			}
			if len(service.Environment) > 0 {
				env := map[string]string{}
				for name, expression := range service.Environment {
					value, err := expand(expression)
					if err != nil {
						return err
					}
					env[name] = value
				}
				item["environment"] = env
			}
			if len(service.Mounts) > 0 {
				mounts := make([]any, 0, len(service.Mounts))
				for _, mount := range service.Mounts {
					mounts = append(mounts, map[string]any{"type": "volume", "source": mount.Volume, "target": mount.Target, "read_only": mount.ReadOnly})
				}
				item["volumes"] = mounts
			}
			if len(service.DependsOn) > 0 {
				dependencies := map[string]any{}
				for name, condition := range service.DependsOn {
					dependencies[name] = map[string]string{"condition": condition}
				}
				item["depends_on"] = dependencies
			}
			if service.Healthcheck != nil {
				test := make([]string, len(service.Healthcheck.Test))
				for n, value := range service.Healthcheck.Test {
					var err error
					test[n], err = expand(value)
					if err != nil {
						return err
					}
				}
				health := map[string]any{"test": test, "retries": service.Healthcheck.Retries}
				if service.Healthcheck.Interval != "" {
					health["interval"] = service.Healthcheck.Interval
				}
				if service.Healthcheck.Timeout != "" {
					health["timeout"] = service.Healthcheck.Timeout
				}
				if service.Healthcheck.StartPeriod != "" {
					health["start_period"] = service.Healthcheck.StartPeriod
				}
				item["healthcheck"] = health
			}
			if service.Name == plan.Stack.Definition.PublicComponent {
				item["ports"] = []string{"127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + ":" + strconv.Itoa(plan.Snapshot.ContainerPort) + "/tcp"}
			}
			services[service.Name] = item
		}
		volumes := map[string]any{}
		for _, volume := range plan.Stack.Volumes {
			volumes[volume.Name] = map[string]any{"external": true, "name": volume.VolumeName}
		}
		document := map[string]any{"services": services, "volumes": volumes, "networks": map[string]any{"default": map[string]any{"external": true, "name": plan.Stack.NetworkName}}}
		var err error
		body, err = json.Marshal(document)
		return err
	}
	var err error
	if environment == nil {
		err = build(nil)
	} else {
		err = environment.WithValues(build)
	}
	if err != nil {
		clear(body)
		return nil, err
	}
	return body, nil
}

func (e *imageEngine) stackCompose(ctx context.Context, plan domain.HostingApplyPlan, body []byte, args ...string) error {
	command, ok := e.command.(protectedComposeCommand)
	if !ok {
		return errors.New("protected Compose stdin unavailable")
	}
	if e.verifyDaemon(ctx, plan.Release.DaemonID) != nil {
		return errors.New("stack daemon changed")
	}
	if !e.stackCommandReady {
		return errors.New("compose executable was not qualified before admission")
	}
	name, prefix := e.stackCommandArguments()
	all := append(prefix, "--project-directory", e.configDir, "--env-file", "/dev/null", "--project-name", plan.Snapshot.RuntimeNamespace, "--file", "-")
	all = append(all, args...)
	output, err := command.RunProtectedInput(ctx, name, all, e.configDir, []string{"DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT=", "DOCKER_CLI_HINTS=false"}, body)
	// Docker diagnostics can quote values from the rendered document. They are
	// never included in errors, events, journals or ordinary command artifacts.
	clear(output)
	if err != nil {
		return errors.New("managed Compose command failed")
	}
	return nil
}

type stackInspectedContainer struct {
	ID     string            `json:"Id"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
	State  struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
		Health   struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Mounts   []imageMount `json:"Mounts"`
	Networks map[string]struct {
		NetworkID string   `json:"NetworkID"`
		Aliases   []string `json:"Aliases"`
	} `json:"Networks"`
	Ports map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	} `json:"Ports"`
}

// inspectStackContainer deliberately excludes Config.Env and healthcheck output.
func (e *imageEngine) inspectStackContainer(ctx context.Context, id string) (stackInspectedContainer, error) {
	var result stackInspectedContainer
	if !dockerObjectID.MatchString(id) {
		return result, errors.New("invalid stack container identity")
	}
	format := `{"Id":{{json .Id}},"Image":{{json .Image}},"Labels":{{json .Config.Labels}},"State":{"Running":{{json .State.Running}},"ExitCode":{{json .State.ExitCode}},"Health":{"Status":{{with index .State "Health"}}{{json .Status}}{{else}}""{{end}}}},"Mounts":{{json .Mounts}},"Networks":{{json .NetworkSettings.Networks}},"Ports":{{json .HostConfig.PortBindings}}}`
	raw, err := e.docker(ctx, "container", "inspect", id, "--format", format)
	if err != nil || json.Unmarshal(raw, &result) != nil {
		return result, errors.New("stack component inspection failed")
	}
	return result, nil
}

func (e *imageEngine) stackContainers(ctx context.Context, binding string) ([]stackInspectedContainer, error) {
	raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+imageBindingLabel+"="+binding)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 32 {
		return nil, errors.New("stack container count exceeds bound")
	}
	result := make([]stackInspectedContainer, 0, len(ids))
	for _, id := range ids {
		container, err := e.inspectStackContainer(ctx, id)
		if err != nil {
			return nil, err
		}
		if container.ID != id || container.Labels[imageBindingLabel] != binding {
			return nil, errors.New("stack ownership changed")
		}
		result = append(result, container)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func stackComponentState(container stackInspectedContainer) domain.HostingStackComponent {
	return domain.HostingStackComponent{Component: container.Labels[stackComponentLabel], ContainerID: container.ID, ImageID: container.Image, OperationID: container.Labels[imageOperationLabel], Running: container.State.Running, ExitCode: container.State.ExitCode, Health: container.State.Health.Status}
}

func stackContainerMatches(container stackInspectedContainer, operation string, plan domain.HostingApplyPlan, networkID string) error {
	if plan.Stack == nil || container.Labels[imageBindingLabel] != plan.Snapshot.BindingID || container.Labels[imageOperationLabel] != operation || container.Labels[imagePlanLabel] != plan.Hash || container.Labels[imageReleaseLabel] != plan.Release.Hash {
		return errors.New("stack component labels changed")
	}
	name := container.Labels[stackComponentLabel]
	index := sort.Search(len(plan.Stack.Definition.Services), func(i int) bool { return plan.Stack.Definition.Services[i].Name >= name })
	if index == len(plan.Stack.Definition.Services) || plan.Stack.Definition.Services[index].Name != name || container.Image != plan.Stack.Images[index].Release.ImageID {
		return errors.New("stack component image changed")
	}
	service := plan.Stack.Definition.Services[index]
	network, ok := container.Networks[plan.Stack.NetworkName]
	if !ok || network.NetworkID != networkID || len(container.Networks) != 1 {
		return errors.New("stack component private network changed")
	}
	alias := false
	for _, value := range network.Aliases {
		alias = alias || value == name
	}
	if !alias {
		return errors.New("stack service DNS alias missing")
	}
	if len(container.Mounts) != len(service.Mounts) {
		return errors.New("stack component mounts changed")
	}
	for _, mount := range service.Mounts {
		found := false
		for _, actual := range container.Mounts {
			if actual.Type == "volume" && actual.Name == domain.HostingStackObjectID(plan.Snapshot.BindingID, "volume", mount.Volume) && actual.Destination == mount.Target && actual.RW != mount.ReadOnly {
				found = true
			}
		}
		if !found {
			return errors.New("stack component volume changed")
		}
	}
	if name == plan.Stack.Definition.PublicComponent {
		ports := container.Ports[strconv.Itoa(plan.Snapshot.ContainerPort)+"/tcp"]
		if len(container.Ports) != 1 || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(plan.Snapshot.HostPort) {
			return errors.New("stack public endpoint changed")
		}
	} else {
		for _, ports := range container.Ports {
			if len(ports) > 0 {
				return errors.New("private stack component published a host port")
			}
		}
	}
	return nil
}

func (e *imageEngine) ensureStackVolumes(ctx context.Context, operation string, plan domain.HostingApplyPlan, createPermitted bool) ([]domain.HostingStackVolume, error) {
	volumes := append([]domain.HostingStackVolume{}, plan.Stack.Volumes...)
	for i, volume := range volumes {
		present, err := e.ownedVolumePresent(ctx, volume.VolumeName)
		if err != nil {
			return nil, err
		}
		if !present {
			if !createPermitted || volume.CreatedAt != "" || volume.CreationOperationID != operation {
				return nil, errors.New("retained stack volume is missing; creation forbidden")
			}
			users, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume.VolumeName)
			if err != nil || len(strings.Fields(string(users))) != 0 {
				return nil, errors.New("stack volume has unaccounted users")
			}
			args := []string{"volume", "create", "--driver", "local", "--label", imageBindingLabel + "=" + plan.Snapshot.BindingID, "--label", stackVolumeLabel + "=" + volume.Name, "--label", stackOriginLabel + "=" + volume.CreationOperationID, "--label", stackNonceLabel + "=" + volume.CreationNonce, volume.VolumeName}
			if _, err = e.mutate(ctx, plan.Release.DaemonID, args...); err != nil {
				return nil, errors.New("stack volume creation outcome is uncertain")
			}
		}
		observed, err := e.inspectStackVolume(ctx, plan, volume)
		if err != nil {
			return nil, err
		}
		volumes[i] = observed
	}
	return volumes, nil
}

func (e *imageEngine) inspectStackVolume(ctx context.Context, plan domain.HostingApplyPlan, volume domain.HostingStackVolume) (domain.HostingStackVolume, error) {
	raw, err := e.docker(ctx, "volume", "inspect", volume.VolumeName, "--format", "{{json .}}")
	var actual hostingInspectedVolume
	if err != nil || json.Unmarshal(raw, &actual) != nil || actual.Name != volume.VolumeName || actual.Driver != "local" || actual.Scope != "local" || len(actual.Options) != 0 || actual.CreatedAt == "" || actual.Labels[imageBindingLabel] != plan.Snapshot.BindingID || actual.Labels[stackVolumeLabel] != volume.Name || actual.Labels[stackOriginLabel] != volume.CreationOperationID || actual.Labels[stackNonceLabel] != volume.CreationNonce || volume.CreatedAt != "" && volume.CreatedAt != actual.CreatedAt {
		return domain.HostingStackVolume{}, errors.New("stack volume provenance changed")
	}
	volume.CreatedAt = actual.CreatedAt
	return volume, nil
}

func (e *imageEngine) ensureStackNetwork(ctx context.Context, operation string, plan domain.HostingApplyPlan, createPermitted bool) (string, error) {
	raw, err := e.docker(ctx, "network", "ls", "--quiet", "--no-trunc", "--filter", "name=^"+plan.Stack.NetworkName+"$")
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		if !createPermitted || plan.PreviousHealthy != nil {
			return "", errors.New("retained stack network is missing")
		}
		raw, err = e.mutate(ctx, plan.Release.DaemonID, "network", "create", "--driver", "bridge", "--label", imageBindingLabel+"="+plan.Snapshot.BindingID, "--label", stackOriginLabel+"="+operation, plan.Stack.NetworkName)
		if err != nil {
			return "", errors.New("stack network creation outcome is uncertain")
		}
		ids = []string{strings.TrimSpace(string(raw))}
	}
	if len(ids) != 1 || !dockerObjectID.MatchString(ids[0]) {
		return "", errors.New("stack network identity is ambiguous")
	}
	var network struct {
		ID         string                     `json:"Id"`
		Name       string                     `json:"Name"`
		Driver     string                     `json:"Driver"`
		Scope      string                     `json:"Scope"`
		Labels     map[string]string          `json:"Labels"`
		Options    map[string]string          `json:"Options"`
		Containers map[string]json.RawMessage `json:"Containers"`
	}
	raw, err = e.docker(ctx, "network", "inspect", ids[0], "--format", "{{json .}}")
	if err != nil || json.Unmarshal(raw, &network) != nil || network.ID != ids[0] || network.Name != plan.Stack.NetworkName || network.Driver != "bridge" || network.Scope != "local" || len(network.Options) != 0 || network.Labels[imageBindingLabel] != plan.Snapshot.BindingID {
		return "", errors.New("stack private network changed")
	}
	known := map[string]bool{}
	if plan.PreviousHealthy != nil {
		if plan.PreviousHealthy.Stack == nil || plan.PreviousHealthy.Stack.NetworkID != network.ID {
			return "", errors.New("retained stack network identity changed")
		}
		for _, component := range plan.PreviousHealthy.Stack.Components {
			known[component.ContainerID] = true
		}
	} else if network.Labels[stackOriginLabel] != operation {
		return "", errors.New("stack network origin changed")
	}
	for id := range network.Containers {
		if !known[id] {
			return "", errors.New("private stack network has foreign users")
		}
	}
	return network.ID, nil
}

// stopStackPredecessor is a whole-generation barrier, including removed services.
// No new component is started until every verified predecessor has stopped.
func (e *imageEngine) stopStackPredecessor(ctx context.Context, plan domain.HostingApplyPlan) error {
	actual, err := e.stackContainers(ctx, plan.Snapshot.BindingID)
	if err != nil {
		return err
	}
	known := map[string]domain.HostingStackComponent{}
	if plan.PreviousStopped != nil {
		if plan.PreviousStopped.Validate() != nil || plan.PreviousHealthy == nil || domain.HostingDocumentHash(plan.PreviousStopped.Target) != domain.HostingDocumentHash(plan.PreviousHealthy) || plan.Snapshot.StackStoppedOperationID != plan.PreviousStopped.StopOperationID {
			return errors.New("confirmed stopped predecessor changed")
		}
	} else if previous := plan.PreviousHealthy; previous != nil {
		for _, component := range previous.Stack.Components {
			known[component.ContainerID] = component
		}
	}
	projectRaw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project="+plan.Snapshot.RuntimeNamespace)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(projectRaw)) {
		if _, ok := known[id]; !ok {
			return errors.New("compose project has a foreign container")
		}
	}
	if len(actual) != len(known) {
		return errors.New("stack predecessor inventory changed")
	}
	ids := make([]string, 0, len(actual))
	for _, container := range actual {
		expected, ok := known[container.ID]
		if !ok || container.Image != expected.ImageID || container.Labels[imageOperationLabel] != expected.OperationID || container.Labels[stackComponentLabel] != expected.Component || container.Labels[imagePlanLabel] != plan.PreviousHealthy.PlanHash || container.Labels[imageReleaseLabel] != plan.PreviousHealthy.Release.Hash {
			return errors.New("stack predecessor ownership changed")
		}
		ids = append(ids, container.ID)
	}
	// Reject foreign users of retained mounts before stopping the old application.
	for _, volume := range plan.Stack.Volumes {
		raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume.VolumeName)
		if err != nil {
			return err
		}
		for _, id := range strings.Fields(string(raw)) {
			if _, ok := known[id]; !ok {
				return errors.New("stack volume has a foreign container user")
			}
		}
	}
	if len(ids) > 0 {
		args := append([]string{"container", "stop", "--time", "5"}, ids...)
		if _, err = e.mutate(ctx, plan.Release.DaemonID, args...); err != nil {
			return errors.New("stack stop barrier outcome is uncertain")
		}
	}
	for _, id := range ids {
		container, err := e.inspectStackContainer(ctx, id)
		if err != nil || container.State.Running {
			return errors.New("stack predecessor writer remains active")
		}
	}
	if len(ids) > 0 {
		args := append([]string{"container", "rm"}, ids...)
		if _, err = e.mutate(ctx, plan.Release.DaemonID, args...); err != nil {
			return errors.New("stack predecessor removal outcome is uncertain")
		}
	}
	return nil
}

func (e *imageEngine) observeStackState(ctx context.Context, operation string, plan domain.HostingApplyPlan, networkID string, volumes []domain.HostingStackVolume, verify bool) (domain.HostingStackState, error) {
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: plan.Stack.Definition.PublicComponent, NetworkID: networkID, Volumes: append([]domain.HostingStackVolume{}, volumes...), Components: []domain.HostingStackComponent{}}
	containers, err := e.stackContainers(ctx, plan.Snapshot.BindingID)
	if err != nil {
		return state, err
	}
	for _, container := range containers {
		state.Components = append(state.Components, stackComponentState(container))
		if verify && stackContainerMatches(container, operation, plan, networkID) != nil {
			return state, errors.New("stack component does not match committed plan")
		}
	}
	sort.Slice(state.Components, func(i, j int) bool { return state.Components[i].Component < state.Components[j].Component })
	if verify {
		for _, volume := range volumes {
			if _, err = e.inspectStackVolume(ctx, plan, volume); err != nil {
				return state, err
			}
		}
		if err = domain.ValidateHostingStackState(plan, operation, state, true); err != nil {
			return state, err
		}
	}
	return state, nil
}

// applyStack is entered only after a durable intent and controller mutation hold.
// Failure retains uncertainty and records observed components instead of claiming rollback.
func (e *imageEngine) applyStack(ctx context.Context, operation string, plan domain.HostingApplyPlan, body []byte, useUntil time.Time, createPermitted bool, seal func([]domain.HostingStackVolume) error, verifying func() error) (result domain.HostingResult) {
	result = domain.HostingResult{Outcome: "failed", DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, Uncertain: true}
	if plan.Stack == nil || verifyImagePlan(plan) != nil {
		return result
	}
	networkID := ""
	volumes := append([]domain.HostingStackVolume{}, plan.Stack.Volumes...)
	defer func() {
		clear(body)
		if result.Outcome != "succeeded" {
			state, err := e.observeStackState(ctx, operation, plan, networkID, volumes, false)
			state.ObservationComplete = err == nil
			result.Stack = &state
		}
	}()
	if ctx.Err() != nil || e.prepareCompose(ctx) != nil || e.ensureStackImages(ctx, plan) != nil || !time.Now().Before(useUntil) {
		return result
	}
	var err error
	volumes, err = e.ensureStackVolumes(ctx, operation, plan, createPermitted)
	if err != nil {
		volumes = append([]domain.HostingStackVolume{}, plan.Stack.Volumes...)
		return result
	}
	networkID, err = e.ensureStackNetwork(ctx, operation, plan, createPermitted)
	if err != nil {
		return result
	}
	if seal == nil || seal(volumes) != nil {
		return result
	}
	if !time.Now().Before(useUntil) || e.stopStackPredecessor(ctx, plan) != nil {
		return result
	}
	// The complete stopped-writer barrier precedes Compose's own dependency scheduler.
	// External retained volumes can never be implicitly created by Compose.
	start, stop := context.WithDeadline(ctx, useUntil)
	err = e.stackCompose(start, plan, body, "up", "--detach", "--no-build", "--pull", "never", "--force-recreate")
	stop()
	clear(body)
	if err != nil || verifying == nil || verifying() != nil {
		return result
	}
	deadline, cancel := context.WithTimeout(ctx, time.Duration(plan.Snapshot.Readiness.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := "http://127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + plan.Snapshot.Readiness.Path
	for {
		state, observeErr := e.observeStackState(deadline, operation, plan, networkID, volumes, true)
		if observeErr == nil {
			request, requestErr := http.NewRequestWithContext(deadline, http.MethodGet, probe, nil)
			if requestErr != nil {
				return result
			}
			response, requestErr := client.Do(request)
			ready := false
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
				ready = response.StatusCode >= 200 && response.StatusCode <= 399
			}
			if ready {
				state, observeErr = e.observeStackState(deadline, operation, plan, networkID, volumes, true)
				if observeErr == nil {
					state.ObservationComplete = true
					result.Stack = &state
					result.Outcome = "succeeded"
					result.Ready = true
					result.Uncertain = false
					return result
				}
			}
		}
		if !waitWorkerInterval(deadline, 200*time.Millisecond) {
			return result
		}
	}
}

// observeStack never repairs or releases an uncertain replacement. A durable
// successful receipt must match every current component and retained volume.
func (e *imageEngine) observeStack(ctx context.Context, op domain.HostingOperation) (domain.HostingObservation, error) {
	out := domain.HostingObservation{BindingID: op.BindingID}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return out, err
	}
	out.DaemonID = daemon
	if op.MutationHold && (op.Result == nil || op.Result.Uncertain) {
		return out, errors.New("stack replacement remains held for recovery")
	}
	expected := op.ReconciliationHealthy
	if op.Result != nil && op.Result.Outcome == "succeeded" && op.Plan != nil && op.Release != nil {
		expected = &domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, PlanHash: op.Plan.Hash, Release: *op.Release, Stack: op.Result.Stack}
	}
	if expected == nil {
		containers, inspectErr := e.stackContainers(ctx, op.BindingID)
		if inspectErr != nil {
			return out, inspectErr
		}
		if len(containers) != 0 {
			return out, errors.New("unaccounted stack containers")
		}
		out.NoOwnedContainers = true
		return out, nil
	}
	if domain.ValidateHostingStackHealthy(*expected) != nil || expected.Release.DaemonID != daemon || op.Plan == nil || op.Plan.Stack == nil || op.Plan.Hash != expected.PlanHash {
		return out, errors.New("exact stack observation plan unavailable")
	}
	state, err := e.observeStackState(ctx, expected.OperationID, *op.Plan, expected.Stack.NetworkID, expected.Stack.Volumes, true)
	if err != nil || domain.HostingDocumentHash(state) != domain.HostingDocumentHash(expected.Stack) {
		return out, errors.New("stack observation differs from durable healthy receipt")
	}
	out.Stack = &state
	out.PlanHash = expected.PlanHash
	out.ReleaseHash = expected.Release.Hash
	out.Ready = true
	return out, nil
}

// Docker's isolated config can hide a user-installed CLI plugin. Prefer the
// normal plugin, then qualify its standalone v2+ executable with the same empty
// registry config and explicit daemon environment before any mutation.
func (e *imageEngine) prepareCompose(ctx context.Context) error {
	if e.stackCommandReady {
		return nil
	}
	version, err := e.docker(ctx, "compose", "version", "--short")
	if err != nil {
		version, err = e.command.RunEnvironment(ctx, "docker-compose", []string{"version", "--short"}, e.configDir, []string{"DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT="})
		if err != nil {
			return errors.New("docker Compose v2 or later is required")
		}
		e.stackStandalone = true
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(strings.TrimPrefix(string(version), "v")), ".", 2)[0])
	if err != nil || major < 2 {
		return errors.New("docker Compose v2 or later is required")
	}
	e.stackCommandReady = true
	return nil
}
func (e *imageEngine) stackCommandArguments() (string, []string) {
	if e.stackStandalone {
		return "docker-compose", nil
	}
	return "docker", []string{"--host", e.endpoint, "--config", e.configDir, "compose"}
}
