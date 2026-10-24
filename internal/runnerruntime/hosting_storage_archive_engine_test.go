//go:build unix

package runnerruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/storagearchive"
)

type archiveEngineFake struct {
	t                    *testing.T
	e                    *storageArchiveEngine
	p                    domain.HostingStorageArchivePlan
	helpers              map[string]archiveHelperContainer
	writers              map[string]imageContainer
	volumes              map[string]map[string]any
	data                 []byte
	last                 string
	mutations, starts    int
	foreign, failInspect bool
	blockStream          bool
	streamStarted        chan struct{}
	partialRestore       bool
	imageMutation        func(map[string]any)
	idListFailure        bool
	idListForeign        bool
}

func archiveEnginePlan(t *testing.T) domain.HostingStorageArchivePlan {
	t.Helper()
	r := ownedVolumeRequest(t)
	r.CreationNonce = domain.HostingStorageCreationNonce(r.Storage.Reference(), r.OriginOperationID, r.DaemonID)
	h := domain.HostingHealthy{OperationID: "writer", ContainerID: strings.Repeat("c", 64), PlanHash: r.OriginPlanHash}
	h.Snapshot = domain.HostingSnapshot{ResourceID: r.Storage.ResourceID, BindingID: r.Storage.BindingID, DestinationID: r.Storage.DestinationID, ServerID: "server", RunnerID: "runner", Storage: r.Storage.Reference(), PolicyVersion: domain.HostingOwnedStoragePolicyVersion, ContainerPort: 8080, HostPort: 18080}
	h.Release = domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: r.DaemonID}
	h.Release.Hash = domain.HostingDocumentHash(h.Release)
	h.Storage = &domain.HostingHealthyStorage{Plan: domain.HostingStoragePlan{Reference: r.Storage.Reference(), VolumeName: r.Storage.VolumeName, MountPath: r.Storage.MountPath, CreationOperationID: r.OriginOperationID, CreationNonce: r.CreationNonce, DaemonID: r.DaemonID}, CreationPlanHash: r.OriginPlanHash}
	m := domain.HostingStorageMaterialization{StorageRef: r.Storage.Reference(), State: "materialized", CreationOperationID: r.OriginOperationID, CreationPlanHash: r.OriginPlanHash, CreationNonce: r.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &domain.HostingReceipt{ID: "materialized", BodyHash: r.OriginPlanHash, CreatedAt: time.Now().UTC()}}
	m.Labels = domain.ExpectedHostingStorageLabels(r.Storage, m)
	helper := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: "sha256:" + strings.Repeat("e", 64), ExecutableSHA256: "sha256:" + strings.Repeat("f", 64), SourceCommit: strings.Repeat("a", 40), OS: "linux", Architecture: "arm64", DaemonID: r.DaemonID}
	helper.Hash = domain.HostingDocumentHash(helper)
	p := domain.HostingStorageArchivePlan{OperationID: "archive", ProjectID: r.Storage.ProjectID, ServerID: "server", RunnerID: "runner", DestinationID: r.Storage.DestinationID, Mode: "backup", BackupID: "backup", SourceHealthy: h, SourceStorage: r.Storage, SourceMaterialization: m, Writers: []domain.HostingHealthy{h}, Helper: helper, Limits: domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 20, FileBytes: 1 << 20, Members: 100, Depth: 16, PathBytes: 255}, CompilerVersion: domain.HostingStorageArchiveVersion, PolicyVersion: domain.HostingStorageArchiveVersion}
	archiveHash(&p)
	if p.Validate() != nil {
		t.Fatal("invalid engine plan fixture")
	}
	return p
}

func archiveHash(p *domain.HostingStorageArchivePlan) {
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(*p)
}

func archiveTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func archiveFixture(t *testing.T) *archiveEngineFake {
	t.Helper()
	p := archiveEnginePlan(t)
	image, err := newImageEngine("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := image.Close(); err != nil {
			t.Error(err)
		}
	})
	artifactRoot := archiveTestRoot(t)
	store, err := artifactfs.OpenStore(artifactRoot, domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	temporary, err := os.OpenRoot(archiveTestRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := temporary.Close(); err != nil {
			t.Error(err)
		}
	})
	e := &storageArchiveEngine{image: image, config: StorageArchiveExecutionConfig{Helper: p.Helper, ArtifactRoot: artifactRoot}, store: store, temporary: temporary, dockerRoot: archiveTestRoot(t), host: DatabaseEngineConfig{stateRoot: "state", secretRoot: "secret", hostCheck: func(context.Context, *imageEngine) error { return nil }}}
	f := &archiveEngineFake{t: t, e: e, p: p, helpers: map[string]archiveHelperContainer{}, writers: map[string]imageContainer{}, volumes: map[string]map[string]any{}}
	image.command = imageCommandFunc(f.command)
	e.stream = f.stream
	f.addVolume(archiveSourceRequest(p))
	h := p.SourceHealthy
	c := imageContainer{ID: h.ContainerID, Image: h.Release.ImageID}
	c.Config.Labels = map[string]string{imageBindingLabel: h.Snapshot.BindingID, imageOperationLabel: h.OperationID, imageReleaseLabel: h.Release.Hash, imagePlanLabel: h.PlanHash}
	c.State.Running = true
	port := strconv.Itoa(h.Snapshot.ContainerPort) + "/tcp"
	raw := []byte(`{"` + port + `":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}`)
	if json.Unmarshal(raw, &c.HostConfig.PortBindings) != nil {
		t.Fatal("port fixture")
	}
	c.Mounts = []imageMount{{Type: "volume", Name: p.SourceStorage.VolumeName, Driver: "local", Destination: p.SourceStorage.MountPath, RW: true}}
	f.writers[c.ID] = c
	f.data = archiveTestBytes(t)
	if e.Prepare(t.Context(), p) != nil {
		t.Fatal("engine preparation failed")
	}
	return f
}

