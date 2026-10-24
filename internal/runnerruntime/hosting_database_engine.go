//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/distribution/reference"
	"nerocd/internal/domain"
)

var errDatabaseInitializationUnresolved = errors.New("database initialization history remains unresolved; authorized recovery is required")
var errDatabaseRetirementUnresolved = errors.New("database predecessor retirement remains unresolved; authorized recovery is required")

func databaseAuthorize(ctx context.Context, before func(context.Context) error) error {
	if ctx.Err() != nil || before == nil {
		return errors.New("database mutation requires current authority")
	}
	if before(ctx) != nil || ctx.Err() != nil {
		return errors.New("database mutation authority unavailable")
	}
	return nil
}

func (c DatabaseEngineConfig) validateHost(ctx context.Context, e *imageEngine) error {
	if c.stateRoot == "" || c.secretRoot == "" || strings.HasPrefix(c.secretRoot, e.configDir+"/") || strings.HasPrefix(c.stateRoot, e.configDir+"/") {
		return errors.New("database persistent host configuration required")
	}
	if c.hostCheck != nil {
		return c.hostCheck(ctx, e)
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || c.uid != 999 || c.gid != 999 {
		return errors.New("database engine requires native Linux root privilege")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return errors.New("database host privilege evidence unavailable")
	}
	if !databaseHostCapabilities(status) {
		return errors.New("database host requires file ownership and protected traversal capabilities")
	}
	for _, path := range []string{c.stateRoot, c.secretRoot} {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path || databaseFilePolicy(path, 0o700, 0, 0, true) != nil || databaseProtectedParents(path) != nil {
			return errors.New("database persistent host roots changed")
		}
	}
	// This profile intentionally excludes container/user-namespace remapping.
	// Root administrators and a forwarded/replaced Unix socket remain trusted;
	// hostname and visible filesystem checks are not cryptographic host identity.
	for _, path := range []string{"/proc/self/uid_map", "/proc/self/gid_map"} {
		raw, err := os.ReadFile(path)
		if err != nil || !slices.Equal(strings.Fields(string(raw)), []string{"0", "0", "4294967295"}) {
			return errors.New("database engine does not support remapped host identities")
		}
	}
	self, err := os.Stat("/proc/self/ns/mnt")
	host, hostErr := os.Stat("/proc/1/ns/mnt")
	if err != nil || hostErr != nil || !os.SameFile(self, host) {
		return errors.New("database engine requires daemon-host mount namespace")
	}
	socket := strings.TrimPrefix(e.endpoint, "unix://")
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o007 != 0 {
		return errors.New("database daemon socket policy mismatch")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("database daemon socket must be root owned")
	}
	raw, err := e.ownedVolumeDocker(ctx, 32<<10, "info", "--format", "{{json .}}")
	var daemon struct {
		OSType, Name, DockerRootDir string
		SecurityOptions             []string
	}
	hostname, hostErr := os.Hostname()
	if err != nil || hostErr != nil || json.Unmarshal(raw, &daemon) != nil || daemon.OSType != "linux" || daemon.Name != hostname || !filepath.IsAbs(daemon.DockerRootDir) {
		return errors.New("database daemon-host profile mismatch")
	}
	for _, option := range daemon.SecurityOptions {
		if strings.Contains(option, "rootless") || strings.Contains(option, "userns") {
			return errors.New("database daemon user namespace mapping unsupported")
		}
	}
	rootInfo, err := os.Stat(daemon.DockerRootDir)
	if err != nil || !rootInfo.IsDir() {
		return errors.New("database daemon host filesystem unavailable")
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootStat.Uid != 0 {
		return errors.New("database daemon filesystem ownership mismatch")
	}
	return nil
}

// PrepareDatabase resolves an exact supported child manifest/config identity.
// Pulling is a mutation and requires the same current-authority callback as apply.
func (e *imageEngine) PrepareDatabase(parent context.Context, snapshot domain.HostingSnapshot, config DatabaseEngineConfig, before func(context.Context) error) (domain.HostingRelease, error) {
	var zero domain.HostingRelease
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	if domain.ValidateHostingDatabaseSnapshot(snapshot) != nil || snapshot.Database == (domain.HostingDatabaseExecutionRef{}) || config.validateHost(ctx, e) != nil {
		return zero, errors.New("invalid database preparation profile")
	}
	recipe, err := domain.HostingDatabaseRecipeFor(snapshot.Database.Engine, snapshot.Database.RecipeVersion)
	if err != nil {
		return zero, errors.New("unsupported database recipe")
	}
	var selected *domain.HostingDatabaseImagePlatform
	for i := range recipe.Platforms {
		p := &recipe.Platforms[i]
		if p.OS == "linux" && p.Architecture == runtime.GOARCH {
			selected = p
			break
		}
	}
	if selected == nil {
		return zero, errors.New("database native image platform unsupported")
	}
	daemon, err := e.daemonID(ctx)
	if err != nil {
		return zero, errors.New("database daemon unavailable")
	}
	canonical := strings.Split(recipe.ImageReference, "@")[0] + "@" + selected.Digest
	if databaseAuthorize(ctx, before) != nil {
		return zero, errors.New("database image pull authority unavailable")
	}
	if _, err = e.mutate(ctx, daemon, "image", "pull", canonical); err != nil {
		return zero, errors.New("database image pull not confirmed")
	}
	image, err := e.inspectImage(ctx, canonical)
	if err != nil {
		return zero, errors.New("database prepared image unavailable")
	}
	found := false
	for _, candidate := range image.RepoDigests {
		parsed, err := reference.ParseNormalizedNamed(candidate)
		if err == nil && parsed.String() == canonical {
			found = true
			break
		}
	}
	if !found {
		return zero, errors.New("database image lacks exact child manifest digest")
	}
	release := domain.HostingRelease{CanonicalImage: canonical, ImageID: image.ID, OS: image.OS, Architecture: image.Architecture, Variant: image.Variant, DaemonID: daemon}
	release.Hash = domain.HostingDocumentHash(release)
	if domain.ValidateHostingDatabaseRelease(snapshot, release) != nil || e.verifyDaemon(ctx, daemon) != nil || ctx.Err() != nil {
		return zero, errors.New("database prepared release changed")
	}
	return release, nil
}

type databaseContainer struct {
	imageEnvironment []string
	ID               string `json:"Id"`
	Name, Image      string
	Config           struct {
		Labels          map[string]string
		Env             []string
		Cmd, Entrypoint []string
		User            string
	}
	State struct {
		Running bool
		Status  string
	}
	HostConfig struct {
		PortBindings                                map[string]json.RawMessage
		PublishAllPorts, Privileged, ReadonlyRootfs bool
		NetworkMode                                 string
		Binds                                       []string
		CapAdd                                      []string
		CapDrop                                     []string
		Runtime                                     string
		PidMode, IpcMode, UsernsMode                string
		Devices                                     []json.RawMessage
		SecurityOpt                                 []string
		RestartPolicy                               struct {
			Name              string
			MaximumRetryCount int
		}
	}
	Mounts []struct {
		Type, Name, Driver, Source, Destination string
		RW                                      bool
	}
	NetworkSettings struct {
		Networks map[string]struct{ NetworkID, IPAddress, GlobalIPv6Address string }
	}
}

func databaseContainerName(operation string, plan domain.HostingApplyPlan) string {
	return plan.Snapshot.RuntimeNamespace + "_db_" + domain.HostingDocumentHash(struct{ Operation, Plan string }{operation, plan.Hash})[7:31]
}

func databaseContainerLabels(operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord) map[string]string {
	return map[string]string{imageBindingLabel: plan.Snapshot.BindingID, imageOperationLabel: operation, imageReleaseLabel: plan.Release.Hash, imagePlanLabel: plan.Hash, "io.nerocd.database.configuration": plan.Database.Reference.ConfigurationHash, "io.nerocd.database.network": record.NetworkID}
}

func databaseRecipeArgs(plan domain.HostingApplyPlan) ([]string, []string) {
	if plan.Database.Reference.Engine == "redis" {
		return nil, []string{"redis-server", databaseSecretMount + "/redis.conf"}
	}
	return []string{"POSTGRES_PASSWORD_FILE=" + databaseSecretMount + "/admin-password", "POSTGRES_USER=postgres", "POSTGRES_DB=app", "POSTGRES_HOST_AUTH_METHOD=scram-sha-256", "POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 --auth-local=peer"}, []string{"postgres", "-c", "log_statement=none", "-c", "log_min_error_statement=panic", "-c", "log_error_verbosity=terse", "-c", "log_parameter_max_length_on_error=0", "-c", "password_encryption=scram-sha-256"}
}

func (e *imageEngine) inspectDatabaseContainer(ctx context.Context, id string) (databaseContainer, error) {
	var container databaseContainer
	if !dockerObjectID.MatchString(id) {
		return container, errors.New("invalid database container identity")
	}
	raw, err := e.ownedVolumeDocker(ctx, 256<<10, "container", "inspect", id, "--format", "{{json .}}")
	if err != nil || json.Unmarshal(raw, &container) != nil {
		return container, errors.New("database container inspection unavailable")
	}
	if !hostingVolumeHash.MatchString(container.Image) {
		return container, errors.New("database container image identity invalid")
	}
	raw, err = e.ownedVolumeDocker(ctx, 32<<10, "image", "inspect", container.Image, "--format", "{{json .Config.Env}}")
	if err != nil || json.Unmarshal(raw, &container.imageEnvironment) != nil || len(container.imageEnvironment) > 64 {
		return container, errors.New("database image environment unavailable")
	}
	return container, nil
}

func databaseContainerMatches(container databaseContainer, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, secretPath string) bool {
	if len(container.HostConfig.CapDrop) != 0 || container.HostConfig.Runtime != "" && container.HostConfig.Runtime != "runc" {
		return false
	}
	if !dockerObjectID.MatchString(container.ID) || container.Name != "/"+databaseContainerName(operation, plan) || container.Image != plan.Release.ImageID || !maps.Equal(container.Config.Labels, databaseContainerLabels(operation, plan, record)) || len(container.HostConfig.PortBindings) != 0 || container.HostConfig.PublishAllPorts || container.HostConfig.Privileged || container.HostConfig.ReadonlyRootfs || container.HostConfig.NetworkMode != plan.Database.NetworkName || len(container.HostConfig.Binds) != 0 || len(container.HostConfig.CapAdd) != 0 || container.HostConfig.PidMode != "" || container.HostConfig.IpcMode != "private" || container.HostConfig.UsernsMode != "" || len(container.HostConfig.Devices) != 0 || len(container.HostConfig.SecurityOpt) != 0 || container.HostConfig.RestartPolicy.Name != "unless-stopped" || container.HostConfig.RestartPolicy.MaximumRetryCount != 0 || len(container.Mounts) != 2 || len(container.NetworkSettings.Networks) != 1 || container.Config.User != "" {
		return false
	}
	env, cmd := databaseRecipeArgs(plan)
	if !slices.Equal(container.Config.Cmd, cmd) {
		return false
	}
	entrypoint := "docker-entrypoint.sh"
	if plan.Database.Reference.Engine == "postgresql" {
		entrypoint = "docker-entrypoint.sh"
	}
	if !slices.Equal(container.Config.Entrypoint, []string{entrypoint}) {
		return false
	}
	// Merge only the fixed overrides onto the exact config-ID environment.
	// Arbitrary container ENV (including loader/entrypoint options) is refused.
	expectedEnv := map[string]string{}
	for _, value := range container.imageEnvironment {
		key, _, ok := strings.Cut(value, "=")
		if !ok || len(key) == 0 {
			return false
		}
		if _, exists := expectedEnv[key]; exists {
			return false
		}
		expectedEnv[key] = value
	}
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		expectedEnv[key] = value
	}
	if len(container.Config.Env) != len(expectedEnv) {
		return false
	}
	seenEnv := map[string]bool{}
	for _, value := range container.Config.Env {
		key, _, _ := strings.Cut(value, "=")
		if seenEnv[key] || expectedEnv[key] != value || key == "POSTGRES_PASSWORD" || key == "PGPASSWORD" || key == "REDISCLI_AUTH" {
			return false
		}
		seenEnv[key] = true
	}
	volume, secret := false, false
	for _, mount := range container.Mounts {
		switch mount.Destination {
		case plan.Storage.MountPath:
			volume = mount.Type == "volume" && mount.Name == plan.Storage.VolumeName && mount.Driver == "local" && mount.RW
		case databaseSecretMount:
			secret = mount.Type == "bind" && mount.Source == secretPath && !mount.RW
		default:
			return false
		}
	}
	network, ok := container.NetworkSettings.Networks[plan.Database.NetworkName]
	return volume && secret && ok && (network.NetworkID == record.NetworkID || !container.State.Running && network.NetworkID == "") && network.GlobalIPv6Address == ""
}

func databaseVolumeRequest(operation string, plan domain.HostingApplyPlan, req hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence) (hostingOwnedVolumeRequest, error) {
	if domain.ValidateHostingDatabasePlan(plan) != nil || !hostingVolumeReference.MatchString(operation) || req.validate() != nil || req.CreatePermitted || req.Storage.Reference() != plan.Storage.Reference || req.Storage.BindingID != plan.Snapshot.BindingID || req.Storage.ResourceID != plan.Snapshot.ResourceID || req.Storage.DestinationID != plan.Snapshot.DestinationID || req.Storage.MountPath != plan.Storage.MountPath || req.Storage.VolumeName != plan.Storage.VolumeName || req.DaemonID != plan.Release.DaemonID || req.OriginOperationID != plan.Storage.CreationOperationID || req.CreationNonce != plan.Storage.CreationNonce || operation == req.OriginOperationID && req.OriginPlanHash != plan.Hash {
		return req, errors.New("database materialization admission differs from plan")
	}
	if plan.PreviousHealthy != nil && (plan.PreviousHealthy.Storage == nil || plan.PreviousHealthy.Storage.CreationPlanHash != req.OriginPlanHash || !dockerObjectID.MatchString(plan.PreviousHealthy.ContainerID)) {
		return req, errors.New("database predecessor volume origin mismatch")
	}
	for _, user := range req.KnownUsers {
		if plan.PreviousHealthy == nil || domain.HostingDocumentHash(user) != domain.HostingDocumentHash(*plan.PreviousHealthy) {
			return req, errors.New("database caller added an unauthorized volume user")
		}
	}
	req.KnownUsers = nil
	if plan.PreviousHealthy != nil {
		req.KnownUsers = []domain.HostingHealthy{*plan.PreviousHealthy}
	}
	expected := hostingOwnedVolumeEvidence{Storage: req.Storage.Reference(), VolumeName: req.Storage.VolumeName, DaemonID: req.DaemonID, CreatedAt: evidence.CreatedAt, OriginOperationID: req.OriginOperationID, OriginPlanHash: req.OriginPlanHash, CreationNonce: req.CreationNonce, Driver: "local", Scope: "local", Labels: req.labels()}
	created, err := time.Parse(time.RFC3339Nano, evidence.CreatedAt)
	if err != nil || created.IsZero() || !sameOwnedVolumeEvidence(evidence, expected) {
		return req, errors.New("database volume materialization evidence mismatch")
	}
	return req, nil
}

// ApplyDatabase requires the caller's durable materialization ACK and ordinary
// apply admission. It never creates storage. Cancellation after effects returns
// uncertainty; neither a callback nor process cancellation fences old effects.
func (e *imageEngine) ApplyDatabase(parent context.Context, operation string, plan domain.HostingApplyPlan, volumeReq hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, config DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	// ApplyBegin may have admitted an earlier invocation whose response was lost.
	// Even a failed preflight cannot prove absence of those effects.
	result := domain.HostingResult{Outcome: "failed", Uncertain: true, DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, ImageID: plan.Release.ImageID}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	req, err := databaseVolumeRequest(operation, plan, volumeReq, evidence)
	if err != nil || before == nil || config.validateHost(ctx, e) != nil {
		return result
	}
	err = credentials.use(ctx, plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
		lock, err := config.lock(ctx, plan.Snapshot.BindingID)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		if e.verifyLocalRelease(ctx, plan.Release) != nil {
			return errors.New("database image release changed")
		}
		// From the first retained-history write onward an unknown outcome keeps
		// the controller hold; there is no inferred rollback or empty-volume reset.
		result.Uncertain = true
		return e.applyDatabase(ctx, operation, plan, req, evidence, values, config, before, &result)
	})
	if err == nil && ctx.Err() == nil {
		result.Outcome = "succeeded"
		result.Ready = true
		result.Uncertain = false
	}
	return result
}

