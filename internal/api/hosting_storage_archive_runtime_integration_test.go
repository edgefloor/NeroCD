//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/store"
)

func TestHostingStorageArchiveCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_STORAGE_ARCHIVE_RUNTIME") != "disposable-dind" {
		t.Skip("requires explicit archive storage runtime gate")
	}
	testHostingStorageCompiledRunnerHTTPS(t, true)
}

func storageRuntimeConfigureArchive(t *testing.T, f *storageRuntimeFixture, pg *store.PostgresStore) {
	t.Helper()
	raw, err := os.ReadFile("/fixture/archive-helper.json")
	if err != nil {
		t.Fatal(err)
	}
	var helper domain.HostingStorageArchiveHelper
	if domain.DecodeHostingStorageArchiveJSON(raw, &helper) != nil || helper.Validate() != nil {
		t.Fatal("invalid exact local helper descriptor")
	}
	actual := strings.TrimSpace(string(storageRuntimeDocker(t, "info", "--format", "{{.ID}} {{.OSType}} {{.Architecture}}")))
	if actual != helper.DaemonID+" linux aarch64" {
		t.Fatal("archive fixture helper daemon/profile mismatch")
	}
	roots := []string{filepath.Join(f.root, "archive-artifacts"), filepath.Join(f.root, "archive-temporary")}
	for _, root := range roots {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw, err = json.Marshal(struct {
		Version       int                                `json:"version"`
		ArtifactRoot  string                             `json:"artifact_root"`
		TemporaryRoot string                             `json:"temporary_root"`
		DockerBinary  string                             `json:"docker_binary"`
		Helper        domain.HostingStorageArchiveHelper `json:"helper"`
	}{1, roots[0], roots[1], "/usr/local/bin/docker", helper})
	if err != nil {
		t.Fatal(err)
	}
	f.archiveConfig = filepath.Join(f.root, "archive-config.json")
	if err := os.WriteFile(f.archiveConfig, raw, 0600); err != nil {
		t.Fatal(err)
	}
	limits := domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 512 << 10, FileBytes: 128 << 10, Members: 1000, Depth: 32, PathBytes: 255}
	if err := pg.ConfigureStorageArchive(&helper, limits); err != nil {
		t.Fatal(err)
	}
	t.Logf("archive configured source=%s executable=%s image=%s platform=%s/%s daemon=%s helper=%s", helper.SourceCommit, helper.ExecutableSHA256, helper.ImageID, helper.OS, helper.Architecture, helper.DaemonID, helper.Hash)
}

func storageArchiveRuntimeLose(f *storageRuntimeFixture, fault *storageRuntimeFault, operation string) runner.JournalStorageArchive {
	f.t.Helper()
	select {
	case <-fault.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatal("archive committed completion fault did not enter", f.logs.String())
	}
	f.stop()
	select {
	case <-fault.finished:
	case <-time.After(10 * time.Second):
		f.t.Fatal("archive fault handler did not join")
	}
	journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot := journal.Snapshot()
	if err := journal.Close(); err != nil {
		f.t.Fatal(err)
	}
	for _, entry := range snapshot.StorageArchive {
		if entry.Plan.OperationID == operation && entry.Complete != nil {
			f.t.Logf("durable archive completion operation=%s receipt=%s result=%s resume=%s", operation, entry.ID, entry.Complete.Result.ArchiveOutcome, entry.Complete.Result.ResumeOutcome)
			if entry.Complete.Result.FailureCode == "helper_failed" {
				storageArchiveHelperFailureProfile(f, entry, snapshot.StorageArchive)
			}
			return entry
		}
	}
	f.t.Fatal("archive completion missing from private durable journal")
	return runner.JournalStorageArchive{}
}

