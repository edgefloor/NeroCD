package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const hostingStorageLabelPrefix = "io.nerocd.storage."

var (
	hostingVolumeReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	hostingVolumeNonce     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	hostingVolumeHash      = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	hostingVolumeName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

	errHostingOwnedVolumeMissing   = errors.New("owned hosting volume absent without fresh creation permission")
	errHostingOwnedVolumeUncertain = errors.New("owned hosting volume creation outcome requires read-only reconciliation")
)

// hostingOwnedVolumeRequest borrows immutable controller-admitted metadata for a
// synchronous call. CreatePermitted must come only from the fresh response after
// durable creation admission, never a replay or a reconstructed local decision.
// OriginPlanHash belongs to that admission, not a later deployment's plan.
type hostingOwnedVolumeRequest struct {
	Storage           domain.HostingStorage
	DaemonID          string
	OriginOperationID string
	OriginPlanHash    string
	CreationNonce     string
	CreatePermitted   bool
	KnownUsers        []domain.HostingHealthy
}

// hostingOwnedVolumeEvidence is nonsecret Docker observation, not permission to
// start a writer. The caller must journal it and obtain controller materialization
// acknowledgment first. Labels and CreatedAt cannot detect a trusted daemon
// administrator replacing a volume and copying its metadata.
type hostingOwnedVolumeEvidence struct {
	Storage           domain.HostingStorageRef
	VolumeName        string
	DaemonID          string
	CreatedAt         string
	OriginOperationID string
	OriginPlanHash    string
	CreationNonce     string
	Driver            string
	Scope             string
	Labels            map[string]string
	Options           map[string]string
}

type hostingInspectedVolume struct {
	Name      string            `json:"Name"`
	Driver    string            `json:"Driver"`
	Scope     string            `json:"Scope"`
	Labels    map[string]string `json:"Labels"`
	Options   map[string]string `json:"Options"`
	CreatedAt string            `json:"CreatedAt"`
}

