package serverbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// This server tests transport framing and failure handling only. It does not
// stand in for the actual Linux helper, enrollment API, or systemd fixture.
func serveOwned(t *testing.T, run func(ssh.Channel, string)) (Target, []byte) {
	t.Helper()
	key, signer := makeKey(t)
	_, host := makeKey(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	configuration := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(public.Marshal(), signer.PublicKey().Marshal()) {
			return nil, HandshakeFailed
		}
		return nil, nil
	}}
	configuration.AddHostKey(host)
	var workers sync.WaitGroup
	var mu sync.Mutex
	var connections []net.Conn
	closed := false
	workers.Go(func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = raw.Close()
				return
			}
			connections = append(connections, raw)
			mu.Unlock()
			workers.Go(func() {
				defer func() { _ = raw.Close() }()
				connection, channels, requests, err := ssh.NewServerConn(raw, configuration)
				if err != nil {
					return
				}
				defer func() { _ = connection.Close() }()
				workers.Go(func() { ssh.DiscardRequests(requests) })
				for next := range channels {
					if next.ChannelType() != "session" {
						_ = next.Reject(ssh.UnknownChannelType, "sessions only")
						continue
					}
					channel, requests, err := next.Accept()
					if err != nil {
						return
					}
					workers.Go(func() {
						defer func() { _ = channel.Close() }()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var command struct{ Command string }
							if ssh.Unmarshal(request.Payload, &command) != nil {
								_ = request.Reply(false, nil)
								return
							}
							_ = request.Reply(true, nil)
							run(channel, command.Command)
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					})
				}
			})
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		closed = true
		for _, raw := range connections {
			_ = raw.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("owned SSH helpers did not reap")
		}
	})
	return Target{Address: netip.MustParseAddr("127.0.0.1"), Port: uint16(listener.Addr().(*net.TCPAddr).Port), User: "root", HostKeyFingerprint: ssh.FingerprintSHA256(host.PublicKey())}, key
}

func TestOwnedTransportFramesFixedCommandAndExactManifest(t *testing.T) {
	manifest := testManifest()
	metadata, digest, _ := CanonicalManifest(manifest)
	target, key := serveOwned(t, func(channel ssh.Channel, command string) {
		if !strings.Contains(command, "/usr/bin/timeout --signal=TERM --kill-after=5s 120s") || !strings.Contains(command, ownedRecipe[:20]) {
			t.Error("command lost fixed process deadline")
		}
		input, err := io.ReadAll(io.LimitReader(channel, 8192))
		if err != nil {
			t.Error(err)
			return
		}
		if !bytes.HasSuffix(input, metadata) || !bytes.Contains(input, []byte("\ninspect\n")) {
			t.Error("request did not carry exact immutable metadata")
		}
		_ = json.NewEncoder(channel).Encode(ownedResponse{Inspection: &OwnedInspection{InstallationID: manifest.InstallationID, ManifestSHA256: digest, OperationID: manifest.InstallOperationID, State: OwnedInstalled}})
	})
	got, err := InspectOwned(probeContext(t), target, key, Ownership{Manifest: manifest})
	if err != nil || got.State != OwnedInstalled {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestOwnedTransportResponseLossAndMalformedOutputRemainUncertain(t *testing.T) {
	for name, output := range map[string]string{"loss": "", "unknown": "{\"unexpected\":true}", "oversized": strings.Repeat("x", 16385), "foreign": "{\"inspection\":{\"installation_id\":\"other\"}}", "extra": "{}{}"} {
		t.Run(name, func(t *testing.T) {
			target, key := serveOwned(t, func(channel ssh.Channel, _ string) {
				_, _ = io.Copy(io.Discard, channel)
				_, _ = io.WriteString(channel, output)
			})
			_, err := RestartOwned(probeContext(t), target, key, Mutation{OperationID: strings.Repeat("d", 32), Ownership: Ownership{Manifest: testManifest()}})
			if err != Uncertain {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestOwnedTransportCancellationAndAdmission(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	target, key := serveOwned(t, func(channel ssh.Channel, _ string) {
		close(started)
		_, _ = io.Copy(io.Discard, channel)
		<-ctx.Done()
	})
	_, err := InspectOwned(ctx, target, key, Ownership{Manifest: testManifest()})
	if err != Uncertain {
		t.Fatalf("accepted command cancellation must remain uncertain: %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("command never started")
	}
	if _, err := InspectOwned(context.Background(), target, key, Ownership{Manifest: testManifest()}); err != DeadlineRequired {
		t.Fatal(err)
	}
	if _, err := InspectOwned(probeContext(t), target, bytes.Repeat([]byte{1}, 65537), Ownership{Manifest: testManifest()}); err != InvalidPrivateKey {
		t.Fatal(err)
	}
	target.User = "other"
	if _, err := InspectOwned(probeContext(t), target, key, Ownership{Manifest: testManifest()}); err != InvalidTarget {
		t.Fatal(err)
	}
}

func TestOwnedHostMismatchPrecedesAuthentication(t *testing.T) {
	server := serve(t, func(ssh.Channel) { t.Error("mismatched host executed command") })
	server.target.HostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := InspectOwned(probeContext(t), server.target, server.key, Ownership{Manifest: testManifest()}); err != HostKeyMismatch {
		t.Fatal(err)
	}
	if server.auth.Load() != 0 {
		t.Fatal("authentication occurred before host verification")
	}
}
