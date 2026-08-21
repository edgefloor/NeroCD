package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func startLifecycle(t *testing.T, handler http.Handler, configure func(*serverLifecycle)) (string, chan struct{}, <-chan error, *serverLifecycle) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	termination := make(chan struct{})
	lifecycle := &serverLifecycle{Listener: listener, Handler: handler, Termination: termination, Grace: 200 * time.Millisecond}
	if configure != nil {
		configure(lifecycle)
	}
	done := make(chan error, 1)
	go func() { done <- lifecycle.Serve(context.Background()) }()
	return listener.Addr().String(), termination, done, lifecycle
}

func stopLifecycle(t *testing.T, termination chan struct{}, done <-chan error) {
	t.Helper()
	close(termination)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not stop")
	}
}

func TestServerLifecycleBoundsSlowHeaderAndBody(t *testing.T) {
	bodyRead := make(chan error, 1)
	addr, termination, done, _ := startLifecycle(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		bodyRead <- err
		if err != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), func(l *serverLifecycle) {
		l.ReadHeaderTimeout = 40 * time.Millisecond
		l.ReadTimeout = 80 * time.Millisecond
	})
	defer stopLifecycle(t, termination, done)

	slowHeader, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer slowHeader.Close()
	if _, err = io.WriteString(slowHeader, "GET / HTTP/1.1\r\nHost: example"); err != nil {
		t.Fatal(err)
	}
	_ = slowHeader.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err = slowHeader.Read(make([]byte, 1)); err == nil {
		t.Fatal("slow header connection remained open")
	}

	slowBody, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer slowBody.Close()
	if _, err = io.WriteString(slowBody, "POST / HTTP/1.1\r\nHost: example\r\nContent-Length: 4\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-bodyRead:
		if err == nil {
			t.Fatal("slow body was accepted without a timeout")
		}
	case <-time.After(400 * time.Millisecond):
		t.Fatal("slow body handler did not receive a bounded read failure")
	}
}

func TestServerLifecycleBoundsWriteAndIdleConnections(t *testing.T) {
	addr, termination, done, _ := startLifecycle(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/write" {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte("late response"))
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
			return
		}
		_, _ = w.Write([]byte("idle response"))
	}), func(l *serverLifecycle) {
		l.WriteTimeout = 40 * time.Millisecond
		l.IdleTimeout = 50 * time.Millisecond
	})
	defer stopLifecycle(t, termination, done)

	writeConn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writeConn.Close()
	if _, err = io.WriteString(writeConn, "GET /write HTTP/1.1\r\nHost: example\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = writeConn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, err = writeConn.Read(make([]byte, 64)); err == nil {
		t.Fatal("write-timeout response unexpectedly reached client")
	}

	idleConn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer idleConn.Close()
	if _, err = io.WriteString(idleConn, "GET / HTTP/1.1\r\nHost: example\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(idleConn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	time.Sleep(100 * time.Millisecond)
	_ = idleConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err = reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle connection error=%v, want EOF", err)
	}
}

func TestServerLifecycleDrainsInflightAndRejectsNewWorkOnAcceptedConnections(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	drained := make(chan struct{})
	var lifecycle *serverLifecycle
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lifecycle.Draining() {
			switch r.URL.Path {
			case "/health":
				w.WriteHeader(http.StatusOK)
			case "/ready":
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		if r.URL.Path == "/hold" {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	})
	addr, termination, done, gotLifecycle := startLifecycle(t, handler, func(l *serverLifecycle) {
		l.Grace = 500 * time.Millisecond
		l.DrainWindow = 300 * time.Millisecond
		l.OnDrain = func(bool) { close(drained) }
	})
	lifecycle = gotLifecycle
	requestDone := make(chan *http.Response, 1)
	go func() {
		response, err := http.Get("http://" + addr + "/hold")
		if err != nil {
			t.Errorf("in-flight request: %v", err)
			requestDone <- nil
			return
		}
		requestDone <- response
	}()
	<-started
	connections := make([]net.Conn, 0, 3)
	for range 3 {
		connection, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	close(termination)
	<-drained
	for i, path := range []string{"/health", "/ready", "/ordinary"} {
		if _, err := io.WriteString(connections[i], "GET "+path+" HTTP/1.1\r\nHost: example\r\nConnection: close\r\n\r\n"); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		response, err := http.ReadResponse(bufio.NewReader(connections[i]), &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want := http.StatusServiceUnavailable
		if path == "/health" {
			want = http.StatusOK
		}
		if response.StatusCode != want {
			t.Fatalf("draining %s status=%d want=%d", path, response.StatusCode, want)
		}
		_ = response.Body.Close()
	}
	close(release)
	response := <-requestDone
	if response == nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("in-flight response=%v", response)
	}
	_ = response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("clean draining lifecycle did not exit")
	}
}

func TestServerLifecycleForceClosesHungHandler(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	addr, termination, done, lifecycle := startLifecycle(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}), func(l *serverLifecycle) { l.Grace = 50 * time.Millisecond })
	clientDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + addr + "/")
		if err == nil {
			response.Body.Close()
		}
		clientDone <- err
	}()
	<-started
	close(termination)
	time.Sleep(15 * time.Millisecond)
	if !lifecycle.Draining() {
		t.Fatal("lifecycle did not enter drain state")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown deadline") {
			t.Fatalf("hung shutdown error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hung handler was not force-closed")
	}
	close(release)
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client did not observe forced close")
	}
}
