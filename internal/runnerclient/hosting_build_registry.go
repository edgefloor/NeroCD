package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

// BuildRegistryCredential retrieves only the snapshot-selected credential.
func (c Client) BuildRegistryCredential(ctx context.Context, runnerID string, in domain.HostingBuildRegistryCredentialRequest, options SourceCredentialTransport) (*domain.HostingRegistryCredentialDelivery, error) {
	var zero *domain.HostingRegistryCredentialDelivery
	if runnerID == "" || hostingbuildprotocol.ValidateAuthority(in.Authority) != nil || !domain.ValidHostingSourceDeliveryID(in.DeliveryID) {
		return zero, ErrRuntimeEnvironment
	}
	if ValidateSourceCredentialServer(c.Server, options) != nil {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/builds/registry-credential", bytes.NewReader(payload))
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
	delivery, err := domain.ReadHostingRegistryDelivery(resp.Body)
	if err != nil {
		return nil, ErrRuntimeEnvironment
	}
	now := time.Now()
	if !now.Before(delivery.UseUntil) || delivery.UseUntil.After(now.Add(time.Minute)) || ctx.Err() != nil {
		delivery.Close()
		return nil, ErrRuntimeEnvironment
	}
	return delivery, nil
}
