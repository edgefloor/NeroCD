package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"nerocd/internal/domain"
)

type backupRemotePlanResponse struct {
	value domain.HostingDatabaseBackupTransferPlan
}

func (r *backupRemotePlanResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 65537))
	if e != nil || len(raw) > 65536 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidDatabaseBackupRemote
	}
	return nil
}

type backupRemoteMutationResponse struct {
	value domain.HostingDatabaseBackupTransfer
}

func (r *backupRemoteMutationResponse) decodeResponse(body io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(body, 16385))
	if e != nil || len(raw) > 16384 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidDatabaseBackupRemote
	}
	return nil
}
func (c Client) HostingDatabaseBackupTransferPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupTransferPlan, error) {
	var response backupRemotePlanResponse
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-transfer/plan", a, &response); e != nil {
		if ctx.Err() != nil {
			return response.value, ctx.Err()
		}
		return response.value, errors.New("remote backup authority unavailable")
	}
	p := response.value
	if p.Validate() != nil || p.Operation.ID != a.OperationID || a.RunID != "db_transfer_"+a.OperationID {
		return p, domain.ErrInvalidDatabaseBackupRemote
	}
	return p, nil
}
func (c Client) HostingDatabaseBackupTransferMutation(ctx context.Context, in domain.HostingDatabaseBackupTransferMutation) (domain.HostingDatabaseBackupTransfer, error) {
	var response backupRemoteMutationResponse
	if in.Validate() != nil {
		return response.value, domain.ErrInvalidDatabaseBackupRemote
	}
	if e := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-transfer/mutation", in, &response); e != nil {
		if ctx.Err() != nil {
			return response.value, ctx.Err()
		}
		return response.value, errors.New("remote backup receipt request failed")
	}
	out := response.value
	expected := map[string]string{"intent": "applying", "complete": "succeeded", "failed": "failed", "uncertain": "uncertain"}[in.Phase]
	if out.Validate() != nil || out.ID != in.OperationID || out.State != expected || out.FailureCode != in.FailureCode {
		return out, domain.ErrInvalidDatabaseBackupRemote
	}
	return out, nil
}

func (c Client) HostingBackupRemoteCredentials(ctx context.Context, runner string, in domain.BackupRemoteCredentialRequest, options RuntimeEnvironmentTransport) (*domain.BackupRemoteCredentials, error) {
	if in.Validate() != nil || ValidateRuntimeEnvironmentServer(c.Server, options) != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
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
	if !ok || base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify {
		return nil, domain.ErrBackupRemoteCredentials
	}
	clone := base.Clone()
	clone.Proxy = nil
	defer clone.CloseIdleConnections()
	client.Transport = clone
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	raw, e := json.Marshal(in)
	if e != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	defer clear(raw)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/database-backup-transfer/credentials", bytes.NewReader(raw))
	if e != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	response, e := client.Do(req)
	if e != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		return nil, domain.ErrBackupRemoteCredentials
	}
	credentials, e := domain.ReadBackupRemoteCredentials(response.Body)
	if e != nil {
		return nil, domain.ErrBackupRemoteCredentials
	}
	m := credentials.Metadata()
	if m.Validate() != nil || m.Request != in || m.RunnerID != runner || !time.Now().Before(m.UseUntil) || m.UseUntil.After(time.Now().Add(time.Minute)) || ctx.Err() != nil {
		credentials.Close()
		return nil, domain.ErrBackupRemoteCredentials
	}
	return credentials, nil
}