func archiveTestBytes(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	if err := w.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0o700, Uname: storagearchive.Format, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHeader(&tar.Header{Name: "upload", Typeflag: tar.TypeReg, Mode: 0o600, Size: 7, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func (f *archiveEngineFake) addVolume(req hostingOwnedVolumeRequest) {
	f.t.Helper()
	path := filepath.Join(f.e.dockerRoot, "volumes", req.Storage.VolumeName, "_data")
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
	f.volumes[req.Storage.VolumeName] = map[string]any{"Name": req.Storage.VolumeName, "Driver": "local", "Scope": "local", "Labels": req.labels(), "CreatedAt": "2026-10-24T00:00:00Z", "Mountpoint": path}
}

func (f *archiveEngineFake) before(ctx context.Context) error {
	f.last = "admission"
	return ctx.Err()
}

func (f *archiveEngineFake) mutation() {
	f.t.Helper()
	if f.last != "admission" {
		f.t.Fatal("daemon mutation did not immediately follow final admission")
	}
	f.mutations++
	f.last = "mutation"
}

func (f *archiveEngineFake) command(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
	args = args[4:]
	if len(args) < 2 {
		return nil, errors.New("invalid fake command")
	}
	action := args[0] + " " + args[1]
	switch action {
	case "container create", "container stop", "container start", "container rm", "volume create":
		f.mutation()
	default:
		f.last = "inspection"
	}
	switch action {
	case "info --format":
		if args[2] == "{{json .ID}}" {
			return json.Marshal(f.p.Helper.DaemonID)
		}
		return json.Marshal(map[string]string{"DockerRootDir": f.e.dockerRoot})
	case "image inspect":
		evidence := map[string]any{"Id": f.p.Helper.ImageID, "Os": "linux", "Architecture": "arm64", "Config": map[string]any{"Entrypoint": []string{"/nerocd-storage-archive"}, "WorkingDir": "/", "Labels": archiveImageLabels(f.p.Helper)}}
		if f.imageMutation != nil {
			f.imageMutation(evidence)
		}
		return json.Marshal(evidence)
	case "volume ls":
		name := strings.TrimPrefix(args[len(args)-1], "name=")
		if _, ok := f.volumes[name]; ok {
			return []byte(name + "\n"), nil
		}
		return nil, nil
	case "volume inspect":
		volume, ok := f.volumes[args[2]]
		if !ok {
			return nil, errors.New("absent volume")
		}
		return json.Marshal(volume)
	case "volume create":
		f.addVolume(archiveTargetRequest(f.p))
		return []byte(f.p.Target.Storage.VolumeName + "\n"), nil
	case "container ls":
		filter := args[len(args)-1]
		if strings.HasPrefix(filter, "id=") {
			if f.idListFailure {
				return nil, errors.New("unavailable listing")
			}
			if f.idListForeign {
				return []byte(strings.Repeat("9", 64) + "\n"), nil
			}
		}
		var ids []string
		if strings.HasPrefix(filter, "volume=") {
			volume := strings.TrimPrefix(filter, "volume=")
			if volume == f.p.SourceStorage.VolumeName {
				for id := range f.writers {
					ids = append(ids, id)
				}
				if f.foreign {
					ids = append(ids, strings.Repeat("9", 64))
				}
			}
			for id, helper := range f.helpers {
				for _, mount := range helper.Mounts {
					if mount.Name == volume {
						ids = append(ids, id)
					}
				}
			}
		} else if strings.HasPrefix(filter, "name=") {
			for id, helper := range f.helpers {
				if "name=^"+helper.Name+"$" == filter {
					ids = append(ids, id)
				}
			}
		} else if id := strings.TrimPrefix(filter, "id="); f.helpers[id].ID != "" {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(ids, "\n") + "\n"), nil
	case "container inspect":
		if helper, ok := f.helpers[args[2]]; ok {
			if f.failInspect {
				return nil, errors.New("unconfirmed helper")
			}
			return json.Marshal(helper)
		}
		return json.Marshal(f.writers[args[2]])
	case "container stop", "container start":
		id := args[len(args)-1]
		writer := f.writers[id]
		writer.State.Running = action == "container start"
		f.writers[id] = writer
		return []byte(id), nil
	case "container create":
		f.checkCreateArgs(args)
		id := strings.Repeat("d", 64)
		helper := archiveHelperContainer{ID: id, Name: "/" + archiveHelperName(f.p, "helper_receipt"), Image: f.p.Helper.ImageID}
		helper.Config.Labels = archiveHelperLabels(f.p, "helper_receipt")
		helper.Config.Cmd = archiveHelperCommand(f.p)
		helper.Config.Entrypoint, helper.Config.Env = []string{"/nerocd-storage-archive"}, []string{"PATH=/", "HOME=/"}
		helper.Config.User, helper.Config.Hostname = "0:0", "nerocd-archive"
		helper.State.Status = "created"
		c := &helper.HostConfig
		c.ReadonlyRootfs, c.NetworkMode, c.IpcMode, c.CapDrop = true, "none", "private", []string{"ALL"}
		c.CgroupnsMode = "private"
		c.CapAdd, c.SecurityOpt = archiveHelperCaps(f.p), []string{"no-new-privileges"}
		c.Tmpfs, c.Memory = map[string]string{"/tmp": archiveTmpfs(f.p)}, f.p.Limits.ArchiveBytes+(128<<20)
		c.MemorySwap, c.NanoCpus, c.PidsLimit = c.Memory, 1000000000, 64
		c.LogConfig.Type, c.RestartPolicy.Name = "none", "no"
		volume, destination, writable := f.p.SourceStorage.VolumeName, "/source", false
		if f.p.Target != nil {
			volume, destination, writable = f.p.Target.Storage.VolumeName, "/target", true
		}
		helper.Mounts = append(helper.Mounts, struct {
			Type, Name, Driver, Source, Destination string
			RW                                      bool
		}{"volume", volume, "local", filepath.Join(f.e.dockerRoot, "volumes", volume, "_data"), destination, writable})
		f.helpers[id] = helper
		return []byte(id + "\n"), nil
	case "container rm":
		delete(f.helpers, args[len(args)-1])
		return nil, nil
	}
	f.t.Fatalf("unrecognized archive fake action %s", action)
	return nil, errStorageArchiveEngine
}

func (f *archiveEngineFake) stream(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	f.mutation()
	f.starts++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Minute {
		f.t.Fatal("unbounded archive stream")
	}
	id := args[len(args)-1]
	h := f.helpers[id]
	h.State.Running, h.State.Status = true, "running"
	f.helpers[id] = h
	if f.blockStream {
		close(f.streamStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	data := f.data
	if in != nil {
		if f.partialRestore {
			target := f.volumes[f.p.Target.Storage.VolumeName]["Mountpoint"].(string)
			if os.WriteFile(filepath.Join(target, storagearchive.IncompleteMarker), []byte("held partial target"), 0o600) != nil {
				f.t.Fatal("partial restore fixture")
			}
			return errStorageArchiveEngine
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(in, f.p.Limits.ArchiveBytes+1))
		if err != nil || !bytes.Equal(data, f.data) {
			return errStorageArchiveEngine
		}
		file, err := os.CreateTemp(f.t.TempDir(), "validate-")
		if err != nil {
			f.t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if _, err := file.Write(data); err != nil {
			f.t.Fatal(err)
		}
		summary, err := storagearchive.Validate(ctx, file, archiveCodecLimits(f.p))
		if err != nil {
			return err
		}
		data, err = json.Marshal(summary)
		if err != nil {
			return err
		}
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	h.State.Running, h.State.Status = false, "exited"
	f.helpers[id] = h
	return nil
}

func (f *archiveEngineFake) checkCreateArgs(args []string) {
	f.t.Helper()
	image := slices.Index(args, f.p.Helper.ImageID)
	if image < 2 || !slices.Equal(args[image+1:], archiveHelperCommand(f.p)) {
		f.t.Fatal("create did not use exact image and fixed helper arguments")
	}
	flags := map[string][]string{}
	for i := 2; i < image; i++ {
		key, value := args[i], "true"
		switch key {
		case "--pull=never", "--interactive", "--read-only":
		case "--name", "--network", "--ipc", "--cgroupns", "--user", "--hostname", "--env", "--cap-drop", "--security-opt", "--log-driver", "--restart", "--pids-limit", "--memory", "--memory-swap", "--cpus", "--tmpfs", "--cap-add", "--label", "--mount":
			i++
			if i >= image {
				f.t.Fatal("missing fixed helper flag value")
			}
			value = args[i]
		default:
			f.t.Fatal("unexpected helper create option")
		}
		flags[key] = append(flags[key], value)
	}
	for flag, value := range map[string]string{"--pull=never": "true", "--interactive": "true", "--read-only": "true", "--network": "none", "--ipc": "private", "--cgroupns": "private", "--user": "0:0", "--hostname": "nerocd-archive", "--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--log-driver": "none", "--restart": "no", "--pids-limit": "64", "--cpus": "1"} {
		if !slices.Equal(flags[flag], []string{value}) {
			f.t.Fatal("helper sandbox option absent, repeated or weakened")
		}
	}
	memory := strconv.FormatInt(f.p.Limits.ArchiveBytes+(128<<20), 10)
	for flag, value := range map[string]string{"--memory": memory, "--memory-swap": memory, "--name": archiveHelperName(f.p, "helper_receipt"), "--tmpfs": "/tmp:" + archiveTmpfs(f.p)} {
		if !slices.Equal(flags[flag], []string{value}) {
			f.t.Fatal("helper resource bound or identity option changed")
		}
	}
	mount := "type=volume,src=" + f.p.SourceStorage.VolumeName + ",dst=/source,readonly,volume-nocopy"
	if f.p.Target != nil {
		mount = "type=volume,src=" + f.p.Target.Storage.VolumeName + ",dst=/target,volume-nocopy"
	}
	if !slices.Equal(flags["--mount"], []string{mount}) || !slices.Equal(flags["--env"], []string{"PATH=/", "HOME=/"}) || !slices.Equal(flags["--cap-add"], archiveHelperCaps(f.p)) {
		f.t.Fatal("helper gained an arbitrary mount, environment or capability")
	}
}

func (f *archiveEngineFake) stoppedHelper() string {
	f.t.Helper()
	states, err := f.e.InspectWriters(f.t.Context(), f.p)
	if err != nil || len(states) != 1 {
		f.t.Fatal("writer snapshot failed")
	}
	if f.e.StopWriter(f.t.Context(), f.p, states[0], f.before) != nil {
		f.t.Fatal("writer stop failed")
	}
	id, err := f.e.CreateHelper(f.t.Context(), f.p, "helper_receipt", f.before)
	if err != nil {
		f.t.Fatal("helper creation failed")
	}
	return id
}

func TestStorageArchiveEngineBackupPublishesOnlyValidatedPrivateCAS(t *testing.T) {
	f := archiveFixture(t)
	id := f.stoppedHelper()
	blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", f.before)
	if err != nil || blob.Validate() != nil || blob.BackupID != f.p.BackupID || blob.SizeBytes != int64(len(f.data)) {
		t.Fatal("bounded backup failed")
	}
	handle, saved, err := f.e.store.Open(t.Context(), strings.TrimPrefix(blob.SHA256, "sha256:"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	raw, err := io.ReadAll(handle)
	if err != nil || !bytes.Equal(raw, f.data) || saved.Size != blob.SizeBytes {
		t.Fatal("CAS does not contain exact validated archive")
	}
	if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
		t.Fatal("exact helper cleanup failed")
	}
	state := domain.HostingStorageArchiveWriterState{ContainerID: f.p.SourceHealthy.ContainerID, OperationID: f.p.SourceHealthy.OperationID, ImageID: f.p.SourceHealthy.Release.ImageID, ReleaseHash: f.p.SourceHealthy.Release.Hash, PlanHash: f.p.SourceHealthy.PlanHash, Running: true}
	if f.e.ResumeWriter(t.Context(), f.p, state, f.before) != nil || !f.writers[state.ContainerID].State.Running {
		t.Fatal("original writer resume failed")
	}
}

func TestStorageArchiveEngineRejectsForeignUsersAndStaleAdmission(t *testing.T) {
	for _, failure := range []string{"foreign", "callback", "nil", "daemon", "mount", "stopped_bit"} {
		t.Run(failure, func(t *testing.T) {
			f := archiveFixture(t)
			states, err := f.e.InspectWriters(t.Context(), f.p)
			if err != nil {
				t.Fatal(err)
			}
			before := f.before
			switch failure {
			case "foreign":
				f.foreign = true
			case "callback":
				before = func(context.Context) error { return errors.New("expired") }
			case "nil":
				before = nil
			case "daemon":
				f.e.config.Helper.DaemonID = "foreign"
			case "mount":
				c := f.writers[states[0].ContainerID]
				c.Mounts[0].RW = false
				f.writers[c.ID] = c
			case "stopped_bit":
				states[0].Running = false
			}
			if f.e.StopWriter(t.Context(), f.p, states[0], before) == nil || f.mutations != 0 {
				t.Fatal("unsafe stop admitted")
			}
		})
	}
}

type archiveFailCAS struct{ archiveCAS }

func (archiveFailCAS) Put(context.Context, io.Reader, string, int64) (artifactfs.Blob, error) {
	return artifactfs.Blob{}, errors.New("publication failed")
}

func TestStorageArchiveEngineArchiveFailureDoesNotPublishOrGuessHelper(t *testing.T) {
	for _, failure := range []string{"malformed", "limit", "publication", "helper_identity", "start_authority"} {
		t.Run(failure, func(t *testing.T) {
			f := archiveFixture(t)
			id := f.stoppedHelper()
			before := f.before
			switch failure {
			case "malformed":
				f.data = []byte("private invalid archive")
			case "limit":
				f.data = bytes.Repeat([]byte{'x'}, int(f.p.Limits.ArchiveBytes)+1)
			case "publication":
				f.e.store = archiveFailCAS{f.e.store}
			case "helper_identity":
				h := f.helpers[id]
				h.Config.Labels = maps.Clone(h.Config.Labels)
				h.Config.Labels[archiveLabel+"nonce"] = "foreign"
				f.helpers[id] = h
			case "start_authority":
				before = func(context.Context) error { return errors.New("expired") }
			}
			blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", before)
			if err == nil || blob != (domain.HostingStorageArchiveBlob{}) {
				t.Fatal("failed backup exposed committed artifact")
			}
			if failure == "helper_identity" || failure == "start_authority" {
				if f.starts != 0 {
					t.Fatal("unowned helper started")
				}
			}
			if failure == "helper_identity" {
				mutations := f.mutations
				if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) == nil || f.mutations != mutations {
					t.Fatal("foreign helper was removed")
				}
			}
			dir, err := f.e.temporary.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			entries, err := dir.ReadDir(-1)
			if err != nil || len(entries) != 0 {
				t.Fatal("private stage survived failure")
			}
		})
	}
}

func TestStorageArchiveEngineUnconfirmedCreateReturnsOnlyObservedID(t *testing.T) {
	f := archiveFixture(t)
	states, err := f.e.InspectWriters(t.Context(), f.p)
	if err != nil || f.e.StopWriter(t.Context(), f.p, states[0], f.before) != nil {
		t.Fatal("stop fixture")
	}
	f.failInspect = true
	id, err := f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before)
	if err == nil || id != strings.Repeat("d", 64) || len(f.helpers) != 1 || f.mutations != 2 {
		t.Fatal("unconfirmed creation lost exact observation or removed state")
	}
	if guessed, err := f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before); err == nil || guessed != "" || f.mutations != 2 {
		t.Fatal("existing helper was guessed or recreated")
	}
}

func (f *archiveEngineFake) restorePlan(blob domain.HostingStorageArchiveBlob) {
	f.t.Helper()
	p := f.p
	p.Mode, p.OperationID, p.Writers, p.Blob = "restore", "restore", nil, &blob
	storage := p.SourceStorage
	storage.ResourceID, storage.BindingID = "restored_resource", "restored_binding"
	storage.ID, storage.VolumeName = domain.HostingStorageIdentity(storage.BindingID)
	storage.Hash = storage.DocumentHash()
	plan := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: storage.VolumeName, MountPath: storage.MountPath, CreationOperationID: p.OperationID, DaemonID: p.Helper.DaemonID}
	plan.CreationNonce = domain.HostingStorageCreationNonce(plan.Reference, plan.CreationOperationID, plan.DaemonID)
	p.Target = &domain.HostingStorageArchiveTarget{Storage: storage, Plan: plan, Resource: domain.Resource{ID: storage.ResourceID, ProjectID: storage.ProjectID, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: p.SourceHealthy.Release.CanonicalImage}}}}
	archiveHash(&p)
	if p.Validate() != nil {
		f.t.Fatal("invalid restore fixture")
	}
	f.p = p
	if f.e.Prepare(f.t.Context(), p) != nil {
		f.t.Fatal("restore preparation failed")
	}
}

func TestStorageArchiveEngineRestoreOnlyNewEmptyOwnedTarget(t *testing.T) {
	f := archiveFixture(t)
	id := f.stoppedHelper()
	blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", f.before)
	if err != nil || f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
		t.Fatal("backup fixture")
	}
	f.restorePlan(blob)
	req := archiveTargetRequest(f.p)
	req.CreatePermitted = true
	materialized, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before)
	if err != nil || materialized.StorageRef != req.Storage.Reference() || materialized.CreationPlanHash != f.p.Hash || !maps.Equal(materialized.Labels, req.labels()) {
		t.Fatal("restore materialization failed")
	}
	req.CreatePermitted = false
	mutations := f.mutations
	if _, err := f.e.EnsureRestoreVolume(t.Context(), req, func(context.Context) error { return errors.New("expired") }); err == nil || f.mutations != mutations {
		t.Fatal("target observation bypassed current authority or recreated volume")
	}
	id, err = f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before)
	if err != nil || f.e.Restore(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
		t.Fatal("restore helper did not verify exact blob")
	}
	if f.e.Restore(t.Context(), f.p, id, "helper_receipt", f.before) == nil {
		t.Fatal("extract replay admitted")
	}
	if f.e.StopWriter(t.Context(), f.p, domain.HostingStorageArchiveWriterState{Running: true}, f.before) == nil {
		t.Fatal("restore granted source stop authority")
	}
}