func (e *imageEngine) applyDatabase(ctx context.Context, operation string, plan domain.HostingApplyPlan, req hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, values []DatabaseCredentialValue, c DatabaseEngineConfig, before func(context.Context) error, result *domain.HostingResult) error {
	name := databaseContainerName(operation, plan)
	raw, err := e.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	if err != nil {
		return err
	}
	ids, err := hostingVolumeLines(raw, 1, dockerObjectID)
	if err != nil {
		return err
	}
	candidate := ""
	if len(ids) == 1 {
		candidate = ids[0]
	}
	verifyVolume := func() error {
		check := req
		if candidate != "" {
			check.KnownUsers = append(slices.Clone(req.KnownUsers), domain.HostingHealthy{OperationID: operation, ContainerID: candidate, Snapshot: plan.Snapshot, Release: plan.Release, PlanHash: plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *plan.Storage, CreationPlanHash: req.OriginPlanHash}})
		}
		got, err := e.ensureOwnedVolume(ctx, check)
		if err != nil || !sameOwnedVolumeEvidence(got, evidence) {
			return errors.New("database retained volume identity changed")
		}
		return nil
	}
	if err = verifyVolume(); err != nil {
		return err
	}
	known := databaseKnownContainers(plan, candidate)
	record, _, err := e.ensureDatabaseNetwork(ctx, operation, plan, req, evidence, c, before, known)
	if err != nil {
		return err
	}
	retirement, retirementExists, err := c.readRetirement(operation, plan, record, candidate)
	if err != nil {
		return err
	}
	if retirementExists {
		if err := c.checkSecretFiles(ctx, operation, plan, values); err != nil {
			return err
		}
		if e.databaseReplacementReady(ctx, operation, plan, record, c, candidate, values) != nil {
			return errors.New("database retirement requires existing ready replacement")
		}
	}
	secretPath, err := c.secrets(ctx, operation, plan, values, before)
	if err != nil {
		return err
	}
	if err = c.admitContainer(ctx, operation, plan, record, candidate != "", before); err != nil {
		return err
	}
	if candidate != "" {
		container, err := e.inspectDatabaseContainer(ctx, candidate)
		if err != nil || !databaseContainerMatches(container, operation, plan, record, secretPath) {
			return errors.New("database candidate ownership mismatch")
		}
		result.ContainerID = candidate
	}
	if err = e.databaseWriters(ctx, operation, plan, record, c, candidate, candidate, true); err != nil {
		return err
	}
	if plan.PreviousHealthy != nil {
		previous := *plan.PreviousHealthy
		priorPlan := domain.HostingApplyPlan{Snapshot: previous.Snapshot, Release: previous.Release, Storage: &previous.Storage.Plan, Database: plan.Database, Hash: previous.PlanHash}
		present, err := e.databaseContainerPresent(ctx, previous.ContainerID)
		if err != nil {
			return err
		}
		if !present {
			if !retirementExists || e.databaseReplacementReady(ctx, operation, plan, record, c, candidate, values) != nil {
				return errors.New("database predecessor absent without exact retirement and ready replacement")
			}
		} else {
			if retirementExists && retirement.Completed {
				return errors.New("completed database retirement predecessor unexpectedly present")
			}
			container, err := e.inspectDatabaseContainer(ctx, previous.ContainerID)
			if err != nil || !databaseContainerMatches(container, previous.OperationID, priorPlan, record, filepath.Join(c.secretRoot, databaseGeneration(previous.OperationID, priorPlan))) {
				return errors.New("database predecessor ownership mismatch")
			}
			if container.State.Running {
				if candidate != "" {
					current, err := e.inspectDatabaseContainer(ctx, candidate)
					if err != nil || current.State.Running {
						return errors.New("database predecessor and replacement may both be writers")
					}
				}
				if databaseAuthorize(ctx, before) != nil {
					return errors.New("database predecessor stop authority unavailable")
				}
				if _, err = e.mutate(ctx, req.DaemonID, "container", "stop", "--time", "30", previous.ContainerID); err != nil {
					return errors.New("database predecessor stop outcome unconfirmed")
				}
			}
			container, err = e.inspectDatabaseContainer(ctx, previous.ContainerID)
			if err != nil || container.State.Running || !databaseContainerMatches(container, previous.OperationID, priorPlan, record, filepath.Join(c.secretRoot, databaseGeneration(previous.OperationID, priorPlan))) {
				return errors.New("database predecessor writer stop not confirmed")
			}
		}
		result.PreviousContainerID = previous.ContainerID
	}
	if err = verifyVolume(); err != nil {
		return err
	}
	if err = e.databaseWriters(ctx, operation, plan, record, c, candidate, candidate, false); err != nil {
		return err
	}
	if candidate == "" {
		args := []string{"container", "create", "--name", name, "--network", plan.Database.NetworkName, "--ipc", "private", "--restart", "unless-stopped", "--mount", "type=volume,source=" + plan.Storage.VolumeName + ",target=" + plan.Storage.MountPath + ",volume-nocopy", "--mount", "type=bind,source=" + secretPath + ",target=" + databaseSecretMount + ",readonly"}
		labels := databaseContainerLabels(operation, plan, record)
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "--label", key+"="+labels[key])
		}
		env, cmd := databaseRecipeArgs(plan)
		for _, value := range env {
			args = append(args, "--env", value)
		}
		args = append(args, plan.Release.ImageID)
		args = append(args, cmd...)
		if databaseAuthorize(ctx, before) != nil {
			return errors.New("database create authority unavailable")
		}
		output, err := e.mutate(ctx, req.DaemonID, args...)
		if err != nil {
			return errors.New("database container creation outcome requires inspection")
		}
		candidate = strings.TrimSpace(string(output))
		if !dockerObjectID.MatchString(candidate) {
			return errors.New("database container creation identity unavailable")
		}
		result.ContainerID = candidate
	}
	container, err := e.inspectDatabaseContainer(ctx, candidate)
	if err != nil || !databaseContainerMatches(container, operation, plan, record, secretPath) {
		return errors.New("database created container provenance changed")
	}
	if !container.State.Running {
		if retirementExists {
			return errors.New("database retirement cannot restart its replacement")
		}
		if err = verifyVolume(); err != nil {
			return err
		}
		if err = e.databaseWriters(ctx, operation, plan, record, c, candidate, "", false); err != nil {
			return err
		}
		if databaseAuthorize(ctx, before) != nil {
			return errors.New("database start authority unavailable")
		}
		if _, err = e.mutate(ctx, req.DaemonID, "container", "start", candidate); err != nil {
			return errors.New("database writer start outcome requires inspection")
		}
	}
	for {
		container, err = e.inspectDatabaseContainer(ctx, candidate)
		if err != nil || !container.State.Running || !databaseContainerMatches(container, operation, plan, record, secretPath) {
			return errors.New("database writer state or identity changed")
		}
		network, err := e.inspectDatabaseNetwork(ctx, plan.Database.NetworkName, record, databaseKnownContainers(plan, candidate))
		if err != nil {
			return err
		}
		address, err := databaseNetworkAddress(network, candidate)
		if err != nil {
			return err
		}
		if err = c.probe(ctx, address, plan, values, false, !record.InitReady, before); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return errDatabaseReadiness
		case <-time.After(250 * time.Millisecond):
		}
	}
	if e.databaseProcessOwnership(ctx, req.DaemonID, candidate) != nil {
		return errors.New("database runtime identity not confirmed")
	}
	if err = verifyVolume(); err != nil {
		return err
	}
	if err = e.databaseWriters(ctx, operation, plan, record, c, candidate, candidate, false); err != nil {
		return err
	}
	container, err = e.inspectDatabaseContainer(ctx, candidate)
	if err != nil || !container.State.Running || !databaseContainerMatches(container, operation, plan, record, secretPath) {
		return errors.New("database final writer identity changed")
	}
	if e.verifyLocalRelease(ctx, plan.Release) != nil || ctx.Err() != nil {
		return errors.New("database final identity unavailable")
	}
	if _, err = e.inspectDatabaseNetwork(ctx, plan.Database.NetworkName, record, databaseKnownContainers(plan, candidate)); err != nil {
		return err
	}
	if !record.InitReady {
		record.InitReady = true
		if err = c.writeRecord(ctx, plan.Database.NetworkName+".json", record, before); err != nil {
			return err
		}
	}
	if err = e.retireDatabasePredecessor(ctx, operation, plan, record, c, candidate, values, before); err != nil {
		return err
	}
	if err = verifyVolume(); err != nil {
		return err
	}
	if err = e.databaseWriters(ctx, operation, plan, record, c, candidate, candidate, false); err != nil {
		return err
	}
	if plan.PreviousHealthy != nil && e.databaseReplacementReady(ctx, operation, plan, record, c, candidate, values) != nil {
		return errors.New("database replacement readiness changed after retirement")
	}
	return nil
}

