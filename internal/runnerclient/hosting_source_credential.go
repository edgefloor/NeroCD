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
	"nerocd/internal/hostingbuildprotocol"
)

// ErrSourceCredential contains no HTTP, credential, or parser diagnostics.
var ErrSourceCredential = errors.New("hosting source credential unavailable")

// SourceCredentialTransport permits only explicitly trusted literal loopback HTTP
// fixtures. It never enables insecure remote transport or disables TLS verification.
type SourceCredentialTransport struct{ TrustedLoopbackHTTP bool }

// ValidateSourceCredentialServer checks configuration without requests or effects.
func ValidateSourceCredentialServer(server string, options SourceCredentialTransport) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ErrSourceCredential
	}
	if u.Scheme != "https" {
		ip, e := netip.ParseAddr(u.Hostname())
		fixture := options.TrustedLoopbackHTTP && u.Scheme == "http" && e == nil && (ip == netip.MustParseAddr("127.0.0.1") || ip == netip.IPv6Loopback())
		if !fixture {
			return ErrSourceCredential
		}
	}
	return nil
}

// BuildSourceCredential requests an owned credential under one retained random ID.
// runnerID must be the configured runner identity, never a response-derived value.
func (c Client) BuildSourceCredential(ctx context.Context, runnerID string, in domain.HostingSourceCredentialRequest, options SourceCredentialTransport) (hostingbuildprotocol.SourceCredential, error) {
	var zero hostingbuildprotocol.SourceCredential
	if runnerID == "" || hostingbuildprotocol.ValidateAuthority(in.Authority) != nil || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
		return zero, ErrSourceCredential
	}
	if ValidateSourceCredentialServer(c.Server, options) != nil {
		return zero, ErrSourceCredential
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
		return zero, ErrSourceCredential
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
		return zero, ErrSourceCredential
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/builds/source-credential", bytes.NewReader(payload))
	if err != nil {
		return zero, ErrSourceCredential
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return zero, ErrSourceCredential
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		return zero, ErrSourceCredential
	}
	credential, err := hostingbuildprotocol.ReadSourceCredential(resp.Body)
	if err != nil {
		return zero, ErrSourceCredential
	}
	now := time.Now()
	if credential.Authority != in.Authority || credential.DeliveryID != in.DeliveryID || credential.RequestHash != hostingbuildprotocol.SourceCredentialRequestHash(runnerID, in) || !now.Before(credential.UseUntil) || credential.UseUntil.After(now.Add(60*time.Second)) || ctx.Err() != nil {
		credential.Close()
		return zero, ErrSourceCredential
	}
	return credential, nil
}
