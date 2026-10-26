package notificationtransport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func notificationTestPayload() domain.NotificationPayload {
	return domain.NotificationPayload{EventID: "nte_stable", DeliveryID: "ntd_stable", Event: "upload_failed", Message: "Database backup succeeded locally, remote upload failed", SourceKind: "transfer", SourceID: "transfer_stable", SourceState: "failed", FailureCode: "remote_unavailable", LocalCopyAvailable: true, RecoveryURL: "https://nerocd.example/projects?project_id=p&backup_id=b"}
}
func tlsPolicy(t *testing.T, s *httptest.Server) Policy {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	return Policy{FixtureTLSOrigins: []string{s.URL}, RootCAs: roots}
}
func TestNotificationPolicyDeniesUnsafeAndPinsOrigins(t *testing.T) {
	p := Policy{PrivateOrigins: map[string][]netip.Prefix{"https://inside.example": {netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16")}}}
	for _, value := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "100.100.100.200", "0.0.0.0", "192.0.2.1", "64:ff9b::a9fe:a9fe", "::ffff:127.0.0.1"} {
		if p.permits("https://inside.example", netip.MustParseAddr(value)) {
			t.Fatal("unsafe address accepted", value)
		}
	}
	if !p.permits("https://inside.example", netip.MustParseAddr("10.2.3.4")) || p.permits("https://other.example", netip.MustParseAddr("10.2.3.4")) {
		t.Fatal("origin grant not exact")
	}
	s, _ := New(Policy{})
	s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := s.resolve(t.Context(), "https://example.com", "example.com"); err == nil {
		t.Fatal("mixed DNS set accepted")
	}
	if _, err := New(Policy{PrivateOrigins: map[string][]netip.Prefix{"http://example.com": {netip.MustParsePrefix("10.0.0.0/8")}}}); err == nil {
		t.Fatal("plaintext grant accepted")
	}
}
func TestNotificationWebhookStableRetryRedirectAndSafeErrors(t *testing.T) {
	var count atomic.Int32
	var redirect atomic.Bool
	var mu sync.Mutex
	var seen []domain.NotificationPayload
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, "https://169.254.169.254/metadata", 302)
			return
		}
		defer r.Body.Close()
		var p domain.NotificationPayload
		if json.NewDecoder(io.LimitReader(r.Body, 16385)).Decode(&p) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		if r.Header.Get("Idempotency-Key") != p.DeliveryID {
			t.Error("missing stable key")
		}
		if count.Add(1) == 1 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "canary_receiver_secret")
			return
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	s, err := New(tlsPolicy(t, receiver))
	if err != nil {
		t.Fatal(err)
	}
	c := &domain.NotificationConfiguration{WebhookURL: receiver.URL + "/private?token=canary"}
	p := notificationTestPayload()
	authorize := func(context.Context) ([]string, error) { return nil, nil }
	first := s.Send(t.Context(), c, p, authorize)
	if first.Code != "receiver_rejected" || first.Permanent || first.ConfigurationFailure || strings.Contains(fmt.Sprint(first), "canary") {
		t.Fatal("transient response classification", first)
	}
	if second := s.Send(t.Context(), c, p, authorize); second.Code != "" {
		t.Fatal(second)
	}
	mu.Lock()
	equal := len(seen) == 2 && seen[0] == seen[1]
	mu.Unlock()
	if !equal {
		t.Fatal("retry changed immutable payload")
	}
	redirect.Store(true)
	if r := s.Send(t.Context(), c, p, authorize); !r.Permanent || r.Code != "receiver_rejected" {
		t.Fatal("redirect not rejected", r)
	}
	if r := s.Send(t.Context(), c, p, func(context.Context) ([]string, error) { return nil, ErrAuthorityChanged }); r.Code != "authority_changed" {
		t.Fatal(r)
	}
	if r := s.Send(t.Context(), c, p, func(context.Context) ([]string, error) { return nil, errors.New("canary_database_unavailable") }); r.Code != "authority_unavailable" {
		t.Fatal("transient probe suppressed delivery", r)
	}
}
func TestNotificationWebhookCertificateAndDNSRebinding(t *testing.T) {
	var calls atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer receiver.Close()
	s, _ := New(Policy{FixtureTLSOrigins: []string{receiver.URL}})
	c := &domain.NotificationConfiguration{WebhookURL: receiver.URL}
	authorize := func(context.Context) ([]string, error) { return nil, nil }
	if r := s.Send(t.Context(), c, notificationTestPayload(), authorize); r.Code == "" || calls.Load() != 0 {
		t.Fatal("untrusted TLS accepted")
	}
	s, _ = New(tlsPolicy(t, receiver))
	var lookups int
	s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	if r := s.Send(t.Context(), c, notificationTestPayload(), authorize); r.Code == "" || calls.Load() != 0 {
		t.Fatal("DNS rebinding dialed")
	}
}
func TestNotificationSMTPRejectsSTARTTLSDowngradeBeforeCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	s, _ := New(Policy{FixtureTLSOrigins: []string{"smtp+starttls://" + listener.Addr().String()}})
	configuration := &domain.NotificationConfiguration{SMTPHost: host, SMTPPort: number, SMTPSecurity: "starttls", SMTPUsername: "fixture", SMTPPassword: "canary", FromAddress: "sender@example.com", RecipientUserIDs: []string{"member"}}
	done := make(chan bool, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- false
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 fixture ESMTP\r\n")
		line, e := reader.ReadString('\n')
		if e != nil || !strings.HasPrefix(line, "EHLO") {
			done <- false
			return
		}
		_, _ = io.WriteString(conn, "250 fixture without STARTTLS\r\n")
		line, _ = reader.ReadString('\n')
		done <- strings.HasPrefix(line, "AUTH") || strings.HasPrefix(line, "MAIL")
	}()
	r := s.Send(t.Context(), configuration, notificationTestPayload(), func(context.Context) ([]string, error) { return []string{"member@example.com"}, nil })
	if r.Code != "tls_required" || !r.ConfigurationFailure {
		t.Fatal("SMTP downgraded", r)
	}
	if <-done {
		t.Fatal("credentials or message sent without TLS")
	}
}

