package serverbootstrap

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestProbeHandshakeCancellationClosesSocket(t *testing.T) {
	for _, scenario := range []string{"cancel", "caller deadline", "maximum lifetime"} {
		t.Run(scenario, func(t *testing.T) {
			key, host := makeKey(t)
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			accepted, closed := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var active net.Conn
			shuttingDown := false
			t.Cleanup(func() {
				_ = listener.Close()
				mu.Lock()
				shuttingDown = true
				if active != nil {
					_ = active.Close()
				}
				mu.Unlock()
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Error("stalled test server did not exit")
				}
			})
			go func() {
				defer close(closed)
				raw, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				defer func() { _ = raw.Close() }()
				mu.Lock()
				if shuttingDown {
					mu.Unlock()
					return
				}
				active = raw
				mu.Unlock()
				_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
				close(accepted)
				_, _ = io.Copy(io.Discard, raw) // No SSH banner: stall the actual handshake.
			}()
			target := Target{Address: netip.MustParseAddr("127.0.0.1"), Port: uint16(listener.Addr().(*net.TCPAddr).Port), User: "root", HostKeyFingerprint: ssh.FingerprintSHA256(host.PublicKey())}
			lifetime := 30 * time.Second
			if scenario == "caller deadline" {
				lifetime = 150 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), lifetime)
			defer cancel()
			finished := make(chan error, 1)
			started := time.Now()
			go func() { _, err := Probe(ctx, target, key); finished <- err }()
			select {
			case <-accepted:
			case <-time.After(time.Second):
				t.Fatal("probe did not start a TCP handshake")
			}
			if scenario == "cancel" {
				cancel()
			}
			expected := error(context.DeadlineExceeded)
			if scenario == "cancel" {
				expected = context.Canceled
			}
			select {
			case err := <-finished:
				if !errors.Is(err, expected) {
					t.Fatalf("handshake returned %v", err)
				}
			case <-time.After(17 * time.Second):
				t.Fatal("probe outlived its maximum lifetime")
			}
			if scenario == "maximum lifetime" && (time.Since(started) < 14*time.Second || time.Since(started) > 17*time.Second) {
				t.Fatal("15-second operation bound was not enforced")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("cancellation left the handshake socket open")
			}
		})
	}
}

func TestProbeSessionCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := serve(t, func(ssh.Channel) { close(started); <-release })
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := Probe(ctx, server.target, server.key); finished <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start the fixed recipe")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("session cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session or copy helpers did not return after cancellation")
	}
}
