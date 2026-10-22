package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"nerocd/internal/domain"
)

type proxyIdentityResponse struct {
	RunnerID     string   `json:"runner_id"`
	Capabilities []string `json:"capabilities"`
}

var proxyIdentityID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var proxyCapabilityName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func (r *proxyIdentityResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return errors.New("proxy identity response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(r); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("proxy identity requires one JSON document")
	}
	if !proxyIdentityID.MatchString(r.RunnerID) || len(r.Capabilities) > 128 {
		return errors.New("invalid proxy runner identity")
	}
	seen := make(map[string]bool)
	for _, capability := range r.Capabilities {
		if !proxyCapabilityName.MatchString(capability) || seen[capability] {
			return errors.New("invalid proxy runner capabilities")
		}
		seen[capability] = true
	}
	return nil
}

// ProxyIdentity verifies persisted proxy capability without heartbeat or claim.
// Older servers without capabilities fail closed; Identity remains compatible.
func (c Client) ProxyIdentity(ctx context.Context) (string, error) {
	return c.RuntimeIdentity(ctx, true)
}

// RuntimeIdentity checks authenticated capabilities against local proxy enablement.
// Legacy identity responses remain valid only when proxy execution is disabled.
func (c Client) RuntimeIdentity(ctx context.Context, proxyConfigured bool) (string, error) {
	var result proxyIdentityResponse
	if err := c.get(ctx, "/api/v1/runners/identity", &result); err != nil {
		return "", err
	}
	capable := false
	for _, capability := range result.Capabilities {
		capable = capable || capability == domain.RunTypeProxyApply
	}
	if capable != proxyConfigured {
		return "", errors.New("authenticated proxy_apply capability does not match explicit local proxy configuration")
	}
	return result.RunnerID, nil
}

type proxyOperationResponse domain.HostingProxyOperation

func (r *proxyOperationResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, (128<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 128<<10 {
		return errors.New("proxy response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(r); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("proxy response requires one JSON document")
	}
	return nil
}
func (c Client) proxyPost(ctx context.Context, path string, in any) (domain.HostingProxyOperation, error) {
	var response proxyOperationResponse
	if err := c.post(ctx, "/api/v1/runners/proxy/"+path, in, &response); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return domain.HostingProxyOperation(response), nil
}

// ProxyPlan returns typed input for an exact live proxy attempt.
func (c Client) ProxyPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "plan", in)
}

// ProxyPrepared commits local nonsecret engine identities before admission.
func (c Client) ProxyPrepared(ctx context.Context, in domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "prepared", in)
}

// ProxyApplyBegin acquires the independent durable proxy hold before engine effects.
func (c Client) ProxyApplyBegin(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "apply-begin", in)
}

// ProxyComplete reports durable external effects using full fenced authority.
func (c Client) ProxyComplete(ctx context.Context, in domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "complete", in)
}

// ProxyStatus reads exact persisted attempt state without restoring authority.
func (c Client) ProxyStatus(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "status", in)
}
