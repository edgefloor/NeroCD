package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/netutil"
)

// autoGitBridge is fixture routing only. Git still verifies the original
// github.com TLS peer end to end; the bridge never chooses a caller's upstream.
func autoGitBridge(parent context.Context, target string) (string, func() error, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return "", nil, errors.New("fixture target must be fixed loopback")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	var calls atomic.Uint32
	var relays sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	track := func(conn net.Conn, add bool) {
		mu.Lock()
		defer mu.Unlock()
		if add {
			connections[conn] = true
		} else {
			delete(connections, conn)
		}
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.RequestURI != "github.com:443" || r.Host != "github.com:443" || r.URL.Host != "github.com:443" || r.URL.Scheme != "" || r.ContentLength > 0 || r.Header.Get("Proxy-Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if calls.Add(1) > 64 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		relays.Add(1)
		defer relays.Done()
		upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", target)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		track(client, true)
		track(upstream, true)
		defer track(client, false)
		defer track(upstream, false)
		deadline := time.Now().Add(15 * time.Second)
		if client.SetDeadline(deadline) != nil || upstream.SetDeadline(deadline) != nil {
			return
		}
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if buffer.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.CopyN(upstream, buffer, 4<<20)
			_ = upstream.Close()
			_ = client.Close()
		}()
		_, _ = io.CopyN(client, upstream, 4<<20)
		_ = client.Close()
		_ = upstream.Close()
		<-done
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(netutil.LimitListener(listener, 8)) }()
	var once sync.Once
	var closeErr error
	closeBridge := func() error {
		once.Do(func() {
			cancel()
			shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			closeErr = server.Shutdown(shutdown)
			if closeErr != nil {
				closeErr = errors.Join(closeErr, server.Close())
			}
			mu.Lock()
			owned := make([]net.Conn, 0, len(connections))
			for conn := range connections {
				owned = append(owned, conn)
			}
			mu.Unlock()
			for _, conn := range owned {
				_ = conn.Close()
			}
			relays.Wait()
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				closeErr = errors.Join(closeErr, err)
			}
		})
		return closeErr
	}
	return "http://" + listener.Addr().String(), closeBridge, nil
}

func TestAutomaticDeploymentGitBridgeFixedTarget(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	var dialed atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		dialed.Add(1)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var b [7]byte
		if _, err = io.ReadFull(conn, b[:]); err == nil {
			_, _ = conn.Write(b[:])
		}
	}()
	proxy, closeBridge, err := autoGitBridge(t.Context(), upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeBridge(); err != nil {
			t.Error(err)
		}
	})
	address := strings.TrimPrefix(proxy, "http://")
	for _, request := range []string{
		"GET / HTTP/1.1\r\nHost: github.com:443\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: github.com:443\r\nContent-Length: 0\r\n\r\n",
		"CONNECT alternate.test:443 HTTP/1.1\r\nHost: alternate.test:443\r\n\r\n",
		"CONNECT github.com:444 HTTP/1.1\r\nHost: github.com:444\r\n\r\n",
	} {
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 && response.StatusCode != 405 {
			t.Fatal("alternate bridge request accepted", response.StatusCode)
		}
	}
	if dialed.Load() != 0 {
		t.Fatal("rejected caller selected an upstream")
	}
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = fmt.Fprint(conn, "CONNECT github.com:443 HTTP/1.1\r\nHost: ignored.invalid:444\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// net/http uses the CONNECT authority as Request.Host. An unrelated Host
	// header is neither an upstream selector nor forwarded into the tunnel.
	buffer := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffer, nil)
	if err != nil || response.StatusCode != 200 {
		t.Fatal("fixed bridge rejected", err)
	}
	if _, err = io.WriteString(conn, "fixture"); err != nil {
		t.Fatal(err)
	}
	var body [7]byte
	if _, err = io.ReadFull(buffer, body[:]); err != nil || string(body[:]) != "fixture" {
		t.Fatal("fixed upstream bytes", err)
	}
	<-done
	if dialed.Load() != 1 {
		t.Fatal("unexpected fixed upstream connection count")
	}
	if _, _, err = autoGitBridge(t.Context(), "192.0.2.1:443"); err == nil {
		t.Fatal("nonloopback fixture target accepted")
	}
}
