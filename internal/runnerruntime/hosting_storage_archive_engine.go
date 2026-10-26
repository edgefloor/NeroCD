//go:build unix

package runnerruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/storagearchive"
)

var errStorageArchiveEngine = errors.New("storage archive execution remains held; authorized recovery is required")

const archiveLabel = "io.nerocd.archive."

type archiveCAS interface {
	Put(context.Context, io.Reader, string, int64) (artifactfs.Blob, error)
	Open(context.Context, string) (io.ReadSeekCloser, artifactfs.Blob, error)
	Close() error
}

// A single coordinator owns this synchronous engine. Close follows completion
// of all its calls. No daemon helper is inferred, adopted or removed by Close.
type storageArchiveEngine struct {
	lastBackupProof *domain.BackupLocalCopyProof
	image           *imageEngine
	config          StorageArchiveExecutionConfig
	host            DatabaseEngineConfig
	store           archiveCAS
	temporary       *os.Root
	dockerRoot      string
	prepared        domain.HostingStorageArchivePlan
	stream          func(context.Context, []string, io.Reader, io.Writer) error
}

type archiveDockerCommand struct{ binary string }

func (c archiveDockerCommand) RunEnvironment(ctx context.Context, _ string, args []string, dir string, env []string) ([]byte, error) {
	return (osProvenanceCommand{}).RunEnvironment(ctx, c.binary, args, dir, env)
}

func openStorageArchiveEngine(ctx context.Context, config StorageArchiveExecutionConfig, endpoint string) (storageArchiveExecutionEngine, error) {
	if ctx.Err() != nil || config.validate() != nil || runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return nil, errStorageArchiveEngine
	}
	host, err := NewDatabaseEngineConfig(config.ArtifactRoot, config.TemporaryRoot)
	if err != nil {
		return nil, errStorageArchiveEngine
	}
	image, err := newImageEngine(endpoint)
	if err != nil {
		return nil, errStorageArchiveEngine
	}
	image.command = archiveDockerCommand{binary: config.DockerBinary}
	e := &storageArchiveEngine{image: image, config: config, host: host}
	if host.validateHost(ctx, image) != nil {
		return nil, errors.Join(errStorageArchiveEngine, image.Close())
	}
	e.store, err = artifactfs.OpenStore(config.ArtifactRoot, domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		return nil, errors.Join(errStorageArchiveEngine, image.Close())
	}
	e.temporary, err = os.OpenRoot(config.TemporaryRoot)
	if err != nil {
		return nil, errors.Join(errStorageArchiveEngine, e.store.Close(), image.Close())
	}
	e.stream = func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
		all := append([]string{"--host", image.endpoint, "--config", image.configDir}, args...)
		return archiveOSStream(ctx, config.DockerBinary, all, image.configDir, in, out)
	}
	return e, nil
}

func (e *storageArchiveEngine) Close() error {
	if errors.Join(e.temporary.Close(), e.store.Close(), e.image.Close()) != nil {
		return errStorageArchiveEngine
	}
	return nil
}

func archiveOSStream(ctx context.Context, binary string, args []string, dir string, in io.Reader, out io.Writer) error {
	if ctx.Err() != nil {
		return errStorageArchiveEngine
	}
	command := exec.CommandContext(ctx, binary, args...)
	configureProvenanceProcess(command)
	command.WaitDelay = 2 * time.Second
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "LANG=C", "LC_ALL=C", "DOCKER_CONFIG=" + dir, "DOCKER_CONTEXT=", "DOCKER_CLI_HINTS=false"}
	command.Stdin, command.Stdout, command.Stderr = in, out, io.Discard
	if err := command.Run(); err != nil || ctx.Err() != nil {
		return errors.Join(errStorageArchiveEngine, ctx.Err())
	}
	return nil
}

func archiveImageLabels(h domain.HostingStorageArchiveHelper) map[string]string {
	return map[string]string{"io.nerocd.storage-archive.recipe": "scratch-v1", "io.nerocd.storage-archive.source-commit": h.SourceCommit, "io.nerocd.storage-archive.executable-sha256": strings.TrimPrefix(h.ExecutableSHA256, "sha256:")}
}

