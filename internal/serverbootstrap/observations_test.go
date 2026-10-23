package serverbootstrap

import (
	"errors"
	"strings"
	"testing"
)

func TestObservationsMissingAndUnsupported(t *testing.T) {
	data := "nerocd_probe_v1\nos\tavailable\tdebian\t12\triscv64\nsystemd\tnot_systemd\ndocker\tunavailable\t-\ncompose\tmissing\t-\nbuildx\tunavailable\t-\ndisk\tmissing\t-\t-\nend\n"
	got, err := parseObservations([]byte(data))
	if err != nil || got.OS.State != Unsupported || got.OS.Architecture != "unsupported" || got.Systemd.State != Unsupported || got.Docker.State != Unavailable || got.Compose.State != Missing || got.Buildx.State != Unavailable || got.Disk.State != Missing {
		t.Fatalf("missing prerequisites: %+v %v", got, err)
	}
	for _, status := range []string{"degraded", "starting", "offline"} {
		got, err := parseObservations([]byte(strings.Replace(validOutput, "systemd\trunning", "systemd\t"+status, 1)))
		if err != nil || got.Systemd.State != Unavailable || string(got.Systemd.Status) != status {
			t.Fatalf("systemd %s: %+v %v", status, got, err)
		}
	}
}

func TestObservationsClosedParsing(t *testing.T) {
	for _, change := range []struct{ before, after string }{
		{"nerocd_probe_v1", "nerocd_probe_v2"},
		{"debian\t12", "sensitive raw message\t12"},
		{"systemd\trunning", "systemd\twhatever"},
		{"docker\tavailable\t1.54", "docker\tavailable\t1.54;id"},
		{"v2.40.0", "v2.40.0\r"},
		{"disk\tavailable\t2048\t1024", "disk\tavailable\t18446744073709551615\t1"},
		{"disk\tavailable\t2048\t1024", "disk\tavailable\t2048\t2049"},
		{"disk\tavailable\t2048\t1024", "disk\tavailable\t0\t0"},
		{"disk\tavailable\t2048\t1024", "disk\tavailable\t-1\t0"},
		{"end\n", "end\nextra\n"},
	} {
		got, err := parseObservations([]byte(strings.Replace(validOutput, change.before, change.after, 1)))
		if !errors.Is(err, InvalidOutput) || got != (Observations{}) {
			t.Fatalf("accepted malformed protocol replacement %q: %+v %v", change.after, got, err)
		}
	}
}
