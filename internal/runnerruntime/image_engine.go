package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

const defaultImageDockerEndpoint = "unix:///var/run/docker.sock"
const imageBindingLabel = "io.nerocd.binding"
const imageOperationLabel = "io.nerocd.operation"
const imageReleaseLabel = "io.nerocd.release"
const imagePlanLabel = "io.nerocd.plan"

var dockerObjectID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var imageNamespace = regexp.MustCompile(`^ncd_[a-f0-9]{24,64}$`)
var pulledDigestLine = regexp.MustCompile(`(?m)^Digest: (sha256:[a-f0-9]{64})\s*$`)

type imageCommand interface {
	RunEnvironment(context.Context, string, []string, string, []string) ([]byte, error)
}
type imageEngine struct {
	endpoint, configDir                string
	stackCommandReady, stackStandalone bool
	command                            imageCommand
}

// newImageEngine pins every invocation to one explicitly configured local Unix
// daemon. Neither Docker context nor ambient registry credentials are inherited.
func newImageEngine(endpoint string) (*imageEngine, error) {
	if endpoint == "" {
		endpoint = defaultImageDockerEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || !filepath.IsAbs(parsed.Path) || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.ContainsAny(parsed.Path, "\r\n\x00") {
		return nil, errors.New("image execution requires an explicit local Docker Unix socket")
	}
	dir, err := os.MkdirTemp("", "nerocd-image-docker-")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &imageEngine{endpoint: endpoint, configDir: dir, command: osProvenanceCommand{}}, nil
}

// Close removes the isolated empty registry configuration.
func (e *imageEngine) Close() error { return os.RemoveAll(e.configDir) }
func (e *imageEngine) docker(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	all := append([]string{"--host", e.endpoint, "--config", e.configDir}, args...)
	contents, err := e.command.RunEnvironment(ctx, "docker", all, e.configDir, []string{"DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT=", "DOCKER_CLI_HINTS=false"})
	if err != nil {
		return nil, errors.New("image Docker command failed")
	}
	return contents, nil
}
func (e *imageEngine) daemonID(ctx context.Context) (string, error) {
	body, err := e.docker(ctx, "info", "--format", "{{json .ID}}")
	if err != nil {
		return "", err
	}
	var id string
	if err = json.Unmarshal(body, &id); err != nil || id == "" || len(id) > 128 {
		return "", errors.New("docker daemon identity missing")
	}
	return id, nil
}
func (e *imageEngine) verifyDaemon(ctx context.Context, expected string) error {
	got, err := e.daemonID(ctx)
	if err != nil {
		return err
	}
	if got != expected {
		return errors.New("docker daemon identity changed")
	}
	return nil
}

type inspectedImage struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	Variant      string   `json:"Variant"`
}

func (e *imageEngine) inspectImage(ctx context.Context, identity string) (inspectedImage, error) {
	var result inspectedImage
	body, err := e.docker(ctx, "image", "inspect", identity, "--format", "{{json .}}")
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return result, errors.New("invalid Docker image evidence")
	}
	return result, nil
}

// Prepare only pulls and inspects images. The digest comes from this pull's
// immutable result, never from a later inspection of a moving local tag.
func (e *imageEngine) Prepare(ctx context.Context, snapshot domain.HostingSnapshot) (domain.HostingRelease, error) {
	if snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		return e.prepareLocalBuild(ctx, snapshot)
	}
	normalized, err := domain.NormalizeHostingImage(snapshot.ImageReference)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	output, err := e.docker(ctx, "image", "pull", normalized)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	matches := pulledDigestLine.FindAllStringSubmatch(string(output), -1)
	if len(matches) != 1 {
		return domain.HostingRelease{}, errors.New("pull did not produce one unambiguous repository digest")
	}
	named, err := reference.ParseNormalizedNamed(normalized)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	digest := matches[0][1]
	if supplied, ok := named.(reference.Digested); ok && supplied.Digest().String() != digest {
		return domain.HostingRelease{}, errors.New("pulled digest differs from supplied digest")
	}
	canonical := reference.TrimNamed(named).Name() + "@" + digest
	image, err := e.inspectImage(ctx, canonical)
	if err != nil {
		return domain.HostingRelease{}, err
	}
	found := false
	for _, candidate := range image.RepoDigests {
		parsed, err := reference.ParseNormalizedNamed(candidate)
		if err == nil && parsed.String() == canonical {
			found = true
		}
	}
	if !found {
		return domain.HostingRelease{}, errors.New("docker image lacks the pulled repository digest")
	}
	if err = e.verifyDaemon(ctx, daemon); err != nil {
		return domain.HostingRelease{}, err
	}
	release := domain.HostingRelease{CanonicalImage: canonical, ImageID: image.ID, OS: image.OS, Architecture: image.Architecture, Variant: image.Variant, DaemonID: daemon}
	release.Hash = domain.HostingDocumentHash(release)
	if err = domain.ValidateHostingRelease(release); err != nil {
		return domain.HostingRelease{}, err
	}
	return release, nil
}