// Docker may inject this single default PATH into the fixed scratch image.
// CreateHelper overrides it; container inspection still requires PATH=/ HOME=/.
func archiveImageEnvironment(env []string) bool {
	return len(env) == 0 || len(env) == 1 && env[0] == "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

func (e *storageArchiveEngine) inspectProfile(ctx context.Context, p domain.HostingStorageArchivePlan) error {
	if ctx.Err() != nil || p.Validate() != nil || p.Helper != e.config.Helper || e.host.validateHost(ctx, e.image) != nil || e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil {
		return errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 32<<10, "image", "inspect", p.Helper.ImageID, "--format", "{{json .}}")
	var image struct {
		ID, OS, Architecture, Variant string
		Config                        struct {
			Entrypoint, Cmd, Env []string
			Labels               map[string]string
			Volumes              map[string]json.RawMessage
			User, WorkingDir     string
		}
	}
	if err != nil || json.Unmarshal(raw, &image) != nil || image.ID != p.Helper.ImageID || image.OS != "linux" || image.Architecture != "arm64" || image.Variant != "" || !slices.Equal(image.Config.Entrypoint, []string{"/nerocd-storage-archive"}) || len(image.Config.Cmd) != 0 || !archiveImageEnvironment(image.Config.Env) || len(image.Config.Volumes) != 0 || image.Config.User != "" || image.Config.WorkingDir != "/" || !maps.Equal(image.Config.Labels, archiveImageLabels(p.Helper)) {
		return errStorageArchiveEngine
	}
	return nil
}

func (e *storageArchiveEngine) Prepare(ctx context.Context, p domain.HostingStorageArchivePlan) error {
	e.prepared = domain.HostingStorageArchivePlan{}
	if e.inspectProfile(ctx, p) != nil {
		return errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 32<<10, "info", "--format", "{{json .}}")
	var daemon struct{ DockerRootDir string }
	if err != nil || json.Unmarshal(raw, &daemon) != nil || !filepath.IsAbs(daemon.DockerRootDir) || filepath.Clean(daemon.DockerRootDir) != daemon.DockerRootDir {
		return errStorageArchiveEngine
	}
	e.dockerRoot = daemon.DockerRootDir
	if _, err := e.volume(ctx, archiveSourceRequest(p)); err != nil {
		return errStorageArchiveEngine
	}
	// Own the immutable copy used by the volume-only seam. Later input slices
	// or pointers cannot change this operation's target admission.
	raw, err = json.Marshal(p)
	if err != nil || json.Unmarshal(raw, &e.prepared) != nil {
		return errStorageArchiveEngine
	}
	return nil
}

func (e *storageArchiveEngine) check(ctx context.Context, p domain.HostingStorageArchivePlan, cleanup bool) error {
	if e.prepared.Hash == "" || e.inspectProfile(ctx, p) != nil {
		return errStorageArchiveEngine
	}
	if p.Hash == e.prepared.Hash {
		return nil
	}
	if cleanup && e.prepared.Mode == "recover" && e.prepared.RecoveryEvidence != nil && p.Hash == e.prepared.RecoveryEvidence.OriginalPlanHash && p.OperationID == e.prepared.RecoveryOfOperationID && p.Helper == e.prepared.Helper && p.SourceStorage.Hash == e.prepared.SourceStorage.Hash {
		return nil
	}
	return errStorageArchiveEngine
}

func archiveSourceRequest(p domain.HostingStorageArchivePlan) hostingOwnedVolumeRequest {
	m := p.SourceMaterialization
	return hostingOwnedVolumeRequest{Storage: p.SourceStorage, DaemonID: m.DaemonID, OriginOperationID: m.CreationOperationID, OriginPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce}
}

func archiveTargetRequest(p domain.HostingStorageArchivePlan) hostingOwnedVolumeRequest {
	return hostingOwnedVolumeRequest{Storage: p.Target.Storage, DaemonID: p.Target.Plan.DaemonID, OriginOperationID: p.Target.Plan.CreationOperationID, OriginPlanHash: p.Hash, CreationNonce: p.Target.Plan.CreationNonce}
}

func (e *storageArchiveEngine) volume(ctx context.Context, req hostingOwnedVolumeRequest) (string, error) {
	if req.validate() != nil || e.image.verifyDaemon(ctx, req.DaemonID) != nil {
		return "", errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 64<<10, "volume", "inspect", req.Storage.VolumeName, "--format", "{{json .}}")
	var volume struct {
		hostingInspectedVolume
		Mountpoint string
	}
	if err != nil || json.Unmarshal(raw, &volume) != nil || volume.Name != req.Storage.VolumeName || volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 || !maps.Equal(volume.Labels, req.labels()) {
		return "", errStorageArchiveEngine
	}
	created, err := time.Parse(time.RFC3339Nano, volume.CreatedAt)
	expected := filepath.Join(e.dockerRoot, "volumes", req.Storage.VolumeName, "_data")
	canonical, pathErr := filepath.EvalSymlinks(volume.Mountpoint)
	if err != nil || created.IsZero() || volume.Mountpoint != expected || pathErr != nil || canonical != expected {
		return "", errStorageArchiveEngine
	}
	return expected, nil
}

func (e *storageArchiveEngine) users(ctx context.Context, volume string, expected []string) error {
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume)
	ids, parseErr := hostingVolumeLines(raw, 128, dockerObjectID)
	sort.Strings(ids)
	expected = slices.Clone(expected)
	sort.Strings(expected)
	if err != nil || parseErr != nil || !slices.Equal(ids, expected) {
		return errStorageArchiveEngine
	}
	return nil
}