func TestNotificationSMTPVerifiedTLSStableMessageAndCancellation(t *testing.T) {
	certSource := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certSource.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certSource.TLS.Certificates})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := x509.NewCertPool()
	root.AddCert(certSource.Certificate())
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	s, _ := New(Policy{FixtureTLSOrigins: []string{"smtp+tls://" + listener.Addr().String()}, RootCAs: root})
	c := &domain.NotificationConfiguration{SMTPHost: host, SMTPPort: n, SMTPSecurity: "tls", SMTPUsername: "fixture", SMTPPassword: "canary-password", FromAddress: "sender@example.com", RecipientUserIDs: []string{"member"}}
	message := make(chan string, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 fixture ESMTP\r\n")
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(conn, "250-fixture\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN "):
				decoded, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(line, "AUTH PLAIN ")))
				if string(decoded) != "\x00fixture\x00canary-password" {
					return
				}
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case line == "DATA\r\n":
				_, _ = io.WriteString(conn, "354 data\r\n")
				var body strings.Builder
				for {
					l, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					body.WriteString(l)
					if body.Len() > 16384 {
						return
					}
				}
				message <- body.String()
				_, _ = io.WriteString(conn, "250 queued\r\n")
			case line == "QUIT\r\n":
				_, _ = io.WriteString(conn, "221 bye\r\n")
				return
			default:
				return
			}
		}
	}()
	result := s.Send(t.Context(), c, notificationTestPayload(), func(context.Context) ([]string, error) { return []string{"member@example.com"}, nil })
	if result.Code != "" {
		t.Fatal(result)
	}
	select {
	case body := <-message:
		for _, want := range []string{"Message-ID: <ntd_stable@nerocd.invalid>", "nte_stable", "transfer_stable", "remote_unavailable"} {
			if !strings.Contains(body, want) {
				t.Fatal("missing safe identity", want)
			}
		}
		if strings.Contains(body, "canary-password") {
			t.Fatal("credential in body")
		}
	case <-time.After(time.Second):
		t.Fatal("missing message")
	}
	<-serverDone
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result = s.Send(ctx, c, notificationTestPayload(), func(ctx context.Context) ([]string, error) { return nil, ctx.Err() }); result.Code != "authority_unavailable" {
		t.Fatal("cancellation ignored")
	}
}
