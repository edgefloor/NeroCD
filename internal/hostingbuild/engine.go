package hostingbuild

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type ownedResources struct{ DaemonID, Token, Builder, Planner, Volume, Group string }
type containerInfo struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string
		Env    []string
		Image  string
		Cmd    []string
	}
	State struct {
		PID     int `json:"Pid"`
		Running bool
	}
	Mounts []struct{ Type, Source, Destination, Name string }
}

func durableJSON(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p))
}
func syncDir(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
func (e *Engine) daemon(ctx context.Context, dir, expected string) error {
	b, err := e.docker(ctx, dir, "info", "--format", "{{json .}} ")
	if err != nil {
		return err
	}
	var info struct {
		ID, OSType, CgroupVersion, DockerRootDir string
		SecurityOptions                          []string
	}
	if json.Unmarshal(b, &info) != nil || info.ID != expected || info.OSType != "linux" || info.CgroupVersion != "2" {
		return errors.New("fixed Linux cgroup-v2 daemon identity mismatch")
	}
	for _, s := range info.SecurityOptions {
		if strings.Contains(s, "rootless") {
			return errors.New("rootful BuildKit daemon host required")
		}
	}
	// The engine must see the daemon storage filesystem, not a remote socket VM.
	if !filepath.IsAbs(info.DockerRootDir) {
		return errors.New("daemon storage path unavailable")
	}
	if _, err = os.Stat(info.DockerRootDir); err != nil {
		return errors.New("engine must run on daemon host with storage visibility")
	}
	return nil
}
func (e *Engine) inspect(ctx context.Context, dir, name string) (containerInfo, error) {
	var c containerInfo
	b, err := e.docker(ctx, dir, "inspect", "--type", "container", name)
	if err != nil {
		return c, err
	}
	var rows []containerInfo
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].ID == "" {
		return c, errors.New("invalid container inspection")
	}
	return rows[0], nil
}
func owned(c containerInfo, r ownedResources) bool {
	return c.Config.Labels["io.nerocd.hostingbuild.owner"] == r.Token
}

// Build is synchronous. A process-wide host flock rejects concurrent admission.
// A durable intent blocks reuse after crashes or uncertain Docker cleanup.
func (e *Engine) Build(parent context.Context, r Request) (result Result, retErr error) {
	return e.BuildWithOutput(parent, r, nil)
}

// BuildWithOutput builds synchronously and observes selected fresh build text.
// Receipt replay emits no output. The sink is not part of immutable identity.
func (e *Engine) BuildWithOutput(parent context.Context, r Request, sink OutputSink) (result Result, retErr error) {
	return e.BuildWithRegistryCredential(parent, r, sink, nil)
}