func (e *storageArchiveEngine) writers(ctx context.Context, p domain.HostingStorageArchivePlan, helperID string) ([]domain.HostingStorageArchiveWriterState, error) {
	if p.Target != nil {
		return nil, errStorageArchiveEngine
	}
	if _, err := e.volume(ctx, archiveSourceRequest(p)); err != nil {
		return nil, errStorageArchiveEngine
	}
	ids := make([]string, 0, len(p.Writers)+1)
	states := make([]domain.HostingStorageArchiveWriterState, 0, len(p.Writers))
	for _, expected := range p.Writers {
		container, err := e.image.inspectContainer(ctx, expected.ContainerID)
		if err != nil || !storageContainerMatches(container, expected) {
			return nil, errStorageArchiveEngine
		}
		ids = append(ids, expected.ContainerID)
		states = append(states, domain.HostingStorageArchiveWriterState{ContainerID: expected.ContainerID, OperationID: expected.OperationID, ImageID: expected.Release.ImageID, ReleaseHash: expected.Release.Hash, PlanHash: expected.PlanHash, Running: container.State.Running})
	}
	if helperID != "" {
		ids = append(ids, helperID)
	}
	if e.users(ctx, p.SourceStorage.VolumeName, ids) != nil {
		return nil, errStorageArchiveEngine
	}
	return states, nil
}

func (e *storageArchiveEngine) InspectWriters(ctx context.Context, p domain.HostingStorageArchivePlan) ([]domain.HostingStorageArchiveWriterState, error) {
	if e.check(ctx, p, false) != nil || p.Target != nil || p.Mode != "backup" && p.Mode != "recover" {
		return nil, errStorageArchiveEngine
	}
	// Recovery observes the same exact source inventory. It does not widen
	// writerAction's separate running-only, narrowed resume authorization.
	return e.writers(ctx, p, "")
}

func sameArchiveWriter(a, b domain.HostingStorageArchiveWriterState) bool {
	a.Running, b.Running = false, false
	return a == b
}

func (e *storageArchiveEngine) writerAction(ctx context.Context, p domain.HostingStorageArchivePlan, state domain.HostingStorageArchiveWriterState, resume bool, before func(context.Context) error) error {
	recoveryResume := resume && p.Mode == "recover" && p.RecoveryEvidence != nil && slices.Contains(p.RecoveryEvidence.ResumeRequiredContainerIDs, state.ContainerID)
	if before == nil || !state.Running || e.check(ctx, p, false) != nil || p.Target != nil || p.Mode != "backup" && !recoveryResume {
		return errStorageArchiveEngine
	}
	states, err := e.writers(ctx, p, "")
	if err != nil {
		return errStorageArchiveEngine
	}
	for _, current := range states {
		if !sameArchiveWriter(current, state) {
			continue
		}
		if current.Running == resume {
			return nil
		}
		args := []string{"container", "start", current.ContainerID}
		if !resume {
			args = []string{"container", "stop", "--time", "10", current.ContainerID}
		}
		if e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil || databaseAuthorize(ctx, before) != nil {
			return errStorageArchiveEngine
		}
		if _, err := e.image.docker(ctx, args...); err != nil {
			return errStorageArchiveEngine
		}
		got, err := e.image.inspectContainer(ctx, current.ContainerID)
		for _, expected := range p.Writers {
			if expected.ContainerID == current.ContainerID && err == nil && storageContainerMatches(got, expected) && got.State.Running == resume {
				return ctx.Err()
			}
		}
		return errStorageArchiveEngine
	}
	return errStorageArchiveEngine
}

