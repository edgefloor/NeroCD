//go:build unix

package runnerruntime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestExternalSQLStageBoundsAndGzipIntegrity(t *testing.T) {
	sql := []byte("CREATE TABLE imported(id int); INSERT INTO imported VALUES(7);\n")
	compressed := func(value []byte) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		if _, err := w.Write(value); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	gz := compressed(sql)
	corrupt := bytes.Clone(gz)
	corrupt[len(corrupt)-5] ^= 0xff
	for _, tc := range []struct {
		name, compression, failure string
		input                      []byte
		max                        int64
	}{
		{name: "plain", input: sql, max: 1024, compression: "plain"},
		{name: "gzip magic without filename", input: gz, max: 1024, compression: "gzip"},
		{name: "empty", max: 1024, failure: "unsupported_format"},
		{name: "binary", input: []byte("PK\x00archive"), max: 1024, failure: "unsupported_format"},
		{name: "truncated gzip", input: gz[:len(gz)-1], max: 1024, failure: "unsupported_format"},
		{name: "bad checksum", input: corrupt, max: 1024, failure: "unsupported_format"},
		{name: "gzip trailing garbage", input: append(bytes.Clone(gz), []byte("unverified tail")...), max: 1024, failure: "unsupported_format"},
		{name: "plain encoded bound", input: sql, max: 10, failure: "too_large"},
		{name: "encoded gzip bound", input: gz, max: int64(len(gz) - 1), failure: "too_large"},
		{name: "decoded expansion bound", input: compressed([]byte(strings.Repeat("SELECT 1;", 1000))), max: 100, failure: "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var staged bytes.Buffer
			got, err := inspectExternalSQL(t.Context(), bytes.NewReader(tc.input), &staged, tc.max)
			if tc.failure != "" {
				if err == nil || err.Error() != databaseImportError(tc.failure).Error() || got.Blob.Digest != "" {
					t.Fatalf("invalid stage outcome: result=%+v error=%v", got, err)
				}
				return
			}
			digest := sha256.Sum256(sql)
			if err != nil || !bytes.Equal(staged.Bytes(), sql) || got.SourceBytes != int64(len(tc.input)) || got.Blob.Bytes != int64(len(sql)) || got.Blob.Digest != "sha256:"+hex.EncodeToString(digest[:]) || got.Compression != tc.compression {
				t.Fatalf("verified stage changed: result=%+v error=%v", got, err)
			}
		})
	}
}

func TestExternalSQLStageCancellationAndWriterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var b bytes.Buffer
	if got, err := inspectExternalSQL(ctx, strings.NewReader("SELECT 1;"), &b, 1024); err != databaseImportError("interrupted") || got.Blob.Digest != "" || b.Len() != 0 {
		t.Fatal("cancelled inspection published bytes")
	}
	if got, err := inspectExternalSQL(t.Context(), strings.NewReader("SELECT 1;"), externalImportFailWriter{}, 1024); err == nil || got.Blob.Digest != "" {
		t.Fatal("failed private stage write published a receipt")
	}
}

type externalImportFailWriter struct{}

func (externalImportFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