func (e *imageEngine) databaseContainerPresent(ctx context.Context, id string) (bool, error) {
	if !dockerObjectID.MatchString(id) {
		return false, errors.New("invalid database predecessor identity")
	}
	raw, err := e.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	if err != nil {
		return false, errors.New("database predecessor presence unconfirmed")
	}
	ids, err := hostingVolumeLines(raw, 1, dockerObjectID)
	if err != nil || len(ids) == 1 && ids[0] != id {
		return false, errors.New("database predecessor presence mismatch")
	}
	return len(ids) == 1, nil
}

func (e *imageEngine) databaseReplacementReady(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, candidate string, values []DatabaseCredentialValue) error {
	container, err := e.inspectDatabaseContainer(ctx, candidate)
	if err != nil || !record.InitReady || !container.State.Running || !databaseContainerMatches(container, operation, plan, record, filepath.Join(c.secretRoot, databaseGeneration(operation, plan))) {
		return errors.New("database retirement replacement identity unavailable")
	}
	network, err := e.inspectDatabaseNetwork(ctx, plan.Database.NetworkName, record, databaseKnownContainers(plan, candidate))
	if err != nil {
		return err
	}
	address, err := databaseNetworkAddress(network, candidate)
	if err != nil {
		return err
	}
	if c.probe(ctx, address, plan, values, true, false, nil) != nil || e.databaseProcessOwnership(ctx, plan.Release.DaemonID, candidate) != nil || ctx.Err() != nil {
		return errors.New("database retirement replacement readiness unconfirmed")
	}
	return nil
}

