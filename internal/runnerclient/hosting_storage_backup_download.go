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

type storageDownloadPlanResponse struct {
	value domain.HostingStorageBackupDownloadPlan
}

func (r *storageDownloadPlanResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, 16385))
	if err != nil || len(raw) > 16384 || domain.DecodeHostingDatabaseBackupJSON(raw, &r.value) != nil {
		return domain.ErrInvalidStorageBackupDownload
	}
	return nil
}

// HostingStorageBackupDownloadPlan resolves one currently authorized local copy.
func (c Client) HostingStorageBackupDownloadPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error) {
	var out storageDownloadPlanResponse
	if (domain.HostingStorageBackupDownloadStream{HostingAuthority: a, PlanHash: "sha256:" + string(bytes.Repeat([]byte("0"), 64))}).Validate() != nil {
		return out.value, domain.ErrInvalidStorageBackupDownload
	}
	if err := c.post(databaseBackupProtectedContext(ctx), "/api/v1/runners/hosting/storage-backup-download/plan", a, &out); err != nil {
		return domain.HostingStorageBackupDownloadPlan{}, storageDownloadError(ctx)
	}
	if out.value.Validate() != nil || !time.Now().Before(out.value.ExpiresAt) {
		return domain.HostingStorageBackupDownloadPlan{}, domain.ErrInvalidStorageBackupDownload
	}
	return out.value, nil
}

func storageDownloadError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("storage archive download request failed")
}

// storageDownloadReader refuses both truncated and oversized sources before the
// closing multipart boundary. The caller owns interruption of source Read calls.
type storageDownloadReader struct {
	src       io.Reader
	remaining int64
	ctx       context.Context
}

func (r *storageDownloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.src.Read(extra[:])
		if n != 0 || err == nil {
			return 0, domain.ErrInvalidStorageBackupDownload
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

// StreamStorageBackupDownload sends one archive once. It never retries bytes;
// only the caller's ordinary durable run completion may later be replayed.
func (c Client) StreamStorageBackupDownload(parent context.Context, in domain.HostingStorageBackupDownloadStream, p domain.HostingStorageBackupDownloadPlan, src io.Reader) error {
	if src == nil || in.Validate() != nil || p.Validate() != nil || in.PlanHash != p.Hash || !time.Now().Before(p.ExpiresAt) {
		return domain.ErrInvalidStorageBackupDownload
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
		return storageDownloadError(ctx)
	}
	defer closeTransport()
	// This stream follows supervisor cancellation and its immutable expiry rather
	// than the short metadata request timeout inherited from the shared client.
	secured.HTTP.Timeout = 0
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	metadata, err := json.Marshal(in)
	if err != nil || len(metadata) > 4096 {
		return domain.ErrInvalidStorageBackupDownload
	}
	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(mh)
	if err != nil {
		return domain.ErrInvalidStorageBackupDownload
	}
	if _, err = part.Write(metadata); err != nil {
		return domain.ErrInvalidStorageBackupDownload
	}
	ah := make(textproto.MIMEHeader)
	ah.Set("Content-Disposition", `form-data; name="archive"; filename="archive.tar"`)
	ah.Set("Content-Type", "application/x-tar")
	if _, err = mw.CreatePart(ah); err != nil {
		return domain.ErrInvalidStorageBackupDownload
	}
	// Build only the bounded framing in memory, never the archive itself.
	start := append([]byte(nil), prefix.Bytes()...)
	prefix.Reset()
	if err = mw.Close(); err != nil {
		return domain.ErrInvalidStorageBackupDownload
	}
	body := io.MultiReader(bytes.NewReader(start), &storageDownloadReader{src: src, remaining: p.Blob.SizeBytes, ctx: ctx}, bytes.NewReader(prefix.Bytes()))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/runners/hosting/storage-backup-download/stream", body)
	if err != nil {
		return storageDownloadError(ctx)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := secured.HTTP.Do(req)
	if err != nil {
		return storageDownloadError(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return storageDownloadError(ctx)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(raw) > 4096 || !observationObject(raw, "session_id", "plan_hash") {
		return storageDownloadError(ctx)
	}
	var ack struct {
		SessionID string `json:"session_id"`
		PlanHash  string `json:"plan_hash"`
	}
	if domain.DecodeHostingDatabaseBackupJSON(raw, &ack) != nil || ack.SessionID != p.SessionID || ack.PlanHash != p.Hash {
		return storageDownloadError(ctx)
	}
	return ctx.Err()
}
