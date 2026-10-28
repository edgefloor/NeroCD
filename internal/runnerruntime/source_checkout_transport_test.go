//go:build unix

package runnerruntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/source"
)

func fixtureProcess(t *testing.T, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture %s failed: %v: %s", name, err, b)
	}
	return strings.TrimSpace(string(b))
}

func sourceCheckoutSSHServer(t *testing.T, root, image string) (string, string) {
	t.Helper()
	for _, key := range []string{"host", "client"} {
		fixtureProcess(t, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(root, key))
	}
	pub, err := os.ReadFile(filepath.Join(root, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(string(pub))
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	config := "Port 2222\nListenAddress 0.0.0.0\nHostKey /fixture/host\nPidFile /tmp/sshd.pid\nAuthorizedKeysFile /fixture/client.pub\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers git\nForceCommand git -c safe.directory=/fixture/repo.git upload-pack /fixture/repo.git\n"
	if err := os.WriteFile(filepath.Join(root, "sshd_config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	id := fixtureProcess(t, "docker", "run", "--detach", "--rm", "--label", "io.nerocd.source-checkout.fixture=20261022", "--publish", "127.0.0.1::2222", "--mount", "type=bind,src="+root+",dst=/fixture,readonly", image, "/usr/sbin/sshd", "-D", "-e", "-f", "/fixture/sshd_config")
	t.Cleanup(func() { fixtureProcess(t, "docker", "rm", "--force", id) })
	address := fixtureProcess(t, "docker", "port", id, "2222/tcp")
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSH fixture not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return address, fingerprint
}

func TestSourceCheckoutSSHTransport(t *testing.T) {
	image := os.Getenv("NEROCD_SOURCE_SSH_IMAGE")
	if image == "" {
		t.Skip("set NEROCD_SOURCE_SSH_IMAGE to the isolated runtime-provenance Git fixture image")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "hello"), []byte("ssh fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "add", ".")
	checkoutCommand(t, work, "commit", "-qm", "fixture")
	commit := checkoutCommand(t, work, "rev-parse", "HEAD")
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	address, fingerprint := sourceCheckoutSSHServer(t, root, image)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	req := SourceCheckoutRequest{BuildID: "ssh-build", SourceID: "ssh-source", InputID: "ssh-input", RepositoryURL: "ssh://git@127.0.0.1:" + port + "/repo.git", RequestedRef: "main", CheckoutPath: filepath.Join(parent, "checkout"), Limits: checkoutLimits(), Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"ssh"}, AllowedHosts: []string{"127.0.0.1"}, AllowedCIDRs: []string{"127.0.0.0/8"}, SSHHostFingerprints: []string{fingerprint}, CredentialReferenceID: "opaque"}}
	fixtureProcess(t, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(root, "wrong"))
	for _, mode := range []string{"valid", "changed-host", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			r := req
			if mode == "changed-host" {
				r.Policy.SSHHostFingerprints = []string{"SHA256:" + strings.Repeat("a", 43)}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			used := false
			receipt, err := WithSourceCheckout(ctx, r, func(context.Context) (SourceCredential, error) {
				keyName := "client"
				if mode == "wrong-key" {
					keyName = "wrong"
				}
				key, e := os.ReadFile(filepath.Join(root, keyName))
				return SourceCredential{Kind: "ssh_private_key", Bytes: key, UseUntil: time.Now().Add(time.Minute)}, e
			}, func(context.Context, SourceReceipt) error { return nil }, func(_ context.Context, path string, _ SourceReceipt) error {
				used = true
				b, e := os.ReadFile(filepath.Join(path, "hello"))
				if e != nil || string(b) != "ssh fixture\n" {
					t.Fatal("bad SSH tree")
				}
				entries, e := os.ReadDir(parent)
				if e != nil || len(entries) != 1 {
					t.Fatal("SSH material retained before use")
				}
				return nil
			})
			if mode != "valid" {
				if err == nil || used {
					t.Fatal("changed host key accepted")
				}
			} else if err != nil || receipt.Commit != commit || !used {
				t.Fatalf("SSH checkout: %v", err)
			}
		})
	}
}

func TestSourceCheckoutDNSAndCredentialOrder(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	original := lookupRepositoryIP
	lookupRepositoryIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	defer func() { lookupRepositoryIP = original }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	called := false
	_, err := WithSourceCheckout(ctx, req, func(context.Context) (SourceCredential, error) { called = true; return SourceCredential{}, nil }, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err == nil || called {
		t.Fatal("credential acquired before DNS policy rejection")
	}
}

func TestSourceScanCancellationJoins(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- monitorSourceWorkspace(ctx, root, root, checkoutLimits(), cancel) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled scan did not join")
	}
	// State may be deleted only after the monitor joins.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCheckoutUsesPinnedDNS(t *testing.T) {
	req, _, _ := checkoutTLSFixture(t, false)
	// httptest's isolated certificate authorizes example.com; the source URL uses
	// that hostname while only the policy-approved fixture IP receives traffic.
	req.RepositoryURL = strings.Replace(req.RepositoryURL, "127.0.0.1", "example.com", 1)
	req.Policy.AllowedHosts = []string{"example.com"}
	original := lookupRepositoryIP
	calls := 0
	lookupRepositoryIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		calls++
		if host != "example.com" {
			t.Fatalf("unexpected DNS host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	defer func() { lookupRepositoryIP = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := WithSourceCheckout(ctx, req, nil, func(context.Context, SourceReceipt) error { return nil }, func(context.Context, string, SourceReceipt) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unexpected repeated DNS lookup")
	}
}

func TestSourceCheckoutHTTPSExactHostnamePin(t *testing.T) {
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	if err = os.WriteFile(filepath.Join(work, "hello"), []byte("exact hostname pin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "add", ".")
	checkoutCommand(t, work, "commit", "-qm", "fixture")
	commit := checkoutCommand(t, work, "rev-parse", "HEAD")
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	checkoutCommand(t, filepath.Join(root, "repo.git"), "update-server-info")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated localhost fixture"}, DNSNames: []string{"localhost", "localhost."}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests, authorized atomic.Int32
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "fixture" || password != "fixture-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authorized.Add(1)
		backend.ServeHTTP(w, r)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	listener := server.Listener.Addr().(*net.TCPAddr)
	if !listener.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("fixture listener must bind only 127.0.0.1: %v", listener)
	}
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+shellQuote(actual)+" -c http.sslCAInfo="+shellQuote(ca)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	original := lookupRepositoryIP
	defer func() { lookupRepositoryIP = original }()
	for _, tc := range []struct {
		name, host, pin string
		success         bool
	}{
		{"dotted-unselected-address", "localhost.", "127.0.0.2", false},
		{"dotted-selected-address", "localhost.", "127.0.0.1", true},
		{"ordinary-selected-address", "localhost", "127.0.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests.Store(0)
			authorized.Store(0)
			lookupRepositoryIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
				if host != tc.host {
					t.Fatalf("transport hostname changed before DNS: %q", host)
				}
				return []netip.Addr{netip.MustParseAddr(tc.pin)}, nil
			}
			req := SourceCheckoutRequest{BuildID: "pin-build", SourceID: "pin-source", InputID: "pin-input", RepositoryURL: fmt.Sprintf("https://%s:%d/repo.git", tc.host, listener.Port), RequestedRef: "main", FrozenCommit: commit, CheckoutPath: filepath.Join(parent, "checkout"), Limits: checkoutLimits(), Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"localhost"}, AllowedCIDRs: []string{"127.0.0.0/8"}, CredentialReferenceID: "opaque"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			used := false
			receipt, err := WithSourceCheckout(ctx, req, func(context.Context) (SourceCredential, error) {
				return SourceCredential{Kind: "https_basic", Username: "fixture", Bytes: []byte("fixture-password"), UseUntil: time.Now().Add(time.Minute)}, nil
			}, func(context.Context, SourceReceipt) error { return nil }, func(_ context.Context, path string, _ SourceReceipt) error {
				used = true
				b, e := os.ReadFile(filepath.Join(path, "hello"))
				if e != nil || string(b) != "exact hostname pin\n" {
					t.Fatal("wrong fetched content")
				}
				return nil
			})
			if tc.success {
				if err != nil || !used || receipt.Commit != commit || authorized.Load() == 0 {
					t.Fatalf("selected address fetch failed: %v requests=%d authorized=%d", err, requests.Load(), authorized.Load())
				}
			} else if err == nil || used || requests.Load() != 0 || authorized.Load() != 0 {
				t.Fatalf("unselected address received request or credentials: err=%v requests=%d authorized=%d", err, requests.Load(), authorized.Load())
			}
			t.Logf("pin=%s requests=%d credential_deliveries=%d", tc.pin, requests.Load(), authorized.Load())
		})
	}
}
