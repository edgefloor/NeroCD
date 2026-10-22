package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"nerocd/internal/domain"
)

type proxyIdentityResponse struct {
	RunnerID     string   `json:"runner_id"`
	Capabilities []string `json:"capabilities"`
}

func (r *proxyIdentityResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxIdentityResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxIdentityResponseBytes {
		return errors.New("proxy identity response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var result proxyIdentityResponse
	if err = decoder.Decode(&result); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("proxy identity requires one JSON document")
	}
	*r = result
	return nil
}

// ProxyIdentity verifies persisted proxy capability without heartbeat or claim.
// Older servers without capabilities fail closed; Identity remains compatible.
func (c Client) ProxyIdentity(ctx context.Context) (string, error) {
	return c.RuntimeIdentity(ctx, true)
}

// RuntimeIdentity preserves the proxy-only startup contract and requires build
// execution to be disabled. Build-capable runners need explicit build validation.
func (c Client) RuntimeIdentity(ctx context.Context, proxyConfigured bool) (string, error) {
	return c.RuntimeCapabilitiesIdentity(ctx, proxyConfigured, false)
}

// RuntimeCapabilitiesIdentity checks exact persisted proxy/build capabilities
// against explicit local configuration. Legacy responses require both disabled.
func (c Client) RuntimeCapabilitiesIdentity(ctx context.Context, proxyConfigured, buildConfigured bool) (string, error) {
	return c.RuntimeExecutionIdentity(ctx, proxyConfigured, buildConfigured, false)
}

// RuntimeExecutionIdentity checks persisted execution capabilities against all
// explicit local execution modes. Legacy wrappers require local builds disabled.
func (c Client) RuntimeExecutionIdentity(ctx context.Context, proxyConfigured, buildConfigured, localBuildConfigured bool) (string, error) {
	var result proxyIdentityResponse
	if err := c.get(ctx, "/api/v1/runners/identity", &result); err != nil {
		return "", err
	}
	// Preserve Identity's established ID contract and registration's opaque
	// capability names. Only exact persisted proxy/build names participate in
	// local configuration checks; duplicates do not change those checks.
	if result.RunnerID == "" || strings.TrimSpace(result.RunnerID) != result.RunnerID {
		return "", errors.New("runner identity response did not include a valid runner_id")
	}
	proxyCapable, buildCapable, shellCapable, localBuildCapable := false, false, false, false
	for _, capability := range result.Capabilities {
		if capability == domain.HostingPrivateGitCapability {
			return "", errors.New("authenticated private Git capability requires unavailable execution support")
		}
		proxyCapable = proxyCapable || capability == domain.RunTypeProxyApply
		buildCapable = buildCapable || capability == domain.HostingBuildTaskType
		localBuildCapable = localBuildCapable || capability == domain.RunTypeLocalBuildDeploy
		shellCapable = shellCapable || capability == "shell"
	}
	if (buildConfigured || localBuildConfigured) && shellCapable {
		return "", errors.New("authenticated shell capability cannot share configured build execution")
	}
	if proxyCapable != proxyConfigured {
		return "", errors.New("authenticated proxy_apply capability does not match explicit local proxy configuration")
	}
	if buildCapable != buildConfigured {
		return "", errors.New("authenticated hosting_build_v1 capability does not match explicit local build configuration")
	}
	if localBuildCapable != localBuildConfigured {
		return "", errors.New("authenticated local_build_deploy_v1 capability does not match explicit local configuration")
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