func TestStorageArchiveEngineCanceledStreamLeavesHeldHelper(t *testing.T) {
	f := archiveFixture(t)
	id := f.stoppedHelper()
	f.blockStream = true
	f.streamStarted = make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	var blob domain.HostingStorageArchiveBlob
	var err error
	go func() {
		blob, err = f.e.Backup(ctx, f.p, id, "helper_receipt", f.before)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("fake archive stream was not joined")
		}
	})
	select {
	case <-f.streamStarted:
	case <-time.After(time.Second):
		t.Fatal("fake archive stream never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fake archive stream did not join")
	}
	if err == nil || blob != (domain.HostingStorageArchiveBlob{}) || len(f.helpers) != 1 {
		t.Fatal("cancellation published or discarded held helper")
	}
	mutations := f.mutations
	if f.e.CleanupHelper(ctx, f.p, id, "helper_receipt", f.before) == nil || f.mutations != mutations {
		t.Fatal("canceled authority removed daemon helper")
	}
	if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
		t.Fatal("fresh authority could not clean exact helper")
	}
}

func TestStorageArchiveEnginePartialTargetIsHeldAndRecoveryNeverExtractsAgain(t *testing.T) {
	f := archiveFixture(t)
	id := f.stoppedHelper()
	blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", f.before)
	if err != nil || f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
		t.Fatal("backup fixture")
	}
	f.restorePlan(blob)
	req := archiveTargetRequest(f.p)
	req.CreatePermitted = true
	if _, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before); err != nil {
		t.Fatal("target fixture")
	}
	id, err = f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before)
	if err != nil {
		t.Fatal("helper fixture")
	}
	f.partialRestore = true
	if f.e.Restore(t.Context(), f.p, id, "helper_receipt", f.before) == nil {
		t.Fatal("partial extraction reported success")
	}
	original := f.p
	p := original
	p.Mode, p.OperationID, p.RecoveryOfOperationID = "recover", "restore_recovery", original.OperationID
	p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: original.Hash}
	archiveHash(&p)
	if p.Validate() != nil || f.e.Prepare(t.Context(), p) != nil {
		t.Fatal("restore recovery fixture")
	}
	if f.e.CleanupHelper(t.Context(), original, id, "helper_receipt", f.before) != nil {
		t.Fatal("partial helper cleanup failed")
	}
	mutations := f.mutations
	if _, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before); err == nil || f.e.Restore(t.Context(), p, id, "helper_receipt", f.before) == nil || f.mutations != mutations {
		t.Fatal("recovery recreated volume or replayed extraction")
	}
	target := f.volumes[req.Storage.VolumeName]["Mountpoint"].(string)
	data, err := os.ReadFile(filepath.Join(target, storagearchive.IncompleteMarker))
	if err != nil || string(data) != "held partial target" {
		t.Fatal("partial target was erased or reused")
	}
}

