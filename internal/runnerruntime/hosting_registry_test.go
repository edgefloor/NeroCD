package runnerruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestRegistryPullEngineAuthDigestAndBounds(t *testing.T) {
	for _, mode := range []string{"success", "slow-stream", "wrong-registry", "bad-digest", "duplicate-digest", "registry-error", "oversize", "expired", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ncd-registry-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			socket := filepath.Join(dir, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(chan struct{}, 1)
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/images/create" || r.URL.Query().Get("fromImage") != "docker.io/library/nginx:1.27" {
					t.Error("wrong Engine request")
				}
				raw, e := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
				if e != nil {
					t.Error("missing auth")
				}
				var auth map[string]string
				if json.Unmarshal(raw, &auth) != nil || auth["username"] != "user" || auth["password"] != "token" || auth["serveraddress"] != "docker.io" {
					t.Error("wrong registry auth")
				}
				clear(raw)
				seen <- struct{}{}
				switch mode {
				case "slow-stream":
					time.Sleep(150 * time.Millisecond)
					_, _ = fmt.Fprintf(w, "{\"status\":\"Digest: sha256:%s\"}\n", strings.Repeat("a", 64))
				case "cancel":
					<-r.Context().Done()
					return
				case "registry-error":
					_, _ = fmt.Fprint(w, `{"error":"token must never reach output"}`)
				case "oversize":
					_, _ = fmt.Fprintf(w, `{"status":"%s"}`, strings.Repeat("x", (8<<20)+1))
				case "bad-digest":
					_, _ = fmt.Fprintf(w, `{"status":"Digest: wrong"}`)
				case "duplicate-digest":
					for range 2 {
						_, _ = fmt.Fprintf(w, "{\"status\":\"Digest: sha256:%s\"}\n", strings.Repeat("a", 64))
					}
				default:
					_, _ = fmt.Fprintf(w, "{\"status\":\"Digest: sha256:%s\"}\n", strings.Repeat("a", 64))
				}
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			engine, err := newImageEngine("unix://" + socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.Close() })
			engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				for _, arg := range args {
					if strings.Contains(arg, "token") {
						t.Error("secret in process argument")
					}
				}
				if args[4] == "info" {
					return []byte(`"daemon-image-test"`), nil
				}
				if args[4] == "image" && args[5] == "inspect" {
					return json.Marshal(map[string]any{"Id": "sha256:" + strings.Repeat("b", 64), "RepoDigests": []string{"docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)}, "Os": "linux", "Architecture": "amd64"})
				}
				return nil, fmt.Errorf("unexpected command")
			})
			d := &domain.HostingRegistryCredentialDelivery{Credential: domain.HostingRegistryCredential{Registry: "docker.io", Username: "user", Password: "token"}, UseUntil: time.Now().Add(time.Minute)}
			defer d.Close()
			if mode == "wrong-registry" {
				d.Credential.Registry = "ghcr.io"
			}
			if mode == "slow-stream" {
				d.UseUntil = time.Now().Add(100 * time.Millisecond)
			}
			if mode == "expired" {
				d.UseUntil = time.Now().Add(-time.Second)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				go func() { <-seen; cancel() }()
			}
			release, err := engine.prepareRegistryImage(ctx, domain.HostingSnapshot{ImageReference: "nginx:1.27"}, d)
			if mode == "success" || mode == "slow-stream" {
				if err != nil || release.CanonicalImage != "docker.io/library/nginx@sha256:"+strings.Repeat("a", 64) {
					t.Fatal("pull proof invalid", err)
				}
			} else if err == nil {
				t.Fatal("failed pull accepted")
			}
			if err != nil && strings.Contains(err.Error(), "token") {
				t.Fatal("secret in error")
			}
			body, err := os.ReadFile(filepath.Join(engine.configDir, "config.json"))
			if err != nil || string(body) != "{}\n" {
				t.Fatal("credentials persisted")
			}
			if mode == "wrong-registry" || mode == "expired" {
				select {
				case <-seen:
					t.Fatal("ineligible request sent")
				default:
				}
			}
		})
	}
}
