package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

// ErrRuntimeEnvironment contains no HTTP, environment, or parser diagnostics.
var ErrRuntimeEnvironment = errors.New("hosting runtime environment unavailable")

// RuntimeEnvironmentTransport permits only explicitly trusted literal loopback HTTP
// fixtures. It never enables insecure remote transport or disables TLS verification.
type RuntimeEnvironmentTransport struct{ TrustedLoopbackHTTP bool }

// ValidateRuntimeEnvironmentServer checks configuration without requests or effects.
func ValidateRuntimeEnvironmentServer(server string, options RuntimeEnvironmentTransport) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ErrRuntimeEnvironment
	}
	if u.Scheme != "https" {
		ip, e := netip.ParseAddr(u.Hostname())
		fixture := options.TrustedLoopbackHTTP && u.Scheme == "http" && e == nil && (ip == netip.MustParseAddr("127.0.0.1") || ip == netip.IPv6Loopback())
		if !fixture {
			return ErrRuntimeEnvironment
		}
	}
	return nil
}

// HostingRuntimeEnvironment requests protected values under one retained random ID.
// runnerID must be the configured runner identity, never a response-derived value.
func (c Client) HostingRuntimeEnvironment(ctx context.Context, runnerID string, in domain.HostingRuntimeEnvironmentRequest, options RuntimeEnvironmentTransport) (*hostingruntimeprotocol.Environment, error) {
	var zero *hostingruntimeprotocol.Environment
	if runnerID == "" || hostingruntimeprotocol.ValidateRequest(in) != nil {
		return zero, ErrRuntimeEnvironment
	}
	if ValidateRuntimeEnvironmentServer(c.Server, options) != nil {
		return zero, ErrRuntimeEnvironment
	}
	u, _ := url.Parse(c.Server)
	fixture := u.Scheme == "http"
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	client := *hc
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, ok := transport.(*http.Transport)
	if !ok || (base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify) {
		return zero, ErrRuntimeEnvironment
	}
	clone := base.Clone()
	if fixture {
		clone.Proxy = nil
	}
	defer clone.CloseIdleConnections()
	client.Transport = clone
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	payload, err := json.Marshal(in)
	if err != nil {
		return zero, ErrRuntimeEnvironment
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/runtime-environment", bytes.NewReader(payload))
	if err != nil {
		return zero, ErrRuntimeEnvironment
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return zero, &APIError{StatusCode: 503, Err: ErrRuntimeEnvironment}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return zero, &APIError{StatusCode: resp.StatusCode, Err: ErrRuntimeEnvironment}
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		return zero, ErrRuntimeEnvironment
	}
	credential, err := hostingruntimeprotocol.ReadEnvironment(resp.Body)
	if err != nil {
		return zero, ErrRuntimeEnvironment
	}
	now := time.Now()
	m, metadataErr := credential.Metadata()
	if metadataErr != nil || m.Authority != in.Authority || m.DeliveryID != in.DeliveryID || m.PlanHash != in.PlanHash || m.RuntimeConfiguration != in.RuntimeConfiguration || m.RequestHash != hostingruntimeprotocol.RuntimeEnvironmentRequestHash(runnerID, in) || m.Purpose != hostingruntimeprotocol.Purpose || !now.Before(m.UseUntil) || m.UseUntil.After(now.Add(60*time.Second)) || ctx.Err() != nil {
		credential.Close()
		return zero, ErrRuntimeEnvironment
	}
	return credential, nil
}
