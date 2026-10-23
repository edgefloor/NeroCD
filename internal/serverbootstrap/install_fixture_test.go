package serverbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestInstallerPreparedHostFixture mutates only the freshly owned qualifier
// target. Its enrollment token comes from the registered API and PostgreSQL.
func TestInstallerPreparedHostFixture(t *testing.T) {
	if os.Getenv("NEROCD_BOOTSTRAP_INSTALL_FIXTURE") != "owned-systemd-target" {
		t.Skip("owned installer qualifier only")
	}
	port, err := strconv.ParseUint(os.Getenv("NEROCD_BOOTSTRAP_PROBE_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid fixture port")
	}
	key, err := os.ReadFile(os.Getenv("NEROCD_BOOTSTRAP_PROBE_KEY_FILE"))
	if err != nil {
		t.Fatal("fixture key unavailable")
	}
	token, err := os.ReadFile(os.Getenv("NEROCD_BOOTSTRAP_INSTALL_TOKEN_FILE"))
	if err != nil {
		t.Fatal("fixture enrollment unavailable")
	}
	path := os.Getenv("NEROCD_BOOTSTRAP_INSTALL_ARTIFACT")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest()
	manifest.InstallationID = strings.Repeat("1", 32)
	manifest.InstallOperationID = strings.Repeat("2", 32)
	manifest.RunnerID = "runner_ssh_installer_fixture"
	manifest.Artifact = Artifact{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil)), Version: "0.1.0-dev", Architecture: os.Getenv("NEROCD_BOOTSTRAP_INSTALL_ARCH")}
	target := Target{Address: netip.MustParseAddr("127.0.0.1"), Port: uint16(port), User: "root", HostKeyFingerprint: os.Getenv("NEROCD_BOOTSTRAP_PROBE_FINGERPRINT")}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	prerequisites, err := Probe(ctx, target, key)
	if err != nil || prerequisites.OS.ID != "debian" || prerequisites.OS.VersionID != "12" || prerequisites.OS.Architecture != manifest.Artifact.Architecture || prerequisites.Systemd.Status != SystemdRunning || prerequisites.Docker.State != Available || prerequisites.Docker.APIVersion != "1.54" || prerequisites.Compose.State != Available || (prerequisites.Compose.Version != "v5.1.1" && prerequisites.Compose.Version != "5.1.1") || prerequisites.Disk.AvailableBytes < 1<<30 {
		t.Fatalf("prepared installer fixture profile: %+v %v", prerequisites, err)
	}
	t.Logf("qualified prepared profile Debian12 native=%s systemd=running DockerAPI1.54 Compose=%s free>=1GiB; Buildx not required", prerequisites.OS.Architecture, prerequisites.Compose.Version)
	request := InstallRequest{OperationID: manifest.InstallOperationID, Manifest: manifest, Artifact: ArtifactSource{Path: path}, EnrollmentToken: token}
	got, err := Install(ctx, target, key, request)
	if err != nil || got.State != OwnedInstalled || !got.ServiceActive {
		t.Fatalf("actual install: %+v %v", got, err)
	}
	var enrolled OwnedInspection
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		enrolled, err = InspectOwned(ctx, target, key, Ownership{Manifest: manifest})
		if err != nil {
			t.Fatal(err)
		}
		if enrolled.CredentialFileExists && !enrolled.EnrollmentFileExists {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !enrolled.CredentialFileExists || enrolled.EnrollmentFileExists {
		t.Fatal("real enrollment did not transition to credential mode")
	}
	if _, err := Install(ctx, target, key, request); err != nil {
		t.Fatalf("exact install replay: %v", err)
	}
	fixtureSSH(t, ctx, target, key, "mv /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work.saved; ln -s /fixture/sentinel /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work")
	unsafe := Mutation{OperationID: strings.Repeat("0", 32), Ownership: Ownership{Manifest: manifest}}
	if _, err := RestartOwned(ctx, target, key, unsafe); err != OwnershipConflict {
		t.Fatalf("runtime symlink restart: %v", err)
	}
	if _, err := CleanupOwned(ctx, target, key, unsafe); err != OwnershipConflict {
		t.Fatalf("runtime symlink cleanup: %v", err)
	}
	fixtureSSH(t, ctx, target, key, "systemctl is-active --quiet nerocd-agent-11111111111111111111111111111111.service && test \"$(systemctl show nerocd-agent-11111111111111111111111111111111.service -p InvocationID --value)\" = '"+enrolled.InvocationID+"'")
	fixtureSSH(t, ctx, target, key, "test -L /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work && test -f /fixture/sentinel; rm /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work; mv /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work.saved /var/lib/nerocd-bootstrap/installations/11111111111111111111111111111111/work")
	restarted, err := RestartOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("3", 32), Ownership: Ownership{Manifest: manifest}})
	if err != nil || !restarted.ServiceActive || restarted.InvocationID == enrolled.InvocationID {
		t.Fatalf("actual restart: %+v %v", restarted, err)
	}
	if restarted.EnrollmentFileExists || !restarted.CredentialFileExists {
		t.Fatal("restart changed credential mode")
	}
	if replay, err := RestartOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("3", 32), Ownership: Ownership{Manifest: manifest}}); err != nil || replay.InvocationID != restarted.InvocationID {
		t.Fatalf("exact restart replay changed invocation: %+v %v", replay, err)
	}
	fixtureSSH(t, ctx, target, key, "for i in $(seq 1 50); do test -f /fixture/restart.identity && exit 0; sleep 0.1; done; exit 1")
	fixtureSSH(t, ctx, target, key, "systemctl --failed --no-pager --plain; systemctl show nerocd-agent-11111111111111111111111111111111.service -p ActiveState -p SubState -p Result -p ExecMainCode -p ExecMainStatus")
	removed, err := CleanupOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("4", 32), Ownership: Ownership{Manifest: manifest}})
	if err != nil || removed.State != OwnedRemoved {
		t.Fatalf("actual cleanup: %+v %v", removed, err)
	}
	if got, err := InspectOwned(ctx, target, key, Ownership{Manifest: manifest}); err != nil || got.State != OwnedRemoved {
		t.Fatalf("tombstone inspection: %+v %v", got, err)
	}
	if got, err := CleanupOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("4", 32), Ownership: Ownership{Manifest: manifest}}); err != nil || got.State != OwnedRemoved {
		t.Fatalf("exact cleanup replay: %+v %v", got, err)
	}
	if _, err := CleanupOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("a", 32), Ownership: Ownership{Manifest: manifest}}); err != OwnershipConflict {
		t.Fatalf("different cleanup borrowed tombstone: %v", err)
	}
	if _, err := RestartOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("4", 32), Ownership: Ownership{Manifest: manifest}}); err != OwnershipConflict {
		t.Fatalf("different action borrowed tombstone: %v", err)
	}
	// A genuinely truncated transfer must not execute and can be cleaned only
	// through its exact persisted claim; it is separate from enrollment replay.
	partial := manifest
	partial.InstallationID = strings.Repeat("5", 32)
	partial.InstallOperationID = strings.Repeat("6", 32)
	if _, err := ownedAction(ctx, target, key, "install", partial.InstallOperationID, partial, bytes.NewReader([]byte("short")), token); err != PartialTransfer {
		t.Fatalf("truncated transfer: %v", err)
	}
	if got, err := InspectOwned(ctx, target, key, Ownership{Manifest: partial}); err != nil || got.State != OwnedStaged {
		t.Fatalf("partial inspection: %+v %v", got, err)
	}
	if got, err := CleanupOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("7", 32), Ownership: Ownership{Manifest: partial}}); err != nil || got.State != OwnedRemoved {
		t.Fatalf("partial cleanup: %+v %v", got, err)
	}
	// Fixture-only collision preparation is fixed and touches only this target.
	fixtureSSH(t, ctx, target, key, "ln -s /fixture/sentinel /var/lib/nerocd-bootstrap/installations/88888888888888888888888888888888")
	collision := manifest
	collision.InstallationID = strings.Repeat("8", 32)
	collision.InstallOperationID = strings.Repeat("9", 32)
	if _, err := ownedAction(ctx, target, key, "install", collision.InstallOperationID, collision, strings.NewReader(""), token); err != OwnershipConflict {
		t.Fatalf("symlink collision: %v", err)
	}
	fixtureSSH(t, ctx, target, key, "mkdir -m 0700 /etc/systemd/system/nerocd-agent-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service.d; printf '%s\\n' '[Service]' 'ExecStart=' 'ExecStart=/usr/bin/touch /fixture/foreign-dropin-executed' >/etc/systemd/system/nerocd-agent-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service.d/override.conf; chmod 0600 /etc/systemd/system/nerocd-agent-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service.d/override.conf")
	dropin := manifest
	dropin.InstallationID = strings.Repeat("e", 32)
	dropin.InstallOperationID = strings.Repeat("a", 32)
	if _, err := Install(ctx, target, key, InstallRequest{OperationID: dropin.InstallOperationID, Manifest: dropin, Artifact: ArtifactSource{Path: path}, EnrollmentToken: token}); err != OwnershipConflict {
		t.Fatalf("foreign unit drop-in adopted: %v", err)
	}
	fixtureSSH(t, ctx, target, key, "test -f /etc/systemd/system/nerocd-agent-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service.d/override.conf && test ! -e /fixture/foreign-dropin-executed && test ! -e /etc/systemd/system/nerocd-agent-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.service")
	// A restricted fixture-only key executes exactly the production command and
	// delays only its successful public response. No production failpoint exists.
	lossToken, err := os.ReadFile(os.Getenv("NEROCD_BOOTSTRAP_INSTALL_LOSS_TOKEN_FILE"))
	if err != nil {
		t.Fatal("response-loss fixture enrollment unavailable")
	}
	loss := manifest
	loss.InstallationID = strings.Repeat("b", 32)
	loss.InstallOperationID = strings.Repeat("c", 32)
	loss.RunnerID = "runner_ssh_loss_fixture"
	lossRequest := InstallRequest{OperationID: loss.InstallOperationID, Manifest: loss, Artifact: ArtifactSource{Path: path}, EnrollmentToken: lossToken}
	lossPrerequisites, probeErr := Probe(ctx, target, key)
	t.Logf("before response-loss agent prerequisite observations: %+v error=%v", lossPrerequisites, probeErr)
	if probeErr != nil || lossPrerequisites.Systemd.Status != SystemdRunning {
		t.Fatal("owned cleanup left host systemd unavailable")
	}
	fixtureSSH(t, ctx, target, key, "systemctl --failed --no-pager --plain; systemctl show nerocd-agent-11111111111111111111111111111111.service -p ActiveState -p SubState -p Result -p ExecMainCode -p ExecMainStatus")
	lossInstalled, err := Install(ctx, target, key, lossRequest)
	if err != nil || lossInstalled.State != OwnedInstalled {
		t.Fatalf("response-loss agent install: %+v %v", lossInstalled, err)
	}
	fixtureSSH(t, ctx, target, key, "for i in $(seq 1 50); do test -f /fixture/loss.identity && exit 0; sleep 0.1; done; exit 1")
	lossKey, err := os.ReadFile(os.Getenv("NEROCD_BOOTSTRAP_INSTALL_LOSS_KEY_FILE"))
	if err != nil {
		t.Fatal("restricted fixture key unavailable")
	}
	commandHash := sha256.Sum256([]byte(ownedCommand()))
	fixtureSSH(t, ctx, target, key, "umask 077; set -C; printf '%s\\n' '"+hex.EncodeToString(commandHash[:])+"' >/fixture/owned-command.sha256")
	metadata, manifestHash, _ := CanonicalManifest(loss)
	header := fmt.Sprintf("nerocd_owned_v1\nrestart\n%s\n%s\n%s\n%d\n%s\n%s\n%d\n%s\n0\n", loss.InstallationID, strings.Repeat("d", 32), loss.InstallOperationID, loss.Artifact.Size, loss.Artifact.SHA256, loss.Artifact.Architecture, len(metadata), manifestHash)
	requestHash := sha256.Sum256(append([]byte(header), metadata...))
	fixtureSSH(t, ctx, target, key, "umask 077; set -C; printf '%s\\n' '"+hex.EncodeToString(requestHash[:])+"' >/fixture/owned-request.sha256")
	if _, err := fixtureSSHResult(ctx, target, lossKey, "touch /fixture/forbidden-command"); err == nil {
		t.Fatal("restricted key accepted arbitrary command")
	}
	if _, err := fixtureSSHResult(ctx, target, lossKey, ownedCommand()); err == nil {
		t.Fatal("restricted key accepted another or missing request")
	}
	fixtureSSH(t, ctx, target, key, "test ! -e /fixture/forbidden-command && test ! -e /fixture/response-loss.committed")
	lossContext, stopLoss := context.WithTimeout(ctx, 10*time.Second)
	lossOperation := Mutation{OperationID: strings.Repeat("d", 32), Ownership: Ownership{Manifest: loss}}
	lossResult := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := RestartOwned(lossContext, target, lossKey, lossOperation)
		lossResult <- err
	}()
	defer func() {
		stopLoss()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("SSH loss caller did not reap")
		}
	}()
	fixtureSSH(t, ctx, target, key, "for i in $(seq 1 50); do test -f /fixture/response-loss.committed && exit 0; sleep 0.1; done; exit 1")
	stopLoss()
	lossErr := <-lossResult
	if lossErr != Uncertain {
		t.Fatalf("submitted actual SSH restart loss must be uncertain: %v", lossErr)
	}
	recovered, err := InspectOwned(ctx, target, key, Ownership{Manifest: loss})
	if err != nil || recovered.OperationID != lossOperation.OperationID {
		t.Fatalf("exact remote receipt recovery: %+v %v", recovered, err)
	}
	if recovered.State != OwnedInstalled || recovered.InvocationID == lossInstalled.InvocationID || !recovered.ServiceActive || recovered.ServiceJobPending {
		t.Fatalf("unproven SSH loss outcome: %+v", recovered)
	}
	if replay, err := RestartOwned(ctx, target, key, lossOperation); err != nil || replay.InvocationID != recovered.InvocationID {
		t.Fatalf("lost-response retry repeated mutation: %+v %v", replay, err)
	}
	if _, err := CleanupOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("f", 32), Ownership: Ownership{Manifest: loss}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual SSH response-loss recovery retained exact operation=%s state=%s; exact retry preserved InvocationID", recovered.OperationID, recovered.State)
	fixtureSSH(t, ctx, target, key, "cat /var/lib/nerocd-bootstrap/receipts/11111111111111111111111111111111/44444444444444444444444444444444.json")
	if after, err := Probe(ctx, target, key); err != nil || after.Systemd.Status != SystemdRunning {
		t.Fatalf("owned cleanup left systemd failed: %+v %v", after.Systemd, err)
	}
	t.Log("actual SSH exact install/replay/restart/credential transition/cleanup, truncated transfer, and symlink collision passed; no workload operations")
}

func fixtureSSH(t *testing.T, ctx context.Context, target Target, key []byte, command string) {
	t.Helper()
	output, err := fixtureSSHResult(ctx, target, key, command)
	if err != nil {
		t.Fatalf("fixed fixture setup: %v", err)
	}
	if len(output) > 0 {
		t.Logf("fixed fixture diagnostics: %s", output)
	}
}

func fixtureSSHResult(ctx context.Context, target Target, key []byte, command string) ([]byte, error) {
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Address.String(), strconv.Itoa(int(target.Port))))
	if err != nil {
		return nil, err
	}
	defer func() { _ = raw.Close() }()
	deadline, _ := ctx.Deadline()
	_ = raw.SetDeadline(deadline)
	configuration := &ssh.ClientConfig{User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != target.HostKeyFingerprint {
			return HostKeyMismatch
		}
		return nil
	}}
	connection, channels, requests, err := ssh.NewClientConn(raw, "fixture", configuration)
	if err != nil {
		return nil, err
	}
	client := ssh.NewClient(connection, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	output := &boundedOutput{closeTransport: raw.Close}
	session.Stdout, session.Stderr = output, output
	err = session.Run(command)
	return output.data, err
}