func TestStorageArchiveEngineRecoveryOnlyCleansExactOriginalHelperAndResumes(t *testing.T) {
	f := archiveFixture(t)
	id := f.stoppedHelper()
	original := f.p
	state := domain.HostingStorageArchiveWriterState{ContainerID: original.SourceHealthy.ContainerID, OperationID: original.SourceHealthy.OperationID, ImageID: original.SourceHealthy.Release.ImageID, ReleaseHash: original.SourceHealthy.Release.Hash, PlanHash: original.SourceHealthy.PlanHash, Running: true}
	p := original
	p.Mode, p.OperationID, p.RecoveryOfOperationID = "recover", "recovery", original.OperationID
	p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: original.Hash, SnapshotReceiptID: "snapshot", Writers: []domain.HostingStorageArchiveWriterState{state}, ResumeRequiredContainerIDs: []string{state.ContainerID}}
	archiveHash(&p)
	if p.Validate() != nil || f.e.Prepare(t.Context(), p) != nil {
		t.Fatal("recovery preparation failed")
	}
	mutations := f.mutations
	if _, err := f.e.CreateHelper(t.Context(), p, "helper_receipt", f.before); err == nil || f.mutations != mutations {
		t.Fatal("recovery created or guessed helper")
	}
	if f.e.CleanupHelper(t.Context(), original, id, "wrong_nonce", f.before) == nil || f.mutations != mutations {
		t.Fatal("recovery removed mismatched original helper")
	}
	if f.e.CleanupHelper(t.Context(), original, id, "helper_receipt", f.before) != nil || f.e.ResumeWriter(t.Context(), p, state, f.before) != nil {
		t.Fatal("fresh recovery authority could not discharge exact obligations")
	}
	mutations = f.mutations
	if f.e.CleanupHelper(t.Context(), original, id, "helper_receipt", f.before) != nil || f.mutations != mutations {
		t.Fatal("exact absent-helper cleanup replay mutated daemon")
	}
	if _, err := f.e.Backup(t.Context(), original, id, "helper_receipt", f.before); err == nil {
		t.Fatal("recovery replayed original archive")
	}
}