// Inspect only the exact uncertain ID already retained by this owned attempt.
// Diagnostics contain closed comparisons, never raw Docker configuration or authority.
func storageArchiveHelperFailureProfile(f *storageRuntimeFixture, complete runner.JournalStorageArchive, entries []runner.JournalStorageArchive) {
	f.t.Helper()
	if os.Getenv("NEROCD_STORAGE_ARCHIVE_RUNTIME") != "disposable-dind" || !strings.HasPrefix(filepath.Base(f.root), storageRuntimeOwner+"-") {
		f.t.Log("archive helper diagnostic unavailable: fixture ownership missing")
		return
	}
	var uncertain *runner.JournalStorageArchive
	for i := range entries {
		e := &entries[i]
		if e.Phase == "helper_unverified" && e.Authority() == complete.Authority() && reflect.DeepEqual(e.Plan, complete.Plan) {
			if uncertain != nil {
				f.t.Log("archive helper diagnostic unavailable: ambiguous durable identity")
				return
			}
			uncertain = e
		}
	}
	if uncertain == nil {
		f.t.Log("archive helper diagnostic unavailable: no exact unverified ID")
		return
	}
	p := uncertain.Plan
	sum := sha256.Sum256([]byte("archive\x00" + uncertain.Attempt.LeaseID + "\x00" + fmt.Sprint(uncertain.Attempt.Attempt) + "\x00helper_intent"))
	nonce := fmt.Sprintf("archive_%x", sum[:16])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspect := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...).Output()
	}
	daemon, err := inspect("info", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(daemon)) != p.Helper.DaemonID {
		f.t.Log("archive helper diagnostic unavailable: daemon mismatch")
		return
	}
	raw, err := inspect("container", "inspect", uncertain.UncertainHelperContainerID)
	var objects []map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &objects) != nil || len(objects) != 1 {
		f.t.Log("archive helper diagnostic unavailable: exact inspection failed")
		return
	}
	var id, image string
	var config, host, state map[string]json.RawMessage
	if json.Unmarshal(objects[0]["Id"], &id) != nil || json.Unmarshal(objects[0]["Image"], &image) != nil || json.Unmarshal(objects[0]["Config"], &config) != nil || json.Unmarshal(objects[0]["HostConfig"], &host) != nil || json.Unmarshal(objects[0]["State"], &state) != nil {
		f.t.Log("archive helper diagnostic unavailable: malformed profile")
		return
	}
	labels := map[string]string{"io.nerocd.storage-archive.recipe": "scratch-v1", "io.nerocd.storage-archive.source-commit": p.Helper.SourceCommit, "io.nerocd.storage-archive.executable-sha256": strings.TrimPrefix(p.Helper.ExecutableSHA256, "sha256:"), "io.nerocd.archive.operation": p.OperationID, "io.nerocd.archive.plan": p.Hash, "io.nerocd.archive.nonce": nonce, "io.nerocd.archive.daemon": p.Helper.DaemonID, "io.nerocd.archive.helper": p.Helper.Hash, "io.nerocd.archive.mode": p.Mode, "io.nerocd.archive.source": p.SourceStorage.Hash}
	if p.Target != nil {
		labels["io.nerocd.archive.target"] = p.Target.Storage.Hash
	}
	match := func(raw json.RawMessage, expected any) bool {
		var got, want any
		encoded, marshalErr := json.Marshal(expected)
		return marshalErr == nil && json.Unmarshal(raw, &got) == nil && json.Unmarshal(encoded, &want) == nil && reflect.DeepEqual(got, want)
	}
	if id != uncertain.UncertainHelperContainerID || image != p.Helper.ImageID || !match(config["Labels"], labels) {
		f.t.Log("archive helper diagnostic unavailable: operation/plan/nonce/image ownership mismatch")
		return
	}
	environmentSummary, _ := json.Marshal(storageDiagnosticHelperEnvironment(config["Env"]))
	f.t.Logf("archive helper closed environment projection=%s", environmentSummary)
	mode, caps := "create", []string{"DAC_READ_SEARCH"}
	if p.Mode == "restore" {
		mode, caps = "restore", []string{"CHOWN", "FOWNER"}
	}
	l := p.Limits
	command := []string{mode, fmt.Sprintf("--max-archive-bytes=%d", l.ArchiveBytes), fmt.Sprintf("--max-data-bytes=%d", l.DataBytes), fmt.Sprintf("--max-file-bytes=%d", l.FileBytes), fmt.Sprintf("--max-members=%d", l.Members), fmt.Sprintf("--max-depth=%d", l.Depth), fmt.Sprintf("--max-path-bytes=%d", l.PathBytes)}
	memory := l.ArchiveBytes + (128 << 20)
	groups := map[string]struct {
		actual   map[string]json.RawMessage
		expected map[string]any
	}{
		"config": {config, map[string]any{"Cmd": command, "Entrypoint": []string{"/nerocd-storage-archive"}, "Env": []string{"PATH=/", "HOME=/"}, "User": "0:0", "Hostname": "nerocd-archive", "Tty": false}},
		"host":   {host, map[string]any{"ReadonlyRootfs": true, "Privileged": false, "PublishAllPorts": false, "AutoRemove": false, "NetworkMode": "none", "PidMode": "", "IpcMode": "private", "UsernsMode": "", "UTSMode": "", "CgroupnsMode": "private", "CapDrop": []string{"ALL"}, "CapAdd": caps, "SecurityOpt": []string{"no-new-privileges"}, "Memory": memory, "MemorySwap": memory, "NanoCpus": int64(1000000000), "PidsLimit": 64, "Tmpfs": map[string]string{"/tmp": fmt.Sprintf("rw,noexec,nosuid,nodev,size=%d,mode=1777", l.ArchiveBytes+(32<<20))}}},
		"state":  {state, map[string]any{"Running": false, "Status": "created", "Dead": false, "OOMKilled": false}},
	}
	for name, group := range groups {
		checks := map[string]bool{}
		for key, expected := range group.expected {
			checks[key] = match(group.actual[key], expected)
		}
		f.t.Logf("archive helper closed profile %s matches=%v", name, checks)
	}
	for _, key := range []string{"Binds", "VolumesFrom", "PortBindings", "Devices", "DeviceRequests", "GroupAdd"} {
		var value any
		valid := json.Unmarshal(host[key], &value) == nil
		empty := value == nil
		switch v := value.(type) {
		case []any:
			empty = len(v) == 0
		case map[string]any:
			empty = len(v) == 0
		}
		f.t.Logf("archive helper closed profile host.%s empty=%t", key, valid && empty)
	}
	var runtime string
	var logs, restart map[string]json.RawMessage
	_ = json.Unmarshal(host["Runtime"], &runtime)
	_ = json.Unmarshal(host["LogConfig"], &logs)
	_ = json.Unmarshal(host["RestartPolicy"], &restart)
	f.t.Logf("archive helper closed profile runtime=%t log_driver=%t restart=%t name=%t", runtime == "" || runtime == "runc", match(logs["Type"], "none"), match(restart["Name"], "no"), match(objects[0]["Name"], "/ncd_archive_"+domain.HostingDocumentHash(struct{ Operation, Plan, Nonce string }{p.OperationID, p.Hash, nonce})[7:39]))
	var mounts []struct {
		Type, Name, Driver, Source, Destination string
		RW                                      bool
	}
	volume, destination, writable := p.SourceStorage.VolumeName, "/source", false
	if p.Target != nil {
		volume, destination, writable = p.Target.Storage.VolumeName, "/target", true
	}
	root, rootErr := inspect("info", "--format", "{{.DockerRootDir}}")
	mountOK := rootErr == nil && json.Unmarshal(objects[0]["Mounts"], &mounts) == nil && len(mounts) <= 2
	volumeCount := 0
	for _, mount := range mounts {
		if mount.Type == "tmpfs" && mount.Destination == "/tmp" && mount.RW {
			continue
		}
		volumeCount++
		mountOK = mountOK && mount.Type == "volume" && mount.Name == volume && mount.Driver == "local" && mount.Destination == destination && mount.RW == writable && mount.Source == filepath.Join(strings.TrimSpace(string(root)), "volumes", volume, "_data")
	}
	f.t.Logf("archive helper closed profile mounts=%t", mountOK && volumeCount == 1)
	for _, key := range []string{"CapAdd", "CapDrop", "SecurityOpt"} {
		var values []string
		if json.Unmarshal(host[key], &values) == nil {
			for i, value := range values {
				switch value {
				case "ALL", "DAC_READ_SEARCH", "CAP_DAC_READ_SEARCH", "CHOWN", "CAP_CHOWN", "FOWNER", "CAP_FOWNER", "no-new-privileges", "no-new-privileges=true":
				default:
					values[i] = "other"
				}
			}
			f.t.Logf("archive helper closed profile host.%s observed=%v", key, values)
		}
	}
}