func (e *storageArchiveEngine) StopWriter(ctx context.Context, p domain.HostingStorageArchivePlan, state domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	return e.writerAction(ctx, p, state, false, before)
}

func (e *storageArchiveEngine) ResumeWriter(ctx context.Context, p domain.HostingStorageArchivePlan, state domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	return e.writerAction(ctx, p, state, true, before)
}

func (e *storageArchiveEngine) emptyTarget(ctx context.Context, p domain.HostingStorageArchivePlan, helperID string) (err error) {
	target, err := e.volume(ctx, archiveTargetRequest(p))
	if err != nil {
		return errStorageArchiveEngine
	}
	source, err := e.volume(ctx, archiveSourceRequest(p))
	if err != nil {
		return errStorageArchiveEngine
	}
	sourceInfo, err := os.Stat(source)
	targetInfo, targetErr := os.Stat(target)
	if err != nil || targetErr != nil || !targetInfo.IsDir() || os.SameFile(sourceInfo, targetInfo) || targetInfo.Mode().Perm()&0o022 != 0 || targetInfo.Mode().Perm()&0o700 != 0o700 {
		return errStorageArchiveEngine
	}
	owner, ok := targetInfo.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return errStorageArchiveEngine
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return errStorageArchiveEngine
	}
	defer func() {
		if root.Close() != nil {
			err = errStorageArchiveEngine
		}
	}()
	dir, err := root.Open(".")
	if err != nil {
		return errStorageArchiveEngine
	}
	defer func() {
		if dir.Close() != nil {
			err = errStorageArchiveEngine
		}
	}()
	entries, err := dir.ReadDir(1)
	if len(entries) != 0 || err != nil && !errors.Is(err, io.EOF) {
		return errStorageArchiveEngine
	}
	var users []string
	if helperID != "" {
		users = []string{helperID}
	}
	return e.users(ctx, p.Target.Storage.VolumeName, users)
}

func (e *storageArchiveEngine) EnsureRestoreVolume(ctx context.Context, req hostingOwnedVolumeRequest, before func(context.Context) error) (domain.HostingStorageMaterialization, error) {
	var zero domain.HostingStorageMaterialization
	p := e.prepared
	if before == nil || p.Mode != "restore" || p.Target == nil || e.check(ctx, p, false) != nil || req.validate() != nil || req.Storage.Hash != p.Target.Storage.Hash || req.Storage.Reference() != p.Target.Plan.Reference || req.DaemonID != p.Helper.DaemonID || req.OriginOperationID != p.OperationID || req.OriginPlanHash != p.Hash || req.CreationNonce != p.Target.Plan.CreationNonce || len(req.KnownUsers) != 0 {
		return zero, errStorageArchiveEngine
	}
	evidence, err := e.image.ensureOwnedVolumeWithAuthority(ctx, req, before)
	if err != nil || e.emptyTarget(ctx, p, "") != nil || databaseAuthorize(ctx, before) != nil {
		return zero, errStorageArchiveEngine
	}
	return domain.HostingStorageMaterialization{StorageRef: evidence.Storage, CreationOperationID: evidence.OriginOperationID, CreationPlanHash: evidence.OriginPlanHash, CreationNonce: evidence.CreationNonce, DaemonID: evidence.DaemonID, Labels: evidence.Labels}, nil
}

func archiveNonce(nonce string) bool {
	return nonce != "" && len(nonce) <= 128 && utf8.ValidString(nonce) && strings.TrimSpace(nonce) == nonce && !strings.ContainsFunc(nonce, unicode.IsControl)
}

func archiveHelperName(p domain.HostingStorageArchivePlan, nonce string) string {
	return "ncd_archive_" + domain.HostingDocumentHash(struct{ Operation, Plan, Nonce string }{p.OperationID, p.Hash, nonce})[7:39]
}