// Only the fake daemon's admission marker is adapted. Preparation, writer
// inspection and resume all execute the production engine implementation.
type archiveRecoveryCoordinatorEngine struct {
	*storageArchiveEngine
	markAdmission func(context.Context) error
}

func (e archiveRecoveryCoordinatorEngine) ResumeWriter(ctx context.Context, p domain.HostingStorageArchivePlan, state domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	return e.storageArchiveEngine.ResumeWriter(ctx, p, state, func(ctx context.Context) error {
		if before == nil {
			return errStorageArchiveEngine
		}
		if err := before(ctx); err != nil {
			return err
		}
		return e.markAdmission(ctx)
	})
}

func archiveRealRecoveryCoordinator(t *testing.T) (*archiveCoordinator, *archiveRuntimeController, *archiveEngineFake, []domain.HostingStorageArchiveWriterState) {
	t.Helper()
	f := archiveFixture(t)
	first := f.p.Writers[0]
	for _, id := range []string{strings.Repeat("d", 64), strings.Repeat("e", 64)} {
		h := first
		h.ContainerID = id
		f.p.Writers = append(f.p.Writers, h)
		container := f.writers[first.ContainerID]
		container.ID = id
		container.State.Running = id == strings.Repeat("d", 64)
		f.writers[id] = container
	}
	archiveHash(&f.p)
	if f.p.Validate() != nil {
		t.Fatal("invalid original writer fixture")
	}
	var states []domain.HostingStorageArchiveWriterState
	for i, h := range f.p.Writers {
		states = append(states, domain.HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: i < 2})
	}
	// The first originally-running writer still needs resume. The second was
	// already resumed, and the third was originally stopped.
	container := f.writers[first.ContainerID]
	container.State.Running = false
	f.writers[first.ContainerID] = container
	x, c, _, _ := archiveRuntimeFixture(t)
	originalAttempt := runner.AttemptIdentity{RunID: "backup_" + f.p.OperationID, LeaseID: "original_archive_lease", Attempt: 1, Fence: "original_archive_fence", CreatedAt: time.Now().Add(-time.Second).UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
	originalAuthority := domain.HostingAuthority{OperationID: f.p.OperationID, RunID: originalAttempt.RunID, LeaseID: originalAttempt.LeaseID, Attempt: originalAttempt.Attempt, Fence: originalAttempt.Fence}
	appendProgress := func(id, phase string, observed []domain.HostingStorageArchiveWriterState) {
		request := domain.HostingStorageArchiveProgress{HostingAuthority: originalAuthority, ReceiptID: id, PlanHash: f.p.Hash, Phase: phase, Writers: observed}
		if _, err := x.j.AppendStorageArchive(runner.JournalStorageArchive{ID: id, Attempt: originalAttempt, Plan: f.p, Phase: "progress", Progress: &request, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	appendProgress("original_snapshot", "writer_snapshot", states)
	appendProgress("original_stop", "stop_intent", states)
	// A prior recovery resumed only the second writer. Its wire receipt still
	// carries the full original inventory; it cannot prove resume of the first.
	prior := f.p
	prior.OperationID, prior.Mode, prior.RecoveryOfOperationID = "prior_real_recovery", "recover", f.p.OperationID
	prior.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: f.p.Hash, SnapshotReceiptID: "original_snapshot", Writers: states, ResumeRequiredContainerIDs: []string{states[1].ContainerID}}
	archiveHash(&prior)
	priorAttempt := originalAttempt
	priorAttempt.RunID, priorAttempt.LeaseID, priorAttempt.Fence = "archive_recover_"+prior.OperationID, "prior_real_lease", "prior_real_fence"
	priorAuthority := domain.HostingAuthority{OperationID: prior.OperationID, RunID: priorAttempt.RunID, LeaseID: priorAttempt.LeaseID, Attempt: priorAttempt.Attempt, Fence: priorAttempt.Fence}
	resumed := domain.HostingStorageArchiveProgress{HostingAuthority: priorAuthority, ReceiptID: "prior_resumed_second", PlanHash: prior.Hash, Phase: "resumed", Writers: states}
	if _, err := x.j.AppendStorageArchive(runner.JournalStorageArchive{ID: resumed.ReceiptID, Attempt: priorAttempt, Plan: prior, Phase: "progress", Progress: &resumed, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	p := f.p
	p.OperationID = "real_engine_recovery"
	p.Mode = "recover"
	p.RecoveryOfOperationID = f.p.OperationID
	p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: f.p.Hash, SnapshotReceiptID: "original_snapshot", Writers: states, ResumeRequiredContainerIDs: []string{states[0].ContainerID}}
	archiveHash(&p)
	if p.Validate() != nil || f.e.Prepare(t.Context(), p) != nil {
		t.Fatal("valid real recovery preparation failed")
	}
	if _, err := f.e.writers(t.Context(), p, ""); err != nil {
		t.Fatal("valid underlying recovery inventory failed")
	}
	x.p = p
	x.a.OperationID = p.OperationID
	x.a.RunID = "archive_recover_" + p.OperationID
	x.a.LeaseID = "fresh_recovery_lease"
	x.a.Fence = "fresh_recovery_fence"
	x.lease.ID = x.a.LeaseID
	x.lease.RunID = x.a.RunID
	x.lease.Fence = x.a.Fence
	c.lease = x.lease
	c.op = domain.HostingOperation{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: p.SourceStorage.ResourceID, BindingID: p.SourceStorage.BindingID, RunID: x.a.RunID, TaskMode: p.Mode, State: "preparing", StorageArchive: &domain.HostingStorageArchive{Mode: p.Mode, BackupID: p.BackupID, RecoveryOfOperationID: p.RecoveryOfOperationID, Plan: p}}
	x.e = archiveRecoveryCoordinatorEngine{storageArchiveEngine: f.e, markAdmission: f.before}
	return x, c, f, states
}

func TestStorageArchiveCoordinatorRecoveryUsesRealEngineWriterInspection(t *testing.T) {
	x, c, f, states := archiveRealRecoveryCoordinator(t)
	if err := x.execute(c.op); err != nil {
		t.Fatalf("coordinator recovery failed with valid real engine inventory: %v", err)
	}
	if f.mutations != 1 || f.starts != 0 || len(f.helpers) != 0 {
		t.Fatalf("recovery effects mutations=%d archive_streams=%d helpers=%d", f.mutations, f.starts, len(f.helpers))
	}
	for i, w := range states {
		if got, want := f.writers[w.ContainerID].State.Running, i < 2; got != want {
			t.Fatalf("writer %d running=%v want=%v", i, got, want)
		}
	}
	if len(x.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("exact recovered chain not settled")
	}
	if c.op.StorageArchive.Result == nil || c.op.StorageArchive.Result.ResumeOutcome != "resumed" || c.op.StorageArchive.Result.ArchiveOutcome != "not_attempted" {
		t.Fatal("recovery replayed archive or failed resume")
	}
	for _, state := range states[1:] {
		if err := f.e.ResumeWriter(t.Context(), x.p, state, f.before); err == nil {
			t.Fatal("recovery widened narrowed or originally-stopped resume authority")
		}
	}
	if f.mutations != 1 {
		t.Fatal("unauthorized remaining writer mutated")
	}
}

func TestStorageArchiveCoordinatorRealRecoveryRejectsForeignInventoryAndExpiredFence(t *testing.T) {
	for _, failure := range []string{"foreign users", "foreign image", "daemon", "ownership", "expired lease"} {
		t.Run(failure, func(t *testing.T) {
			x, c, f, states := archiveRealRecoveryCoordinator(t)
			switch failure {
			case "foreign users":
				f.foreign = true
			case "foreign image":
				container := f.writers[states[0].ContainerID]
				container.Image = "sha256:" + strings.Repeat("9", 64)
				f.writers[states[0].ContainerID] = container
			case "daemon":
				f.p.Helper.DaemonID = "foreign_daemon"
			case "ownership":
				f.volumes[f.p.SourceStorage.VolumeName]["Labels"] = map[string]string{"foreign": "volume"}
			case "expired lease":
				c.lease.ExpiresAt = time.Now().Add(-time.Second)
			}
			if err := x.execute(c.op); err == nil {
				t.Fatal("unproven real-engine recovery accepted")
			}
			if f.mutations != 0 || f.starts != 0 || f.writers[states[0].ContainerID].State.Running || !f.writers[states[1].ContainerID].State.Running || f.writers[states[2].ContainerID].State.Running {
				t.Fatal("failed recovery changed writers or replayed archive")
			}
			if len(x.j.Snapshot().StorageArchive) == 0 {
				t.Fatal("failed recovery discarded obligations")
			}
		})
	}
}

func TestStorageArchiveWriterInspectionPreservesExactModeAndTargetBoundary(t *testing.T) {
	f := archiveFixture(t)
	if _, err := f.e.InspectWriters(t.Context(), f.p); err != nil {
		t.Fatal("valid backup inventory rejected")
	}
	invalid := f.p
	invalid.Mode = "arbitrary"
	archiveHash(&invalid)
	if _, err := f.e.InspectWriters(t.Context(), invalid); err == nil {
		t.Fatal("arbitrary mode obtained writer inventory")
	}
	blob := domain.HostingStorageArchiveBlob{BackupID: f.p.BackupID, DestinationID: f.p.DestinationID, DaemonID: f.p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 1024, Format: domain.HostingStorageArchiveFormat}
	f.restorePlan(blob)
	if _, err := f.e.InspectWriters(t.Context(), f.p); err == nil {
		t.Fatal("restore obtained source writer inventory")
	}
	p := f.p
	p.OperationID = "target_recovery"
	p.Mode = "recover"
	p.RecoveryOfOperationID = f.p.OperationID
	p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: f.p.Hash}
	archiveHash(&p)
	if p.Validate() != nil || f.e.Prepare(t.Context(), p) != nil {
		t.Fatal("valid target recovery preparation failed")
	}
	if _, err := f.e.InspectWriters(t.Context(), p); err == nil {
		t.Fatal("target recovery obtained source writer inventory")
	}
	if f.mutations != 0 || f.starts != 0 {
		t.Fatal("inventory validation caused a mutation")
	}
}

func TestStorageArchiveEngineAbsentHelperRequiresExactSuccessfulListingAndAuthority(t *testing.T) {
	for _, failure := range []string{"list_error", "unexpected_id", "invalid_saved_id", "authority"} {
		t.Run(failure, func(t *testing.T) {
			f := archiveFixture(t)
			id := strings.Repeat("d", 64)
			before := f.before
			switch failure {
			case "list_error":
				f.idListFailure = true
			case "unexpected_id":
				f.idListForeign = true
			case "invalid_saved_id":
				id = archiveHelperName(f.p, "helper_receipt")
			case "authority":
				before = func(context.Context) error { return errors.New("expired") }
			}
			if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", before) == nil || f.mutations != 0 {
				t.Fatal("unproven helper absence admitted or mutated")
			}
		})
	}
}

func TestStorageArchiveEngineObservedImageEnvironmentKeepsContainerOverride(t *testing.T) {
	f := archiveFixture(t)
	// Config projected from the accepted scratch recipe's physical image inspect.
	// Daemon/volume authority remains the isolated fake fixture's authority.
	f.p.Helper.ImageID = "sha256:cf90ca4bae9d117a4671eea1591612e94d48fa4b1cccf8af9a8a5a311b85e88d"
	f.p.Helper.ExecutableSHA256 = "sha256:1e9e58b76d5efcbcad419d94bedef83960391b2e75690b8a8007b3f1e59bc164"
	f.p.Helper.SourceCommit = "774169ede7d8e430df8978dbdad429a67a994f6b"
	f.p.Helper.Hash = ""
	f.p.Helper.Hash = domain.HostingDocumentHash(f.p.Helper)
	archiveHash(&f.p)
	f.e.config.Helper = f.p.Helper
	var config map[string]any
	if json.Unmarshal([]byte(`{
		"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
		"Entrypoint":["/nerocd-storage-archive"],"WorkingDir":"/",
		"Labels":{
			"io.nerocd.storage-archive.executable-sha256":"1e9e58b76d5efcbcad419d94bedef83960391b2e75690b8a8007b3f1e59bc164",
			"io.nerocd.storage-archive.recipe":"scratch-v1",
			"io.nerocd.storage-archive.source-commit":"774169ede7d8e430df8978dbdad429a67a994f6b"
		}
	}`), &config) != nil {
		t.Fatal("observed image configuration fixture failed")
	}
	f.imageMutation = func(image map[string]any) { image["Config"] = config }
	if f.e.Prepare(t.Context(), f.p) != nil || f.e.check(t.Context(), f.p, false) != nil || f.mutations != 0 {
		t.Fatal("observed scratch image profile rejected or mutated during preparation")
	}
	// checkCreateArgs independently requires the exact two override flags and
	// absolute immutable image/entrypoint profile during this creation.
	id := f.stoppedHelper()
	helper := f.helpers[id]
	if !slices.Equal(helper.Config.Env, []string{"PATH=/", "HOME=/"}) {
		t.Fatal("image environment escaped the fixed container override")
	}
	for name, environment := range map[string][]string{
		"inherited_default_path": {"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/"},
		"missing_home":           {"PATH=/"},
		"duplicate_path":         {"PATH=/", "HOME=/", "PATH=/"},
		"additional_key":         {"PATH=/", "HOME=/", "PRIVATE=environment_canary"},
	} {
		t.Run(name, func(t *testing.T) {
			changed := helper
			changed.Config.Env = environment
			f.helpers[id] = changed
			mutations := f.mutations
			if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) == nil || f.mutations != mutations {
				t.Fatal("changed container environment gained cleanup authority")
			}
		})
	}
}