// BuildWithRegistryCredential supplies fresh authentication separately from immutable request identity.
// Retained receipt replay never invokes provider.
func (e *Engine) BuildWithRegistryCredential(parent context.Context, r Request, sink OutputSink, provider RegistryCredentialProvider) (result Result, retErr error) {
	r = ownedBuildRequest(r)
	if err := r.validate(parent); err != nil {
		return result, err
	}
	// Uncertainty is safe to reject before host admission, including callers
	// without permission to acquire the administrative host lock.
	marker := filepath.Join(e.config.StateDir, "cleanup_unconfirmed.json")
	if _, err := os.Lstat(marker); err == nil {
		return result, ErrCleanupUnconfirmed
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err := validateSourceSettings(parent, r); err != nil {
		return result, err
	}
	hostLock, err := acquireHostLock()
	if err != nil {
		return result, err
	}
	defer func() { _ = hostLock.Close() }()
	lock, err := acquireLock(e.config.StateDir)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()
	if _, err = os.Lstat(marker); err == nil {
		return result, ErrCleanupUnconfirmed
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err = ScrubRegistryCredentials(e.config.StateDir); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, r.Limits.Deadline)
	defer cancel()
	dir := filepath.Join(e.config.StateDir, r.BuildID)
	policy := currentContextPolicy(r)
	requestHash, err := requestIdentity(r, e.config, policy)
	if err != nil {
		return result, err
	}
	if _, err = os.Lstat(dir); err == nil {
		return e.replay(ctx, dir, r)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return result, err
	}
	if err = os.Mkdir(filepath.Join(dir, "docker"), 0700); err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(dir, "docker", "config.json"), []byte("{}"), 0600); err != nil {
		return result, err
	}
	if r.BuildRegistry != nil {
		// Installed immediately after config creation, before any material is delivered.
		defer func() {
			if cleanupErr := removeBuildRegistryConfig(dir); cleanupErr != nil {
				retErr = errors.Join(ErrCleanupUnconfirmed, errors.New("build registry config cleanup failed"))
			}
			if retErr != nil {
				held := errors.Is(retErr, ErrCleanupUnconfirmed)
				cancelled := errors.Is(retErr, context.Canceled)
				deadline := errors.Is(retErr, context.DeadlineExceeded)
				retErr = errors.New("authenticated build failed")
				if held {
					retErr = errors.Join(retErr, ErrCleanupUnconfirmed)
				}
				if cancelled {
					retErr = errors.Join(retErr, context.Canceled)
				}
				if deadline {
					retErr = errors.Join(retErr, context.DeadlineExceeded)
				}
			}
		}()
		if provider == nil {
			return result, errors.New("build registry provider unavailable")
		}
		if err = writeBuildRegistryConfig(ctx, dir, provider); err != nil {
			return result, err
		}
	}
	if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
		return result, err
	}
	if err = e.admissionFree(ctx, dir, r.Limits); err != nil {
		return result, err
	}
	if _, err = e.docker(ctx, dir, "image", "inspect", BuildKitImage); err != nil {
		return result, errors.New("pinned BuildKit image must be provisioned before admission")
	}
	source := filepath.Join(dir, "context")
	contextHash, err := stageRequestContext(ctx, r, source, policy)
	if err != nil {
		return result, err
	}
	if r.InjectBuildArgs {
		if err = injectDockerfileBuildArgs(ctx, r, source); err != nil {
			return result, err
		}
	}
	var tokenBytes [16]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return result, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	builder := "nerocd-" + token
	resources := ownedResources{DaemonID: r.DaemonID, Token: token, Builder: "buildx_buildkit_" + builder + "0", Planner: "nerocd-planner-" + token, Volume: "buildx_buildkit_" + builder + "0_state", Group: filepath.Join(e.config.CgroupRoot, "build-"+token)}
	if err = durableJSON(marker, resources); err != nil {
		return result, err
	}
	defer func() {
		// Cleanup gets its own bounded context after caller/deadline cancellation.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if cleanupErr := e.cleanup(cleanupCtx, dir, resources); cleanupErr != nil {
			retErr = errors.Join(retErr, ErrCleanupUnconfirmed, cleanupErr)
			return
		}
		if err := os.Remove(marker); err != nil {
			retErr = errors.Join(retErr, ErrCleanupUnconfirmed, err)
			return
		}
		if err := syncDir(e.config.StateDir); err != nil {
			retErr = errors.Join(retErr, ErrCleanupUnconfirmed, err)
		}
	}()
	if err = createBuildGroup(resources.Group, r.Limits); err != nil {
		return result, err
	}
	if _, err = e.docker(ctx, dir, "volume", "create", "--label", "io.nerocd.hostingbuild.owner="+token, resources.Volume); err != nil {
		return result, err
	}
	caTOML, caMounts, err := e.stageRegistryCAs(dir)
	if err != nil {
		return result, err
	}
	config := filepath.Join(dir, "buildkitd.toml")
	if err = os.WriteFile(config, []byte("insecure-entitlements = []\n[worker.oci]\n  enabled = true\n  max-parallelism = 2\n  defaultCgroupParent = "+strconv.Quote(groupRelative(resources.Group))+"\n[worker.containerd]\n  enabled = false\n"+caTOML), 0600); err != nil {
		return result, err
	}
	l := r.Limits
	builderQuota, builderMemory, builderSwap := l.CPUQuota, l.MemoryBytes, l.SwapBytes
	if r.Strategy == Railpack {
		builderQuota /= 2
		builderMemory /= 2
		builderSwap /= 2
	}
	args := []string{"create", "--pull=never", "--name", resources.Builder, "--label", "io.nerocd.hostingbuild.owner=" + token, "--privileged", "--cgroupns=host", "--cgroup-parent", groupRelative(resources.Group), "--cpu-period", "100000", "--cpu-quota", strconv.FormatInt(builderQuota, 10), "--memory", strconv.FormatInt(builderMemory, 10), "--memory-swap", strconv.FormatInt(builderMemory+builderSwap, 10), "--log-driver", "none", "--mount", "type=volume,src=" + resources.Volume + ",dst=/var/lib/buildkit", "--mount", "type=bind,src=" + config + ",dst=/etc/buildkit/buildkitd.toml,readonly", BuildKitImage, "--config", "/etc/buildkit/buildkitd.toml", "--oci-worker-net=bridge"}
	if len(caMounts) > 0 {
		at := len(args) - 4
		args = append(args[:at], append(caMounts, args[at:]...)...)
	}
	if _, err = e.docker(ctx, dir, args...); err != nil {
		return result, err
	}
	if _, err = e.docker(ctx, dir, "start", resources.Builder); err != nil {
		return result, err
	}
	c, err := e.inspect(ctx, dir, resources.Builder)
	if err != nil {
		return result, err
	}
	if !owned(c, resources) || !c.State.Running {
		return result, errors.New("builder ownership/running verification failed")
	}
	group := groupRelative(resources.Group)
	actualGroup, inspectErr := containment(c.State.PID, l, resources.Group)
	err = inspectErr
	if err == nil && actualGroup != group && !strings.HasPrefix(actualGroup, group+"/") {
		err = errors.New("host PID/cgroup view does not match owned builder placement")
	}
	if err == nil {
		err = checkGroup(resources.Group, l)
	}
	if err != nil {
		return result, err
	}
	logs := &cappedWriter{remaining: l.LogBytes, cancel: cancel}
	observer := newOutputObserver(ctx, cancel, sink)
	// Register the pre-created, explicitly constrained builder. Buildx therefore
	// cannot add its default network.host daemon entitlement.
	if err = e.command(ctx, dir, logs, e.config.BuildxBinary, "create", "--name", builder, "--node", builder+"0", "--driver", "docker-container", "--driver-opt", "image="+BuildKitImage, "unix://"+e.config.Socket); err != nil {
		return result, err
	}
	if err = e.command(ctx, dir, logs, e.config.BuildxBinary, "inspect", "--bootstrap", builder); err != nil {
		return result, err
	}
	booted, err := e.inspect(ctx, dir, resources.Builder)
	if err != nil {
		return result, err
	}
	if booted.ID != c.ID || !owned(booted, resources) || !booted.State.Running {
		return result, errors.New("buildx did not reuse the exact owned builder")
	}
	c = booted
	actualGroup, err = containment(c.State.PID, l, resources.Group)
	if err != nil {
		return result, err
	}
	if actualGroup != group && !strings.HasPrefix(actualGroup, group+"/") {
		return result, errors.New("bootstrapped builder escaped owned placement")
	}
	insideConfig, err := e.docker(ctx, dir, "exec", c.ID, "cat", "/etc/buildkit/buildkitd.toml")
	if err != nil {
		return result, err
	}
	expectedConfig, err := os.ReadFile(config)
	if err != nil {
		return result, err
	}
	if string(insideConfig) != string(expectedConfig) {
		return result, errors.New("BuildKit worker configuration changed")
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan error, 1)
	go func() { watchDone <- e.monitor(watchCtx, dir, resources, c, l, group, cancel) }()
	defer func() {
		if watchErr := joinMonitor(stopWatch, watchDone); watchErr != nil {
			retErr = errors.Join(retErr, watchErr)
		}
	}()
	frontend := DockerfileFrontend
	filename := r.DockerfilePath
	if filename == "" {
		filename = "Dockerfile"
	}
	switch r.Strategy {
	case Static:
		source, filename, err = prepareStaticContext(source, r.SPAFallback)
		if err != nil {
			return result, err
		}
	case Railpack:
		frontend = RailpackFrontend
		filename = "railpack-plan.json"
		if err = e.plan(ctx, dir, source, resources, r, logs, observer); err != nil {
			return result, err
		}
	}
	export := filepath.Join(dir, "image.oci.tar")
	buildArgs := r.buildxArguments(builder, frontend, source, filename, export)
	if err = e.observedCommand(ctx, dir, logs, observer, OutputBuildKit, false, e.config.BuildxBinary, buildArgs...); err != nil {
		return result, err
	}
	if err = verifyDescendants(ctx, c.State.PID, group, l); err != nil {
		return result, err
	}
	identity, err := validateExport(ctx, export, filepath.Join(dir, "validated"), r.Platform, l)
	if err != nil {
		return result, err
	}
	// New imports require the native compatibility mapping validated above.
	// Legacy OCI-only receipts remain replayable without another import.
	if _, err = os.Stat(filepath.Join(dir, "validated", "manifest.json")); err != nil {
		return result, errors.New("docker-compatible export manifest required before import")
	}
	if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
		return result, err
	}
	if _, err = e.docker(ctx, dir, "load", "--input", export); err != nil {
		return result, err
	}
	result = Result{Origin: "local_build", BuildID: r.BuildID, SourceID: r.SourceID, InputID: r.InputID, RequestDigest: requestHash, ContextDigest: contextHash, DaemonID: r.DaemonID, ImageID: identity.Index, IndexDigest: identity.Index, ManifestDigest: identity.Manifest, ConfigDigest: identity.Config, Platform: identity.Platform, ContextPolicy: policy}
	// Docker's containerd store identifies OCI imports by descriptor digest;
	// classic stores may identify the image by its config digest. Every
	// candidate below comes exclusively from the validated export.
	for _, id := range []string{identity.Index, identity.Manifest, identity.Config} {
		result.ImageID = id
		err = e.verifyImage(ctx, dir, result)
		if err == nil {
			break
		}
	}
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if err = durableJSON(filepath.Join(dir, "receipt.json"), result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (e *Engine) replay(ctx context.Context, dir string, r Request) (Result, error) {
	var receipt Result
	b, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if err != nil {
		return receipt, errors.New("build ID already used without completed receipt")
	}
	if len(b) > 16384 || json.Unmarshal(b, &receipt) != nil {
		return Result{}, errors.New("invalid retained build receipt")
	}
	requestHash, err := requestIdentity(r, e.config, receipt.ContextPolicy)
	if err != nil {
		return Result{}, err
	}
	if receipt.RequestDigest != requestHash || receipt.Origin != "local_build" || receipt.BuildID != r.BuildID || receipt.SourceID != r.SourceID || receipt.InputID != r.InputID || receipt.DaemonID != r.DaemonID || receipt.Platform != r.Platform || !digestPattern.MatchString(receipt.ContextDigest) {
		return Result{}, errors.New("build ID immutable input mismatch")
	}
	temp, err := os.MkdirTemp(e.config.StateDir, "replay-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	digest, err := stageRequestContext(ctx, r, filepath.Join(temp, "context"), receipt.ContextPolicy)
	if err != nil {
		return Result{}, err
	}
	if digest != receipt.ContextDigest {
		return Result{}, errors.New("completed build source content changed")
	}
	identity, err := validateExport(ctx, filepath.Join(dir, "image.oci.tar"), filepath.Join(temp, "validated"), r.Platform, r.Limits)
	if err != nil {
		return Result{}, err
	}
	if identity.Config != receipt.ConfigDigest || identity.Manifest != receipt.ManifestDigest || identity.Index != receipt.IndexDigest {
		return Result{}, errors.New("retained export identity changed")
	}
	if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
		return Result{}, err
	}
	if err = e.verifyImage(ctx, dir, receipt); err != nil {
		return Result{}, err
	}
	return receipt, nil
}
func (e *Engine) verifyImage(ctx context.Context, dir string, r Result) error {
	if !digestPattern.MatchString(r.ImageID) || (r.ImageID != r.ConfigDigest && r.ImageID != r.ManifestDigest && r.ImageID != r.IndexDigest) {
		return errors.New("invalid image identity")
	}
	b, err := e.docker(ctx, dir, "image", "inspect", r.ImageID)
	if err != nil {
		return err
	}
	var rows []struct {
		ID               string `json:"Id"`
		Architecture, OS string
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].ID != r.ImageID || rows[0].OS+"/"+rows[0].Architecture != r.Platform {
		return errors.New("loaded image config/platform mismatch")
	}
	return nil
}
func checkFree(p string, l Limits) error {
	n, err := freeBytes(p)
	if err != nil {
		return err
	}
	if n < l.ReserveBytes {
		return errors.New("free disk reserve breached")
	}
	return nil
}
func usage(ctx context.Context, p string) (int64, error) {
	var total int64
	err := scanTree(newScanBudget(ctx), p, func(_ string, i fs.FileInfo) error {
		if i.Mode().IsRegular() {
			if i.Size() > 1<<50-total {
				return errors.New("usage overflow")
			}
			total += i.Size()
		}
		return nil
	})
	return total, err
}

func (e *Engine) monitor(ctx context.Context, dir string, r ownedResources, c containerInfo, l Limits, group string, cancel context.CancelFunc) error {
	ticker := time.NewTicker(l.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := verifyDescendants(ctx, c.State.PID, group, l)
			if err == nil {
				err = checkFree(e.config.StateDir, l)
			}
			var n int64
			if err == nil {
				n, err = usage(ctx, dir)
				if n > l.UsageBytes {
					err = errors.New("owned workspace disk budget exceeded")
				}
			}
			if err == nil {
				st, e := os.Stat(filepath.Join(dir, "image.oci.tar"))
				if e == nil && st.Size() > l.ExportBytes {
					err = errors.New("export byte budget exceeded")
				}
			}
			// BuildKit storage lives on an owned Docker volume, which must be visible on
			// the local Linux daemon host. It is measured separately from the workspace.
			if err == nil {
				for _, m := range c.Mounts {
					if m.Type == "volume" && m.Name == r.Volume {
						if err = checkFree(m.Source, l); err != nil {
							break
						}
						var v int64
						v, err = usage(ctx, m.Source)
						if err == nil && v > l.UsageBytes-n {
							err = errors.New("owned builder disk budget exceeded")
						}
					}
				}
			}
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				cancel()
				return err
			}
		}
	}
}

