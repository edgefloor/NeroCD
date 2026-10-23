//go:build linux

package serverbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ownedBase = "/var/lib/nerocd-bootstrap"

type helperRequest struct {
	Action      string          `json:"action"`
	OperationID string          `json:"operation_id"`
	Manifest    InstallManifest `json:"manifest"`
}

type ownedReceipt struct {
	OperationID    string `json:"operation_id"`
	Action         string `json:"action"`
	Manifest       string `json:"manifest_sha256"`
	Complete       bool   `json:"complete"`
	Before         string `json:"before_invocation"`
	StopResult     string `json:"stop_result,omitempty"`
	StopExecCode   int    `json:"stop_exec_code,omitempty"`
	StopExecStatus int    `json:"stop_exec_status,omitempty"`
}

// ServeOwnedHelper is the fixed root-only shipped-binary protocol bridge. It is
// not a controller authorization endpoint and accepts no arbitrary commands.
func ServeOwnedHelper(parent context.Context, version string, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, 115*time.Second)
	defer cancel()
	var request helperRequest
	decoder := json.NewDecoder(io.LimitReader(input, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return helperReply(output, ownedResponse{Code: InvalidManifest})
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || os.Geteuid() != 0 || !operationIDPattern.MatchString(request.OperationID) {
		return helperReply(output, ownedResponse{Code: InvalidManifest})
	}
	if request.Action != "install" && request.Action != "inspect" && request.Action != "restart" && request.Action != "cleanup" {
		return helperReply(output, ownedResponse{Code: InvalidManifest})
	}
	var inherited, named unix.Stat_t
	if unix.Fstat(9, &inherited) != nil || unix.Lstat("/run/nerocd-bootstrap.lock", &named) != nil || inherited.Ino != named.Ino || inherited.Dev != named.Dev || named.Uid != 0 || named.Mode&07777 != 0600 || named.Mode&unix.S_IFMT != unix.S_IFREG || unix.Flock(9, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return helperReply(output, ownedResponse{Code: OwnershipConflict})
	}
	_, digest, err := manifestBytes(request.Manifest)
	if err != nil || version != request.Manifest.Artifact.Version {
		return helperReply(output, ownedResponse{Code: InvalidArtifact})
	}
	home := filepath.Join(ownedBase, "installations", request.Manifest.InstallationID)
	if err := verifyHome(home, request.Manifest); err != nil {
		return helperReply(output, ownedResponse{Code: OwnershipConflict})
	}
	file, _, err := openArtifact(ArtifactSource{Path: home + "/agent"}, request.Manifest.Artifact)
	if err != nil {
		return helperReply(output, ownedResponse{Code: OwnershipConflict})
	}
	_ = file.Close()
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return helperReply(output, ownedResponse{Code: OwnershipConflict})
	}
	hash := sha256.New()
	count, hashErr := io.Copy(hash, io.LimitReader(self, maxArtifactSize+1))
	_ = self.Close()
	if hashErr != nil || count != request.Manifest.Artifact.Size || hex.EncodeToString(hash.Sum(nil)) != request.Manifest.Artifact.SHA256 {
		return helperReply(output, ownedResponse{Code: OwnershipConflict})
	}
	inspection, code := handleOwned(ctx, request, digest, home)
	if code != "" {
		return helperReply(output, ownedResponse{Code: code})
	}
	return helperReply(output, ownedResponse{Inspection: &inspection})
}

func helperReply(output io.Writer, response ownedResponse) error {
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return RemoteFailure
	}
	return nil
}

func verifyHome(home string, manifest InstallManifest) error {
	for _, directory := range []string{ownedBase, ownedBase + "/installations", ownedBase + "/receipts", home} {
		if err := requireOwned(directory, true, 0700); err != nil {
			return err
		}
	}
	data, err := readOwned(home+"/manifest.json", 4096, 0600)
	if err != nil {
		return err
	}
	expected, digest, err := manifestBytes(manifest)
	if err != nil || !bytes.Equal(data, expected) {
		return OwnershipConflict
	}
	claim, err := readOwned(home+"/claim", 1024, 0600)
	wantClaim := fmt.Sprintf("%s %s %s %s %d %s\n", manifest.InstallationID, manifest.InstallOperationID, digest, manifest.Artifact.SHA256, manifest.Artifact.Size, manifest.Artifact.Architecture)
	if err != nil || string(claim) != wantClaim {
		return OwnershipConflict
	}
	if verifyRuntimePaths(home, false) != nil {
		return OwnershipConflict
	}
	if err := requireOwned(home+"/runner.lock", false, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return OwnershipConflict
	}
	return nil
}

