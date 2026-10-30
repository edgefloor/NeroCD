package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDatabaseDownloadRealStalledSocketInterruptReleasesCapacity(t *testing.T) {
	var broker databaseDownloadBroker
	trigger := make(chan struct{})
	entered := make(chan struct{})
	joined := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		session, ok := broker.reserve("session", "actor", ctx, cancel, func(context.Context) error { return nil })
		if !ok {
			t.Error("reserve")
			return
		}
		defer broker.remove("session", false)
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(time.Minute))
		interrupted := make(chan struct{})
		go func() {
			defer close(interrupted)
			<-trigger
			session.cancel()
			interruptDatabaseDownloadBody(controller, r.Body, session, context.Canceled)
		}()
		close(entered)
		var data [32]byte
		if _, err := io.ReadFull(r.Body, data[:]); err == nil {
			t.Error("stalled body unexpectedly complete")
		}
		<-interrupted
		close(joined)
	}))
	server.Config.ReadTimeout = 100 * time.Millisecond
	server.Start()
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 32\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler absent")
	}
	close(trigger)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("real stalled socket was not interrupted")
	}
	// Wait for handler's remove before asserting the later actor can reserve.
	deadline := time.Now().Add(time.Second)
	for broker.present("session") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, ok := broker.reserve("next", "actor", ctx, cancel, func(context.Context) error { return nil }); !ok {
		t.Fatal("actor capacity leaked")
	}
	broker.remove("next", false)
}