func (e *imageEngine) retireDatabasePredecessor(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, candidate string, values []DatabaseCredentialValue, before func(context.Context) error) error {
	if plan.PreviousHealthy == nil {
		return nil
	}
	previous := plan.PreviousHealthy
	retirement, exists, err := c.readRetirement(operation, plan, record, candidate)
	if err != nil {
		return err
	}
	present, err := e.databaseContainerPresent(ctx, previous.ContainerID)
	if err != nil {
		return err
	}
	if !present && !exists {
		return errors.New("database predecessor missing without retirement history")
	}
	if present {
		if exists && retirement.Completed {
			return errors.New("completed database retirement predecessor unexpectedly present")
		}
		priorPlan := domain.HostingApplyPlan{Snapshot: previous.Snapshot, Release: previous.Release, Storage: &previous.Storage.Plan, Database: plan.Database, Hash: previous.PlanHash}
		matchesStopped := func() bool {
			container, err := e.inspectDatabaseContainer(ctx, previous.ContainerID)
			return err == nil && !container.State.Running && databaseContainerMatches(container, previous.OperationID, priorPlan, record, filepath.Join(c.secretRoot, databaseGeneration(previous.OperationID, priorPlan)))
		}
		if !matchesStopped() {
			return errors.New("database retirement requires exact stopped predecessor")
		}
		if e.databaseReplacementReady(ctx, operation, plan, record, c, candidate, values) != nil {
			return errors.New("database retirement replacement readiness changed")
		}
		if !exists {
			retirement = databaseExpectedRetirement(operation, plan, record, candidate)
			if err = c.writeRetirement(ctx, operation, plan, retirement, before); err != nil {
				return err
			}
		}
		// Current authority and ownership are rechecked after durable intent. A
		// racing restart makes plain rm fail; force/volume removal are forbidden.
		if databaseAuthorize(ctx, before) != nil || !matchesStopped() {
			return errors.New("database predecessor retirement authority or ownership changed")
		}
		if e.databaseWriters(ctx, operation, plan, record, c, candidate, candidate, false) != nil {
			return errors.New("database retirement has unknown or competing volume users")
		}
		replacement, err := e.inspectDatabaseContainer(ctx, candidate)
		if err != nil || !replacement.State.Running || !databaseContainerMatches(replacement, operation, plan, record, filepath.Join(c.secretRoot, databaseGeneration(operation, plan))) {
			return errors.New("database retirement replacement stopped or changed")
		}
		if _, err = e.mutate(ctx, plan.Release.DaemonID, "container", "rm", previous.ContainerID); err != nil {
			return errors.New("database predecessor retirement outcome requires inspection")
		}
		present, err = e.databaseContainerPresent(ctx, previous.ContainerID)
		if err != nil || present {
			return errors.New("database predecessor retirement absence unconfirmed")
		}
	}
	if e.verifyDaemon(ctx, plan.Release.DaemonID) != nil || ctx.Err() != nil {
		return errors.New("database retirement daemon continuity unconfirmed")
	}
	if !retirement.Completed {
		retirement.Completed = true
		if err = c.writeRetirement(ctx, operation, plan, retirement, before); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (e *imageEngine) databaseWriters(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, candidate, running string, allowPrevious bool) error {
	raw, err := e.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+plan.Storage.VolumeName)
	if err != nil {
		return err
	}
	ids, err := hostingVolumeLines(raw, 2, dockerObjectID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		expectedPlan, expectedOp := plan, operation
		if id != candidate {
			if plan.PreviousHealthy == nil || id != plan.PreviousHealthy.ContainerID {
				return errors.New("database volume has an unknown writer")
			}
			old := plan.PreviousHealthy
			expectedOp = old.OperationID
			expectedPlan = domain.HostingApplyPlan{Snapshot: old.Snapshot, Release: old.Release, Storage: &old.Storage.Plan, Database: plan.Database, Hash: old.PlanHash}
		}
		container, err := e.inspectDatabaseContainer(ctx, id)
		if err != nil || !databaseContainerMatches(container, expectedOp, expectedPlan, record, filepath.Join(c.secretRoot, databaseGeneration(expectedOp, expectedPlan))) {
			return errors.New("database volume user provenance changed")
		}
		previousAllowed := allowPrevious && plan.PreviousHealthy != nil && id == plan.PreviousHealthy.ContainerID
		if container.State.Running && id != running && !previousAllowed {
			return errors.New("database volume has a competing writer")
		}
	}
	return nil
}

func (c DatabaseEngineConfig) probe(ctx context.Context, address string, plan domain.HostingApplyPlan, values []DatabaseCredentialValue, readOnly, initialize bool, before func(context.Context) error) error {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if c.readiness != nil {
		return c.readiness(probe, address, plan, values, readOnly, initialize, before)
	}
	return databaseReady(probe, address, plan, values, readOnly, initialize, before)
}

// ObserveDatabase validates an already-durable writer without initialization,
// repairs, starts, stops, network creation, or writable readiness commands.
func (e *imageEngine) ObserveDatabase(parent context.Context, operation domain.HostingOperation, credentials *DatabaseCredentials, c DatabaseEngineConfig) (domain.HostingObservation, error) {
	var observation domain.HostingObservation
	if operation.Plan == nil || operation.Result == nil || operation.Result.ContainerID == "" || domain.ValidateHostingDatabasePlan(*operation.Plan) != nil || operation.Result.PlanHash != operation.Plan.Hash || operation.Result.DaemonID != operation.Plan.Release.DaemonID || operation.Result.ImageID != operation.Plan.Release.ImageID {
		return observation, errors.New("database observation requires exact durable result")
	}
	plan := *operation.Plan
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if c.validateHost(ctx, e) != nil {
		return observation, errors.New("database observation host profile unavailable")
	}
	err := credentials.use(ctx, plan, func(ctx context.Context, values []DatabaseCredentialValue) error {
		lock, err := c.lock(ctx, plan.Snapshot.BindingID)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		record, exists, err := c.readRecord(plan.Database.NetworkName + ".json")
		if err != nil || !exists || record.Binding != plan.Snapshot.BindingID || record.Destination != plan.Snapshot.DestinationID || record.Storage != plan.Snapshot.Storage || record.VolumeNonce != plan.Storage.CreationNonce || record.OriginOperation != plan.Storage.CreationOperationID || operation.ID == record.OriginOperation && record.OriginPlan != plan.Hash || plan.PreviousHealthy != nil && record.OriginPlan != plan.PreviousHealthy.Storage.CreationPlanHash || record.Configuration != plan.Database.Reference.ConfigurationHash || record.Daemon != plan.Release.DaemonID {
			return errors.New("database observation origin unavailable")
		}
		container, err := e.inspectDatabaseContainer(ctx, operation.Result.ContainerID)
		if err != nil || !container.State.Running || !databaseContainerMatches(container, operation.ID, plan, record, filepath.Join(c.secretRoot, databaseGeneration(operation.ID, plan))) {
			return errors.New("database observed container provenance mismatch")
		}
		healthy := domain.HostingHealthy{Snapshot: plan.Snapshot, Release: plan.Release, PlanHash: plan.Hash, ContainerID: container.ID, OperationID: operation.ID, Storage: &domain.HostingHealthyStorage{Plan: *plan.Storage, CreationPlanHash: record.OriginPlan}}
		if e.verifyHealthyStorageVolume(ctx, healthy) != nil || e.databaseWriters(ctx, operation.ID, plan, record, c, container.ID, container.ID, false) != nil {
			return errors.New("database observed retained volume or writer changed")
		}
		raw, err := e.ownedVolumeDocker(ctx, 64<<10, "volume", "inspect", plan.Storage.VolumeName, "--format", "{{json .}}")
		var volume hostingInspectedVolume
		if err != nil || json.Unmarshal(raw, &volume) != nil || volume.CreatedAt != record.VolumeCreatedAt {
			return errors.New("database observed volume evidence changed")
		}
		network, err := e.inspectDatabaseNetwork(ctx, plan.Database.NetworkName, record, databaseKnownContainers(plan, container.ID))
		if err != nil {
			return err
		}
		address, err := databaseNetworkAddress(network, container.ID)
		if err != nil {
			return err
		}
		if c.probe(ctx, address, plan, values, true, false, nil) != nil || e.verifyLocalRelease(ctx, plan.Release) != nil || ctx.Err() != nil {
			return errDatabaseReadiness
		}
		if e.databaseProcessOwnership(ctx, plan.Release.DaemonID, container.ID) != nil {
			return errors.New("database observed runtime identity not confirmed")
		}
		if !record.InitReady {
			return errDatabaseInitializationUnresolved
		}
		if plan.PreviousHealthy != nil {
			retirement, exists, err := c.readRetirement(operation.ID, plan, record, container.ID)
			if err != nil {
				return errDatabaseRetirementUnresolved
			}
			present, err := e.databaseContainerPresent(ctx, plan.PreviousHealthy.ContainerID)
			if err != nil || present || !exists || !retirement.Completed || e.verifyDaemon(ctx, plan.Release.DaemonID) != nil || ctx.Err() != nil {
				return errDatabaseRetirementUnresolved
			}
		}
		observation = domain.HostingObservation{DaemonID: plan.Release.DaemonID, ContainerID: container.ID, ImageID: plan.Release.ImageID, BindingID: plan.Snapshot.BindingID, PlanHash: plan.Hash, ReleaseHash: plan.Release.Hash, Ready: true}
		return nil
	})
	if err != nil {
		if errors.Is(err, errDatabaseInitializationUnresolved) {
			return domain.HostingObservation{}, errDatabaseInitializationUnresolved
		}
		if errors.Is(err, errDatabaseRetirementUnresolved) {
			return domain.HostingObservation{}, errDatabaseRetirementUnresolved
		}
		return domain.HostingObservation{}, errors.New("database read-only observation not confirmed")
	}
	return observation, nil
}

// Keep arbitrary driver/output diagnostics out of ordinary values/errors.
var _ fmt.Formatter = DatabaseEngineConfig{}

func databaseHostCapabilities(status []byte) bool {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false
		}
		caps, err := strconv.ParseUint(fields[1], 16, 64)
		return err == nil && caps&3 == 3 // Linux CAP_CHOWN and CAP_DAC_OVERRIDE.
	}
	return false
}

func (e *imageEngine) databaseProcessOwnership(parent context.Context, daemon, id string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if !dockerObjectID.MatchString(id) || e.verifyDaemon(ctx, daemon) != nil {
		return errors.New("database process identity unavailable")
	}
	raw, err := e.ownedVolumeDocker(ctx, 64, "container", "exec", "--user", "0:0", id, "stat", "-c", "%u:%g", "/proc/1")
	if err != nil || strings.TrimSpace(string(raw)) != "999:999" || e.verifyDaemon(ctx, daemon) != nil || ctx.Err() != nil {
		return errors.New("database process ownership mismatch")
	}
	return nil
}
