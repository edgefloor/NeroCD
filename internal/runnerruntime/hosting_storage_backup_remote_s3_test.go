package runnerruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestStorageRemoteS3ImmutableReplayAndResponseLoss(t *testing.T) {
	data := []byte("storage object identity fixture")
	hash := sha256.Sum256(data)
	blob := domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: "sha256:" + hex.EncodeToString(hash[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
	var saved []byte
	puts, gets := 0, 0
	client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "HEAD":
			if saved == nil {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(saved)))
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing immutable conditional PUT")
			}
			puts++
			saved, _ = io.ReadAll(r.Body)
			w.WriteHeader(500)
		case "GET":
			gets++
			w.Header().Set("Content-Length", fmt.Sprint(len(saved)))
			_, _ = w.Write(saved)
		default:
			t.Error("unexpected method")
		}
	})
	for range 2 {
		if err := client.storageUpload(t.Context(), target.Bucket, domain.StorageRemoteObjectKey("backup", blob), blob, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	if puts != 1 || gets != 2 {
		t.Fatalf("puts=%d gets=%d", puts, gets)
	}
}
func TestStorageRemoteS3RejectsCorruptAndTruncatedBody(t *testing.T) {
	for _, mode := range []string{"corrupt", "truncated", "size"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("complete archive identity")
			digest := sha256.Sum256(data)
			blob := domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
			client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				switch mode {
				case "corrupt":
					_, _ = io.WriteString(w, strings.Repeat("x", len(data)))
				case "truncated":
					_, _ = w.Write(data[:4])
				case "size":
					w.Header().Set("Content-Length", "2")
					_, _ = w.Write(data[:2])
				}
			})
			if err := client.storageDownload(t.Context(), target.Bucket, "object", blob, io.Discard); err == nil {
				t.Fatal("invalid remote body accepted")
			}
		})
	}
}
