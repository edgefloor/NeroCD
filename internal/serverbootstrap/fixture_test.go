package serverbootstrap

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestPreparedHostFixture is the only fixture entry point. It observes an
// already-qualified disposable target; it never mutates or installs on it.
func TestPreparedHostFixture(t *testing.T) {
	if os.Getenv("NEROCD_BOOTSTRAP_PROBE_FIXTURE") != "owned-systemd-target" {
		t.Skip("run acceptance/runtime-server-bootstrap/qualify.sh for the owned systemd target")
	}
	port, err := strconv.ParseUint(os.Getenv("NEROCD_BOOTSTRAP_PROBE_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid owned fixture port")
	}
	key, err := os.ReadFile(os.Getenv("NEROCD_BOOTSTRAP_PROBE_KEY_FILE"))
	if err != nil {
		t.Fatal("cannot read owned fixture key")
	}
	target := Target{Address: netip.MustParseAddr("127.0.0.1"), Port: uint16(port), User: "root", HostKeyFingerprint: os.Getenv("NEROCD_BOOTSTRAP_PROBE_FINGERPRINT")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got, err := Probe(ctx, target, key)
	if err != nil {
		t.Fatalf("prepared host probe: %v", err)
	}
	if got.OS.State != Available || got.OS.ID != "debian" || (got.OS.Architecture != "arm64" && got.OS.Architecture != "amd64") ||
		(got.Systemd.Status != SystemdRunning && got.Systemd.Status != SystemdDegraded) || got.Docker.State != Available || got.Docker.APIVersion != "1.54" ||
		got.Compose.State != Missing || got.Buildx.State != Missing || got.Disk.State != Available || got.Disk.TotalBytes == 0 || got.Disk.AvailableBytes > got.Disk.TotalBytes {
		t.Fatalf("unexpected prepared host prerequisites: %+v", got)
	}
	t.Logf("actual fixture prerequisite observations: %+v", got)
	target.HostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Probe(ctx, target, key); !errors.Is(err, HostKeyMismatch) {
		t.Fatalf("actual host mismatch: %v", err)
	}
	target.HostKeyFingerprint = os.Getenv("NEROCD_BOOTSTRAP_PROBE_FINGERPRINT")
	wrongKey, _ := makeKey(t)
	if _, err := Probe(ctx, target, wrongKey); !errors.Is(err, HandshakeFailed) {
		t.Fatalf("actual wrong client: %v", err)
	}
}