func TestStorageArchiveEngineImageEnvironmentProfile(t *testing.T) {
	const observed = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, tc := range []struct {
		name        string
		environment any
		allowed     bool
	}{
		{"legacy_nil", nil, true},
		{"legacy_empty", []string{}, true},
		{"docker_default", []string{observed}, true},
		{"duplicate", []string{observed, observed}, false},
		{"additional_home", []string{observed, "HOME=/"}, false},
		{"additional_private", []string{observed, "PRIVATE=environment_canary"}, false},
		{"changed_path", []string{"PATH=/usr/bin:/bin"}, false},
		{"container_path", []string{"PATH=/"}, false},
		{"lowercase_key", []string{"path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, false},
		{"missing_equals", []string{"PATH"}, false},
		{"trailing_separator", []string{observed + ":"}, false},
		{"trailing_space", []string{observed + " "}, false},
		{"nonarray", observed, false},
		{"nonstring_member", []any{123}, false},
		{"object", map[string]string{"PATH": "/"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := archiveFixture(t)
			f.imageMutation = func(image map[string]any) { image["Config"].(map[string]any)["Env"] = tc.environment }
			// Reinspect an already-prepared engine as well as preparing it anew.
			if (f.e.check(t.Context(), f.p, false) == nil) != tc.allowed {
				t.Fatal("image environment drift check disagreed with closed profile")
			}
			if (f.e.Prepare(t.Context(), f.p) == nil) != tc.allowed || f.mutations != 0 {
				t.Fatal("image environment preparation disagreed with closed profile or mutated")
			}
			if !tc.allowed && f.e.prepared.Hash != "" {
				t.Fatal("failed image profile retained preparation approval")
			}
		})
	}
}

func TestStorageArchiveEngineCapabilitySpellings(t *testing.T) {
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			legacy, canonical := []string{"DAC_READ_SEARCH"}, []string{"CAP_DAC_READ_SEARCH"}
			if mode == "restore" {
				legacy, canonical = []string{"CHOWN", "FOWNER"}, []string{"CAP_CHOWN", "CAP_FOWNER"}
			}
			type capabilityCase struct {
				name    string
				caps    []string
				allowed bool
			}
			cases := []capabilityCase{
				{"legacy", legacy, true},
				{"docker_canonical", canonical, true},
				{"missing", nil, false},
				{"empty", []string{}, false},
				{"duplicate", append(slices.Clone(legacy), legacy[0]), false},
				{"alias_duplicate", append(slices.Clone(legacy), canonical[0]), false},
				{"unknown", append(slices.Clone(legacy), "CAP_SYS_ADMIN"), false},
				{"unexpected_mode", []string{"CHOWN"}, false},
			}
			if mode == "restore" {
				cases[len(cases)-1].caps = []string{"CAP_DAC_READ_SEARCH", "FOWNER"}
				cases = append(cases,
					capabilityCase{"reordered_mixed_spelling", []string{"CAP_FOWNER", "CHOWN"}, true},
					capabilityCase{"missing_chown", []string{"FOWNER"}, false},
					capabilityCase{"missing_fowner", []string{"CHOWN"}, false})
			}
			for _, malformed := range []struct{ name, value string }{
				{"unknown_member", "CAP_SYS_ADMIN"},
				{"repeated_prefix", "CAP_" + canonical[0]},
				{"lowercase", strings.ToLower(canonical[0])},
				{"mixed_case", "Cap_" + legacy[0]},
				{"leading_space", " " + canonical[0]},
				{"trailing_space", canonical[0] + " "},
				{"empty_member", ""},
				{"prefix_only", "CAP_"},
			} {
				caps := []string{malformed.value}
				if mode == "restore" {
					// Keep the second required member, so the malformed spelling
					// is tested independently of cardinality rejection.
					caps = append(caps, "FOWNER")
				}
				cases = append(cases, capabilityCase{malformed.name, caps, false})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := archiveFixture(t)
					id := f.stoppedHelper()
					if mode == "restore" {
						blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", f.before)
						if err != nil || f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
							t.Fatal("backup fixture failed")
						}
						f.restorePlan(blob)
						req := archiveTargetRequest(f.p)
						req.CreatePermitted = true
						if _, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before); err != nil {
							t.Fatal("restore target fixture failed")
						}
					} else if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
						t.Fatal("backup helper fixture cleanup failed")
					}
					// Keep the actual create argument assertion. Only the daemon's
					// inspected spelling differs, as in the physical Docker receipt.
					f.e.image.command = imageCommandFunc(func(ctx context.Context, binary string, args []string, dir string, env []string) ([]byte, error) {
						raw, err := f.command(ctx, binary, args, dir, env)
						if err == nil && len(args) > 5 && args[4] == "container" && args[5] == "inspect" {
							var h archiveHelperContainer
							if json.Unmarshal(raw, &h) != nil {
								t.Fatal("helper inspection fixture failed")
							}
							h.HostConfig.CapAdd = tc.caps
							return json.Marshal(h)
						}
						return raw, err
					})
					mutations, starts := f.mutations, f.starts
					id, err := f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before)
					if (err == nil) != tc.allowed || id != strings.Repeat("d", 64) || f.mutations != mutations+1 || f.starts != starts {
						t.Fatal("capability profile disagreed with closed set or started an unverified helper")
					}
					mutations = f.mutations
					if (f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) == nil) != tc.allowed {
						t.Fatal("capability inspection disagreed during cleanup")
					}
					if !tc.allowed && (f.mutations != mutations || len(f.helpers) != 1) {
						t.Fatal("rejected helper was mutated or discarded")
					}
				})
			}
		})
	}
}

