// Package notificationtransport sends bounded backup notifications without
// environment proxies, redirects, raw receiver errors, or unvalidated DNS dials.
package notificationtransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/smtp"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// Policy is protected operator configuration, never project-supplied. A private
// grant requires both an exact origin and a matching address range. Loopback
// requires a separate explicit TLS fixture origin and still verifies TLS.
type Policy struct {
	PrivateOrigins    map[string][]netip.Prefix
	FixtureTLSOrigins []string
	RootCAs           *x509.CertPool
}

// Sender delivers notifications under a validated network policy.
type Sender struct {
	policy Policy
	lookup func(context.Context, string, string) ([]netip.Addr, error)
}

// Result classifies delivery success, retryability, and configuration failures.
type Result struct {
	Permanent            bool
	Code                 string
	ConfigurationFailure bool
	Uncertain            bool
}

// ErrAuthorityChanged is definitive revocation, unlike a transient database
// probe failure. Only this sentinel suppresses a durable queued delivery.
var ErrAuthorityChanged = errors.New("notification_authority_changed")
var errNetworkDenied = errors.New("network_denied")
var errNetworkUnavailable = errors.New("network_unavailable")

func authorityFailure(ctx context.Context, err error) Result {
	if ctx.Err() == nil && errors.Is(err, ErrAuthorityChanged) {
		return Result{Code: "authority_changed"}
	}
	return Result{Code: "authority_unavailable"}
}
func networkFailure(err error) Result {
	if errors.Is(err, errNetworkDenied) {
		return Result{Code: "network_denied", ConfigurationFailure: true}
	}
	return Result{Code: "network_unavailable"}
}

// New validates and copies the protected notification network policy.
func New(p Policy) (*Sender, error) {
	copyPolicy := Policy{PrivateOrigins: map[string][]netip.Prefix{}, FixtureTLSOrigins: append([]string(nil), p.FixtureTLSOrigins...), RootCAs: p.RootCAs}
	if p.RootCAs != nil {
		copyPolicy.RootCAs = p.RootCAs.Clone()
	}
	if len(p.PrivateOrigins) > 32 {
		return nil, errors.New("notification_policy_invalid")
	}
	for origin, prefixes := range p.PrivateOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "smtp+tls" && u.Scheme != "smtp+starttls") || len(prefixes) < 1 || len(prefixes) > 32 {
			return nil, errors.New("notification_policy_invalid")
		}
		for _, prefix := range prefixes {
			if !prefix.IsValid() {
				return nil, errors.New("notification_policy_invalid")
			}
		}
		copyPolicy.PrivateOrigins[origin] = append([]netip.Prefix(nil), prefixes...)
	}
	if len(p.FixtureTLSOrigins) > 4 {
		return nil, errors.New("notification_policy_invalid")
	}
	for _, origin := range p.FixtureTLSOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "smtp+tls" && u.Scheme != "smtp+starttls") {
			return nil, errors.New("notification_policy_invalid")
		}
	}
	return &Sender{policy: copyPolicy, lookup: net.DefaultResolver.LookupNetIP}, nil
}

