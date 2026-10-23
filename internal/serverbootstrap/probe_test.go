package serverbootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const validOutput = "nerocd_probe_v1\nos\tavailable\tdebian\t12\taarch64\nsystemd\trunning\ndocker\tavailable\t1.54\ncompose\tavailable\tv2.40.0\nbuildx\tavailable\tv0.29.0\ndisk\tavailable\t2048\t1024\nend\n"

func makeKey(t *testing.T) ([]byte, ssh.Signer) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test-only")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), signer
}

type testServer struct {
	target Target
	key    []byte
	auth   atomic.Int32
	dials  atomic.Int32
}

func serve(t *testing.T, run func(ssh.Channel)) *testServer {
	t.Helper()
	key, client := makeKey(t)
	_, host := makeKey(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &testServer{key: key, target: Target{Address: netip.MustParseAddr("127.0.0.1"), Port: uint16(listener.Addr().(*net.TCPAddr).Port), User: "root", HostKeyFingerprint: ssh.FingerprintSHA256(host.PublicKey())}}
	configuration := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		server.auth.Add(1)
		if !bytes.Equal(key.Marshal(), client.PublicKey().Marshal()) {
			return nil, errors.New("test client denied")
		}
		return nil, nil
	}}
	configuration.AddHostKey(host)
	var workers sync.WaitGroup
	var mu sync.Mutex
	var connections []net.Conn
	shuttingDown := false
	workers.Go(func() {
		for {
			raw, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			server.dials.Add(1)
			mu.Lock()
			if shuttingDown {
				mu.Unlock()
				_ = raw.Close()
				return
			}
			connections = append(connections, raw)
			mu.Unlock()
			workers.Go(func() {
				defer func() { _ = raw.Close() }()
				connection, channels, requests, handshakeErr := ssh.NewServerConn(raw, configuration)
				if handshakeErr != nil {
					return
				}
				defer func() { _ = connection.Close() }()
				workers.Go(func() { ssh.DiscardRequests(requests) })
				for opened := range channels {
					if opened.ChannelType() != "session" {
						_ = opened.Reject(ssh.UnknownChannelType, "test session only")
						continue
					}
					channel, requests, channelErr := opened.Accept()
					if channelErr != nil {
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
							if err := ssh.Unmarshal(request.Payload, &command); err != nil || command.Command != "env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C /bin/sh -s" {
								t.Error("probe sent an unfrozen command")
								_ = request.Reply(false, nil)
								return
							}
							_ = request.Reply(true, nil)
							input, err := io.ReadAll(io.LimitReader(channel, int64(len(probeRecipe)+1)))
							if err != nil || string(input) != probeRecipe {
								t.Error("probe recipe did not match the frozen read-only recipe")
								return
							}
							run(channel)
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
		shuttingDown = true
		for _, connection := range connections {
			_ = connection.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SSH server helpers did not exit")
		}
	})
	return server
}

func probeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestProbeRealSSH(t *testing.T) {
	server := serve(t, func(channel ssh.Channel) { _, _ = io.WriteString(channel, validOutput) })
	got, err := Probe(probeContext(t), server.target, server.key)
	if err != nil || got.OS.Architecture != "arm64" || got.OS.ID != "debian" || got.Systemd.State != Available || got.Docker.APIVersion != "1.54" || got.Compose.Version != "v2.40.0" || got.Disk.TotalBytes != 2097152 || got.Disk.AvailableBytes != 1048576 {
		t.Fatalf("observations=%+v error=%v", got, err)
	}
}

func TestProbeHostMismatchBeforeAuthentication(t *testing.T) {
	server := serve(t, func(ssh.Channel) { t.Error("untrusted host executed recipe") })
	_, otherHost := makeKey(t)
	server.target.HostKeyFingerprint = ssh.FingerprintSHA256(otherHost.PublicKey())
	got, err := Probe(probeContext(t), server.target, server.key)
	if !errors.Is(err, HostKeyMismatch) || got != (Observations{}) || server.auth.Load() != 0 {
		t.Fatalf("mismatch error=%v auth_calls=%d observations=%+v", err, server.auth.Load(), got)
	}
}

func TestProbeWrongClientKey(t *testing.T) {
	server := serve(t, func(ssh.Channel) { t.Error("wrong client executed recipe") })
	wrong, _ := makeKey(t)
	got, err := Probe(probeContext(t), server.target, wrong)
	if !errors.Is(err, HandshakeFailed) || got != (Observations{}) || server.auth.Load() == 0 {
		t.Fatalf("wrong client error=%v observations=%+v", err, got)
	}
}

func TestProbeMalformedAndCombinedOversizedOutput(t *testing.T) {
	for _, scenario := range []string{"malformed", "oversized", "combined"} {
		t.Run(scenario, func(t *testing.T) {
			server := serve(t, func(channel ssh.Channel) {
				switch scenario {
				case "malformed":
					_, _ = io.WriteString(channel, "remote-secret-message\n")
				case "oversized":
					_, _ = io.WriteString(channel, strings.Repeat("s", maxOutput+1))
				case "combined":
					var streams sync.WaitGroup
					streams.Go(func() { _, _ = io.WriteString(channel, strings.Repeat("o", 9000)) })
					streams.Go(func() { _, _ = io.WriteString(channel.Stderr(), strings.Repeat("e", 9000)) })
					streams.Wait()
				}
			})
			got, err := Probe(probeContext(t), server.target, server.key)
			expected := OutputLimitExceeded
			if scenario == "malformed" {
				expected = InvalidOutput
			}
			if !errors.Is(err, expected) || got != (Observations{}) || strings.Contains(err.Error(), "remote-secret") {
				t.Fatalf("output error=%v observations=%+v", err, got)
			}
		})
	}
}

func TestProbeRejectsInputsBeforeDial(t *testing.T) {
	server := serve(t, func(ssh.Channel) { t.Error("invalid input executed recipe") })
	for _, address := range []string{"invalid", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "255.255.255.255", "fe80::1%en0", "::ffff:0.0.0.0", "::ffff:224.0.0.1", "::ffff:255.255.255.255"} {
		target := server.target
		target.Address, _ = netip.ParseAddr(address)
		if _, err := Probe(probeContext(t), target, server.key); !errors.Is(err, InvalidTarget) {
			t.Errorf("address %q: %v", address, err)
		}
	}
	for _, change := range []func(*Target){func(target *Target) { target.Port = 0 }, func(target *Target) { target.User = "root;id" }, func(target *Target) { target.HostKeyFingerprint += "=" }, func(target *Target) { target.HostKeyFingerprint = "SHA256:short" }} {
		target := server.target
		change(&target)
		if _, err := Probe(probeContext(t), target, server.key); !errors.Is(err, InvalidTarget) {
			t.Errorf("invalid target: %v", err)
		}
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, err := Probe(ctx, server.target, server.key); !errors.Is(err, DeadlineRequired) {
			t.Errorf("deadline admission: %v", err)
		}
	}
	for _, key := range [][]byte{nil, []byte("secret-invalid-key"), bytes.Repeat([]byte{'k'}, 65537)} {
		if _, err := Probe(probeContext(t), server.target, key); !errors.Is(err, InvalidPrivateKey) {
			t.Errorf("key admission: %v", err)
		}
	}
	_, encryptedKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(encryptedKey, "test-only", []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(probeContext(t), server.target, pem.EncodeToMemory(block)); !errors.Is(err, InvalidPrivateKey) {
		t.Errorf("encrypted key admission: %v", err)
	}
	if server.dials.Load() != 0 {
		t.Fatal("invalid input dialed SSH")
	}
}
