package runnerclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func clientReadinessChallenge() domain.ServerReadinessChallenge {
	now := time.Now().UTC()
	return domain.ServerReadinessChallenge{SampleID: "srd_" + strings.Repeat("a", 32), Profile: domain.ServerReadinessProfile, ServerID: "server", DestinationID: "destination", RunnerID: "runner", IssuedAt: now, ExpiresAt: now.Add(10 * time.Second)}
}
func clientReadinessReport() domain.ServerReadinessReport {
	return domain.ServerReadinessReport{SampleID: clientReadinessChallenge().SampleID, Profile: domain.ServerReadinessProfile, OS: "unknown", Architecture: "unknown", Execution: "enabled", Configuration: "matched", Docker: "unreachable", DaemonOS: "unknown", DaemonArchitecture: "unknown", CLI: "unknown", WorkRoot: "unknown", JournalRoot: "unknown"}
}

func TestServerReadinessClientStrictChallengeAndAck(t *testing.T) {
	valid, _ := json.Marshal(clientReadinessChallenge())
	for _, body := range []string{`null`, `{}`, string(valid) + ` {}`, strings.Replace(string(valid), `"profile":`, `"Profile":`, 1), strings.Replace(string(valid), `"profile":`, `"profile":"basic_image_hosting.v1","profile":`, 1), strings.Replace(string(valid), `"runner_id":"runner"`, `"runner_id":"other"`, 1), strings.Replace(string(valid), `basic_image_hosting.v1`, `basic_image_hosting.v2`, 1), strings.Repeat(" ", 8193) + string(valid)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		client := New(server.URL, "fixture")
		_, err := client.ServerReadinessChallenge(t.Context(), "runner", ServerReadinessTransport{TrustedLoopbackHTTP: true})
		server.Close()
		if err == nil || ClassifyFailure(err) == FailureTransient {
			t.Fatalf("invalid response treated as retryable: %v", err)
		}
	}
	challenge := clientReadinessChallenge()
	for _, change := range []func(*domain.ServerReadinessChallenge){func(v *domain.ServerReadinessChallenge) { v.ServerID = "../server" }, func(v *domain.ServerReadinessChallenge) { v.ExpiresAt = v.IssuedAt.Add(11 * time.Second) }, func(v *domain.ServerReadinessChallenge) {
		v.IssuedAt = v.IssuedAt.Add(time.Minute)
		v.ExpiresAt = v.IssuedAt.Add(10 * time.Second)
	}, func(v *domain.ServerReadinessChallenge) {
		v.IssuedAt = v.IssuedAt.Add(-time.Minute)
		v.ExpiresAt = v.IssuedAt.Add(10 * time.Second)
	}, func(v *domain.ServerReadinessChallenge) { v.SampleID = "srd_bad" }} {
		v := challenge
		change(&v)
		if ValidateServerReadinessChallenge(v, "runner", time.Now()) == nil {
			t.Fatal(v)
		}
	}
	for _, mode := range []string{"valid", "wrong_sample", "not_accepted", "missing_time", "duplicate", "case", "null", "unknown"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fixture" || r.Method != "POST" {
				t.Error("request authority")
			}
			ack := domain.ServerReadinessAck{SampleID: challenge.SampleID, ObservedAt: time.Now().UTC(), Accepted: true}
			if mode == "wrong_sample" {
				ack.SampleID = "other"
			}
			if mode == "not_accepted" {
				ack.Accepted = false
			}
			if mode == "missing_time" {
				ack.ObservedAt = time.Time{}
			}
			raw, _ := json.Marshal(ack)
			text := string(raw)
			if mode == "duplicate" {
				text = strings.Replace(text, `"accepted":true`, `"accepted":true,"accepted":true`, 1)
			}
			if mode == "case" {
				text = strings.Replace(text, `"accepted":`, `"Accepted":`, 1)
			}
			if mode == "null" {
				text = strings.Replace(text, `"accepted":true`, `"accepted":null`, 1)
			}
			if mode == "unknown" {
				text = strings.Replace(text, `"accepted":true`, `"accepted":true,"path":"secret"`, 1)
			}
			_, _ = w.Write([]byte(text))
		}))
		client := New(server.URL, "fixture")
		_, err := client.ReportServerReadiness(t.Context(), clientReadinessReport(), ServerReadinessTransport{TrustedLoopbackHTTP: true})
		server.Close()
		if (err == nil) != (mode == "valid") {
			t.Fatal(mode, err)
		}
	}
}

func TestServerReadinessClientTransport(t *testing.T) {
	for _, server := range []string{"http://example.com", "http://127.0.0.1:1234", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1"} {
		if ValidateServerReadinessServer(server, ServerReadinessTransport{}) == nil {
			t.Fatal(server)
		}
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	if _, err := New(server.URL, "fixture").ServerReadinessChallenge(t.Context(), "runner", ServerReadinessTransport{TrustedLoopbackHTTP: true}); err == nil || redirected.Load() != 0 {
		t.Fatal("followed redirect", err)
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(clientReadinessChallenge())
	}))
	defer tlsServer.Close()
	if _, err := New(tlsServer.URL, "fixture").ServerReadinessChallenge(t.Context(), "runner"); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	client := New(tlsServer.URL, "fixture")
	client.HTTP = tlsServer.Client()
	var proxied atomic.Int32
	transport := client.HTTP.Transport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) { proxied.Add(1); return nil, nil }
	client.HTTP = &http.Client{Transport: transport}
	if _, err := client.ServerReadinessChallenge(t.Context(), "runner"); err != nil || proxied.Load() != 0 {
		t.Fatal("proxy inherited", err)
	}
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client.HTTP = &http.Client{Transport: transport}
	if _, err := client.ServerReadinessChallenge(t.Context(), "runner"); err == nil {
		t.Fatal("insecure TLS accepted")
	}
	delayed := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer delayed.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := New(delayed.URL, "fixture").ServerReadinessChallenge(ctx, "runner", ServerReadinessTransport{TrustedLoopbackHTTP: true}); err == nil || time.Since(started) > time.Second {
		t.Fatal("deadline lost", err)
	}
}
