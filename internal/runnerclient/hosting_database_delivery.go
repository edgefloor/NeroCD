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
	return c.hostingDatabaseCredentials(ctx, runnerID, in, plan, options, domain.ValidateHostingDatabasePlan(plan) == nil)
}

// HostingDatabaseUpgradeCredentials keeps v2 delivery behind the full accepted proof.
// The request and protected response wire contracts are unchanged.
func (c Client) HostingDatabaseUpgradeCredentials(ctx context.Context, runnerID string, in domain.HostingDatabaseCredentialRequest, op domain.HostingOperation, options RuntimeEnvironmentTransport) (*hostingdatabaseprotocol.Credentials, error) {
	if op.DatabaseUpgrade == nil || op.Plan == nil || domain.ValidateHostingDatabaseUpgradeOperation(op) != nil || op.ID != in.Authority.OperationID || op.Snapshot.RunnerID != runnerID || op.TaskMode != in.Mode || op.RestoreOwnershipHeld {
		return nil, ErrDatabaseCredentials
	}
	if in.Mode == "deploy" && (op.RunID != in.Authority.RunID || op.State != "prepared" || op.Result != nil) || in.Mode == "observe" && (op.ReconciliationRunID != in.Authority.RunID || !op.ReconcileRequested || op.Result == nil || op.Result.ContainerID == "") {
		return nil, ErrDatabaseCredentials
	}
	return c.hostingDatabaseCredentials(ctx, runnerID, in, *op.Plan, options, true)
}

func (c Client) hostingDatabaseCredentials(ctx context.Context, runnerID string, in domain.HostingDatabaseCredentialRequest, plan domain.HostingApplyPlan, options RuntimeEnvironmentTransport, valid bool) (*hostingdatabaseprotocol.Credentials, error) {
	var zero *hostingdatabaseprotocol.Credentials
	if !valid || runnerID == "" || in.Validate() != nil || plan.Database == nil || plan.Hash != in.PlanHash {
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
