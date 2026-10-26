package runnerruntime

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
)

func TestStorageRetentionS3ConditionalQualificationAndExactDeletion(t *testing.T) {
	for _, mode := range []string{"qualified", "nonversioned", "ignores_condition", "denied", "changed", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			etags := map[string]string{}
			backupDeletes := 0
			client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if mode != "nonversioned" {
					w.Header().Set("x-amz-version-id", "fixture-version-1")
				}
				key := r.URL.Path
				body, present := objects[key]
				if r.Method == "DELETE" && mode == "denied" {
					w.WriteHeader(403)
					if _, err := io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`); err != nil {
						t.Error("fixture response write failed")
					}
					return
				}
				switch r.Method {
				case "PUT":
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("unconditional creation")
					}
					if present {
						w.WriteHeader(412)
						return
					}
					data, e := io.ReadAll(io.LimitReader(r.Body, 4096))
					if e != nil {
						t.Error(e)
					}
					objects[key] = data
					sum := sha256.Sum256(data)
					etags[key] = fmt.Sprintf(`"%x"`, sum[:16])
					w.Header().Set("ETag", etags[key])
				case "HEAD", "GET":
					if !present {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("ETag", etags[key])
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
					if r.Method == "GET" {
						if _, err := w.Write(body); err != nil {
							t.Error("fixture response write failed")
						}
					}
				case "DELETE":
					match := r.Header.Get("If-Match")
					if match == "" || match == "*" {
						t.Error("unscoped delete")
					}
					if mode != "ignores_condition" && match != etags[key] {
						w.WriteHeader(412)
						if _, err := io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`); err != nil {
							t.Error("fixture response write failed")
						}
						return
					}
					if !strings.Contains(key, "retention-probes") {
						backupDeletes++
					}
					delete(objects, key)
					delete(etags, key)
					if mode == "uncertain" && !strings.Contains(key, "retention-probes") {
						w.WriteHeader(500)
						return
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected request")
				}
			})
			data := []byte("PGDMP saved database copy")
			sum := sha256.Sum256(data)
			blob := domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: fmt.Sprintf("sha256:%x", sum), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
			key := domain.StorageRemoteObjectKey("backup", blob)
			owned, e := client.storageUploadOwned(t.Context(), target.Bucket, key, blob, bytes.NewReader(data))
			if e != nil || owned == nil || !owned.Created {
				t.Fatal("new creation proof", e)
			}
			proof := *owned
			probe := "nerocd/storage/retention-probes/execution/0123456789abcdef0123456789abcdef"
			err := client.qualifyStorageRetention(t.Context(), target.Bucket, probe)
			if mode == "ignores_condition" || mode == "denied" {
				if err == nil {
					t.Fatal("unsupported conditional delete accepted")
				}
				if backupDeletes != 0 {
					t.Fatal("qualification deleted selected copy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copy := domain.StorageBackupRetentionCopy{Item: domain.BackupRetentionItem{Digest: blob.SHA256, Bytes: blob.SizeBytes}, Remote: &proof, Target: &domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: key}
			if mode == "changed" {
				mu.Lock()
				etags["/"+target.Bucket+"/"+key] = `"foreign"`
				mu.Unlock()
			}
			absent, err := client.deleteStorageRetentionCopy(t.Context(), copy)
			if mode == "changed" {
				if err == nil || backupDeletes != 0 {
					t.Fatal("changed object deleted")
				}
				return
			}
			if mode == "uncertain" {
				if err == nil {
					t.Fatal("lost delete response claimed completed")
				}
				if absent, err = client.retentionHead(t.Context(), target.Bucket, key, proof, domain.HostingDatabaseBackupBlob{Digest: blob.SHA256, Bytes: blob.SizeBytes, Format: "postgresql_custom"}); err != nil || !absent {
					t.Fatal("exact absence reconciliation", err)
				}
				return
			}
			if err != nil || absent || backupDeletes != 1 {
				t.Fatalf("deletion: absent=%v deletes=%d err=%v", absent, backupDeletes, err)
			}
			if absent, err = client.deleteStorageRetentionCopy(t.Context(), copy); err != nil || !absent || backupDeletes != 1 {
				t.Fatal("replay did not observe exact absence", err)
			}
		})
	}
}
