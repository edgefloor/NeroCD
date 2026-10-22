package runnerclient

import (
	"context"
	"errors"
	"io"
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

func TestIdentityRequiresBoundedSingleDocument(t *testing.T) {
	const limit = 1 << 20
	valid := `{"runner_id":"runner_actual"}`
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"trailing garbage", valid + " trailing-secret-sentinel", false},
		{"concatenated object", valid + `{"runner_id":"runner_other"}`, false},
		{"extra scalar", valid + ` true`, false},
		{"valid whitespace", " \t\r\n" + valid + " \t\r\n", true},
		{"exact size bound", valid + strings.Repeat(" ", limit-len(valid)), true},
		{"over size whitespace", valid + strings.Repeat(" ", limit-len(valid)+1), false},
		{"over size object", `{"runner_id":"` + strings.Repeat("a", limit) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			id, err := New(server.URL, "runner-credential").Identity(t.Context())
			if tc.valid {
				if err != nil || id != "runner_actual" {
					t.Fatalf("valid document rejected: id=%q err=%v", id, err)
				}
			} else {
				if err == nil || id != "" {
					t.Fatalf("invalid identity document accepted: id=%q err=%v", id, err)
				}
				if strings.Contains(err.Error(), "trailing-secret-sentinel") {
					t.Fatalf("response reflected in error: %v", err)
				}
			}
		})
	}
}

type countedIdentityReader struct {
	io.Reader
	bytesRead int
}

func (r *countedIdentityReader) Read(buffer []byte) (int, error) {
	n, err := r.Reader.Read(buffer)
	r.bytesRead += n
	return n, err
}

func TestIdentityDecodeStopsAtSizeBound(t *testing.T) {
	body := &countedIdentityReader{Reader: strings.NewReader(`{"runner_id":"runner_actual"}` + strings.Repeat(" ", 2*maxIdentityResponseBytes))}
	var response identityResponse
	if err := response.decodeResponse(body); err == nil {
		t.Fatal("oversized identity accepted")
	}
	if body.bytesRead != maxIdentityResponseBytes+1 {
		t.Fatalf("read=%d want=%d", body.bytesRead, maxIdentityResponseBytes+1)
	}
	if response.RunnerID != "" {
		t.Fatalf("invalid response published identity %q", response.RunnerID)
	}
}