func storageArchiveRuntimeReplay(f *storageRuntimeFixture, operation string, before int64) {
	f.t.Helper()
	f.start()
	for deadline := time.Now().Add(20 * time.Second); f.archiveCompletions.Load() <= before; {
		if time.Now().After(deadline) {
			f.t.Fatal("archive saved completion was not replayed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.await(operation, "succeeded")
}

func storageArchiveWriterRunning(t *testing.T, id string) bool {
	t.Helper()
	var inspected []struct {
		ID    string `json:"Id"`
		State struct{ Running bool }
	}
	if err := json.Unmarshal(storageRuntimeDocker(t, "container", "inspect", id), &inspected); err != nil || len(inspected) != 1 || inspected[0].ID != id {
		t.Fatal("exact archive writer inspection failed", err)
	}
	return inspected[0].State.Running
}

func storageArchiveFileEvidence(t *testing.T, volume string) string {
	t.Helper()
	var inspected []struct{ Name, Mountpoint string }
	if err := json.Unmarshal(storageRuntimeDocker(t, "volume", "inspect", volume), &inspected); err != nil || len(inspected) != 1 || inspected[0].Name != volume {
		t.Fatal("archive metadata volume inspect", err)
	}
	directory := inspected[0].Mountpoint
	var evidence strings.Builder
	for _, relative := range []string{"saved", "nested", "nested/probe"} {
		info, err := os.Lstat(filepath.Join(directory, relative))
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("numeric ownership unavailable")
		}
		size := info.Size()
		if info.IsDir() {
			size = 0
		}
		fmt.Fprintf(&evidence, "%s:%d:%d:%04o:%d;", relative, stat.Uid, stat.Gid, info.Mode().Perm(), size)
	}
	return evidence.String()
}

// Count immutable helper lifecycle events rather than container names: completed
// helpers are intentionally removed, and a lost ACK must not create a second one.
func storageArchiveHelperStarts(t *testing.T, operation, since string) int {
	t.Helper()
	raw := storageRuntimeDocker(t, "events", "--since", since, "--until", time.Now().Add(time.Second).Format(time.RFC3339Nano), "--filter", "type=container", "--filter", "event=start", "--filter", "label=io.nerocd.archive.operation="+operation, "--format", "{{json .}}")
	var count int
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event struct {
			Actor struct {
				ID         string
				Attributes map[string]string
			}
		}
		if json.Unmarshal(line, &event) != nil || event.Actor.Attributes["io.nerocd.archive.operation"] != operation || len(event.Actor.ID) != 64 {
			t.Fatal("invalid exact helper event")
		}
		count++
	}
	return count
}

func storageRuntimeArchiveRoundtrip(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, source domain.HostingStorage, origin domain.HostingStorageMaterializedRequest, a, b domain.HostingOperation, upload []byte) {
	t.Helper()
	if storageArchiveWriterRunning(t, a.Result.ContainerID) || !storageArchiveWriterRunning(t, b.Result.ContainerID) {
		t.Fatal("backup fixture does not preserve original stopped/running inventory")
	}
	f.https("storage-runtime.example.test", "POST", "/metadata", nil, 204)
	sourceMetadata := storageArchiveFileEvidence(t, source.VolumeName)
	if !strings.Contains(sourceMetadata, "saved:12345:12346:0640:") || !strings.Contains(sourceMetadata, "nested:12345:12346:0750:") {
		t.Fatal("nontrivial ownership/mode fixture missing", sourceMetadata)
	}
	since := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	fault := f.arm("/api/v1/runners/hosting/storage-archive/complete")
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", domain.HostingStorageBackupInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: resource.Generation}, 202)
	entry := storageArchiveRuntimeLose(f, fault, backup.OperationID)
	if entry.Complete.Result.ArchiveOutcome != "succeeded" || entry.Complete.Result.ResumeOutcome != "resumed" || entry.Complete.Result.Uncertain || entry.Complete.Result.Blob == nil {
		t.Fatal("backup did not complete before ACK loss", entry.Complete.Result)
	}
	var accepted domain.HostingOperation
	if json.Unmarshal(fault.response, &accepted) != nil || accepted.CompleteReceipt == nil || accepted.CompleteReceipt.BodyHash != domain.HostingDocumentHash(*entry.Complete) {
		t.Fatal("backup exact committed receipt missing")
	}
	if starts := storageArchiveHelperStarts(t, backup.OperationID, since); starts != 1 {
		t.Fatalf("backup helper starts=%d want1", starts)
	}
	requests := f.archiveCompletions.Load()
	storageArchiveRuntimeReplay(f, backup.OperationID, requests)
	if starts := storageArchiveHelperStarts(t, backup.OperationID, since); starts != 1 {
		t.Fatalf("backup completion replay repeated helper starts=%d", starts)
	}
	if got := f.https("storage-runtime.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, upload) {
		t.Fatal("source upload changed after backup")
	}
	if storageArchiveWriterRunning(t, a.Result.ContainerID) || !storageArchiveWriterRunning(t, b.Result.ContainerID) {
		t.Fatal("backup changed originally-stopped writer")
	}
	backup = storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backup.ID, nil, 200)
	if backup.Blob == nil || backup.State != "succeeded" {
		t.Fatal("public backup metadata did not settle")
	}
	restoreLoss := f.arm("/api/v1/runners/hosting/storage-archive/complete")
	restored := storageRuntimeJSON[domain.HostingStorageRestore](f, "POST", "/api/v1/storage-backups/"+backup.ID+"/restores", domain.HostingStorageRestoreInput{RequestID: strings.Repeat("b", 32), Name: "restored uploads"}, 202)
	if restored.Storage.ID == source.ID || restored.Storage.VolumeName == source.VolumeName || restored.Resource.ID == resource.ID {
		t.Fatal("restore reused original identity")
	}
	restoreEntry := storageArchiveRuntimeLose(f, restoreLoss, restored.Operation.ID)
	if !restoreEntry.Complete.Result.RestoreVerified || !reflect.DeepEqual(restoreEntry.Complete.Result.Blob, backup.Blob) || restoreEntry.Complete.Result.Uncertain {
		t.Fatal("restore did not verify original immutable archive")
	}
	targetMetadata := storageArchiveFileEvidence(t, restored.Storage.VolumeName)
	if targetMetadata != sourceMetadata {
		t.Fatal("restored UID/GID/modes differ", sourceMetadata, targetMetadata)
	}
	targetCreated := strings.TrimSpace(string(storageRuntimeDocker(t, "volume", "inspect", "--format", "{{.CreatedAt}}", restored.Storage.VolumeName)))
	requests = f.archiveCompletions.Load()
	storageArchiveRuntimeReplay(f, restored.Operation.ID, requests)
	if storageArchiveHelperStarts(t, restored.Operation.ID, since) != 1 || strings.TrimSpace(string(storageRuntimeDocker(t, "volume", "inspect", "--format", "{{.CreatedAt}}", restored.Storage.VolumeName))) != targetCreated {
		t.Fatal("restore replay repeated extraction or recreated volume")
	}
	deployed := f.await(f.deploy(restored.Resource, "restore-ordinary-deploy", "/", 20104, nil).ID, "succeeded")
	if deployed.Result == nil || !deployed.Result.Ready {
		t.Fatal("ordinary restored application deploy not ready")
	}
	storageRuntimeJSON[domain.HostingHostnameGrant](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "storage-restored.example.test"}, 201)
	route := storageRuntimeJSON[domain.HostingRoute](f, "POST", "/api/v1/resources/"+restored.Resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "restored-route", Hostname: "storage-restored.example.test", PathPrefix: "/", ExpectedHealthyOperationID: deployed.ID}, 202)
	f.proxyAwait(route.OperationID)
	if got := f.https("storage-restored.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, upload) {
		t.Fatal("restored application did not read old upload")
	}
	replacement := make([]byte, 65539)
	if _, err := rand.Read(replacement); err != nil {
		t.Fatal(err)
	}
	f.https("storage-restored.example.test", "PUT", "/upload", replacement, 201)
	f.https("storage-restored.example.test", "POST", "/rename", nil, 204)
	if got := f.https("storage-restored.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, replacement) {
		t.Fatal("restored application did not write/read new upload")
	}
	if got := f.https("storage-runtime.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, upload) {
		t.Fatal("restored writes changed source volume")
	}
	if storageArchiveFileEvidence(t, source.VolumeName) != sourceMetadata {
		t.Fatal("source ownership or modes changed during restore")
	}
	// A bounded oversized file is created by the ordinary application. The helper
	// must fail after the acknowledged stopped phase, then safely resume B only.
	f.https("storage-runtime.example.test", "POST", "/oversize", nil, 201)
	failedBackup := storageRuntimeJSON[domain.HostingStorageBackup](f, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", domain.HostingStorageBackupInput{RequestID: strings.Repeat("c", 32), ExpectedResourceGeneration: resource.Generation}, 202)
	failed := f.await(failedBackup.OperationID, "failed")
	if failed.StorageArchive == nil || failed.StorageArchive.Result == nil || failed.StorageArchive.Result.ArchiveOutcome != "failed" || failed.StorageArchive.Result.ResumeOutcome != "resumed" || failed.StorageArchive.Result.Uncertain || failed.MutationHold {
		t.Fatal("failed backup did not truthfully settle compensated writers", failed.StorageArchive)
	}
	phases := map[string]bool{}
	for _, p := range failed.StorageArchive.Progress {
		phases[p.Phase] = true
	}
	if !phases["stopped"] || !phases["helper_identified"] || !phases["resumed"] || phases["archive_published"] {
		t.Fatal("failure did not occur after quiescence/before publication", phases)
	}
	if storageArchiveWriterRunning(t, a.Result.ContainerID) || !storageArchiveWriterRunning(t, b.Result.ContainerID) {
		t.Fatal("failed backup resumed originally-stopped writer or left B stopped")
	}
	f.https("storage-runtime.example.test", "DELETE", "/oversize", nil, 204)
	if got := f.https("storage-runtime.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, upload) {
		t.Fatal("failed backup changed serving upload")
	}
	if leftovers := strings.TrimSpace(string(storageRuntimeDocker(t, "ps", "-aq", "--filter", "label=io.nerocd.archive.helper"))); leftovers != "" {
		t.Fatal("archive helper remains after settlement", leftovers)
	}
	t.Logf("physical archive backup=%s restore=%s failed_backup=%s source_volume=%s restored_volume=%s source_upload=%x restored_new_upload=%x source_metadata=%s helper_backup_starts=1 helper_restore_starts=1 source_origin=%s", backup.OperationID, restored.Operation.ID, failedBackup.OperationID, source.VolumeName, restored.Storage.VolumeName, sha256.Sum256(upload), sha256.Sum256(replacement), sourceMetadata, origin.CreationOperationID)
}
