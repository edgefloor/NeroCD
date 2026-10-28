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
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func remoteS3Fixture(t *testing.T, handler http.HandlerFunc) (*backupRemoteS3, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: server.URL, Bucket: "fixture-backups", Region: "us-east-1", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	client, err := newBackupRemoteS3(t.Context(), target, []byte("fixture-access-canary"), []byte("fixture-secret-canary"), backupRemoteNetworkPolicy{fixtureOrigin: server.URL}, time.Second, backupRemoteGrant{useUntil: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, target
}

func TestBackupRemoteS3UploadReplayAndDownload(t *testing.T) {
	for _, format := range []string{"postgresql_custom", "mysql_sql"} {
		t.Run(format, func(t *testing.T) { backupRemoteS3UploadReplayAndVerify(t, format) })
	}
}

func backupRemoteS3UploadReplayAndVerify(t *testing.T, format string) {
	t.Helper()
	data := []byte("PGDMPsynthetic fixture archive")
	if format == "mysql_sql" {
		data = []byte("CREATE TABLE retained (id INT); INSERT INTO retained VALUES (1);")
	}
	digest := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Format: format}
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
	for _, origin := range []string{"http://127.0.0.1:9000", "http://127.0.0.1:9002"} {
		transport, err := newBackupRemoteTransport(t.Context(), "http://127.0.0.1:9002", backupRemoteNetworkPolicy{fixtureOrigin: origin})
		if origin == "http://127.0.0.1:9000" {
			if err != errBackupRemoteNetwork || transport != nil {
				t.Fatal("different loopback origin accepted")
			}
		} else {
			if err != nil || transport == nil {
				t.Fatal("exact fixture loopback origin rejected", err)
			}
			transport.Close()
		}
	}
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

func TestBackupRemoteS3RefreshesGrantDuringLongBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data := []byte("PGDMPa body spanning several independent credential grants")
	sum := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: fmt.Sprintf("sha256:%x", sum), Bytes: int64(len(data)), Format: "postgresql_custom"}
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data[:5])
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-release:
			_, _ = w.Write(data[5:])
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: server.URL, Bucket: "fixture-backups", Region: "us-east-1", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	grants := make(chan struct{}, 8)
	firstUntil := time.Now().Add(300 * time.Millisecond)
	grant := backupRemoteGrant{useUntil: firstUntil, refresh: func(ctx context.Context, access, secret []byte) (time.Time, error) {
		if ctx.Err() != nil || !bytes.Equal(access, []byte("access")) || !bytes.Equal(secret, []byte("secret")) {
			return time.Time{}, backupRemoteError("authority_denied")
		}
		grants <- struct{}{}
		return time.Now().Add(300 * time.Millisecond), nil
	}}
	client, err := newBackupRemoteS3(ctx, target, []byte("access"), []byte("secret"), backupRemoteNetworkPolicy{fixtureOrigin: server.URL}, 4*time.Second, grant)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	done := make(chan error, 1)
	var body bytes.Buffer
	go func() { done <- client.download(ctx, target.Bucket, "archive", blob, &body) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("body did not start")
	}
	for range 3 {
		select {
		case <-grants:
		case <-ctx.Done():
			t.Fatal("fresh grant not obtained")
		}
	}
	if time.Now().Before(firstUntil) {
		t.Fatal("fixture did not cross the first grant deadline")
	}
	close(release)
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("long body did not join")
	}
	if err != nil || !bytes.Equal(body.Bytes(), data) {
		t.Fatal("fresh authorized grants failed to preserve long transfer", err)
	}
}

func TestBackupRemoteS3GrantFailureCancelsActiveBodyAndLaterSigning(t *testing.T) {
	for _, kind := range []string{"no_refresh", "denied", "late_refresh", "caller_cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			data := []byte("PGDMPblocked body")
			sum := sha256.Sum256(data)
			blob := domain.HostingDatabaseBackupBlob{Digest: fmt.Sprintf("sha256:%x", sum), Bytes: int64(len(data)), Format: "postgresql_custom"}
			started := make(chan struct{})
			canceled := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				_, _ = w.Write(data[:5])
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: server.URL, Bucket: "fixture-backups", Region: "us-east-1", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			lateReturn := make(chan struct{})
			refreshJoined := make(chan struct{})
			grant := backupRemoteGrant{useUntil: time.Now().Add(300 * time.Millisecond)}
			switch kind {
			case "denied":
				grant.refresh = func(context.Context, []byte, []byte) (time.Time, error) {
					defer close(refreshJoined)
					return time.Time{}, backupRemoteError("authority_denied")
				}
			case "late_refresh":
				grant.refresh = func(ctx context.Context, _, _ []byte) (time.Time, error) {
					defer close(refreshJoined)
					<-ctx.Done()
					<-lateReturn
					return time.Now().Add(time.Minute), nil
				}
			}
			client, err := newBackupRemoteS3(ctx, target, []byte("access"), []byte("secret"), backupRemoteNetworkPolicy{fixtureOrigin: server.URL}, 3*time.Second, grant)
			if err != nil {
				t.Fatal(err)
			}
			// Release a deliberately late refresh before closing its owning client.
			defer client.Close()
			if kind == "late_refresh" {
				defer func() {
					select {
					case <-lateReturn:
					default:
						close(lateReturn)
					}
				}()
			}
			done := make(chan error, 1)
			go func() { done <- client.download(ctx, target.Bucket, "archive", blob, io.Discard) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("body did not start")
			}
			if kind == "caller_cancel" {
				cancel()
			}
			select {
			case <-canceled:
			case <-ctx.Done():
				if kind != "caller_cancel" {
					t.Fatal("grant failed to cancel in-flight body")
				}
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("caller cancellation failed to join HTTP")
				}
			}
			if kind == "late_refresh" {
				close(lateReturn)
				select {
				case <-refreshJoined:
				case <-ctx.Done():
					t.Fatal("late refresh did not join")
				}
			}
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("body request did not join")
			}
			if err == nil {
				t.Fatal("expired or denied grant accepted body")
			}
			if err = client.validate(t.Context(), target.Bucket); err == nil || requests.Load() != 1 {
				t.Fatal("expired grant signed another request")
			}
		})
	}
}
