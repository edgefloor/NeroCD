package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceCredentialNoStoreAcrossFailures(t *testing.T) {
	for _, test := range []struct {
		name, method, body, content string
		draining                    bool
	}{
		{"authentication", "POST", `{}`, "application/json", false},
		{"malformed", "POST", `{`, "application/json", false},
		{"content_type", "POST", `{}`, "text/plain", false},
		{"method", "GET", ``, "", false},
		{"draining", "POST", `{}`, "application/json", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newTestServer()
			s.SetDraining(test.draining)
			req := httptest.NewRequest(test.method, "/api/v1/runners/builds/source-credential", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.content)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, req)
			if response.Code < http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%s", response.Code, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestSourceCredentialTLSTerminationTrust(t *testing.T) {
	for _, test := range []struct {
		name, peer, host    string
		proto               []string
		forwarded           string
		fixture, tls, allow bool
	}{
		{name: "direct TLS", peer: "unparsed", tls: true, allow: true},
		{name: "trusted proxy", peer: "192.0.2.1:9000", proto: []string{"https"}, allow: true},
		{name: "untrusted spoof", peer: "192.0.2.2:9000", proto: []string{"https"}},
		{name: "duplicate", peer: "192.0.2.1:9000", proto: []string{"https", "https"}},
		{name: "comma list", peer: "192.0.2.1:9000", proto: []string{"https,http"}},
		{name: "case", peer: "192.0.2.1:9000", proto: []string{"HTTPS"}},
		{name: "whitespace", peer: "192.0.2.1:9000", proto: []string{" https"}},
		{name: "missing proxy assertion", peer: "192.0.2.1:9000"},
		{name: "malformed peer", peer: "192.0.2.1", proto: []string{"https"}},
		{name: "default loopback denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080"},
		{name: "explicit loopback", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, allow: true},
		{name: "explicit IPv6 loopback", peer: "[::1]:9000", host: "[::1]:8080", fixture: true, allow: true},
		{name: "fixture DNS denied", peer: "127.0.0.1:9000", host: "localhost:8080", fixture: true},
		{name: "fixture remote Host denied", peer: "127.0.0.1:9000", host: "example.com", fixture: true},
		{name: "fixture remote peer denied", peer: "192.0.2.2:9000", host: "127.0.0.1:8080", fixture: true},
		{name: "fixture forwarded denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, forwarded: "for=127.0.0.1;proto=https"},
		{name: "fixture proxy header denied", peer: "127.0.0.1:9000", host: "127.0.0.1:8080", fixture: true, proto: []string{"https"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newTestServerWithConfig(ServerConfig{TrustedProxyCIDRs: []string{"192.0.2.1/32"}, TrustedLoopbackSourceCredentialHTTP: test.fixture})
			r := httptest.NewRequest("POST", "http://example.com/api/v1/runners/builds/source-credential", nil)
			r.RemoteAddr = test.peer
			r.Host = test.host
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for _, p := range test.proto {
				r.Header.Add("X-Forwarded-Proto", p)
			}
			if test.forwarded != "" {
				r.Header.Set("Forwarded", test.forwarded)
			}
			if got := s.sourceCredentialTransport(r); got != test.allow {
				t.Fatalf("transport=%v expected=%v", got, test.allow)
			}
		})
	}
}
