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
	endpoint, configDir string
	command             imageCommand
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
		return "", errors.New("Docker daemon identity missing")
	}
	return id, nil
}
func (e *imageEngine) verifyDaemon(ctx context.Context, expected string) error {
	got, err := e.daemonID(ctx)
	if err != nil {
		return err
	}
	if got != expected {
		return errors.New("Docker daemon identity changed")
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
		return domain.HostingRelease{}, errors.New("Docker image lacks the pulled repository digest")
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
	image, err := e.inspectImage(ctx, release.CanonicalImage)
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
	hash := plan.Hash
	plan.Hash = ""
	if hash == "" || hash != domain.HostingDocumentHash(plan) || plan.CompilerVersion != domain.HostingCompilerVersion || plan.Snapshot.PolicyVersion != domain.HostingPolicyVersion || !imageNamespace.MatchString(plan.Snapshot.RuntimeNamespace) {
		return errors.New("invalid immutable image apply plan")
	}
	in := domain.HostingDeploymentInput{ExpectedGeneration: plan.Snapshot.ResourceGeneration, IdempotencyKey: "internal-plan", ContainerPort: plan.Snapshot.ContainerPort, HostPort: plan.Snapshot.HostPort, Readiness: plan.Snapshot.Readiness}
	if err := domain.ValidateHostingDeployment(in); err != nil {
		return err
	}
	return domain.ValidateHostingRelease(plan.Release)
}

// Apply permits fixed-port interruption. A failed pull never reaches this
// method. Unknown command outcomes are retained for reconciliation; only a
// known readiness failure enters bounded restoration of the prior workload.
func (e *imageEngine) Apply(ctx context.Context, operationID string, plan domain.HostingApplyPlan) domain.HostingResult {
	result := domain.HostingResult{Outcome: "failed", DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, ImageID: plan.Release.ImageID}
	if verifyImagePlan(plan) != nil || e.verifyLocalRelease(ctx, plan.Release) != nil {
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
	for _, id := range strings.Fields(string(raw)) {
		if plan.PreviousHealthy == nil || id != plan.PreviousHealthy.ContainerID {
			preserved, inspectErr := e.inspectContainer(ctx, id)
			if inspectErr != nil || preserved.State.Running {
				result.Uncertain = true
				return result
			}
		}
	}
	if plan.PreviousHealthy != nil {
		if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "stop", "--time", "10", plan.PreviousHealthy.ContainerID); err != nil {
			result.Uncertain = true
			return result
		}
	}
	suffix := strings.TrimPrefix(domain.HostingDocumentHash(operationID), "sha256:")[:16]
	name := plan.Snapshot.RuntimeNamespace + "_" + suffix
	args := []string{"container", "create", "--name", name, "--restart", "no", "--publish", "127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + ":" + strconv.Itoa(plan.Snapshot.ContainerPort), "--label", imageBindingLabel + "=" + plan.Snapshot.BindingID, "--label", imageOperationLabel + "=" + operationID, "--label", imageReleaseLabel + "=" + plan.Release.Hash, "--label", imagePlanLabel + "=" + plan.Hash, plan.Release.CanonicalImage}
	raw, err = e.mutate(ctx, plan.Release.DaemonID, args...)
	if err != nil {
		result.Uncertain = true
		return result
	}
	result.ContainerID = strings.TrimSpace(string(raw))
	if !dockerObjectID.MatchString(result.ContainerID) {
		result.Uncertain = true
		return result
	}
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "start", result.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	if err = e.readiness(ctx, plan, operationID, result.ContainerID); err == nil {
		result.Outcome = "succeeded"
		result.Ready = true
		return result
	}
	if ctx.Err() != nil {
		result.Uncertain = true
		return result
	}
	// Readiness failed after known successful start. Preserve the stopped prior
	// container and recover it only while this attempt still owns live authority.
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "stop", "--time", "5", result.ContainerID); err != nil {
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
	if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "start", plan.PreviousHealthy.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	restored := plan
	restored.Snapshot = plan.PreviousHealthy.Snapshot
	restored.Release = plan.PreviousHealthy.Release
	restored.Hash = plan.PreviousHealthy.PlanHash
	if err = e.readiness(ctx, restored, plan.PreviousHealthy.OperationID, plan.PreviousHealthy.ContainerID); err != nil {
		result.Uncertain = true
		return result
	}
	result.Restored = true
	return result
}

func (e *imageEngine) readiness(ctx context.Context, plan domain.HostingApplyPlan, operationID, containerID string) error {
	deadline, cancel := context.WithTimeout(ctx, time.Duration(plan.Snapshot.Readiness.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probeURL := "http://127.0.0.1:" + strconv.Itoa(plan.Snapshot.HostPort) + plan.Snapshot.Readiness.Path
	for {
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

func (e *imageEngine) Observe(ctx context.Context, operation domain.HostingOperation) (domain.HostingObservation, error) {
	observation := domain.HostingObservation{BindingID: operation.BindingID}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return observation, err
	}
	observation.DaemonID = daemon
	expected := operation.ReconciliationHealthy
	if operation.Result != nil && operation.Plan != nil && operation.Release != nil {
		expected = &domain.HostingHealthy{Snapshot: operation.Snapshot, OperationID: operation.ID, ContainerID: operation.Result.ContainerID, Release: *operation.Release, PlanHash: operation.Plan.Hash}
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
	if err = e.readiness(ctx, plan, expected.OperationID, expected.ContainerID); err != nil {
		return observation, err
	}
	observation.ContainerID = expected.ContainerID
	observation.ImageID = expected.Release.ImageID
	observation.PlanHash = expected.PlanHash
	observation.ReleaseHash = expected.Release.Hash
	observation.Ready = true
	return observation, nil
}