func archiveHelperLabels(p domain.HostingStorageArchivePlan, nonce string) map[string]string {
	labels := archiveImageLabels(p.Helper)
	for k, v := range map[string]string{"operation": p.OperationID, "plan": p.Hash, "nonce": nonce, "daemon": p.Helper.DaemonID, "helper": p.Helper.Hash, "mode": p.Mode, "source": p.SourceStorage.Hash} {
		labels[archiveLabel+k] = v
	}
	if p.Target != nil {
		labels[archiveLabel+"target"] = p.Target.Storage.Hash
	}
	return labels
}

func archiveHelperCommand(p domain.HostingStorageArchivePlan) []string {
	mode := "create"
	if p.Mode == "restore" {
		mode = "restore"
	}
	l := p.Limits
	return []string{mode, "--max-archive-bytes=" + strconv.FormatInt(l.ArchiveBytes, 10), "--max-data-bytes=" + strconv.FormatInt(l.DataBytes, 10), "--max-file-bytes=" + strconv.FormatInt(l.FileBytes, 10), "--max-members=" + strconv.Itoa(l.Members), "--max-depth=" + strconv.Itoa(l.Depth), "--max-path-bytes=" + strconv.Itoa(l.PathBytes)}
}

func archiveHelperCaps(p domain.HostingStorageArchivePlan) []string {
	if p.Mode == "restore" {
		return []string{"CHOWN", "FOWNER"}
	}
	return []string{"DAC_READ_SEARCH"}
}

func archiveTmpfs(p domain.HostingStorageArchivePlan) string {
	return "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(p.Limits.ArchiveBytes+(32<<20), 10) + ",mode=1777"
}

type archiveHelperContainer struct {
	ID, Name, Image string
	Config          struct {
		Labels               map[string]string
		Cmd, Entrypoint, Env []string
		User, Hostname       string
		Tty                  bool
	}
	State struct {
		Running, Dead, OOMKilled bool
		Status, Error            string
		ExitCode                 int
	}
	HostConfig struct {
		ReadonlyRootfs, Privileged, PublishAllPorts, AutoRemove bool
		NetworkMode, PidMode, IpcMode, UsernsMode, Runtime      string
		UtsMode, CgroupnsMode                                   string
		GroupAdd                                                []string
		DeviceRequests                                          []json.RawMessage
		Binds, VolumesFrom, CapAdd, CapDrop, SecurityOpt        []string
		PortBindings                                            map[string]json.RawMessage
		Devices                                                 []json.RawMessage
		Tmpfs                                                   map[string]string
		Memory, MemorySwap, NanoCpus                            int64
		PidsLimit                                               int64
		LogConfig                                               struct{ Type string }
		RestartPolicy                                           struct{ Name string }
	}
	Mounts []struct {
		Type, Name, Driver, Source, Destination string
		RW                                      bool
	}
}

// Unique environment keys have no ordering authority. Keep the permitted
// values and cardinality exact; duplicate or additional entries remain invalid.
func archiveHelperEnvironmentMatches(environment []string) bool {
	return len(environment) == 2 && slices.Contains(environment, "PATH=/") && slices.Contains(environment, "HOME=/")
}