func (e *Engine) admissionFree(ctx context.Context, dir string, l Limits) error {
	storage, err := e.docker(ctx, dir, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return err
	}
	daemonRoot := strings.TrimSpace(string(storage))
	if !filepath.IsAbs(daemonRoot) {
		return errors.New("daemon storage path unavailable")
	}
	// Nominal owned bytes plus an image import fit above the reserve on both
	// filesystems. Sampling still permits overshoot and concurrent disk use.
	required := l.ReserveBytes + uint64(l.UsageBytes) + uint64(l.UnpackedBytes)
	for _, p := range []string{e.config.StateDir, daemonRoot} {
		free, err := freeBytes(p)
		if err != nil {
			return err
		}
		if free < required {
			return errors.New("build budgets do not fit above free-space reserve")
		}
	}
	return nil
}

// orderedBuildArguments keeps argument identity deterministic without shell or
// process environment expansion. Values are one argv element, including spaces.
func orderedBuildArguments(args map[string]string) []string {
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+args[name])
	}
	return out
}

func ownedBuildRequest(r Request) Request {
	if r.BuildRegistry != nil {
		ref := *r.BuildRegistry
		r.BuildRegistry = &ref
	}
	r.BuildArgs = maps.Clone(r.BuildArgs)
	if len(r.BuildArgs) == 0 {
		r.InjectBuildArgs = false
	}
	return r
}

func (r Request) buildxArguments(builder, frontend, source, filename, export string) []string {
	buildArgs := []string{"build", "--no-cache", "--builder", builder, "--platform", r.Platform, "--provenance=false", "--sbom=false", "--progress=plain", "--network=default", "--build-arg", "BUILDKIT_SYNTAX=" + frontend, "--file", filepath.Join(source, filename), "--output", "type=docker,oci-mediatypes=true,dest=" + export + ",compression=gzip,force-compression=true", source}
	for _, argument := range orderedBuildArguments(r.BuildArgs) {
		buildArgs = append(buildArgs[:len(buildArgs)-1], "--build-arg", argument, source)
	}
	if r.BuildTarget != "" {
		buildArgs = append(buildArgs[:len(buildArgs)-1], "--target", r.BuildTarget, source)
	}
	return buildArgs
}
