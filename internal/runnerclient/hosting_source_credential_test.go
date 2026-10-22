package runnerclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

func TestBuildSourceCredentialTransportAndIdentity(t *testing.T) {
	in := domain.HostingSourceCredentialRequest{Authority: domain.HostingBuildAuthority{BuildID: "credential", RunID: "build_run_credential", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32)}
	for _, test := range []string{"success", "wrong_runner", "wrong_authority", "wrong_delivery", "expired", "long_window", "error_body", "malformed", "cache", "redirect"} {
		t.Run(test, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if r.URL.Path != "/api/v1/runners/builds/source-credential" || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong request")
				}
				var request domain.HostingSourceCredentialRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request != in {
					t.Error("wrong intent")
				}
				if test == "redirect" {
					http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
					return
				}
				if test == "error_body" {
					http.Error(w, "private-upstream-credential", http.StatusServiceUnavailable)
					return
				}
				if test == "malformed" {
					_, _ = fmt.Fprint(w, `{"private":"private-upstream-credential"}`)
					return
				}
				if test == "cache" {
					w.Header().Del("Cache-Control")
				}
				c := hostingbuildprotocol.SourceCredential{Authority: in.Authority, DeliveryID: in.DeliveryID, RequestHash: hostingbuildprotocol.SourceCredentialRequestHash("runner", in), Kind: "https_basic", Username: "u", Bytes: []byte("private-password"), UseUntil: time.Now().Add(30 * time.Second)}
				defer c.Close()
				switch test {
				case "wrong_runner":
					c.RequestHash = hostingbuildprotocol.SourceCredentialRequestHash("other", in)
				case "wrong_authority":
					c.Authority.Fence = "other"
				case "wrong_delivery":
					c.DeliveryID = strings.Repeat("b", 32)
				case "expired":
					c.UseUntil = time.Now().Add(-time.Second)
				case "long_window":
					c.UseUntil = time.Now().Add(2 * time.Minute)
				}
				if err := hostingbuildprotocol.WriteSourceCredential(w, c); err != nil {
					t.Error(err)
				}
			}))
			defer s.Close()
			c := New(s.URL, "token")
			c.HTTP = s.Client()
			out, err := c.BuildSourceCredential(context.Background(), "runner", in, SourceCredentialTransport{})
			t.Cleanup(out.Close)
			if test == "success" {
				if err != nil || string(out.Bytes) != "private-password" {
					t.Fatal("delivery failed", err)
				}
			} else if err == nil || len(out.Bytes) > 0 || strings.Contains(fmt.Sprintf("%+v", err), "private") {
				t.Fatal("unsafe response", err)
			}
		})
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer s.Close()
	c := New(s.URL, "token")
	if _, err := c.BuildSourceCredential(t.Context(), "runner", in, SourceCredentialTransport{}); err == nil || calls != 0 {
		t.Fatal("insecure request attempted")
	}
	if _, err := c.BuildSourceCredential(t.Context(), "runner", in, SourceCredentialTransport{TrustedLoopbackHTTP: true}); err == nil || calls != 1 {
		t.Fatal("explicit fixture transport not used")
	}
	c.Server = strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	if _, err := c.BuildSourceCredential(t.Context(), "runner", in, SourceCredentialTransport{TrustedLoopbackHTTP: true}); err == nil || calls != 1 {
		t.Fatal("fixture accepted DNS")
	}
	c = New("https://example.invalid", "token")
	c.HTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if _, err := c.BuildSourceCredential(t.Context(), "runner", in, SourceCredentialTransport{}); err == nil {
		t.Fatal("TLS bypass accepted")
	}
}
