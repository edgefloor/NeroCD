//go:build unix

package runnerruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"nerocd/internal/domain"
)

func TestMySQLRemoteDownloadRejectsCorruptAndUnboundedBytes(t *testing.T) {
	data := []byte("CREATE TABLE preserved (id INT); INSERT INTO preserved VALUES (7);")
	sum := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(sum[:]), Bytes: int64(len(data)), Format: "mysql_sql"}
	for _, scenario := range []string{"exact", "digest", "truncated", "oversized", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("download reached a write")
				}
				if scenario == "redirect" {
					w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
					w.WriteHeader(307)
					return
				}
				body := bytes.Clone(data)
				switch scenario {
				case "digest":
					body[len(body)-1] = 'X'
				case "truncated":
					body = body[:len(body)-1]
				case "oversized":
					body = append(body, 'X')
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write(body)
			})
			var dst bytes.Buffer
			err := client.download(t.Context(), target.Bucket, domain.BackupRemoteObjectKey("backup", blob), blob, &dst)
			if (err == nil) != (scenario == "exact") {
				t.Fatal("MySQL immutable download verification mismatch", err)
			}
			if err == nil && !bytes.Equal(dst.Bytes(), data) {
				t.Fatal("SQL download changed exact bytes")
			}
		})
	}
}