func TestStorageArchiveEngineCapabilityDropRemainsExact(t *testing.T) {
	for name, drop := range map[string][]string{
		"missing": nil, "empty": {}, "prefixed": {"CAP_ALL"},
		"duplicate": {"ALL", "ALL"}, "lowercase": {"all"}, "extra": {"ALL", "CHOWN"},
	} {
		t.Run(name, func(t *testing.T) {
			f := archiveFixture(t)
			id := f.stoppedHelper()
			h := f.helpers[id]
			h.HostConfig.CapAdd = []string{"CAP_DAC_READ_SEARCH"}
			h.HostConfig.CapDrop = drop
			f.helpers[id] = h
			mutations := f.mutations
			if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) == nil || f.mutations != mutations || len(f.helpers) != 1 {
				t.Fatal("changed capability drop profile gained mutation authority")
			}
		})
	}
}

func TestStorageArchiveEngineHelperProvenanceAndSandboxDriftRejects(t *testing.T) {
	for _, failure := range []string{"image", "label", "entrypoint", "privilege", "device", "network", "source_write", "environment"} {
		t.Run(failure, func(t *testing.T) {
			f := archiveFixture(t)
			id := f.stoppedHelper()
			if failure == "image" || failure == "label" || failure == "entrypoint" {
				f.imageMutation = func(image map[string]any) {
					switch failure {
					case "image":
						image["Id"] = "sha256:" + strings.Repeat("0", 64)
					case "label":
						image["Config"].(map[string]any)["Labels"] = map[string]string{}
					case "entrypoint":
						image["Config"].(map[string]any)["Entrypoint"] = []string{"/bin/sh"}
					}
				}
			} else {
				h := f.helpers[id]
				switch failure {
				case "privilege":
					h.HostConfig.Privileged = true
				case "device":
					h.HostConfig.DeviceRequests = []json.RawMessage{json.RawMessage(`{}`)}
				case "network":
					h.HostConfig.NetworkMode = "host"
				case "source_write":
					h.Mounts[0].RW = true
				case "environment":
					h.Config.Env = []string{"PRIVATE=value"}
				}
				f.helpers[id] = h
			}
			mutations := f.mutations
			if f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) == nil || f.mutations != mutations {
				t.Fatal("unowned or unsafe helper was mutated")
			}
		})
	}
}

