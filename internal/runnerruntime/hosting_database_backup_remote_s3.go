package runnerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"nerocd/internal/domain"
)

// Only a closed code crosses the runtime boundary. SDK errors can contain
// signed requests, object locations and arbitrary remote response bodies.
type backupRemoteError string

func (e backupRemoteError) Error() string { return "remote backup transfer: " + string(e) }

func backupRemoteFailure(err error) error {
	if err == nil {
		return nil
	}
	var controlled backupRemoteError
	if errors.As(err, &controlled) && domain.ValidBackupRemoteFailure(string(controlled)) {
		return controlled
	}
	if errors.Is(err, errBackupRemoteNetwork) {
		return backupRemoteError("network_denied")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return backupRemoteError("interrupted")
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken":
			return backupRemoteError("access_denied")
		}
	}
	return backupRemoteError("remote_unavailable")
}

type backupRemoteS3 struct {
	client    *s3.Client
	transport *backupRemoteTransport
	access    []byte
	secret    []byte
}

// Credentials are owned copies, cleared at Close, never environment-derived or
// cached by an SDK credential cache. Signing necessarily creates temporary Go
// strings inside the maintained SDK; those cannot be erased by this caller.
func newBackupRemoteS3(ctx context.Context, target domain.HostingDatabaseBackupTarget, access, secret []byte, policy backupRemoteNetworkPolicy, timeout time.Duration) (*backupRemoteS3, error) {
	if target.Validate() != nil || len(access) < 1 || len(access) > 256 || len(secret) < 1 || len(secret) > 4096 || timeout < time.Second || timeout > time.Hour {
		return nil, backupRemoteError("authority_denied")
	}
	transport, err := newBackupRemoteTransport(ctx, target.Endpoint, policy)
	if err != nil {
		return nil, backupRemoteFailure(err)
	}
	c := &backupRemoteS3{transport: transport, access: append([]byte(nil), access...), secret: append([]byte(nil), secret...)}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errBackupRemoteNetwork }}
	c.client = s3.New(s3.Options{
		Region: target.Region, BaseEndpoint: aws.String(target.Endpoint), UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			if ctx.Err() != nil || len(c.access) == 0 || len(c.secret) == 0 {
				return aws.Credentials{}, backupRemoteError("authority_denied")
			}
			return aws.Credentials{AccessKeyID: string(c.access), SecretAccessKey: string(c.secret), Source: "nerocd-scoped-backup-delivery"}, nil
		}),
		HTTPClient: client, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return c, nil
}

func (c *backupRemoteS3) Close() {
	clear(c.access)
	clear(c.secret)
	c.access = nil
	c.secret = nil
	c.transport.Close()
}

func (c *backupRemoteS3) validate(ctx context.Context, bucket string) error {
	_, err := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), MaxKeys: aws.Int32(1)})
	return backupRemoteFailure(err)
}

// download always reads and verifies the remote object, even if a valid local
// archive exists. The caller must write into a protected unpublished staging file.
func (c *backupRemoteS3) download(ctx context.Context, bucket, key string, blob domain.HostingDatabaseBackupBlob, dst io.Writer) error {
	if blob.Validate() != nil || dst == nil {
		return backupRemoteError("object_mismatch")
	}
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return backupRemoteFailure(err)
	}
	defer out.Body.Close()
	if out.ContentLength == nil || *out.ContentLength != blob.Bytes {
		return backupRemoteError("object_mismatch")
	}
	// An ETag is deliberately ignored: multipart ETags are not content digests.
	h := sha256.New()
	reader := io.LimitReader(out.Body, blob.Bytes+1)
	magic := make([]byte, 5)
	if _, err = io.ReadFull(reader, magic); err != nil || string(magic) != "PGDMP" {
		return backupRemoteError("object_mismatch")
	}
	if _, err = io.MultiWriter(dst, h).Write(magic); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	n, err := io.Copy(io.MultiWriter(dst, h), reader)
	if err != nil {
		return backupRemoteFailure(err)
	}
	if n+5 != blob.Bytes || "sha256:"+hex.EncodeToString(h.Sum(nil)) != blob.Digest {
		return backupRemoteError("object_mismatch")
	}
	return nil
}

func remoteObjectMissing(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound")
}

func (c *backupRemoteS3) upload(ctx context.Context, bucket, key string, blob domain.HostingDatabaseBackupBlob, src io.ReadSeeker) error {
	if blob.Validate() != nil || src == nil {
		return backupRemoteError("archive_unavailable")
	}
	// Reconcile a lost Put response with the complete body, not its metadata.
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return c.download(ctx, bucket, key, blob, io.Discard)
	}
	if !remoteObjectMissing(err) {
		return backupRemoteFailure(err)
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(src, blob.Bytes+1))
	if err != nil || n != blob.Bytes || "sha256:"+hex.EncodeToString(h.Sum(nil)) != blob.Digest {
		return backupRemoteError("archive_unavailable")
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return backupRemoteError("archive_unavailable")
	}
	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: src, ContentLength: aws.Int64(blob.Bytes), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"sha256": strings.TrimPrefix(blob.Digest, "sha256:")}})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict") {
			return c.download(ctx, bucket, key, blob, io.Discard)
		}
		return backupRemoteFailure(err)
	}
	return c.download(ctx, bucket, key, blob, io.Discard)
}

// Prevent accidental diagnostic formatting of credentials held by the client.
func (*backupRemoteS3) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[remote backup client]")
}