func (e *imageEngine) verifyLocalRelease(ctx context.Context, release domain.HostingRelease) error {
	if err := domain.ValidateHostingRelease(release); err != nil {
		return err
	}
	if err := e.verifyDaemon(ctx, release.DaemonID); err != nil {
		return err
	}
	image, err := e.inspectImage(ctx, hostingReleaseImage(release))
	if err != nil {
		return err
	}
	if image.ID != release.ImageID || image.OS != release.OS || image.Architecture != release.Architecture || image.Variant != release.Variant {
		return errors.New("prepared local image identity changed")
	}
	return nil
}

type imageContainer struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Mounts     []imageMount `json:"Mounts"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
}

func (e *imageEngine) inspectContainer(ctx context.Context, id string) (imageContainer, error) {
	var container imageContainer
	if !dockerObjectID.MatchString(id) {
		return container, errors.New("invalid owned container ID")
	}
	raw, err := e.docker(ctx, "container", "inspect", id, "--format", "{{json .}}")
	if err != nil {
		return container, err
	}
	if err = json.Unmarshal(raw, &container); err != nil {
		return container, errors.New("invalid container evidence")
	}
	return container, nil
}
func ownedImageContainer(container imageContainer, binding, operation, release, plan string) bool {
	labels := container.Config.Labels
	return dockerObjectID.MatchString(container.ID) && labels[imageBindingLabel] == binding && labels[imageOperationLabel] == operation && labels[imageReleaseLabel] == release && labels[imagePlanLabel] == plan
}
func (e *imageEngine) mutate(ctx context.Context, daemon string, args ...string) ([]byte, error) {
	if err := e.verifyDaemon(ctx, daemon); err != nil {
		return nil, err
	}
	return e.docker(ctx, args...)
}
func verifyImagePlan(plan domain.HostingApplyPlan) error {
	if !domain.HostingPlanFamilyMatches(plan.Snapshot, plan) {
		return errors.New("image plan policy family mismatch")
	}
	hash := plan.Hash
	plan.Hash = ""
	if hash == "" || hash != domain.HostingDocumentHash(plan) || !imageNamespace.MatchString(plan.Snapshot.RuntimeNamespace) {
		return errors.New("invalid immutable image apply plan")
	}
	switch plan.CompilerVersion {
	case domain.HostingStackCompilerVersion:
		if plan.Stack == nil {
			return errors.New("stack plan is missing")
		}
		expected, err := domain.CompileHostingStackPlan(plan.Snapshot, plan.Release, plan.PreviousHealthy, *plan.Stack)
		if err != nil || expected.Hash != hash {
			return errors.New("invalid immutable stack plan")
		}
	case domain.HostingOwnedStorageRuntimeCompilerVersion:
		expected, err := domain.CompileHostingOwnedStorageRuntimePlan(plan.Snapshot, plan.Release, plan.PreviousHealthy, plan.RetainedReleases, *plan.Storage)
		if err != nil || expected.Hash != hash {
			return errors.New("invalid owned storage runtime image apply plan")
		}
	case domain.HostingOwnedStorageCompilerVersion:
		expected, err := domain.CompileHostingOwnedStoragePlan(plan.Snapshot, plan.Release, plan.PreviousHealthy, plan.RetainedReleases, *plan.Storage)
		if err != nil || expected.Hash != hash {
			return errors.New("invalid owned storage image apply plan")
		}
	case domain.HostingCompilerVersion:
		if plan.Snapshot.PolicyVersion != domain.HostingPolicyVersion || plan.RetainPrevious || len(plan.RetainedReleases) != 0 {
			return errors.New("retained release fields require versioned image protocol")
		}
	case domain.HostingReleaseCompilerVersion, domain.HostingRuntimeCompilerVersion:
		expected, err := domain.CompileRetainedHostingPlan(plan.Snapshot, plan.Release, plan.PreviousHealthy, plan.RetainedReleases)
		if err != nil || expected.Hash != hash {
			return errors.New("invalid retained image apply plan")
		}
	case domain.HostingLocalBuildCompilerVersion, domain.HostingLocalBuildRuntimeCompilerVersion:
		expected, err := domain.CompileLocalBuildHostingPlan(plan.Snapshot, plan.Release, plan.PreviousHealthy, plan.RetainedReleases)
		if err != nil || expected.Hash != hash {
			return errors.New("invalid local build apply plan")
		}
	default:
		return errors.New("unsupported image compiler version")
	}
	if plan.Snapshot.HostPort < 1024 {
		return errors.New("image apply requires a resolved host port")
	}
	in := domain.HostingDeploymentInput{ExpectedGeneration: plan.Snapshot.ResourceGeneration, IdempotencyKey: "internal-plan", ContainerPort: plan.Snapshot.ContainerPort, HostPort: plan.Snapshot.HostPort, Readiness: plan.Snapshot.Readiness}
	if err := domain.ValidateHostingDeployment(in); err != nil {
		return err
	}
	return domain.ValidateHostingSnapshotRelease(plan.Snapshot, plan.Release)
}

// Apply permits fixed-port interruption. A failed pull never reaches this
// method. Unknown command outcomes are retained for reconciliation; only a
// known readiness failure enters bounded restoration of the prior workload.
func (e *imageEngine) Apply(ctx context.Context, operationID string, plan domain.HostingApplyPlan, verifying ...func() error) domain.HostingResult {
	return e.apply(ctx, operationID, plan, nil, time.Time{}, nil, verifying...)
}

// ApplyWithRuntimeEnvironment borrows protected bytes only during container creation.
func (e *imageEngine) ApplyWithRuntimeEnvironment(ctx context.Context, operationID string, plan domain.HostingApplyPlan, environment *hostingruntimeprotocol.Environment, localDeadline time.Time, verifying ...func() error) domain.HostingResult {
	return e.apply(ctx, operationID, plan, environment, localDeadline, nil, verifying...)
}
func (e *imageEngine) apply(ctx context.Context, operationID string, plan domain.HostingApplyPlan, environment *hostingruntimeprotocol.Environment, localDeadline time.Time, storage *ownedStorageExecution, verifying ...func() error) domain.HostingResult {
	result := domain.HostingResult{Outcome: "failed", DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, ImageID: plan.Release.ImageID}
	if storage == nil && (plan.Storage != nil || plan.Snapshot.Storage != (domain.HostingStorageRef{})) {
		return result
	}
	currentEnvironment := plan.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{})
	var metadata hostingruntimeprotocol.Metadata
	if currentEnvironment {
		var err error
		metadata, err = environment.Metadata()
		if err != nil || metadata.Authority.OperationID != operationID || metadata.PlanHash != plan.Hash || metadata.RuntimeConfiguration != plan.Snapshot.RuntimeConfiguration || environment.WithValues(hostingruntimeprotocol.ValidateEntries) != nil {
			return result
		}
	} else if environment != nil {
		return result
	}
	if verifyImagePlan(plan) != nil || e.verifyLocalRelease(ctx, plan.Release) != nil {
		return result
	}
	previousID := ""
	if plan.PreviousHealthy != nil {
		previousID = plan.PreviousHealthy.ContainerID
	}
	if storage != nil && storage.verify(ctx, e, plan, nil, previousID) != nil {
		result.Uncertain = true
		return result
	}
	if plan.PreviousHealthy != nil {
		previous := plan.PreviousHealthy
		existing, err := e.inspectContainer(ctx, previous.ContainerID)
		if err != nil || !ownedImageContainer(existing, plan.Snapshot.BindingID, previous.OperationID, previous.Release.Hash, previous.PlanHash) || existing.Image != previous.Release.ImageID {
			result.Uncertain = true
			return result
		}
		result.PreviousContainerID = previous.ContainerID
	}
	// Refuse an unexpected owned workload: a listing is not permission to delete
	// or replace effects that have no matching durable healthy pointer.
	raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+imageBindingLabel+"="+plan.Snapshot.BindingID)
	if err != nil {
		result.Uncertain = true
		return result
	}
	known := map[string]domain.HostingHealthy{}
	if plan.PreviousHealthy != nil {
		known[plan.PreviousHealthy.ContainerID] = *plan.PreviousHealthy
	}
	for _, retained := range plan.RetainedReleases {
		known[retained.ContainerID] = retained
	}
	for _, id := range strings.Fields(string(raw)) {
		container, inspectErr := e.inspectContainer(ctx, id)
		expected, allowed := known[id]
		if inspectErr != nil {
			result.Uncertain = true
			return result
		}
		if allowed {
			port := container.HostConfig.PortBindings[strconv.Itoa(expected.Snapshot.ContainerPort)+"/tcp"]
			if !ownedImageContainer(container, expected.Snapshot.BindingID, expected.OperationID, expected.Release.Hash, expected.PlanHash) || container.Image != expected.Release.ImageID || len(port) != 1 || port[0].HostIP != "127.0.0.1" || port[0].HostPort != strconv.Itoa(expected.Snapshot.HostPort) {
				result.Uncertain = true
				return result
			}
			delete(known, id)
		} else if storage != nil || container.State.Running {
			result.Uncertain = true
			return result
		}
	}
	if len(known) != 0 {
		result.Uncertain = true
		return result
	}
	// Read-only verification can take time. Recheck the original delivery window
	// immediately before the first mutation, while the attempt context is live.
	if ctx.Err() != nil || currentEnvironment && (!time.Now().Before(metadata.UseUntil) || !localDeadline.IsZero() && !time.Now().Before(localDeadline)) {
		return result
	}
	if plan.PreviousHealthy != nil && !plan.RetainPrevious {
		if currentEnvironment {
			deadline := metadata.UseUntil
			if !localDeadline.IsZero() && localDeadline.Before(deadline) {
				deadline = localDeadline
			}
			_, err = e.mutateBeforeRuntimeDeadline(ctx, plan.Release.DaemonID, deadline, "container", "stop", "--time", "10", plan.PreviousHealthy.ContainerID)
		} else {
			_, err = e.mutate(ctx, plan.Release.DaemonID, "container", "stop", "--time", "10", plan.PreviousHealthy.ContainerID)
		}
		if err != nil {
			result.Uncertain = true
			return result
		}
		if storage != nil && storage.confirmStopped(ctx, e, *plan.PreviousHealthy) != nil {
			result.Uncertain = true
			return result
		}
	}
	if storage != nil && storage.verify(ctx, e, plan, nil, "") != nil {
		result.Uncertain = true
		return result
	}
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(operationID), "sha256:")[:16]
	name := plan.Snapshot.RuntimeNamespace + "_" + suffix
	args := []string{"container", "create", "--name", name, "--restart", "no", "--publish", "127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + ":" + strconv.Itoa(plan.Snapshot.ContainerPort), "--label", imageBindingLabel + "=" + plan.Snapshot.BindingID, "--label", imageOperationLabel + "=" + operationID, "--label", imageReleaseLabel + "=" + plan.Release.Hash, "--label", imagePlanLabel + "=" + plan.Hash}
	if plan.Release.LocalBuild != (domain.HostingBuildArtifact{}) {
		args = append(args, "--pull=never")
	}
	if storage != nil && !currentEnvironment {
		args = append(args, "--mount", "type=volume,src="+plan.Storage.VolumeName+",dst="+plan.Storage.MountPath+",volume-nocopy")
	}
	args = append(args, hostingReleaseImage(plan.Release))
	if currentEnvironment {
		var id string
		createCtx := ctx
		if plan.PreviousHealthy == nil || plan.RetainPrevious {
			deadline := metadata.UseUntil
			if !localDeadline.IsZero() && localDeadline.Before(deadline) {
				deadline = localDeadline
			}
			var cancel context.CancelFunc
			createCtx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		err = environment.WithValues(func(entries []hostingruntimeprotocol.Entry) error {
			values := make(map[string][]byte, len(entries))
			for _, entry := range entries {
				values[entry.Name] = entry.Value
			}
			var createErr error
			if storage != nil {
				before := func(check context.Context) error {
					if check.Err() != nil || !time.Now().Before(metadata.UseUntil) || !localDeadline.IsZero() && !time.Now().Before(localDeadline) {
						return errors.New("runtime environment expired")
					}
					return storage.verify(check, e, plan, nil, "")
				}
				id, createErr = e.createWithAuthorizedOwnedStorageRuntimeEnvironment(createCtx, plan.Release.DaemonID, args[2:len(args)-1], hostingReleaseImage(plan.Release), values, hostingEnvironmentOwnedStorage{Volume: storage.request, Plan: *plan.Storage}, before)
			} else {
				id, createErr = e.createWithRuntimeEnvironment(createCtx, plan.Release.DaemonID, args[2:len(args)-1], hostingReleaseImage(plan.Release), values)
			}
			return createErr
		})
		raw = []byte(id)
	} else {
		raw, err = e.mutate(ctx, plan.Release.DaemonID, args...)
	}
	if err != nil {
		result.Uncertain = true
		return result
	}
	result.ContainerID = strings.TrimSpace(string(raw))
	if !dockerObjectID.MatchString(result.ContainerID) {
		result.Uncertain = true
		return result
	}
	var candidate *domain.HostingHealthy
	if storage != nil {
		candidate = storage.healthy(operationID, result.ContainerID, plan)
		// Docker can implicitly create a missing named volume. Verify the pinned
		// volume again after create and before admitting any application writer.
		if storage.verify(ctx, e, plan, candidate, "") != nil {
			result.Uncertain = true
			return result
		}
	}
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "start", result.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	if len(verifying) > 0 {
		if err = verifying[0](); err != nil {
			result.Uncertain = true
			return result
		}
	}
	storageCheck := func(context.Context) error { return nil }
	if storage != nil {
		storageCheck = func(checkContext context.Context) error {
			return storage.verify(checkContext, e, plan, candidate, result.ContainerID)
		}
	}
	if err = e.readiness(ctx, plan, operationID, result.ContainerID, storageCheck); err == nil {
		result.Outcome = "succeeded"
		result.Ready = true
		return result
	}
	if ctx.Err() != nil {
		result.Uncertain = true
		return result
	}
	if storage != nil && storageCheck(ctx) != nil {
		result.Uncertain = true
		return result
	}
	// Readiness failed after known successful start. Preserve the stopped prior
	// container and recover it only while this attempt still owns live authority.
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "stop", "--time", "5", result.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	if storage != nil && storage.confirmStopped(ctx, e, *candidate) != nil {
		result.Uncertain = true
		return result
	}
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "rm", result.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	if plan.PreviousHealthy == nil {
		result.Restored = true
		return result
	}
	if !plan.RetainPrevious {
		if storage != nil && storage.verify(ctx, e, plan, nil, "") != nil {
			result.Uncertain = true
			return result
		}
		if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "start", plan.PreviousHealthy.ContainerID); err != nil {
			result.Uncertain = true
			return result
		}
	}
	restored := plan
	restored.Snapshot = plan.PreviousHealthy.Snapshot
	restored.Release = plan.PreviousHealthy.Release
	restored.Hash = plan.PreviousHealthy.PlanHash
	restored.Storage = nil
	if plan.PreviousHealthy.Storage != nil {
		restored.Storage = &plan.PreviousHealthy.Storage.Plan
	}
	if storage != nil {
		storageCheck = func(checkContext context.Context) error {
			return storage.verify(checkContext, e, plan, nil, plan.PreviousHealthy.ContainerID)
		}
	}
	if err = e.readiness(ctx, restored, plan.PreviousHealthy.OperationID, plan.PreviousHealthy.ContainerID, storageCheck); err != nil {
		result.Uncertain = true
		return result
	}
	result.Restored = true
	return result
}

func (e *imageEngine) readiness(ctx context.Context, plan domain.HostingApplyPlan, operationID, containerID string, checks ...func(context.Context) error) error {
	deadline, cancel := context.WithTimeout(ctx, time.Duration(plan.Snapshot.Readiness.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probeURL := "http://127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + plan.Snapshot.Readiness.Path
	for {
		for _, check := range checks {
			if err := check(deadline); err != nil {
				return err
			}
		}
		if err := e.verifyDaemon(deadline, plan.Release.DaemonID); err != nil {
			return err
		}
		container, err := e.inspectContainer(deadline, containerID)
		if err != nil || !container.State.Running || container.Image != plan.Release.ImageID || !ownedImageContainer(container, plan.Snapshot.BindingID, operationID, plan.Release.Hash, plan.Hash) {
			return errors.New("readiness target is not the owned running container")
		}
		bindings := container.HostConfig.PortBindings[strconv.Itoa(plan.Snapshot.ContainerPort)+"/tcp"]
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != strconv.Itoa(plan.Snapshot.HostPort) {
			return errors.New("owned port mapping changed")
		}
		request, err := http.NewRequestWithContext(deadline, http.MethodGet, probeURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode <= 399 {
				for _, check := range checks {
					if err := check(deadline); err != nil {
						return err
					}
				}
				return nil
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			return errors.New("owned container HTTP readiness failed")
		case <-timer.C:
		}
	}
}

// Observe inspects only the persisted target and never changes workloads.
func (e *imageEngine) Observe(ctx context.Context, operation domain.HostingOperation) (domain.HostingObservation, error) {
	if operation.Snapshot.Stack != (domain.HostingStackRef{}) {
		return e.observeStack(ctx, operation)
	}
	observation := domain.HostingObservation{BindingID: operation.BindingID}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return observation, err
	}
	observation.DaemonID = daemon
	expected := operation.ReconciliationHealthy
	if operation.Result != nil && operation.Plan != nil && operation.Release != nil {
		expected = &domain.HostingHealthy{Snapshot: operation.Snapshot, OperationID: operation.ID, ContainerID: operation.Result.ContainerID, Release: *operation.Release, PlanHash: operation.Plan.Hash}
		if operation.Snapshot.Storage != (domain.HostingStorageRef{}) && operation.Result.Outcome == "succeeded" {
			if operation.ReconciliationHealthy == nil || operation.ReconciliationHealthy.ContainerID != expected.ContainerID || operation.ReconciliationHealthy.PlanHash != expected.PlanHash || operation.ReconciliationHealthy.Snapshot != expected.Snapshot || operation.ReconciliationHealthy.OperationID != expected.OperationID || operation.ReconciliationHealthy.Release != expected.Release {
				return observation, errors.New("durable healthy storage origin evidence missing")
			}
			expected.Storage = operation.ReconciliationHealthy.Storage
		}
		if operation.Result.Outcome != "succeeded" {
			if !operation.Result.Restored || operation.Plan.PreviousHealthy == nil {
				return observation, errors.New("no durable prior healthy result")
			}
			expected = operation.Plan.PreviousHealthy
		}
	}
	if expected == nil {
		raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+imageBindingLabel+"="+operation.BindingID)
		if err != nil {
			return observation, err
		}
		if len(strings.Fields(string(raw))) != 0 {
			return observation, errors.New("unaccounted owned container prevents observation proof")
		}
		observation.NoOwnedContainers = true
		return observation, nil
	}
	plan := domain.HostingApplyPlan{Snapshot: expected.Snapshot, Release: expected.Release, Hash: expected.PlanHash}
	check := func(context.Context) error { return nil }
	if domain.ValidateHostingHealthyStorage(*expected) != nil {
		return observation, errors.New("healthy storage evidence changed")
	}
	if expected.Storage != nil {
		plan.Storage = &expected.Storage.Plan
		var known []domain.HostingHealthy
		if operation.Plan != nil {
			known = storagePlanUsers(*operation.Plan)
		}
		check = func(checkContext context.Context) error {
			return e.verifyStorageObservation(checkContext, *expected, known)
		}
	}
	if err = e.readiness(ctx, plan, expected.OperationID, expected.ContainerID, check); err != nil {
		return observation, err
	}
	observation.ContainerID = expected.ContainerID
	observation.ImageID = expected.Release.ImageID
	observation.PlanHash = expected.PlanHash
	observation.ReleaseHash = expected.Release.Hash
	observation.Ready = true
	return observation, nil
}

// EnsurePreparedRelease restores a collected cache only by the committed digest.
// It never resolves the original tag again.
func (e *imageEngine) EnsurePreparedRelease(ctx context.Context, release domain.HostingRelease) error {
	if release.LocalBuild != (domain.HostingBuildArtifact{}) {
		return e.verifyLocalRelease(ctx, release)
	}
	if err := e.verifyDaemon(ctx, release.DaemonID); err != nil {
		return err
	}
	if err := e.verifyLocalRelease(ctx, release); err == nil {
		return nil
	}
	if _, err := e.docker(ctx, "image", "pull", release.CanonicalImage); err != nil {
		return err
	}
	return e.verifyLocalRelease(ctx, release)
}

// Cleanup removes only the exact server-selected retained container. A failed or
// ambiguous Docker command never releases external ownership through observation.
func (e *imageEngine) Cleanup(ctx context.Context, plan domain.HostingCleanupPlan) domain.HostingResult {
	result := domain.HostingResult{Outcome: "failed", DaemonID: plan.Target.Release.DaemonID, ImageID: plan.Target.Release.ImageID, ContainerID: plan.Target.ContainerID, PlanHash: plan.Hash}
	expected, err := domain.CompileHostingCleanupPlan(plan.Target, plan.CurrentHealthyOperationID)
	if err != nil || expected.Hash != plan.Hash || domain.ValidateHostingHealthyStorage(plan.Target) != nil || !dockerObjectID.MatchString(plan.Target.ContainerID) || !imageNamespace.MatchString(plan.Target.Snapshot.RuntimeNamespace) {
		return result
	}
	absent := func() (bool, error) {
		if err := e.verifyDaemon(ctx, plan.Target.Release.DaemonID); err != nil {
			return false, err
		}
		raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+plan.Target.ContainerID)
		if err != nil {
			return false, err
		}
		ids := strings.Fields(string(raw))
		if len(ids) == 0 {
			return true, nil
		}
		if len(ids) != 1 || ids[0] != plan.Target.ContainerID {
			return false, errors.New("cleanup target listing mismatch")
		}
		return false, nil
	}
	missing, err := absent()
	if err != nil {
		result.Uncertain = true
		return result
	}
	if !missing {
		container, err := e.inspectContainer(ctx, plan.Target.ContainerID)
		if err != nil {
			result.Uncertain = true
			return result
		}
		target := plan.Target
		if target.Storage != nil && (!storageContainerMatches(container, target) || e.verifyHealthyStorageVolume(ctx, target) != nil) {
			result.Uncertain = true
			return result
		}
		ports := container.HostConfig.PortBindings[strconv.Itoa(target.Snapshot.ContainerPort)+"/tcp"]
		if !ownedImageContainer(container, target.Snapshot.BindingID, target.OperationID, target.Release.Hash, target.PlanHash) || container.Image != target.Release.ImageID || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(target.Snapshot.HostPort) {
			result.Uncertain = true
			return result
		}
		if container.State.Running {
			if _, err = e.mutate(ctx, target.Release.DaemonID, "container", "stop", "--time", "10", target.ContainerID); err != nil {
				result.Uncertain = true
				return result
			}
		}
		if target.Storage != nil && (&ownedStorageExecution{}).confirmStopped(ctx, e, target) != nil {
			result.Uncertain = true
			return result
		}
		if _, err = e.mutate(ctx, target.Release.DaemonID, "container", "rm", target.ContainerID); err != nil {
			result.Uncertain = true
			return result
		}
	}
	if missing, err = absent(); err != nil || !missing {
		result.Uncertain = true
		return result
	}
	result.Outcome = "succeeded"
	return result
}