func verifyRuntimePaths(home string, required bool) error {
	for _, name := range []string{"work", "journal", "secrets"} {
		if err := requireOwned(home+"/"+name, true, 0700); err != nil {
			if !required && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return OwnershipConflict
		}
	}
	return nil
}

func removeOwnedAt(directory int, name string, isDirectory bool, mode uint32) error {
	var entry unix.Stat_t
	if err := unix.Fstatat(directory, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	wantType := uint32(unix.S_IFREG)
	flags := 0
	if isDirectory {
		wantType = unix.S_IFDIR
		flags = unix.AT_REMOVEDIR
	}
	if entry.Uid != 0 || entry.Mode&07777 != mode || entry.Mode&unix.S_IFMT != wantType {
		return OwnershipConflict
	}
	return unix.Unlinkat(directory, name, flags)
}

func cleanupOwnedHome(home string) error {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var opened unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || opened.Uid != 0 || opened.Mode&07777 != 0700 {
		return OwnershipConflict
	}
	for _, name := range []string{"enrollment", "credential", "runner.lock", "agent", "manifest.json", "claim"} {
		mode := uint32(0600)
		if name == "agent" {
			mode = 0700
		}
		if err := removeOwnedAt(fd, name, false, mode); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Removing a verified directory entry never traverses a replacement symlink.
	// Nonempty or unknown data remains in the exact installation directory.
	for _, name := range []string{"work", "journal", "secrets"} {
		if err := removeOwnedAt(fd, name, true, 0700); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOTEMPTY) {
			return err
		}
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	parent, err := unix.Open(filepath.Dir(home), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	var named unix.Stat_t
	if unix.Fstatat(parent, filepath.Base(home), &named, unix.AT_SYMLINK_NOFOLLOW) != nil || named.Ino != opened.Ino || named.Dev != opened.Dev {
		return OwnershipConflict
	}
	if err := removeOwnedAt(parent, filepath.Base(home), true, 0700); err != nil && !errors.Is(err, unix.ENOTEMPTY) {
		return err
	}
	return unix.Fsync(parent)
}

func requireOwned(path string, directory bool, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm() != mode || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		(directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return OwnershipConflict
	}
	return nil
}

func readOwned(path string, limit int64, mode os.FileMode) ([]byte, error) {
	if err := requireOwned(path, false, mode); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Size() > limit {
		return nil, OwnershipConflict
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, OwnershipConflict
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, OwnershipConflict
	}
	return data, nil
}

func writeOwned(path string, data []byte, replace bool) error {
	if replace {
		if err := requireOwned(path, false, 0600); err != nil {
			return err
		}
	}
	temp := path + ".pending"
	fd, err := unix.Open(temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if replace {
		err = os.Rename(temp, path)
	} else {
		err = os.Link(temp, path)
		if err == nil {
			err = os.Remove(temp)
		}
	}
	if err != nil {
		return err
	}
	return syncOwnedDirectory(filepath.Dir(path))
}

func syncOwnedDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func verifyUnitLink(unitPath string) error {
	path := "/etc/systemd/system/multi-user.target.wants/" + filepath.Base(unitPath)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return OwnershipConflict
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	target, err := os.Readlink(path)
	if !ok || stat.Uid != 0 || err != nil || target != unitPath {
		return OwnershipConflict
	}
	return nil
}

func removeUnitLink(unitPath string) error {
	path := "/etc/systemd/system/multi-user.target.wants/" + filepath.Base(unitPath)
	if err := verifyUnitLink(unitPath); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	fd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var entry unix.Stat_t
	if unix.Fstatat(fd, filepath.Base(path), &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || entry.Uid != 0 || entry.Mode&unix.S_IFMT != unix.S_IFLNK {
		return OwnershipConflict
	}
	link := make([]byte, 512)
	n, err := unix.Readlinkat(fd, filepath.Base(path), link)
	if err != nil || string(link[:n]) != unitPath {
		return OwnershipConflict
	}
	if err := unix.Unlinkat(fd, filepath.Base(path), 0); err != nil {
		return err
	}
	return unix.Fsync(fd)
}

func systemctl(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", arguments...)
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	command.WaitDelay = time.Second
	buffer := &boundedOutput{closeTransport: func() error {
		if command.Process != nil {
			return command.Process.Kill()
		}
		return nil
	}}
	command.Stdout, command.Stderr = buffer, buffer
	err := command.Run()
	if buffer.exceeded {
		return "", RemoteFailure
	}
	return strings.TrimSpace(string(buffer.data)), err
}

func agentUnit(manifest InstallManifest, home string) []byte {
	return []byte("[Unit]\nDescription=NeroCD owned deployment agent " + manifest.InstallationID + "\nAfter=network.target\nStartLimitIntervalSec=60\nStartLimitBurst=5\n[Service]\nType=simple\nUser=root\nUMask=0077\nExecStart=" + home + "/agent bootstrap-agent run-owned " + manifest.InstallationID + "\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=15\nKillMode=control-group\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n")
}

func parseStopEvidence(output string) (string, int, int, error) {
	values := make(map[string]string, 3)
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || (name != "Result" && name != "ExecMainCode" && name != "ExecMainStatus") {
			return "", 0, 0, RemoteFailure
		}
		if _, duplicate := values[name]; duplicate {
			return "", 0, 0, RemoteFailure
		}
		values[name] = value
	}
	if len(values) != 3 {
		return "", 0, 0, RemoteFailure
	}
	switch values["Result"] {
	case "success", "exit-code", "signal", "core-dump", "watchdog", "timeout", "resources", "protocol", "start-limit-hit", "oom-kill":
	default:
		return "", 0, 0, RemoteFailure
	}
	code, err := strconv.ParseUint(values["ExecMainCode"], 10, 8)
	if err != nil || code > 6 {
		return "", 0, 0, RemoteFailure
	}
	status, err := strconv.ParseUint(values["ExecMainStatus"], 10, 8)
	if err != nil {
		return "", 0, 0, RemoteFailure
	}
	return values["Result"], int(code), int(status), nil
}

func inspectHelper(ctx context.Context, request helperRequest, digest, home string) (OwnedInspection, ErrorCode) {
	result := OwnedInspection{InstallationID: request.Manifest.InstallationID, ManifestSHA256: digest, OperationID: request.OperationID, State: OwnedStaged, ArtifactMatches: true}
	unitPath := "/etc/systemd/system/nerocd-agent-" + request.Manifest.InstallationID + ".service"
	if _, err := os.Lstat(unitPath + ".d"); !errors.Is(err, os.ErrNotExist) {
		return OwnedInspection{}, OwnershipConflict
	}
	if verifyUnitLink(unitPath) != nil {
		return OwnedInspection{}, OwnershipConflict
	}
	data, err := readOwned(unitPath, 4096, 0600)
	if errors.Is(err, os.ErrNotExist) {
		if _, linkErr := os.Lstat("/etc/systemd/system/multi-user.target.wants/" + filepath.Base(unitPath)); !errors.Is(linkErr, os.ErrNotExist) {
			return OwnedInspection{}, OwnershipConflict
		}
		return result, ""
	}
	if err != nil || !bytes.Equal(data, agentUnit(request.Manifest, home)) {
		return OwnedInspection{}, OwnershipConflict
	}
	if verifyRuntimePaths(home, true) != nil {
		return OwnedInspection{}, OwnershipConflict
	}
	result.State = OwnedInstalled
	unit := filepath.Base(unitPath)
	state, err := systemctl(ctx, "show", unit, "--property=ActiveState", "--property=InvocationID", "--property=Job", "--property=FragmentPath", "--property=DropInPaths", "--property=LoadState")
	if err != nil {
		return OwnedInspection{}, RemoteFailure
	}
	var code ErrorCode
	result.ServiceActive, result.InvocationID, result.ServiceJobPending, code = parseUnitState(state, unitPath)
	if code != "" {
		return OwnedInspection{}, code
	}
	for _, file := range []struct {
		name string
		set  *bool
	}{{"enrollment", &result.EnrollmentFileExists}, {"credential", &result.CredentialFileExists}} {
		if err := requireOwned(home+"/"+file.name, false, 0600); err == nil {
			*file.set = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return OwnedInspection{}, OwnershipConflict
		}
	}
	return result, ""
}

func parseUnitState(output, unitPath string) (bool, string, bool, ErrorCode) {
	properties := make(map[string]string, 6)
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || len(line) > 1024 {
			return false, "", false, RemoteFailure
		}
		switch name {
		case "ActiveState", "InvocationID", "Job", "FragmentPath", "DropInPaths", "LoadState":
		default:
			return false, "", false, RemoteFailure
		}
		if _, duplicate := properties[name]; duplicate {
			return false, "", false, RemoteFailure
		}
		properties[name] = value
	}
	if len(properties) != 6 {
		return false, "", false, RemoteFailure
	}
	if properties["FragmentPath"] != unitPath || properties["DropInPaths"] != "" || properties["LoadState"] != "loaded" {
		return false, "", false, OwnershipConflict
	}
	invocation := properties["InvocationID"]
	if invocation != "" && !operationIDPattern.MatchString(invocation) {
		return false, "", false, RemoteFailure
	}
	pending := properties["Job"] != "" && properties["Job"] != "0"
	switch properties["ActiveState"] {
	case "active":
		return true, invocation, pending, ""
	case "inactive", "failed":
		return false, invocation, pending, ""
	case "activating", "reloading":
		return true, invocation, true, ""
	case "deactivating", "maintenance":
		return false, invocation, true, ""
	default:
		return false, "", false, RemoteFailure
	}
}

func handleOwned(ctx context.Context, request helperRequest, digest, home string) (OwnedInspection, ErrorCode) {
	inspection, code := inspectHelper(ctx, request, digest, home)
	if code != "" {
		return OwnedInspection{}, code
	}
	ops := filepath.Join(ownedBase, "receipts", request.Manifest.InstallationID)
	if request.Action == "inspect" {
		if head, err := readOwned(ops+"/head.json", 4096, 0600); err == nil {
			var receipt ownedReceipt
			if json.Unmarshal(head, &receipt) != nil || receipt.Manifest != digest || !operationIDPattern.MatchString(receipt.OperationID) {
				return OwnedInspection{}, OwnershipConflict
			}
			inspection.OperationID = receipt.OperationID
			if !receipt.Complete {
				inspection.State = OwnedUncertain
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return OwnedInspection{}, OwnershipConflict
		}
		return inspection, ""
	}
	if inspection.ServiceJobPending {
		return OwnedInspection{}, RemoteBusy
	}
	if err := os.Mkdir(ops, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return OwnedInspection{}, OwnershipConflict
	}
	if requireOwned(ops, true, 0700) != nil {
		return OwnedInspection{}, OwnershipConflict
	}
	if syncOwnedDirectory(filepath.Dir(ops)) != nil {
		return OwnedInspection{}, Uncertain
	}
	receiptPath := ops + "/" + request.OperationID + ".json"
	if data, err := readOwned(receiptPath, 4096, 0600); err == nil {
		var existing ownedReceipt
		if json.Unmarshal(data, &existing) != nil || existing.OperationID != request.OperationID || existing.Action != request.Action || existing.Manifest != digest {
			return OwnedInspection{}, OwnershipConflict
		}
		if !existing.Complete {
			return OwnedInspection{}, Uncertain
		}
		return inspection, ""
	} else if !errors.Is(err, os.ErrNotExist) {
		return OwnedInspection{}, OwnershipConflict
	}
	if data, err := readOwned(ops+"/head.json", 4096, 0600); err == nil {
		var head ownedReceipt
		if json.Unmarshal(data, &head) != nil || head.Manifest != digest || !head.Complete {
			return OwnedInspection{}, Uncertain
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return OwnedInspection{}, OwnershipConflict
	}
	if request.Action == "install" && inspection.State != OwnedStaged || request.Action == "restart" && inspection.State != OwnedInstalled {
		return OwnedInspection{}, OwnershipConflict
	}
	receipt := ownedReceipt{OperationID: request.OperationID, Action: request.Action, Manifest: digest, Before: inspection.InvocationID}
	pending, _ := json.Marshal(receipt) // This fixed scalar structure cannot fail encoding.
	if writeOwned(receiptPath, pending, false) != nil {
		return OwnedInspection{}, OwnershipConflict
	}
	_, headErr := os.Lstat(ops + "/head.json")
	if writeOwned(ops+"/head.json", pending, headErr == nil) != nil {
		return OwnedInspection{}, Uncertain
	}
	unitPath := "/etc/systemd/system/nerocd-agent-" + request.Manifest.InstallationID + ".service"
	unit := filepath.Base(unitPath)
	switch request.Action {
	case "install":
		for _, name := range []string{"work", "journal", "secrets"} {
			if os.Mkdir(home+"/"+name, 0700) != nil {
				return OwnedInspection{}, Uncertain
			}
		}
		if writeOwned(unitPath, agentUnit(request.Manifest, home), false) != nil {
			return OwnedInspection{}, Uncertain
		}
		if _, err := systemctl(ctx, "daemon-reload"); err != nil {
			return OwnedInspection{}, Uncertain
		}
		if _, code := inspectHelper(ctx, request, digest, home); code != "" {
			return OwnedInspection{}, Uncertain
		}
		if _, err := systemctl(ctx, "enable", unit); err != nil || verifyUnitLink(unitPath) != nil {
			return OwnedInspection{}, Uncertain
		}
		if syncOwnedDirectory(filepath.Dir(unitPath)) != nil || syncOwnedDirectory(filepath.Dir(unitPath)+"/multi-user.target.wants") != nil {
			return OwnedInspection{}, Uncertain
		}
		if _, err := systemctl(ctx, "start", unit); err != nil {
			return OwnedInspection{}, Uncertain
		}
	case "restart":
		if _, err := systemctl(ctx, "restart", unit); err != nil {
			return OwnedInspection{}, Uncertain
		}
	case "cleanup":
		if inspection.State == OwnedInstalled {
			if _, err := systemctl(ctx, "stop", unit); err != nil {
				return OwnedInspection{}, Uncertain
			}
			stopped, code := inspectHelper(ctx, request, digest, home)
			if code != "" || stopped.ServiceActive || stopped.ServiceJobPending {
				return OwnedInspection{}, Uncertain
			}
			stopOutput, err := systemctl(ctx, "show", unit, "--property=Result", "--property=ExecMainCode", "--property=ExecMainStatus")
			if err != nil {
				return OwnedInspection{}, Uncertain
			}
			receipt.StopResult, receipt.StopExecCode, receipt.StopExecStatus, err = parseStopEvidence(stopOutput)
			if err != nil {
				return OwnedInspection{}, Uncertain
			}
			stoppedReceipt, _ := json.Marshal(receipt)
			if writeOwned(receiptPath, stoppedReceipt, true) != nil || writeOwned(ops+"/head.json", stoppedReceipt, true) != nil {
				return OwnedInspection{}, Uncertain
			}
			// The existing runner can exit nonzero on requested cancellation.
			// Clear only this verified stopped unit's retained failure record
			// before removing its definition; never reset other host services.
			if _, err := systemctl(ctx, "reset-failed", unit); err != nil {
				return OwnedInspection{}, Uncertain
			}
			// Disable only the exact link created by this protocol. systemctl
			// disable can also remove administrator-created aliases elsewhere.
			if err := removeUnitLink(unitPath); err != nil {
				return OwnedInspection{}, Uncertain
			}
			unitDirectory, err := unix.Open(filepath.Dir(unitPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return OwnedInspection{}, Uncertain
			}
			removeErr := removeOwnedAt(unitDirectory, unit, false, 0600)
			if removeErr == nil {
				removeErr = unix.Fsync(unitDirectory)
			}
			_ = unix.Close(unitDirectory)
			if removeErr != nil {
				return OwnedInspection{}, Uncertain
			}
			if err := syncOwnedDirectory(filepath.Dir(unitPath) + "/multi-user.target.wants"); err != nil && !errors.Is(err, os.ErrNotExist) {
				return OwnedInspection{}, Uncertain
			}
			if _, err := systemctl(ctx, "daemon-reload"); err != nil {
				return OwnedInspection{}, Uncertain
			}
		}
		claim, err := readOwned(home+"/claim", 1024, 0600)
		tombstone := []byte(strings.TrimSuffix(string(claim), "\n") + " " + request.OperationID + "\n")
		if err != nil || writeOwned(ownedBase+"/receipts/"+request.Manifest.InstallationID+".removed", tombstone, false) != nil {
			return OwnedInspection{}, Uncertain
		}
		if err := cleanupOwnedHome(home); err != nil {
			return OwnedInspection{}, Uncertain
		}
		inspection = OwnedInspection{InstallationID: request.Manifest.InstallationID, ManifestSHA256: digest, OperationID: request.OperationID, State: OwnedRemoved}
	default:
		return OwnedInspection{}, InvalidManifest
	}
	if request.Action != "cleanup" {
		inspection, code = inspectHelper(ctx, request, digest, home)
		if code != "" || inspection.ServiceJobPending || !inspection.ServiceActive ||
			(request.Action == "restart" && (inspection.InvocationID == "" || inspection.InvocationID == receipt.Before)) {
			return OwnedInspection{}, Uncertain
		}
	}
	receipt.Complete = true
	completed, _ := json.Marshal(receipt)
	if writeOwned(receiptPath, completed, true) != nil || writeOwned(ops+"/head.json", completed, true) != nil {
		return OwnedInspection{}, Uncertain
	}
	return inspection, ""
}

// RunOwnedAgent selects only the existing CLI's enrollment or credential mode.
// The descriptor lock survives exec and prevents duplicate launchers.
func RunOwnedAgent(version, installation string) error {
	if os.Geteuid() != 0 || !operationIDPattern.MatchString(installation) {
		return OwnershipConflict
	}
	home := filepath.Join(ownedBase, "installations", installation)
	data, err := readOwned(home+"/manifest.json", 4096, 0600)
	if err != nil {
		return OwnershipConflict
	}
	var manifest InstallManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.InstallationID != installation || manifest.Artifact.Version != version || verifyHome(home, manifest) != nil || verifyRuntimePaths(home, true) != nil {
		return OwnershipConflict
	}
	fd, err := unix.Open(home+"/runner.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return OwnershipConflict
	}
	var opened, named unix.Stat_t
	if requireOwned(home+"/runner.lock", false, 0600) != nil || unix.Fstat(fd, &opened) != nil || unix.Lstat(home+"/runner.lock", &named) != nil || opened.Ino != named.Ino || opened.Dev != named.Dev || opened.Uid != 0 || opened.Mode&07777 != 0600 || opened.Mode&unix.S_IFMT != unix.S_IFREG || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		_ = unix.Close(fd)
		return RemoteBusy
	}
	args := []string{home + "/agent", "runner", "--server", manifest.ControllerURL, "--id", manifest.RunnerID, "--credential-file", home + "/credential", "--execute", "--hosting-readiness", "--capabilities", "compose-deploy,image_deploy,image_release_ports_v1,hosting_readiness_v1", "--compose-image-policy", "pull", "--work-dir", home + "/work", "--journal-dir", home + "/journal", "--secret-root", home + "/secrets"}
	if requireOwned(home+"/enrollment", false, 0600) == nil {
		args = append(args, "--enrollment-file", home+"/enrollment")
	} else if requireOwned(home+"/credential", false, 0600) != nil {
		_ = unix.Close(fd)
		return OwnershipConflict
	}
	err = syscall.Exec(home+"/agent", args, []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LC_ALL=C"})
	_ = unix.Close(fd) // Exec success replaces this process and retains the flock FD.
	if err != nil {
		return RemoteFailure
	}
	return nil
}
