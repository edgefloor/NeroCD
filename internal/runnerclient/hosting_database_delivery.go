package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
)

// ErrDatabaseCredentials contains no HTTP, environment, or parser diagnostics.
var ErrDatabaseCredentials = errors.New("hosting database credentials unavailable")

// HostingDatabaseCredentials requests protected values under one retained random ID.
// plan is the previously accepted prepared plan, never response-derived data.
// runnerID must be the configured runner identity, never a response-derived value.
func (c Client) HostingDatabaseCredentials(ctx context.Context, runnerID string, in domain.HostingDatabaseCredentialRequest, plan domain.HostingApplyPlan, options RuntimeEnvironmentTransport) (*hostingdatabaseprotocol.Credentials, error) {
	var zero *hostingdatabaseprotocol.Credentials
	if runnerID == "" || in.Validate() != nil || domain.ValidateHostingDatabasePlan(plan) != nil || plan.Database == nil || plan.Hash != in.PlanHash {
		return zero, ErrDatabaseCredentials
	}
	if ValidateRuntimeEnvironmentServer(c.Server, options) != nil {
		return zero, ErrDatabaseCredentials
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
		return zero, ErrDatabaseCredentials
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
		return zero, ErrDatabaseCredentials
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/database-credentials", bytes.NewReader(payload))
	if err != nil {
		return zero, ErrDatabaseCredentials
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return zero, &APIError{StatusCode: 503, Err: ErrDatabaseCredentials}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return zero, &APIError{StatusCode: resp.StatusCode, Err: ErrDatabaseCredentials}
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		return zero, ErrDatabaseCredentials
	}
	credential, err := hostingdatabaseprotocol.ReadCredentials(resp.Body)
	if err != nil {
		return zero, ErrDatabaseCredentials
	}
	now := time.Now()
	m, metadataErr := credential.Metadata()
	if metadataErr != nil || m.Authority != in.Authority || m.DeliveryID != in.DeliveryID || m.PlanHash != in.PlanHash || m.RunnerID != runnerID || m.Mode != in.Mode || m.Database != plan.Database.Reference || !slices.Equal(m.References, plan.Database.Credentials) || m.RequestHash != hostingdatabaseprotocol.RequestHash(runnerID, in) || m.Purpose != hostingdatabaseprotocol.Purpose || !now.Before(m.UseUntil) || m.UseUntil.After(now.Add(60*time.Second)) || ctx.Err() != nil {
		credential.Close()
		return zero, ErrDatabaseCredentials
	}
	return credential, nil
}
