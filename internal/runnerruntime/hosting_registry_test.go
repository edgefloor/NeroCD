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
	"sync/atomic"
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

func TestRegistryPullManagedStackMixedRegistries(t *testing.T) {
	for _, mode := range []string{"success", "public-first", "long-private-first", "failed", "expired", "expired-replay", "revoked-next", "no-match", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ncd-stack-registry-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			socket := filepath.Join(dir, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var authenticated atomic.Int32
			firstWindow := time.Now().Add(100 * time.Millisecond)
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authenticated.Add(1)
				image := r.URL.Query().Get("fromImage")
				if r.Method != "POST" || !strings.HasPrefix(image, "ghcr.io/example/") {
					t.Error("auth sent to another registry")
				}
				raw, _ := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
				defer clear(raw)
				var auth map[string]string
				if json.Unmarshal(raw, &auth) != nil || auth["serveraddress"] != "ghcr.io" || auth["password"] != "protected-token" {
					t.Error("wrong selected registry auth")
				}
				if mode == "long-private-first" && authenticated.Load() == 1 {
					time.Sleep(max(time.Until(firstWindow.Add(time.Millisecond)), 0))
				}
				if mode == "cancel" {
					cancel()
					return
				}
				if mode == "failed" {
					_, _ = fmt.Fprint(w, `{"error":"protected-token"}`)
					return
				}
				_, _ = fmt.Fprintf(w, "{\"status\":\"Digest: sha256:%s\"}\n", strings.Repeat("a", 64))
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			engine, err := newImageEngine("unix://" + socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = engine.Close() })
			engine.stackCommandReady = true
			anonymous := 0
			var borrowed *domain.HostingRegistryCredentialDelivery
			engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				for _, arg := range args {
					if strings.Contains(arg, "protected-token") {
						t.Error("credential in command")
					}
				}
				switch {
				case args[4] == "info":
					return []byte(`"daemon-stack"`), nil
				case args[4] == "image" && args[5] == "pull":
					anonymous++
					if args[6] != "docker.io/library/postgres:17" {
						t.Error("private image used anonymous path")
					}
					if borrowed != nil && borrowed.Credential != (domain.HostingRegistryCredential{}) {
						t.Error("plaintext retained during unrelated public pull")
					}
					return []byte("Digest: sha256:" + strings.Repeat("a", 64) + "\n"), nil
				case args[4] == "image" && args[5] == "inspect":
					return json.Marshal(map[string]any{"Id": "sha256:" + strings.Repeat("b", 64), "RepoDigests": []string{args[6]}, "Os": "linux", "Architecture": "arm64"})
				default:
					t.Error("preparation attempted workload mutation")
					return nil, fmt.Errorf("unexpected command")
				}
			})
			definition := domain.HostingStackDefinition{Services: []domain.HostingStackService{{Name: "app", Image: "ghcr.io/example/app:v1"}, {Name: "db", Image: "postgres:17"}}}
			if mode == "public-first" {
				definition.Services[0].Image, definition.Services[1].Image = definition.Services[1].Image, definition.Services[0].Image
			}
			if mode == "long-private-first" || mode == "expired-replay" || mode == "revoked-next" {
				definition.Services = append(definition.Services, domain.HostingStackService{Name: "worker", Image: "ghcr.io/example/worker:v1"})
			}
			var components []string
			deliver := func(_ context.Context, component string) (*domain.HostingRegistryCredentialDelivery, error) {
				if borrowed != nil && borrowed.Credential != (domain.HostingRegistryCredential{}) {
					t.Error("prior material retained across service delivery")
				}
				components = append(components, component)
				if mode == "revoked-next" && len(components) > 1 {
					return nil, fmt.Errorf("revoked")
				}
				borrowed = &domain.HostingRegistryCredentialDelivery{Credential: domain.HostingRegistryCredential{Registry: "ghcr.io", Username: "user", Password: "protected-token"}, UseUntil: time.Now().Add(time.Minute)}
				if mode == "expired" || mode == "expired-replay" && len(components) > 1 {
					borrowed.UseUntil = time.Now().Add(-time.Second)
				}
				if mode == "long-private-first" && len(components) == 1 {
					borrowed.UseUntil = firstWindow
				}
				if mode == "no-match" {
					borrowed.Credential.Registry = "unused.invalid"
				}
				return borrowed, nil
			}
			release, images, err := engine.prepareStack(ctx, definition, deliver)
			if borrowed != nil && borrowed.Credential != (domain.HostingRegistryCredential{}) {
				t.Fatal("protected material retained after preparation")
			}
			success := mode == "success" || mode == "public-first" || mode == "long-private-first"
			if success {
				expectedAuth := int32(1)
				if mode == "long-private-first" {
					expectedAuth = 2
				}
				if err != nil || len(images) != len(definition.Services) || anonymous != 1 || authenticated.Load() != expectedAuth || domain.ValidateHostingStackImages(definition, release.DaemonID, images) != nil {
					t.Fatal("mixed stack preparation failed", err)
				}
				plan := domain.HostingApplyPlan{Release: release, Stack: &domain.HostingStackPlan{Definition: definition, Images: images}}
				calls := len(components)
				if engine.ensureStackImages(ctx, plan) != nil || authenticated.Load() != expectedAuth || anonymous != 1 || len(components) != calls {
					t.Fatal("prepared recovery fetched images again")
				}
			} else if err == nil || release != (domain.HostingRelease{}) || images != nil || strings.Contains(err.Error(), "protected-token") {
				t.Fatal("failed stack pull did not fail closed")
			}
			if mode == "public-first" || mode == "long-private-first" || mode == "expired-replay" || mode == "revoked-next" {
				if len(components) != 2 || components[0] == components[1] {
					t.Fatal("service delivery identity was reused", components)
				}
			}
			if mode == "expired" || mode == "no-match" {
				if authenticated.Load() != 0 || anonymous != 0 {
					t.Fatal("ineligible credential started a pull")
				}
			}
			if mode == "expired-replay" || mode == "revoked-next" {
				if authenticated.Load() != 1 {
					t.Fatal("later service bypassed rejected delivery")
				}
			}
			body, err := os.ReadFile(filepath.Join(engine.configDir, "config.json"))
			if err != nil || string(body) != "{}\n" {
				t.Fatal("registry credential persisted")
			}
		})
	}
}

func TestRegistryPullComponentDeliveryIdentity(t *testing.T) {
	a := domain.HostingAuthority{OperationID: "op", RunID: "run_op", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	first := registryPullDeliveryID(a, "app")
	if first != registryPullDeliveryID(a, "app") || !domain.ValidHostingSourceDeliveryID(first) || first == registryPullDeliveryID(a, "db") {
		t.Fatal("component delivery identity is not stable and distinct")
	}
	changed := a
	changed.Fence = "next-fence"
	if registryPullDeliveryID(changed, "app") == first {
		t.Fatal("component delivery lost full authority binding")
	}
	old := domain.HostingDocumentHash(attemptMutationKey("registry", a.LeaseID, a.Attempt, "pull"))[7:39]
	if registryPullDeliveryID(a, "") != old {
		t.Fatal("ordinary image delivery history changed")
	}
}
