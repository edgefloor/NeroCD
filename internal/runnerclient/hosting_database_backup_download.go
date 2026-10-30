package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"

	"nerocd/internal/domain"
)

type databaseDownloadPlanResponse struct {
	value domain.HostingDatabaseBackupDownloadPlan
}

func (r *databaseDownloadPlanResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, 16385))
	if err != nil || len(raw) > 16384 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidDatabaseBackupDownload
	}
	return nil
}

// HostingDatabaseBackupDownloadPlan resolves one currently authorized local copy.
func (c Client) HostingDatabaseBackupDownloadPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	var out databaseDownloadPlanResponse
	if (domain.HostingDatabaseBackupDownloadStream{HostingAuthority: a, PlanHash: "sha256:" + string(bytes.Repeat([]byte("0"), 64))}).Validate() != nil {
		return out.value, domain.ErrInvalidDatabaseBackupDownload
	}
	if err := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/database-backup-download/plan", a, &out); err != nil {
		return domain.HostingDatabaseBackupDownloadPlan{}, databaseDownloadError(ctx)
	}
	if out.value.Validate() != nil || out.value.SessionID != a.OperationID || !time.Now().Before(out.value.ExpiresAt) {
		return domain.HostingDatabaseBackupDownloadPlan{}, domain.ErrInvalidDatabaseBackupDownload
	}
	return out.value, nil
}

func databaseDownloadError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("database backup download request failed")
}

// databaseDownloadReader refuses both truncated and oversized sources before the
// closing multipart boundary. The caller owns interruption of source Read calls.
type databaseDownloadReader struct {
	src       io.Reader
	remaining int64
	ctx       context.Context
}

func (r *databaseDownloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.src.Read(extra[:])
		if n != 0 || err == nil {
			return 0, domain.ErrInvalidDatabaseBackupDownload
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.src.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining != 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// StreamDatabaseBackupDownload sends one archive once. It never retries bytes;
// only the caller's ordinary durable run completion may later be replayed.
func (c Client) StreamDatabaseBackupDownload(parent context.Context, in domain.HostingDatabaseBackupDownloadStream, p domain.HostingDatabaseBackupDownloadPlan, src io.Reader) error {
	if (src == nil) != (in.FailureCode != "") || in.Validate() != nil || p.Validate() != nil || in.PlanHash != p.Hash || in.OperationID != p.SessionID || !time.Now().Before(p.ExpiresAt) {
		return domain.ErrInvalidDatabaseBackupDownload
	}
	deadline := time.Now().Add(300 * time.Second)
	if p.ExpiresAt.Before(deadline) {
		deadline = p.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	policy, _ := databaseBackupProtectedContext(ctx).Value(databaseTransportKey{}).(DatabaseTransport)
	secured, closeTransport, err := c.serverReadinessHTTP(ServerReadinessTransport(policy))
	if err != nil {
		return databaseDownloadError(ctx)
	}
	defer closeTransport()
	// This stream follows supervisor cancellation and its immutable expiry rather
	// than the short metadata request timeout inherited from the shared client.
	secured.HTTP.Timeout = 0
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	metadata, err := json.Marshal(in)
	if err != nil || len(metadata) > 4096 {
		return domain.ErrInvalidDatabaseBackupDownload
	}
	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(mh)
	if err != nil {
		return domain.ErrInvalidDatabaseBackupDownload
	}
	if _, err = part.Write(metadata); err != nil {
		return domain.ErrInvalidDatabaseBackupDownload
	}
	var body io.Reader
	if in.FailureCode != "" {
		if err = mw.Close(); err != nil {
			return domain.ErrInvalidDatabaseBackupDownload
		}
		body = bytes.NewReader(prefix.Bytes())
	} else {
		ah := make(textproto.MIMEHeader)
		ah.Set("Content-Disposition", `form-data; name="archive"; filename="archive.bin"`)
		ah.Set("Content-Type", "application/octet-stream")
		if _, err = mw.CreatePart(ah); err != nil {
			return domain.ErrInvalidDatabaseBackupDownload
		}
		// Keep only bounded multipart framing in memory.
		start := append([]byte(nil), prefix.Bytes()...)
		prefix.Reset()
		if err = mw.Close(); err != nil {
			return domain.ErrInvalidDatabaseBackupDownload
		}
		body = io.MultiReader(bytes.NewReader(start), &databaseDownloadReader{src: src, remaining: p.Blob.Bytes, ctx: ctx}, bytes.NewReader(prefix.Bytes()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/database-backup-download/stream", body)
	if err != nil {
		return databaseDownloadError(ctx)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := secured.HTTP.Do(req)
	if err != nil {
		return databaseDownloadError(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return databaseDownloadError(ctx)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(raw) > 4096 || !observationObject(raw, "session_id", "plan_hash") {
		return databaseDownloadError(ctx)
	}
	var ack struct {
		SessionID string `json:"session_id"`
		PlanHash  string `json:"plan_hash"`
	}
	if domain.DecodeHostingDatabaseBackupJSON(raw, &ack) != nil || ack.SessionID != p.SessionID || ack.PlanHash != p.Hash {
		return databaseDownloadError(ctx)
	}
	return ctx.Err()
}
