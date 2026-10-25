package runnerruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
)

var errBackupRemoteNetwork = errors.New("remote backup network denied")

// Both the origin and address range come from protected operator configuration.
// Project owners can select a target, but cannot grant access to private networks.
type backupRemoteNetworkPolicy struct {
	privateOrigins map[string][]netip.Prefix
	fixtureOrigin  string
}

var backupRemoteForbiddenNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func (p backupRemoteNetworkPolicy) permits(origin string, ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// Metadata/link-local addresses remain forbidden even under an operator range.
	for _, prefix := range backupRemoteForbiddenNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	if p.fixtureOrigin != "" && origin == p.fixtureOrigin && ip.IsLoopback() {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		for _, prefix := range p.privateOrigins[origin] {
			if !ip.IsLoopback() && prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	return ip.IsGlobalUnicast()
}

// The transport pins every connection to its initial validated address set and
// checks DNS again before every request. Connection reuse, environment proxies,
// redirects, TLS downgrades and SDK endpoint changes cannot bypass that check.
type backupRemoteTransport struct {
	before    func(context.Context) error
	origin    string
	host      string
	port      string
	addresses []netip.Addr
	policy    backupRemoteNetworkPolicy
	lookup    func(context.Context, string) ([]netip.Addr, error)
	transport *http.Transport
}

func newBackupRemoteTransport(ctx context.Context, endpoint string, policy backupRemoteNetworkPolicy) (*backupRemoteTransport, error) {
	return newBackupRemoteTransportWithLookup(ctx, endpoint, policy, net.DefaultResolver.LookupNetIP)
}

func newBackupRemoteTransportWithLookup(ctx context.Context, endpoint string, policy backupRemoteNetworkPolicy, lookup func(context.Context, string, string) ([]netip.Addr, error)) (*backupRemoteTransport, error) {
	if domain.ValidateBackupRemoteEndpoint(endpoint) != nil || lookup == nil {
		return nil, errBackupRemoteNetwork
	}
	u, _ := url.Parse(endpoint)
	origin := u.Scheme + "://" + u.Host
	if u.Scheme != "https" && origin != policy.fixtureOrigin {
		return nil, errBackupRemoteNetwork
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	t := &backupRemoteTransport{origin: origin, host: u.Hostname(), port: port, policy: policy}
	t.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) { return lookup(ctx, "ip", host) }
	addresses, err := t.resolve(ctx)
	if err != nil {
		return nil, errBackupRemoteNetwork
	}
	t.addresses = addresses
	t.transport = &http.Transport{
		Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.host},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != net.JoinHostPort(t.host, t.port) {
				return nil, errBackupRemoteNetwork
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(t.addresses[0].String(), t.port))
		},
	}
	return t, nil
}

func (t *backupRemoteTransport) resolve(ctx context.Context) ([]netip.Addr, error) {
	addresses, err := t.lookup(ctx, t.host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return nil, errBackupRemoteNetwork
	}
	for i, ip := range addresses {
		addresses[i] = ip.Unmap()
		if !t.policy.permits(t.origin, addresses[i]) {
			return nil, errBackupRemoteNetwork
		}
	}
	slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(addresses), nil
}

func (t *backupRemoteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.before != nil && t.before(r.Context()) != nil {
		return nil, backupRemoteError("authority_denied")
	}
	if r.URL.Scheme+"://"+r.URL.Host != t.origin || r.URL.User != nil || r.Host != "" && !strings.EqualFold(r.Host, r.URL.Host) {
		return nil, errBackupRemoteNetwork
	}
	current, err := t.resolve(r.Context())
	if err != nil || !slices.Equal(current, t.addresses) {
		return nil, errBackupRemoteNetwork
	}
	response, err := t.transport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	limit := int64(1 << 20)
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "" {
		limit = domain.HostingDatabaseBackupMaxBytes + 1
	}
	response.Body = &backupRemoteBody{Reader: io.LimitReader(response.Body, limit), Closer: response.Body}
	return response, nil
}

type backupRemoteBody struct {
	io.Reader
	io.Closer
}

func (t *backupRemoteTransport) Close() { t.transport.CloseIdleConnections() }
