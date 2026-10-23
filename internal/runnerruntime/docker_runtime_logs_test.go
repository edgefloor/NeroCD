package runnerruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func runtimeLogTargetFixture() domain.HostingRuntimeLogTarget {
	t := observationTargetFixture()
	return domain.HostingRuntimeLogTarget{ResourceID: t.ResourceID, BindingID: t.BindingID, DestinationID: t.DestinationID, ServerID: t.ServerID, RunnerID: t.RunnerID, OperationID: t.OperationID, DaemonID: t.DaemonID, ContainerID: t.ContainerID, ImageID: t.ImageID, ReleaseHash: t.ReleaseHash, PlanHash: t.PlanHash, RuntimeNamespace: t.RuntimeNamespace}
}
func runtimeLogFrame(stream byte, message string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(message)))
	return append(header, []byte(message)...)
}
func TestRuntimeLogParserRedactsBeforeRetentionAtEverySplit(t *testing.T) {
	token := "ncr_super_private_bearer"
	for split := 0; split <= len(token); split++ {
		data := append(runtimeLogFrame(1, "2026-10-23T01:02:03.000000001Z before "+token[:split]), runtimeLogFrame(1, token[split:]+" after\n")...)
		data = append(data, runtimeLogFrame(2, "stderr line\n")...)
		var events []domain.HostingRuntimeLogEvent
		_ = readRuntimeDockerLogs(t.Context(), bytes.NewReader(data), false, token, func(e domain.HostingRuntimeLogEvent) error { events = append(events, e); return nil })
		if len(events) != 2 || events[0].Stream != "stdout" || events[1].Stream != "stderr" || events[0].SourceTimestamp == nil || events[0].Message != "before "+runner.RedactionMarker+" after\n" {
			t.Fatalf("split%d invalid redacted event shape", split)
		}
		for _, e := range events {
			if strings.Contains(e.Message, token) {
				t.Fatal("bearer reached retained events")
			}
		}
	}
}
func TestRuntimeLogParserTTYClippingMalformedAndTails(t *testing.T) {
	for _, mode := range []string{"tty", "unicode", "oversize", "badheader", "partial", "clip", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			input := runtimeLogFrame(1, "line\n")
			tty := false
			switch mode {
			case "tty":
				tty = true
				input = []byte("combined\n")
			case "unicode":
				input = append(runtimeLogFrame(1, "\xe2"), runtimeLogFrame(1, "\x98\x83\n")...)
			case "oversize":
				input = runtimeLogFrame(1, strings.Repeat("a", 65537))
			case "badheader":
				input[0] = 3
			case "partial":
				input = runtimeLogFrame(1, "discard unflushed")
			case "clip":
				input = runtimeLogFrame(1, strings.Repeat("x", 20000)+"\n")
			case "invalid":
				input = runtimeLogFrame(1, "\x00\xff\n")
			}
			var got []domain.HostingRuntimeLogEvent
			_ = readRuntimeDockerLogs(t.Context(), bytes.NewReader(input), tty, "token", func(e domain.HostingRuntimeLogEvent) error { got = append(got, e); return nil })
			if mode == "oversize" || mode == "badheader" || mode == "partial" {
				if len(got) != 0 {
					t.Fatal("unqualified frame retained")
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("output lost")
			}
			if mode == "tty" && got[0].Stream != "combined" {
				t.Fatal("TTY stream fabricated")
			}
			if mode == "clip" && (len(got) != 2 || !got[0].MessageTruncated || len(got[0].Message) > 16384) {
				t.Fatal("clipping unbounded")
			}
			for _, e := range got {
				b := domain.HostingRuntimeLogBatch{SessionID: "session", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), BatchID: strings.Repeat("c", 32), BatchSequence: 1, State: "following", Events: []domain.HostingRuntimeLogEvent{e}}
				if _, err := b.Canonical(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func unixRuntimeLogFixture(t *testing.T, handler http.Handler) (string, *http.Server) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ncd-log-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	return "unix://" + socket, server
}
func TestRuntimeLogDockerNarrowUnixIdentityAndDrivers(t *testing.T) {
	for _, mode := range []string{"json-file", "local", "tty", "unsupported", "daemon", "label", "container", "version", "oversize", "no-tty"} {
		t.Run(mode, func(t *testing.T) {
			target := runtimeLogTargetFixture()
			var logs atomic.Int32
			endpoint, _ := unixRuntimeLogFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("mutating Docker method")
				}
				switch r.URL.Path {
				case "/version":
					api := "1.54"
					if mode == "version" {
						api = "1.53"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": api, "MinAPIVersion": "1.40"})
				case "/v1.54/info":
					id := target.DaemonID
					if mode == "daemon" {
						id = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"ID": id})
				case "/v1.54/containers/" + target.ContainerID + "/json":
					if mode == "oversize" {
						_, _ = io.WriteString(w, strings.Repeat(" ", 256<<10)+"{}")
						return
					}
					suffix := strings.TrimPrefix(domain.HostingDocumentHash(target.OperationID), "sha256:")[:16]
					driver := mode
					if mode != "local" {
						driver = "json-file"
					}
					if mode == "unsupported" {
						driver = "syslog"
					}
					labels := map[string]string{"io.nerocd.binding": target.BindingID, "io.nerocd.operation": target.OperationID, "io.nerocd.release": target.ReleaseHash, "io.nerocd.plan": target.PlanHash}
					if mode == "label" {
						labels["io.nerocd.binding"] = "foreign"
					}
					id := target.ContainerID
					if mode == "container" {
						id = strings.Repeat("f", 64)
					}
					config := map[string]any{"Tty": mode == "tty", "Labels": labels, "Env": []string{"NEVER_RETAIN_PRIVATE_ENV=canary"}}
					if mode == "no-tty" {
						delete(config, "Tty")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/" + target.RuntimeNamespace + "_" + suffix, "Image": target.ImageID, "Config": config, "HostConfig": map[string]any{"LogConfig": map[string]string{"Type": driver}}})
				case "/v1.54/containers/" + target.ContainerID + "/logs":
					logs.Add(1)
					if r.URL.RawQuery != "stdout=1&stderr=1&timestamps=1&tail=200&follow=1" {
						t.Error("unbounded log selector")
					}
					_, _ = w.Write(runtimeLogFrame(1, "line\n"))
				default:
					t.Error("Docker endpoint outside allowlist")
					http.NotFound(w, r)
				}
			}))
			source, err := newDockerRuntimeLogs(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			identity, err := source.Verify(t.Context(), target)
			good := mode == "json-file" || mode == "local" || mode == "tty"
			if (err == nil) != good {
				t.Fatal("identity or driver accepted incorrectly", err)
			}
			if good {
				if identity.TTY != (mode == "tty") {
					t.Fatal("TTY lost")
				}
				body, e := source.Open(t.Context(), target)
				if e != nil {
					t.Fatal(e)
				}
				_ = body.Close()
				if logs.Load() != 1 {
					t.Fatal("logs not opened")
				}
			}
		})
	}
	for _, endpoint := range []string{"tcp://127.0.0.1:2375", "http://docker", "unix://remote/socket", "unix:///tmp/../socket", "unix:///tmp/a?x=1"} {
		if source, err := newDockerRuntimeLogs(endpoint); err == nil {
			source.Close()
			t.Fatal("remote or ambiguous daemon accepted")
		}
	}
}
func TestRuntimeLogDockerStalledReadCancellation(t *testing.T) {
	started := make(chan struct{})
	endpoint, _ := unixRuntimeLogFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	source, err := newDockerRuntimeLogs(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(t.Context())
	body, err := source.Open(ctx, runtimeLogTargetFixture())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = readRuntimeDockerLogs(ctx, body, false, "token", func(domain.HostingRuntimeLogEvent) error { return nil })
		_ = body.Close()
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled Unix log reader did not join")
	}
}