func (e *storageArchiveEngine) helper(ctx context.Context, p domain.HostingStorageArchivePlan, id, nonce string) (archiveHelperContainer, error) {
	var h archiveHelperContainer
	if !dockerObjectID.MatchString(id) || !archiveNonce(nonce) || p.Mode != "backup" && p.Mode != "restore" {
		return h, errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 128<<10, "container", "inspect", id, "--format", "{{json .}}")
	if err != nil || json.Unmarshal(raw, &h) != nil {
		return h, errStorageArchiveEngine
	}
	c := h.HostConfig
	memory := p.Limits.ArchiveBytes + (128 << 20)
	caps := slices.Clone(c.CapAdd)
	// Docker may return a single CAP_ prefix. The exact mode-specific set
	// below still rejects unknown names, repeated prefixes and duplicates.
	for i, capability := range caps {
		caps[i] = strings.TrimPrefix(capability, "CAP_")
	}
	sort.Strings(caps)
	expectedCaps := archiveHelperCaps(p)
	sort.Strings(expectedCaps)
	if h.ID != id || h.Name != "/"+archiveHelperName(p, nonce) || h.Image != p.Helper.ImageID || !maps.Equal(h.Config.Labels, archiveHelperLabels(p, nonce)) || !slices.Equal(h.Config.Cmd, archiveHelperCommand(p)) || !slices.Equal(h.Config.Entrypoint, []string{"/nerocd-storage-archive"}) || !archiveHelperEnvironmentMatches(h.Config.Env) || h.Config.User != "0:0" || h.Config.Hostname != "nerocd-archive" || h.Config.Tty || !c.ReadonlyRootfs || c.Privileged || c.PublishAllPorts || c.AutoRemove || c.NetworkMode != "none" || c.PidMode != "" || c.IpcMode != "private" || c.UsernsMode != "" || c.UtsMode != "" || c.CgroupnsMode != "private" || c.Runtime != "" && c.Runtime != "runc" || len(c.Binds) != 0 || len(c.VolumesFrom) != 0 || len(c.PortBindings) != 0 || len(c.Devices) != 0 || len(c.DeviceRequests) != 0 || len(c.GroupAdd) != 0 || !slices.Equal(c.CapDrop, []string{"ALL"}) || !slices.Equal(caps, expectedCaps) || !slices.Equal(c.SecurityOpt, []string{"no-new-privileges"}) || c.Memory != memory || c.MemorySwap != memory || c.NanoCpus != 1000000000 || c.PidsLimit != 64 || c.LogConfig.Type != "none" || c.RestartPolicy.Name != "no" || !maps.Equal(c.Tmpfs, map[string]string{"/tmp": archiveTmpfs(p)}) {
		return h, errStorageArchiveEngine
	}
	volume, destination, writable := p.SourceStorage.VolumeName, "/source", false
	if p.Target != nil {
		volume, destination, writable = p.Target.Storage.VolumeName, "/target", true
	}
	matches := 0
	for _, mount := range h.Mounts {
		if mount.Type == "tmpfs" && mount.Destination == "/tmp" && mount.RW {
			continue
		}
		if mount.Type != "volume" || mount.Name != volume || mount.Driver != "local" || mount.Destination != destination || mount.RW != writable || mount.Source != filepath.Join(e.dockerRoot, "volumes", volume, "_data") {
			return h, errStorageArchiveEngine
		}
		matches++
	}
	if matches != 1 || len(h.Mounts) > 2 {
		return h, errStorageArchiveEngine
	}
	return h, nil
}

func (e *storageArchiveEngine) CreateHelper(ctx context.Context, p domain.HostingStorageArchivePlan, nonce string, before func(context.Context) error) (string, error) {
	if before == nil || !archiveNonce(nonce) || p.Mode != "backup" && p.Mode != "restore" || e.check(ctx, p, false) != nil {
		return "", errStorageArchiveEngine
	}
	if p.Target == nil {
		states, err := e.writers(ctx, p, "")
		if err != nil || slices.ContainsFunc(states, func(w domain.HostingStorageArchiveWriterState) bool { return w.Running }) {
			return "", errStorageArchiveEngine
		}
	} else if e.emptyTarget(ctx, p, "") != nil {
		return "", errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+archiveHelperName(p, nonce)+"$")
	if err != nil || len(raw) != 0 {
		return "", errStorageArchiveEngine
	}
	memory := strconv.FormatInt(p.Limits.ArchiveBytes+(128<<20), 10)
	args := []string{"container", "create", "--pull=never", "--interactive", "--name", archiveHelperName(p, nonce), "--read-only", "--network", "none", "--ipc", "private", "--cgroupns", "private", "--user", "0:0", "--hostname", "nerocd-archive", "--env", "PATH=/", "--env", "HOME=/", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--log-driver", "none", "--restart", "no", "--pids-limit", "64", "--memory", memory, "--memory-swap", memory, "--cpus", "1", "--tmpfs", "/tmp:" + archiveTmpfs(p)}
	for _, cap := range archiveHelperCaps(p) {
		args = append(args, "--cap-add", cap)
	}
	labels := archiveHelperLabels(p, nonce)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	mount := "type=volume,src=" + p.SourceStorage.VolumeName + ",dst=/source,readonly,volume-nocopy"
	if p.Target != nil {
		mount = "type=volume,src=" + p.Target.Storage.VolumeName + ",dst=/target,volume-nocopy"
	}
	args = append(args, "--mount", mount, p.Helper.ImageID)
	args = append(args, archiveHelperCommand(p)...)
	if e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil || databaseAuthorize(ctx, before) != nil {
		return "", errStorageArchiveEngine
	}
	raw, err = e.image.docker(ctx, args...)
	id := strings.TrimSuffix(string(raw), "\n")
	if !dockerObjectID.MatchString(id) {
		return "", errStorageArchiveEngine
	}
	if err != nil {
		return id, errStorageArchiveEngine
	}
	h, err := e.helper(ctx, p, id, nonce)
	if err != nil || h.State.Running || h.State.Status != "created" {
		return id, errStorageArchiveEngine
	}
	return id, nil
}

func archiveCodecLimits(p domain.HostingStorageArchivePlan) storagearchive.Limits {
	l := p.Limits
	return storagearchive.Limits{ArchiveBytes: l.ArchiveBytes, DataBytes: l.DataBytes, FileBytes: l.FileBytes, Members: l.Members, Depth: l.Depth, PathBytes: l.PathBytes}
}

type archiveLimitedWriter struct {
	out    io.Writer
	left   int64
	cancel context.CancelFunc
}

func (w *archiveLimitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		w.cancel()
		return 0, storagearchive.ErrLimit
	}
	n, err := w.out.Write(p)
	w.left -= int64(n)
	if err != nil || n != len(p) {
		w.cancel()
		return n, errStorageArchiveEngine
	}
	return n, nil
}

