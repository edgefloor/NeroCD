//go:build unix

package runnerruntime

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"nerocd/internal/domain"
)

type databaseImportError string

func (e databaseImportError) Error() string { return "external database import: " + string(e) }

type databaseImportStageBytes struct {
	SourceBytes int64
	Compression string
	Blob        domain.HostingDatabaseImportBlob
}

type databaseImportEncodedReader struct {
	ctx   context.Context
	input io.Reader
	bytes int64
}

func (r *databaseImportEncodedReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, databaseImportError("interrupted")
	}
	n, err := r.input.Read(p)
	r.bytes += int64(n)
	return n, err
}

type databaseImportSQLWriter struct {
	output io.Writer
}

func (w databaseImportSQLWriter) Write(p []byte) (int, error) {
	// Plain/gzip SQL may use a declared non-UTF8 database character set. NUL,
	// unlike arbitrary high bytes, is not accepted as a text SQL stage.
	for _, b := range p {
		if b == 0 {
			return 0, databaseImportError("unsupported_format")
		}
	}
	n, err := w.output.Write(p)
	if err != nil || n != len(p) {
		return n, databaseImportError("stage_unavailable")
	}
	return n, nil
}

// inspectExternalSQL writes only to unpublished, caller-owned staging storage.
// It never interprets SQL, starts a client, or changes target writer state.
func inspectExternalSQL(ctx context.Context, input io.Reader, output io.Writer, maxBytes int64) (databaseImportStageBytes, error) {
	var result databaseImportStageBytes
	if input == nil || output == nil || maxBytes < 1 || maxBytes > domain.HostingDatabaseImportMaxBytes {
		return result, databaseImportError("stage_unavailable")
	}
	encoded := &databaseImportEncodedReader{ctx: ctx, input: io.LimitReader(input, maxBytes+1)}
	buffer := bufio.NewReader(encoded)
	magic, _ := buffer.Peek(2)
	var decoded io.Reader = buffer
	result.Compression = "plain"
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err := gzip.NewReader(buffer)
		if err != nil {
			return result, databaseImportError("unsupported_format")
		}
		// Multistream remains enabled: every member/trailer and any trailing
		// input must validate, rather than silently accepting a valid prefix.
		defer func() { _ = compressed.Close() }()
		decoded, result.Compression = compressed, "gzip"
	}
	h := sha256.New()
	n, err := io.Copy(databaseImportSQLWriter{output: io.MultiWriter(output, h)}, io.LimitReader(decoded, maxBytes+1))
	if ctx.Err() != nil {
		return result, databaseImportError("interrupted")
	}
	if n > maxBytes || encoded.bytes > maxBytes {
		return result, databaseImportError("too_large")
	}
	if err != nil || n == 0 {
		var safe databaseImportError
		if errors.As(err, &safe) {
			return result, safe
		}
		return result, databaseImportError("unsupported_format")
	}
	result.SourceBytes = encoded.bytes
	result.Blob = domain.HostingDatabaseImportBlob{Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Bytes: n, Format: "mysql_sql"}
	return result, nil
}

// stageExternal performs one protected GET. It does not issue HEAD, retry an
// object, infer backup provenance, or trust an ETag as a content digest.
func (c *backupRemoteS3) stageExternal(ctx context.Context, bucket, key string, dst io.Writer) (databaseImportStageBytes, *domain.HostingDatabaseImportSourceIdentity, error) {
	var result databaseImportStageBytes
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return result, nil, backupRemoteFailure(err)
	}
	defer func() { _ = out.Body.Close() }()
	if out.ContentLength != nil && (*out.ContentLength < 1 || *out.ContentLength > domain.HostingDatabaseImportMaxBytes) {
		return result, nil, databaseImportError("too_large")
	}
	result, err = inspectExternalSQL(ctx, out.Body, dst, domain.HostingDatabaseImportMaxBytes)
	if err != nil {
		return databaseImportStageBytes{}, nil, err
	}
	if out.ContentLength != nil && *out.ContentLength != result.SourceBytes {
		return databaseImportStageBytes{}, nil, databaseImportError("object_mismatch")
	}
	identity := &domain.HostingDatabaseImportSourceIdentity{VersionID: aws.ToString(out.VersionId), ETag: aws.ToString(out.ETag)}
	if identity.VersionID == "" && identity.ETag == "" {
		identity = nil
	} else if identity.Validate() != nil || strings.ContainsAny(identity.VersionID+identity.ETag, "\r\n") {
		return databaseImportStageBytes{}, nil, databaseImportError("object_mismatch")
	}
	return result, identity, nil
}
