package serverbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	restarted, err := RestartOwned(ctx, target, key, Mutation{OperationID: strings.Repeat("3", 32), Ownership: Ownership{Manifest: manifest}})
	if err != nil || !restarted.ServiceActive || restarted.InvocationID == enrolled.InvocationID {
		t.Fatalf("actual restart: %+v %v", restarted, err)
	}
	if restarted.EnrollmentFileExists || !restarted.CredentialFileExists {
		t.Fatal("restart changed credential mode")
	}
	fixtureSSH(t, ctx, target, key, "for i in $(seq 1 50); do test -f /fixture/restart.identity && exit 0; sleep 0.1; done; exit 1")
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
	t.Log("actual SSH exact install/replay/restart/credential transition/cleanup, truncated transfer, and symlink collision passed; no workload operations")
}

func fixtureSSH(t *testing.T, ctx context.Context, target Target, key []byte, command string) {
	t.Helper()
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.Address.String(), strconv.Itoa(int(target.Port))))
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	client := ssh.NewClient(connection, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Run(command); err != nil {
		t.Fatalf("fixed fixture setup: %v", err)
	}
}