func (e *storageArchiveEngine) runHelper(ctx context.Context, p domain.HostingStorageArchivePlan, id, nonce string, in io.Reader, out io.Writer, limit int64, before func(context.Context) error) error {
	h, err := e.helper(ctx, p, id, nonce)
	if err != nil || h.State.Running || h.State.Status != "created" || e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil || databaseAuthorize(ctx, before) != nil {
		return errStorageArchiveEngine
	}
	work, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	bounded := &archiveLimitedWriter{out: out, left: limit, cancel: cancel}
	if e.stream(work, []string{"container", "start", "--attach", "--interactive", id}, in, bounded) != nil || work.Err() != nil {
		return errors.Join(errStorageArchiveEngine, work.Err())
	}
	h, err = e.helper(ctx, p, id, nonce)
	if err != nil || h.State.Running || h.State.Status != "exited" || h.State.ExitCode != 0 || h.State.OOMKilled || h.State.Dead || h.State.Error != "" || e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil {
		return errStorageArchiveEngine
	}
	return nil
}

func (e *storageArchiveEngine) stage() (string, *os.File, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, errStorageArchiveEngine
	}
	name := ".archive-" + hex.EncodeToString(random[:])
	file, err := e.temporary.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return "", nil, errStorageArchiveEngine
	}
	if file.Chmod(0o600) != nil {
		return "", nil, errors.Join(errStorageArchiveEngine, file.Close(), e.temporary.Remove(name))
	}
	return name, file, nil
}

func (e *storageArchiveEngine) Backup(ctx context.Context, p domain.HostingStorageArchivePlan, id, nonce string, before func(context.Context) error) (blob domain.HostingStorageArchiveBlob, err error) {
	if before == nil || p.Mode != "backup" || e.check(ctx, p, false) != nil {
		return blob, errStorageArchiveEngine
	}
	states, err := e.writers(ctx, p, id)
	if err != nil || slices.ContainsFunc(states, func(w domain.HostingStorageArchiveWriterState) bool { return w.Running }) {
		return blob, errStorageArchiveEngine
	}
	name, file, err := e.stage()
	if err != nil {
		return blob, errStorageArchiveEngine
	}
	defer func() {
		if errors.Join(file.Close(), e.temporary.Remove(name)) != nil {
			blob, err = domain.HostingStorageArchiveBlob{}, errStorageArchiveEngine
		}
	}()
	if e.runHelper(ctx, p, id, nonce, nil, file, p.Limits.ArchiveBytes, before) != nil {
		return blob, errStorageArchiveEngine
	}
	summary, err := storagearchive.Validate(ctx, file, archiveCodecLimits(p))
	if err != nil || file.Sync() != nil {
		return blob, errStorageArchiveEngine
	}
	states, err = e.writers(ctx, p, id)
	if err != nil || slices.ContainsFunc(states, func(w domain.HostingStorageArchiveWriterState) bool { return w.Running }) || databaseAuthorize(ctx, before) != nil {
		return blob, errStorageArchiveEngine
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return blob, errStorageArchiveEngine
	}
	e.lastBackupProof = nil
	if owned, ok := e.store.(interface {
		PutOwned(context.Context, io.Reader, string, int64) (artifactfs.Blob, artifactfs.Ownership, error)
	}); ok {
		_, proof, err := owned.PutOwned(ctx, file, strings.TrimPrefix(summary.Digest, "sha256:"), summary.ArchiveBytes)
		if err != nil {
			return blob, errStorageArchiveEngine
		}
		e.lastBackupProof = &domain.BackupLocalCopyProof{PoolID: proof.PoolID, Device: proof.Device, Inode: proof.Inode, Bytes: proof.Bytes, Created: proof.Created}
	} else if _, err := e.store.Put(ctx, file, strings.TrimPrefix(summary.Digest, "sha256:"), summary.ArchiveBytes); err != nil {
		return blob, errStorageArchiveEngine
	}
	return domain.HostingStorageArchiveBlob{BackupID: p.BackupID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: summary.Digest, SizeBytes: summary.ArchiveBytes, Format: summary.Format}, nil
}

