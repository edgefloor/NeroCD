//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"nerocd/internal/domain"
)

func TestStorageRemoteDownloadAlwaysGETAndCodecValidation(t *testing.T) {
	for _, wrongFormat := range []bool{false, true} {
		name := "valid_cached"
		if wrongFormat {
			name = "wrong_format"
		}
		t.Run(name, func(t *testing.T) {
			f := archiveFixture(t)
			data := archiveTestBytes(t)
			if wrongFormat {
				data = bytes.Repeat([]byte("x"), 1536)
			}
			digest := sha256.Sum256(data)
			key := hex.EncodeToString(digest[:])
			blob := domain.HostingStorageArchiveBlob{BackupID: f.p.BackupID, DestinationID: f.p.DestinationID, DaemonID: f.p.Helper.DaemonID, SHA256: "sha256:" + key, SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
			if !wrongFormat {
				if _, err := f.e.store.Put(t.Context(), bytes.NewReader(data), key, int64(len(data))); err != nil {
					t.Fatal(err)
				}
			}
			gets := 0
			remote, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected request")
				}
				gets++
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				_, _ = w.Write(data)
			})
			plan := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "download", Mode: "download", Blob: &blob}, Limits: f.p.Limits, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: domain.StorageRemoteObjectKey(f.p.BackupID, blob)}
			err := executeStorageRemoteObject(t.Context(), f.e, remote, plan, func(_ context.Context) error { return nil })
			if (err != nil) != wrongFormat {
				t.Fatalf("format accepted incorrectly: %v", err)
			}
			if gets != 1 {
				t.Fatalf("remote GET count %d", gets)
			}
			if wrongFormat {
				handle, _, openErr := f.e.store.Open(t.Context(), key)
				if openErr == nil {
					handle.Close()
					t.Fatal("unverified format entered CAS")
				}
			}
			if f.mutations != 0 || f.starts != 0 {
				t.Fatal("download mutated source or started helper")
			}
		})
	}
}