type hostingVolumeUser struct {
	imageContainer
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Driver      string `json:"Driver"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func (r hostingOwnedVolumeRequest) labels() map[string]string {
	return map[string]string{
		imageBindingLabel:                              r.Storage.BindingID,
		hostingStorageLabelPrefix + "id":               r.Storage.ID,
		hostingStorageLabelPrefix + "hash":             r.Storage.Hash,
		hostingStorageLabelPrefix + "generation":       strconv.FormatInt(r.Storage.Generation, 10),
		hostingStorageLabelPrefix + "project":          r.Storage.ProjectID,
		hostingStorageLabelPrefix + "resource":         r.Storage.ResourceID,
		hostingStorageLabelPrefix + "destination":      r.Storage.DestinationID,
		hostingStorageLabelPrefix + "origin_operation": r.OriginOperationID,
		hostingStorageLabelPrefix + "origin_plan":      r.OriginPlanHash,
		hostingStorageLabelPrefix + "creation_nonce":   r.CreationNonce,
		hostingStorageLabelPrefix + "daemon":           r.DaemonID,
	}
}

func (r hostingOwnedVolumeRequest) validate() error {
	if r.Storage.Validate() != nil || !hostingVolumeReference.MatchString(r.OriginOperationID) || !hostingVolumeHash.MatchString(r.OriginPlanHash) || !hostingVolumeNonce.MatchString(r.CreationNonce) || len(r.DaemonID) > 128 || strings.TrimSpace(r.DaemonID) != r.DaemonID || r.DaemonID == "" || strings.ContainsAny(r.DaemonID, "\x00\r\n") || len(r.KnownUsers) > 128 {
		return errors.New("invalid owned hosting volume request")
	}
	seen := make(map[string]bool, len(r.KnownUsers))
	for _, user := range r.KnownUsers {
		if !dockerObjectID.MatchString(user.ContainerID) || seen[user.ContainerID] || user.Snapshot.BindingID != r.Storage.BindingID || user.Snapshot.ResourceID != r.Storage.ResourceID || user.Snapshot.DestinationID != r.Storage.DestinationID || user.Release.DaemonID != r.DaemonID || domain.ValidateHostingRelease(user.Release) != nil || !hostingVolumeReference.MatchString(user.OperationID) || !hostingVolumeHash.MatchString(user.PlanHash) {
			return errors.New("invalid owned hosting volume user authority")
		}
		seen[user.ContainerID] = true
	}
	return nil
}

// ensureOwnedVolume never adopts, removes, or recreates retained storage. A
// creation error has an uncertain outcome even if the daemon may have created
// the volume. Only a subsequent call without creation permission may reconcile
// it. This helper does not consume or persist the controller's permission.
func (e *imageEngine) ensureOwnedVolume(parent context.Context, req hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
	return e.ensureOwnedVolumeWithAuthority(parent, req)
}

func (e *imageEngine) ensureOwnedVolumeWithAuthority(parent context.Context, req hostingOwnedVolumeRequest, beforeMutation ...func(context.Context) error) (hostingOwnedVolumeEvidence, error) {
	var zero hostingOwnedVolumeEvidence
	if len(beforeMutation) > 1 {
		return zero, errors.New("invalid volume mutation authority")
	}
	if err := req.validate(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := e.verifyOwnedVolumeDaemon(ctx, req.DaemonID); err != nil {
		return zero, err
	}
	present, err := e.ownedVolumePresent(ctx, req.Storage.VolumeName)
	if err != nil {
		return zero, err
	}
	if present {
		return e.verifyOwnedVolume(ctx, req)
	}
	if !req.CreatePermitted {
		return zero, errHostingOwnedVolumeMissing
	}
	// A previous foreign container can still reference an absent volume. Reject
	// it before creation as well as during the final inspection.
	if err := e.verifyOwnedVolumeUsers(ctx, req); err != nil {
		return zero, err
	}
	labels := req.labels()
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []string{"volume", "create", "--driver", "local"}
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, req.Storage.VolumeName)
	// Finish fallible daemon verification before the optional authority check.
	// docker checks cancellation again before the joined command executor.
	if err := e.verifyDaemon(ctx, req.DaemonID); err != nil {
		return zero, errors.Join(errHostingOwnedVolumeUncertain, err, ctx.Err())
	}
	if len(beforeMutation) == 1 && (beforeMutation[0] == nil || beforeMutation[0](ctx) != nil || ctx.Err() != nil) {
		return zero, errors.New("owned volume mutation authority unavailable")
	}
	if _, err := e.docker(ctx, args...); err != nil {
		return zero, errors.Join(errHostingOwnedVolumeUncertain, err, ctx.Err())
	}
	evidence, err := e.verifyOwnedVolume(ctx, req)
	if err != nil {
		return zero, errors.Join(errHostingOwnedVolumeUncertain, err)
	}
	return evidence, nil
}

func (e *imageEngine) ownedVolumeDocker(ctx context.Context, limit int, args ...string) ([]byte, error) {
	raw, err := e.docker(ctx, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("owned hosting volume evidence exceeds limit")
	}
	return raw, nil
}

func (e *imageEngine) verifyOwnedVolumeDaemon(ctx context.Context, expected string) error {
	err := e.verifyDaemon(ctx, expected)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// ownedVolumePresent treats only a successful bounded listing as evidence of
// absence. Docker's name filter matches substrings, so compare complete names.
func (e *imageEngine) ownedVolumePresent(ctx context.Context, name string) (bool, error) {
	raw, err := e.ownedVolumeDocker(ctx, 32<<10, "volume", "ls", "--quiet", "--filter", "name="+name)
	if err != nil {
		return false, err
	}
	names, err := hostingVolumeLines(raw, 256, hostingVolumeName)
	if err != nil {
		return false, err
	}
	for _, candidate := range names {
		if candidate == name {
			return true, nil
		}
	}
	return false, nil
}

func hostingVolumeLines(raw []byte, limit int, format *regexp.Regexp) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) > limit {
		return nil, errors.New("too many owned hosting volume evidence entries")
	}
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		if !format.MatchString(line) || seen[line] {
			return nil, errors.New("invalid owned hosting volume evidence listing")
		}
		seen[line] = true
	}
	return lines, nil
}

func (e *imageEngine) verifyOwnedVolume(ctx context.Context, req hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
	var zero hostingOwnedVolumeEvidence
	if err := e.verifyOwnedVolumeDaemon(ctx, req.DaemonID); err != nil {
		return zero, err
	}
	raw, err := e.ownedVolumeDocker(ctx, 64<<10, "volume", "inspect", req.Storage.VolumeName, "--format", "{{json .}}")
	if err != nil {
		return zero, err
	}
	var volume hostingInspectedVolume
	if json.Unmarshal(raw, &volume) != nil || volume.Name != req.Storage.VolumeName || volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 || len(volume.Labels) != len(req.labels()) {
		return zero, errors.New("owned hosting volume identity or policy mismatch")
	}
	created, err := time.Parse(time.RFC3339Nano, volume.CreatedAt)
	if err != nil || created.IsZero() {
		return zero, errors.New("owned hosting volume creation evidence missing")
	}
	for key, value := range req.labels() {
		if volume.Labels[key] != value {
			return zero, errors.New("owned hosting volume ownership mismatch")
		}
	}
	if err := e.verifyOwnedVolumeUsers(ctx, req); err != nil {
		return zero, err
	}
	if err := e.verifyOwnedVolumeDaemon(ctx, req.DaemonID); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return hostingOwnedVolumeEvidence{
		Storage: req.Storage.Reference(), VolumeName: volume.Name, DaemonID: req.DaemonID,
		CreatedAt: volume.CreatedAt, OriginOperationID: req.OriginOperationID, OriginPlanHash: req.OriginPlanHash,
		CreationNonce: req.CreationNonce, Driver: volume.Driver, Scope: volume.Scope,
		Labels: volume.Labels, Options: map[string]string{},
	}, nil
}

func (e *imageEngine) verifyOwnedVolumeUsers(ctx context.Context, req hostingOwnedVolumeRequest) error {
	raw, err := e.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+req.Storage.VolumeName)
	if err != nil {
		return err
	}
	ids, err := hostingVolumeLines(raw, 128, dockerObjectID)
	if err != nil {
		return err
	}
	known := make(map[string]domain.HostingHealthy, len(req.KnownUsers))
	for _, user := range req.KnownUsers {
		known[user.ContainerID] = user
	}
	for _, id := range ids {
		expected, ok := known[id]
		if !ok {
			return errors.New("owned hosting volume has a foreign container user")
		}
		raw, err := e.ownedVolumeDocker(ctx, 256<<10, "container", "inspect", id, "--format", "{{json .}}")
		if err != nil {
			return err
		}
		var user hostingVolumeUser
		if json.Unmarshal(raw, &user) != nil || user.ID != id || user.Image != expected.Release.ImageID || !ownedImageContainer(user.imageContainer, req.Storage.BindingID, expected.OperationID, expected.Release.Hash, expected.PlanHash) || len(user.Mounts) != 1 || len(user.HostConfig.Mounts) > 1 {
			return errors.New("owned hosting volume user identity mismatch")
		}
		for _, mount := range user.HostConfig.Mounts {
			if mount.Type != "volume" || mount.Source != req.Storage.VolumeName || mount.Target != req.Storage.MountPath || mount.ReadOnly {
				return errors.New("owned hosting volume configured mount mismatch")
			}
		}
		matches := 0
		for _, mount := range user.Mounts {
			if mount.Name == req.Storage.VolumeName || mount.Destination == req.Storage.MountPath {
				if mount.Type != "volume" || mount.Name != req.Storage.VolumeName || mount.Driver != "local" || mount.Destination != req.Storage.MountPath || !mount.RW {
					return errors.New("owned hosting volume user mount mismatch")
				}
				matches++
			}
		}
		if matches != 1 {
			return errors.New("owned hosting volume user mount evidence missing or repeated")
		}
	}
	return nil
}