func TestStorageArchiveEngineRestoreRejectsNonemptyAliasForeignAndChangedArtifact(t *testing.T) {
	for _, failure := range []string{"nonempty", "alias", "foreign", "digest", "admission"} {
		t.Run(failure, func(t *testing.T) {
			f := archiveFixture(t)
			id := f.stoppedHelper()
			blob, err := f.e.Backup(t.Context(), f.p, id, "helper_receipt", f.before)
			if err != nil || f.e.CleanupHelper(t.Context(), f.p, id, "helper_receipt", f.before) != nil {
				t.Fatal("backup fixture")
			}
			f.restorePlan(blob)
			req := archiveTargetRequest(f.p)
			req.CreatePermitted = true
			if _, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before); err != nil {
				t.Fatal("target fixture")
			}
			// Reconciliation may inspect this exact previously admitted empty
			// target, but cannot create it again.
			req.CreatePermitted = false
			mutations := f.mutations
			if _, err := f.e.EnsureRestoreVolume(t.Context(), req, f.before); err != nil || f.mutations != mutations {
				t.Fatal("read-only target reconciliation recreated volume")
			}
			target := f.volumes[req.Storage.VolumeName]["Mountpoint"].(string)
			switch failure {
			case "nonempty":
				if os.WriteFile(filepath.Join(target, storagearchive.IncompleteMarker), []byte("held"), 0o600) != nil {
					t.Fatal("marker fixture")
				}
			case "alias":
				f.volumes[req.Storage.VolumeName]["Mountpoint"] = f.volumes[f.p.SourceStorage.VolumeName]["Mountpoint"]
			case "foreign":
				f.volumes[req.Storage.VolumeName]["Labels"] = map[string]string{}
			}
			if failure == "nonempty" || failure == "alias" || failure == "foreign" {
				if id, err := f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before); err == nil || id != "" || f.mutations != mutations {
					t.Fatal("unsafe target received a helper")
				}
				return
			}
			id, err = f.e.CreateHelper(t.Context(), f.p, "helper_receipt", f.before)
			if err != nil {
				t.Fatal("restore helper fixture")
			}
			before := f.before
			if failure == "digest" {
				path := filepath.Join(f.e.config.ArtifactRoot, strings.TrimPrefix(blob.SHA256, "sha256:"))
				if os.WriteFile(path, []byte("private changed archive"), 0o600) != nil {
					t.Fatal("corrupt CAS fixture")
				}
			} else {
				before = func(context.Context) error { return errors.New("expired") }
			}
			mutations = f.mutations
			if f.e.Restore(t.Context(), f.p, id, "helper_receipt", before) == nil || f.mutations != mutations {
				t.Fatal("invalid archive or stale authority extracted")
			}
		})
	}
}

func TestStorageArchiveEngineFactoryRejectsUnqualifiedHost(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && os.Geteuid() == 0 {
		t.Skip("host rejection case targets an unqualified host")
	}
	f := archiveFixture(t)
	config := StorageArchiveExecutionConfig{ArtifactRoot: archiveTestRoot(t), TemporaryRoot: archiveTestRoot(t), DockerBinary: "/usr/bin/docker", Helper: f.p.Helper}
	if engine, err := openStorageArchiveEngine(t.Context(), config, ""); err == nil || engine != nil {
		t.Fatal("unsupported host opened real daemon engine")
	}
}

func TestStorageArchiveStreamChild(t *testing.T) {
	if !slices.Contains(os.Args, "archive-stream-child") {
		return
	}
	if _, err := os.Stdout.Write([]byte("started")); err != nil {
		os.Exit(1)
	}
	for {
		time.Sleep(time.Second)
	}
}

type archiveHandshakeWriter struct {
	started chan struct{}
	once    sync.Once
}

func (w *archiveHandshakeWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}

func TestStorageArchiveOSStreamCancellationJoinsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out := &archiveHandshakeWriter{started: make(chan struct{})}
	done := make(chan struct{})
	var err error
	dir := t.TempDir()
	go func() {
		err = archiveOSStream(ctx, os.Args[0], []string{"-test.run=^TestStorageArchiveStreamChild$", "--", "archive-stream-child"}, dir, nil, out)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("archive process cleanup did not join")
		}
	})
	select {
	case <-out.started:
	case <-time.After(2 * time.Second):
		t.Fatal("archive child never started")
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("archive child did not join")
	}
	if !errors.Is(err, context.Canceled) || time.Since(start) > 4*time.Second {
		t.Fatal("owned archive command did not join on cancellation")
	}
}