func (e *storageArchiveEngine) Restore(ctx context.Context, p domain.HostingStorageArchivePlan, id, nonce string, before func(context.Context) error) (err error) {
	if before == nil || p.Mode != "restore" || p.Target == nil || p.Blob == nil || e.check(ctx, p, false) != nil {
		return errStorageArchiveEngine
	}
	handle, blob, err := e.store.Open(ctx, strings.TrimPrefix(p.Blob.SHA256, "sha256:"))
	if err != nil {
		return errStorageArchiveEngine
	}
	defer func() {
		if handle.Close() != nil {
			err = errStorageArchiveEngine
		}
	}()
	file, ok := handle.(*os.File)
	if !ok || blob.Size != p.Blob.SizeBytes || blob.SHA256 != strings.TrimPrefix(p.Blob.SHA256, "sha256:") {
		return errStorageArchiveEngine
	}
	summary, err := storagearchive.Validate(ctx, file, archiveCodecLimits(p))
	if err != nil || summary.Digest != p.Blob.SHA256 || summary.ArchiveBytes != p.Blob.SizeBytes || e.emptyTarget(ctx, p, id) != nil {
		return errStorageArchiveEngine
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errStorageArchiveEngine
	}
	var out strings.Builder
	if e.runHelper(ctx, p, id, nonce, file, &out, 4096, before) != nil {
		return errStorageArchiveEngine
	}
	var observed storagearchive.Summary
	if domain.DecodeHostingStorageProtocolJSON([]byte(out.String()), &observed) != nil || observed != summary {
		return errStorageArchiveEngine
	}
	return nil
}

func (e *storageArchiveEngine) CleanupHelper(ctx context.Context, p domain.HostingStorageArchivePlan, id, nonce string, before func(context.Context) error) error {
	// The coordinator supplies the exact durably saved ID. Absence is a
	// quiescence observation, never proof of historical ownership or a name lookup.
	if before == nil || !dockerObjectID.MatchString(id) || !archiveNonce(nonce) || e.check(ctx, p, true) != nil {
		return errStorageArchiveEngine
	}
	present, err := e.helperPresent(ctx, id)
	if err != nil {
		return errStorageArchiveEngine
	}
	if !present {
		if e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil || databaseAuthorize(ctx, before) != nil {
			return errStorageArchiveEngine
		}
		return nil
	}
	if _, err := e.helper(ctx, p, id, nonce); err != nil || e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil || databaseAuthorize(ctx, before) != nil {
		return errStorageArchiveEngine
	}
	if _, err := e.image.docker(ctx, "container", "rm", "--force", id); err != nil {
		return errStorageArchiveEngine
	}
	present, err = e.helperPresent(ctx, id)
	if err != nil || present {
		return errStorageArchiveEngine
	}
	return ctx.Err()
}

func (e *storageArchiveEngine) helperPresent(ctx context.Context, id string) (bool, error) {
	raw, err := e.image.ownedVolumeDocker(ctx, 1024, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	ids, parseErr := hostingVolumeLines(raw, 1, dockerObjectID)
	if err != nil || parseErr != nil || len(ids) == 1 && ids[0] != id {
		return false, errStorageArchiveEngine
	}
	return len(ids) == 1, nil
}

var _ storageArchiveExecutionEngine = (*storageArchiveEngine)(nil)

func (e *storageArchiveEngine) BackupCopyProof() *domain.BackupLocalCopyProof {
	if e.lastBackupProof == nil {
		return nil
	}
	p := *e.lastBackupProof
	return &p
}
