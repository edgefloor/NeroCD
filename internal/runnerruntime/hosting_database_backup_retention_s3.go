package runnerruntime

import (
	"bytes"
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

var errBackupRetentionConditional = errors.New("backup retention conditional deletion unsupported")
var errBackupRetentionObjectChanged = errors.New("backup retention object identity changed")

func retentionPrecondition(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "PreconditionFailed"
}
func retentionVersion(v string) *string {
	if v == "" {
		return nil
	}
	return aws.String(v)
}
func (c *backupRemoteS3) retentionHead(ctx context.Context, bucket, key string, proof domain.BackupRemoteCopyProof, blob domain.HostingDatabaseBackupBlob) (bool, error) {
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	out, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: retentionVersion(proof.VersionID)})
	if remoteObjectMissing(err) {
		return true, nil
	}
	if err != nil {
		return false, backupRemoteFailure(err)
	}
	if aws.ToString(out.ETag) != proof.ETag || aws.ToString(out.VersionId) != proof.VersionID || aws.ToInt64(out.ContentLength) != blob.Bytes {
		return false, errBackupRetentionObjectChanged
	}
	return false, nil
}

// Qualification touches only the fresh owned probe. Failure or an ambiguous
// response cannot authorize deletion of any selected recovery copy.
func (c *backupRemoteS3) qualifyRetention(ctx context.Context, bucket, key, version string) error {
	if namespace := domain.HostingDatabaseBackupRetentionProbeNamespace(version); namespace == "" || !strings.HasPrefix(key, namespace) {
		return errBackupRetentionConditional
	}
	body := []byte("PGDMP nerocd retention conditional probe " + key)
	if version == domain.HostingDatabaseMySQLBackupRetentionVersion {
		body = []byte("-- nerocd retention conditional probe " + key)
	}
	digest := sha256.Sum256(body)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(body)), Format: domain.HostingDatabaseBackupRetentionFormat(version)}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	created, err := c.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentLength: aws.Int64(blob.Bytes), IfNoneMatch: aws.String("*")})
	if err != nil {
		return backupRemoteFailure(err)
	}
	proof := domain.BackupRemoteCopyProof{ETag: aws.ToString(created.ETag), VersionID: aws.ToString(created.VersionId), Created: true}
	if proof.Validate() != nil {
		return errBackupRetentionConditional
	}
	if absent, err := c.retentionHead(ctx, bucket, key, proof, blob); err != nil || absent {
		return errBackupRetentionConditional
	}
	wrong := "\"nerocd-does-not-match-" + strings.TrimPrefix(blob.Digest, "sha256:") + "\""
	_, err = c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: retentionVersion(proof.VersionID), IfMatch: aws.String(wrong)})
	if !retentionPrecondition(err) {
		return errBackupRetentionConditional
	}
	if absent, err := c.retentionHead(ctx, bucket, key, proof, blob); err != nil || absent {
		return errBackupRetentionConditional
	}
	// Verify content, not merely metadata or an ETag-as-digest assumption.
	if proof.VersionID == "" {
		if err = c.download(ctx, bucket, key, blob, io.Discard); err != nil {
			return errBackupRetentionConditional
		}
	} else {
		out, e := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: retentionVersion(proof.VersionID)})
		if e != nil {
			return errBackupRetentionConditional
		}
		raw, e := io.ReadAll(io.LimitReader(out.Body, blob.Bytes+1))
		closeErr := out.Body.Close()
		if e != nil || closeErr != nil || !bytes.Equal(raw, body) {
			return errBackupRetentionConditional
		}
	}
	_, err = c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: retentionVersion(proof.VersionID), IfMatch: aws.String(proof.ETag)})
	if err != nil {
		return backupRemoteFailure(err)
	}
	absent, err := c.retentionHead(ctx, bucket, key, proof, blob)
	if err != nil {
		return err
	}
	if !absent {
		return errBackupRetentionConditional
	}
	return nil
}
func (c *backupRemoteS3) deleteRetentionCopy(ctx context.Context, copy domain.BackupRetentionCopy, version string) (bool, error) {
	if copy.Remote == nil || !copy.Remote.Created || copy.Remote.Validate() != nil || copy.Target == nil {
		return false, errBackupRetentionObjectChanged
	}
	blob := domain.HostingDatabaseBackupBlob{Digest: copy.Item.Digest, Bytes: copy.Item.Bytes, Format: domain.HostingDatabaseBackupRetentionFormat(version)}
	bucket := copy.Target.Target.Bucket
	absent, err := c.retentionHead(ctx, bucket, copy.ObjectKey, *copy.Remote, blob)
	if err != nil || absent {
		return absent, err
	}
	// Read back the whole saved digest immediately before its conditional delete.
	// For versioned copies HEAD proves the current exact immutable version; GET
	// must target that same version, not whichever object happens to be latest.
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(copy.ObjectKey), VersionId: retentionVersion(copy.Remote.VersionID), IfMatch: aws.String(copy.Remote.ETag)})
	if err != nil {
		return false, backupRemoteFailure(err)
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(out.Body, blob.Bytes+1))
	closeErr := out.Body.Close()
	if readErr != nil || closeErr != nil {
		return false, backupRemoteError("remote_unavailable")
	}
	if n != blob.Bytes || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		return false, errBackupRetentionObjectChanged
	}
	_, err = c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(copy.ObjectKey), VersionId: retentionVersion(copy.Remote.VersionID), IfMatch: aws.String(copy.Remote.ETag)})
	if retentionPrecondition(err) {
		return false, errBackupRetentionObjectChanged
	}
	if err != nil {
		return false, backupRemoteFailure(err)
	}
	absent, err = c.retentionHead(ctx, bucket, copy.ObjectKey, *copy.Remote, blob)
	if err != nil {
		return false, err
	}
	if !absent {
		return false, backupRemoteError("remote_unavailable")
	}
	return false, nil
}

func (c *backupRemoteS3) retentionProbeAbsent(ctx context.Context, bucket, key, version string) (bool, error) {
	if namespace := domain.HostingDatabaseBackupRetentionProbeNamespace(version); namespace == "" || !strings.HasPrefix(key, namespace) {
		return false, errBackupRetentionConditional
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if remoteObjectMissing(err) {
		return true, nil
	}
	if err != nil {
		return false, backupRemoteFailure(err)
	}
	return false, nil
}
