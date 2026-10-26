package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"nerocd/internal/domain"
)

func (c *backupRemoteS3) storageDownload(ctx context.Context, bucket, key string, blob domain.HostingStorageArchiveBlob, dst io.Writer) error {
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	if blob.Validate() != nil || dst == nil {
		return backupRemoteError("object_mismatch")
	}
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return backupRemoteFailure(err)
	}
	defer out.Body.Close()
	if out.ContentLength == nil || *out.ContentLength != blob.SizeBytes {
		return backupRemoteError("object_mismatch")
	}
	// An ETag is deliberately ignored: multipart ETags are not content digests.
	h := sha256.New()
	reader := io.LimitReader(out.Body, blob.SizeBytes+1)
	n, err := io.Copy(io.MultiWriter(dst, h), reader)
	if err != nil {
		return backupRemoteFailure(err)
	}
	if n != blob.SizeBytes || "sha256:"+hex.EncodeToString(h.Sum(nil)) != blob.SHA256 {
		return backupRemoteError("object_mismatch")
	}
	return nil
}

func (c *backupRemoteS3) storageUpload(ctx context.Context, bucket, key string, blob domain.HostingStorageArchiveBlob, src io.ReadSeeker) error {
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	if blob.Validate() != nil || src == nil {
		return backupRemoteError("archive_unavailable")
	}
	// Reconcile a lost Put response with the complete body, not its metadata.
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return c.storageDownload(ctx, bucket, key, blob, io.Discard)
	}
	if !remoteObjectMissing(err) {
		return backupRemoteFailure(err)
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(src, blob.SizeBytes+1))
	if err != nil || n != blob.SizeBytes || "sha256:"+hex.EncodeToString(h.Sum(nil)) != blob.SHA256 {
		return backupRemoteError("archive_unavailable")
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: src, ContentLength: aws.Int64(blob.SizeBytes), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"sha256": strings.TrimPrefix(blob.SHA256, "sha256:")}})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict") {
			return c.storageDownload(ctx, bucket, key, blob, io.Discard)
		}
		if ctx.Err() == nil {
			if verified := c.storageDownload(ctx, bucket, key, blob, io.Discard); verified == nil {
				return nil
			}
		}
		return backupRemoteFailure(err)
	}
	if err = c.storageDownload(ctx, bucket, key, blob, io.Discard); err != nil {
		return err
	}
	return nil
}