var forbidden = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func (p Policy) permits(origin string, ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, n := range forbidden {
		if n.Contains(ip) {
			return false
		}
	}
	if ip.IsLoopback() {
		return slices.Contains(p.FixtureTLSOrigins, origin)
	}
	if ip.IsPrivate() {
		for _, n := range p.PrivateOrigins[origin] {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return ip.IsGlobalUnicast()
}
func (s *Sender) resolve(ctx context.Context, origin, host string) ([]netip.Addr, error) {
	ips, err := s.lookup(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errNetworkUnavailable
	}
	if len(ips) > 32 {
		return nil, errNetworkDenied
	}
	for i, ip := range ips {
		ips[i] = ip.Unmap()
		if !s.policy.permits(origin, ip) {
			return nil, errNetworkDenied
		}
	}
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(ips), nil
}

// Authorize must recheck the current generation, enabled events, secret version,
// claim fence, and current recipients before every external message.
type Authorize func(context.Context) ([]string, error)

// Send authorizes and delivers a notification within a bounded time budget.
func (s *Sender) Send(ctx context.Context, c *domain.NotificationConfiguration, p domain.NotificationPayload, authorize Authorize) Result {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if s == nil || c == nil || authorize == nil {
		return Result{Code: "configuration_invalid", ConfigurationFailure: true}
	}
	if _, err := authorize(ctx); err != nil {
		return authorityFailure(ctx, err)
	}
	if c.WebhookURL != "" {
		return s.webhook(ctx, c, p, authorize)
	}
	return s.email(ctx, c, p, authorize)
}
func (s *Sender) webhook(ctx context.Context, c *domain.NotificationConfiguration, p domain.NotificationPayload, authorize Authorize) Result {
	if c.Validate("webhook") != nil {
		return Result{Code: "configuration_invalid", ConfigurationFailure: true}
	}
	u, _ := url.Parse(c.WebhookURL)
	origin := u.Scheme + "://" + u.Host
	ips, err := s.resolve(ctx, origin, u.Hostname())
	if err != nil {
		return networkFailure(err)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname(), RootCAs: s.policy.RootCAs}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer tr.CloseIdleConnections()
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(u.Hostname(), port) {
			return nil, errNetworkDenied
		}
		current, e := s.resolve(ctx, origin, u.Hostname())
		if e != nil || !slices.Equal(ips, current) {
			if e != nil {
				return nil, e
			}
			return nil, errNetworkDenied
		}
		if _, e = authorize(ctx); e != nil {
			return nil, e
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > 16384 {
		return Result{Code: "payload_invalid", Permanent: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return Result{Code: "configuration_invalid", ConfigurationFailure: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", p.DeliveryID)
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrAuthorityChanged) {
			return authorityFailure(ctx, err)
		}
		if errors.Is(err, errNetworkDenied) || errors.Is(err, errNetworkUnavailable) {
			return networkFailure(err)
		}
		var unknown x509.UnknownAuthorityError
		var hostname x509.HostnameError
		var invalid x509.CertificateInvalidError
		if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
			return Result{Code: "tls_failed", ConfigurationFailure: true}
		}
		return Result{Code: "delivery_uncertain", Uncertain: true}
	}
	defer func() {
		// The received HTTP status determines delivery; cleanup cannot undo it.
		_ = response.Body.Close()
	}()
	// Drain only a bounded prefix as best-effort cleanup. The status remains
	// authoritative even if the receiver truncates or fails its response body.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return Result{}
	}
	switch response.StatusCode {
	case 401, 403, 404, 410:
		return Result{Code: "receiver_configuration", ConfigurationFailure: true}
	}
	if response.StatusCode >= 300 && response.StatusCode < 500 && response.StatusCode != 408 && response.StatusCode != 429 {
		return Result{Code: "receiver_rejected", Permanent: true}
	}
	return Result{Code: "receiver_rejected"}
}

// readBudgetConn prevents an SMTP peer from returning unbounded greetings or
// response lines, in addition to the whole-attempt time budget.
type readBudgetConn struct {
	net.Conn
	remaining int
}

func (c *readBudgetConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errors.New("smtp_response_limit")
	}
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, e := c.Conn.Read(p)
	c.remaining -= n
	return n, e
}
func smtpFailure(err error, uncertain bool) Result {
	var e *textproto.Error
	if errors.As(err, &e) && (e.Code == 530 || e.Code == 535) {
		return Result{Code: "smtp_authentication", ConfigurationFailure: true}
	}
	if errors.As(err, &e) && e.Code >= 500 {
		return Result{Code: "smtp_rejected", Permanent: true}
	}
	if uncertain {
		return Result{Code: "delivery_uncertain", Uncertain: true}
	}
	return Result{Code: "smtp_unavailable"}
}

func smtpTLSFailure(err error) Result {
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	if errors.As(err, &verification) || errors.As(err, &record) {
		return Result{Code: "tls_failed", ConfigurationFailure: true}
	}
	// EOF, resets, deadlines, cancellation, and temporary SMTP responses do
	// not prove a configuration error. No message has been sent at this point.
	return smtpFailure(err, false)
}

func (s *Sender) email(ctx context.Context, c *domain.NotificationConfiguration, p domain.NotificationPayload, authorize Authorize) Result {
	if c.Validate("email") != nil {
		return Result{Code: "configuration_invalid", ConfigurationFailure: true}
	}
	origin := "smtp+" + c.SMTPSecurity + "://" + net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	ips, err := s.resolve(ctx, origin, c.SMTPHost)
	if err != nil {
		return networkFailure(err)
	}
	recipients, err := authorize(ctx)
	if err != nil {
		return authorityFailure(ctx, err)
	}
	if len(recipients) == 0 {
		return Result{Code: "recipients_unavailable"}
	}
	sent := 0
	for _, recipient := range recipients {
		current, err := authorize(ctx)
		if err != nil {
			return authorityFailure(ctx, err)
		}
		if !slices.Contains(current, recipient) {
			continue
		}
		if !domain.NotificationEmail(recipient) {
			return Result{Code: "recipients_unavailable"}
		}
		currentIPs, e := s.resolve(ctx, origin, c.SMTPHost)
		if e != nil || !slices.Equal(ips, currentIPs) {
			if e != nil {
				return networkFailure(e)
			}
			return networkFailure(errNetworkDenied)
		}
		result := s.emailOne(ctx, c, p, recipient, ips[0], authorize)
		if result.Code != "" {
			return result
		}
		sent++
	}
	if sent == 0 {
		return Result{Code: "recipients_unavailable"}
	}
	return Result{}
}
func (s *Sender) emailOne(ctx context.Context, c *domain.NotificationConfiguration, p domain.NotificationPayload, recipient string, ip netip.Addr, authorize Authorize) Result {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(c.SMTPPort)))
	if err != nil {
		return smtpFailure(err, false)
	}
	defer func() {
		// Release the socket without replacing the SMTP exchange result.
		_ = conn.Close()
	}()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return smtpFailure(err, false)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.SMTPHost, RootCAs: s.policy.RootCAs}
	var wire net.Conn = &readBudgetConn{Conn: conn, remaining: 65536}
	if c.SMTPSecurity == "tls" {
		secured := tls.Client(wire, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return smtpTLSFailure(err)
		}
		wire = secured
	}
	client, err := smtp.NewClient(wire, c.SMTPHost)
	if err != nil {
		return smtpFailure(err, false)
	}
	defer func() {
		// DATA acceptance or the exchange error already determines the result.
		_ = client.Close()
	}()
	if err = client.Hello("nerocd.invalid"); err != nil {
		return smtpFailure(err, false)
	}
	if c.SMTPSecurity == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return Result{Code: "tls_required", ConfigurationFailure: true}
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return smtpTLSFailure(err)
		}
	}
	if c.SMTPUsername != "" {
		if err = client.Auth(smtp.PlainAuth("", c.SMTPUsername, c.SMTPPassword, c.SMTPHost)); err != nil {
			return smtpFailure(err, false)
		}
	}
	current, err := authorize(ctx)
	if err != nil {
		return authorityFailure(ctx, err)
	}
	if !slices.Contains(current, recipient) {
		return Result{Code: "recipients_unavailable"}
	}
	if err = client.Mail(c.FromAddress); err != nil {
		return smtpFailure(err, false)
	}
	if err = client.Rcpt(recipient); err != nil {
		return smtpFailure(err, false)
	}
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: NeroCD: %s\r\nMessage-ID: <%s@nerocd.invalid>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\nEvent: %s\r\nEvent ID: %s\r\nDelivery ID: %s\r\nSource: %s/%s\r\nSource state: %s\r\nFailure code: %s\r\n%s\r\n", c.FromAddress, recipient, domain.NotificationSourceMessage(p.Event, p.SourceKind, p.SourceState), p.DeliveryID, domain.NotificationSourceMessage(p.Event, p.SourceKind, p.SourceState), p.Event, p.EventID, p.DeliveryID, p.SourceKind, p.SourceID, p.SourceState, p.FailureCode, p.RecoveryURL)
	if len(body) > 16384 || strings.ContainsAny(p.DeliveryID+p.FailureCode+p.RecoveryURL, "\r\n\x00") {
		return Result{Code: "payload_invalid", Permanent: true}
	}
	w, err := client.Data()
	if err != nil {
		return smtpFailure(err, false)
	}
	if _, err = io.WriteString(w, body); err != nil {
		return smtpFailure(err, true)
	}
	if err = w.Close(); err != nil {
		return smtpFailure(err, true)
	}
	// DATA acceptance is the delivery acknowledgement. A failed QUIT cannot
	// turn an accepted message into another attempt.
	_ = client.Quit()
	return Result{}
}
