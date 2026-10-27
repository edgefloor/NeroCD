package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func downloadBlob(data []byte) domain.HostingStorageArchiveBlob {
	h := sha256.Sum256(data)
	return domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: "sha256:" + hex.EncodeToString(h[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
}
func TestStorageDownloadWithholdsFinalBlock(t *testing.T) {
	data := bytes.Repeat([]byte("archive"), 10000)
	blob := downloadBlob(data)
	for _, test := range []struct {
		name   string
		data   []byte
		finish func() error
		ok     bool
	}{
		{"exact", data, func() error { return nil }, true},
		{"truncated", data[:len(data)-1], func() error { return nil }, false},
		{"extra", append(append([]byte{}, data...), 1), func() error { return nil }, false},
		{"wrong digest", bytes.Repeat([]byte("changed"), 10000), func() error { return nil }, false},
		{"revoked before final block", data, func() error { return errors.New("revoked") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			err := copyVerifiedStorageDownload(&out, bytes.NewReader(test.data), blob, test.finish)
			if test.ok {
				if err != nil || !bytes.Equal(out.Bytes(), data) {
					t.Fatal("exact archive did not complete", err)
				}
			} else if err == nil || out.Len() >= len(data) {
				t.Fatal("failed stream released complete body")
			}
		})
	}
}
func TestStorageDownloadBrokerBoundsAndDisconnect(t *testing.T) {
	var b storageDownloadBroker
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, ok := b.reserve("one", "owner", ctx, cancel, func(context.Context) error { return nil })
	if !ok {
		t.Fatal("first rejected")
	}
	if _, ok = b.reserve("two", "owner", ctx, cancel, func(context.Context) error { return nil }); ok {
		t.Fatal("same actor exceeded bound")
	}
	other, stop := context.WithCancel(t.Context())
	defer stop()
	if _, ok = b.reserve("two", "other", other, stop, func(context.Context) error { return nil }); !ok {
		t.Fatal("second rejected")
	}
	if _, ok = b.reserve("three", "third", ctx, cancel, func(context.Context) error { return nil }); ok {
		t.Fatal("controller capacity exceeded")
	}
	b.setPlan("one", domain.HostingStorageBackupDownloadPlan{Hash: "hash"})
	if _, ok = b.claim("one", "wrong"); ok {
		t.Fatal("wrong hash")
	}
	if _, ok = b.claim("one", "hash"); !ok {
		t.Fatal("claim")
	}
	if _, ok = b.claim("one", "hash"); ok {
		t.Fatal("duplicate stream")
	}
	done := make(chan error, 1)
	go func() { _, err := s.writer.Write([]byte("blocked")); done <- err }()
	b.remove("one", false)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect leaked blocked stream")
	}
	b.remove("two", false)
}
func TestStorageDownloadMetadataUsesRealClosedDecoder(t *testing.T) {
	valid := `{"operation_id":"download","run_id":"storage_transfer_download","lease_id":"lease","attempt":1,"fence":"fence","plan_hash":"sha256:` + strings.Repeat("a", 64) + `"}`
	var in domain.HostingStorageBackupDownloadStream
	if domain.DecodeHostingStorageArchiveJSON([]byte(valid), &in) != nil || in.Validate() != nil {
		t.Fatal("real stream authority rejected")
	}
	for _, bad := range []string{strings.Replace(valid, `"attempt":1`, `"attempt":0`, 1), strings.Replace(valid, `"run_id":"storage_transfer_download"`, `"run_id":"wrong"`, 1), strings.Replace(valid, `"operation_id":"download"`, `"operation_id":"download","unknown":true`, 1), valid + `{}`} {
		var out domain.HostingStorageBackupDownloadStream
		if domain.DecodeHostingStorageArchiveJSON([]byte(bad), &out) == nil && out.Validate() == nil {
			t.Fatal("invalid stream authority admitted")
		}
	}
	request, _ := http.NewRequest(http.MethodPost, storageDownloadStreamPath, nil)
	if requiresJSONDocument(request) {
		t.Fatal("multipart stream forced through JSON buffer")
	}
	request.URL.Path = "/api/v1/runners/hosting/storage-backup-download/plan"
	if !requiresJSONDocument(request) {
		t.Fatal("JSON plan bypassed boundary")
	}
	_ = io.EOF
}
