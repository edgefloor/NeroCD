package runnerclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentityReadsDedicatedCredentialWithoutOtherRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/runners/identity" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer runner-credential" {
			t.Errorf("unexpected identity request %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"runner_id":"runner_nondefault"}`))
	}))
	defer server.Close()
	id, err := New(server.URL, "runner-credential").Identity(t.Context())
	if err != nil || id != "runner_nondefault" || requests != 1 {
		t.Fatalf("id=%q requests=%d err=%v", id, requests, err)
	}
}

func TestIdentityFailsClosedOnInvalidResponseAndHTTPFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"missing", `{}`, 200}, {"empty", `{"runner_id":""}`, 200}, {"whitespace", `{"runner_id":" runner_x "}`, 200}, {"malformed", `{`, 200}, {"wrong type", `{"runner_id":123}`, 200}, {"null", `null`, 200}, {"unauthorized", `{"error":"denied"}`, 401}, {"unsupported server", `{}`, 404}, {"server error", `{}`, 500}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			id, err := New(server.URL, "credential").Identity(t.Context())
			if err == nil || id != "" {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	id, err := New("http://127.0.0.1:1", "credential").Identity(ctx)
	if id != "" || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "credential") {
		t.Fatalf("canceled transport id=%q err=%v", id, err)
	}
}
