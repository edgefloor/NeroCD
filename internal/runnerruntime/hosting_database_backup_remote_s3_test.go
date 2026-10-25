package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func remoteS3Fixture(t *testing.T, handler http.HandlerFunc) (*backupRemoteS3, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: server.URL, Bucket: "fixture-backups", Region: "us-east-1", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	client, err := newBackupRemoteS3(t.Context(), target, []byte("fixture-access-canary"), []byte("fixture-secret-canary"), backupRemoteNetworkPolicy{fixtureOrigin: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, target
}

func TestBackupRemoteS3UploadReplayAndDownload(t *testing.T) {
	data := []byte("PGDMPsynthetic fixture archive")
	digest := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Format: "postgresql_custom"}
	var mu sync.Mutex
	var saved []byte
	puts := 0
	client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing SDK signature")
		}
		switch r.Method {
		case http.MethodHead:
			if saved == nil {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(saved)))
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing immutable object precondition")
			}
			if saved != nil {
				w.WriteHeader(412)
				return
			}
			saved = make([]byte, len(data))
			if _, err := io.ReadFull(r.Body, saved); err != nil {
				t.Error("missing upload body")
			}
			puts++
		case http.MethodGet:
			w.Header().Set("Content-Length", fmt.Sprint(len(saved)))
			_, _ = w.Write(saved)
		default:
			t.Error("unexpected S3 request")
		}
	})
	key := domain.BackupRemoteObjectKey("backup", blob)
	for range 2 {
		if err := client.upload(t.Context(), target.Bucket, key, blob, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	var downloaded bytes.Buffer
	if err := client.download(t.Context(), target.Bucket, key, blob, &downloaded); err != nil {
		t.Fatal(err)
	}
	if puts != 1 || !bytes.Equal(downloaded.Bytes(), data) {
		t.Fatal("retry repeated upload or changed archive")
	}
}

func TestBackupRemoteS3LocalSuccessSurvivesPutDenial(t *testing.T) {
	data := []byte("PGDMPfixture")
	digest := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Format: "postgresql_custom"}
	client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			_, _ = w.Write([]byte(`<ListBucketResult><Name>fixture-backups</Name><IsTruncated>false</IsTruncated></ListBucketResult>`))
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>fixture-secret-canary</Message></Error>`))
	})
	if err := client.validate(t.Context(), target.Bucket); err != nil {
		t.Fatal(err)
	}
	if err := client.upload(t.Context(), target.Bucket, "key", blob, bytes.NewReader(data)); err == nil || err.Error() != "remote backup transfer: access_denied" {
		t.Fatalf("unexpected safe outcome %v", err)
	}
	if string(data) != "PGDMPfixture" {
		t.Fatal("local archive modified")
	}
	if strings.Contains(fmt.Sprintf("%+v", client), "canary") {
		t.Fatal("credential formatting leak")
	}
}

func TestBackupRemoteS3RejectsCorruptionAndRedirect(t *testing.T) {
	data := []byte("PGDMPfixture")
	digest := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Format: "postgresql_custom"}
	for _, mode := range []string{"digest", "truncated", "oversized", "magic", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
					w.WriteHeader(307)
					return
				}
				body := bytes.Clone(data)
				switch mode {
				case "digest":
					body[len(body)-1] = 'X'
				case "truncated":
					body = body[:len(body)-1]
				case "oversized":
					body = append(body, 'X')
				case "magic":
					body[0] = 'X'
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write(body)
			})
			var dst bytes.Buffer
			if err := client.download(t.Context(), target.Bucket, "key", blob, &dst); err == nil {
				t.Fatal("invalid object accepted")
			}
		})
	}
}

func TestBackupRemoteNetworkPolicyAndRebinding(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.1.1.1", "100.100.100.200", "192.168.1.1", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe"} {
		if (backupRemoteNetworkPolicy{}).permits("https://object.example", netip.MustParseAddr(s)) {
			t.Fatalf("unsafe address accepted: %s", s)
		}
	}
	policy := backupRemoteNetworkPolicy{privateOrigins: map[string][]netip.Prefix{"https://object.example": {netip.MustParsePrefix("10.1.0.0/16")}}}
	if !policy.permits("https://object.example", netip.MustParseAddr("10.1.1.1")) || policy.permits("https://other.example", netip.MustParseAddr("10.1.1.1")) {
		t.Fatal("operator allowlist scope lost")
	}
	lookups := 0
	transport, err := newBackupRemoteTransportWithLookup(t.Context(), "https://object.example", backupRemoteNetworkPolicy{}, func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.1.1.1")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://object.example/bucket", nil)
	if _, err = transport.RoundTrip(req); err != errBackupRemoteNetwork {
		t.Fatal("DNS rebinding was not rejected before dial")
	}
}
