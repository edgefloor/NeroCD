//go:build linux && nerocd_proxy_fixture

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

const wordpressFixtureVersion = "coolify-b12d1386.wordpress-7.1.3-mysql-8.4.13.v1"
const wordpressFixtureImage = "wordpress:7.1.3-php8.3-apache@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834"

func TestHostingCatalogWordPressCompiledRunnerHTTPS(t *testing.T) {
	wordpressRuntimeScenario(t, false)
}

func TestHostingCatalogWordPressMySQLCompiledRunnerHTTPS(t *testing.T) {
	wordpressRuntimeScenario(t, true)
}

func TestWordPressInitialCatalogCommittedRetryContract(t *testing.T) {
	browser := &catalogRuntimeBrowser{}
	testHostingCatalogHTTPCurrentLookupAndRuntimeDefinitions(t, "wordpress-with-mysql", browser.serve)
	browser.mu.Lock()
	defer browser.mu.Unlock()
	if browser.original == nil || browser.retried == nil || !reflect.DeepEqual(*browser.original, *browser.retried) {
		t.Fatal("initial WordPress handler did not preserve committed installation")
	}
}

// TestHostingCatalogWordPressInitialCompiledRunner stops after the first real
// catalog deployment, before browser, preservation or schedule workflows.
func TestHostingCatalogWordPressInitialCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_WORDPRESS_INITIAL_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_WORDPRESS_RUNTIME") != "true" || os.Getenv("NEROCD_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned initial WordPress runtime fixture")
	}
	f := newStorageRuntimeFixture(t, false)
	input := wordpressInitialCatalogRequest(t, f.environmentID, f.destinationID)
	storageRuntimeJSON[map[string]string](f, "POST", "/api/v1/hosting-catalog/installations", input, 503)
	installed := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "POST", "/api/v1/hosting-catalog/installations", input, 201)
	f.catalogBrowser.mu.Lock()
	original, retried := f.catalogBrowser.original, f.catalogBrowser.retried
	f.catalogBrowser.mu.Unlock()
	if original == nil || retried == nil || !reflect.DeepEqual(*original, installed) || !reflect.DeepEqual(*retried, installed) {
		t.Fatal("initial WordPress committed installation retry changed")
	}
	resource := installed.Resource
	deployed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resource.ID+"/deployments", wordpressInitialDeploymentRequest(resource.Generation), 202)
	deployed = f.await(deployed.ID, "succeeded")
	if deployed.Result == nil || deployed.Result.Stack == nil || !deployed.Result.Ready || deployed.Result.Uncertain || deployed.MutationHold || len(deployed.Result.Stack.Components) != 2 || len(deployed.Result.Stack.Volumes) != 2 {
		t.Fatal("initial WordPress stack evidence unavailable")
	}
	for _, component := range deployed.Result.Stack.Components {
		if !component.Running || component.Health != "healthy" {
			t.Fatal("initial WordPress component unhealthy")
		}
	}
	t.Log("WORDPRESS_INITIAL_RUNTIME_PASS: real compiled runner/catalog deployment; no browser or schedules")
}

func wordpressRuntimeScenario(t *testing.T, mysql bool) {
	t.Helper()
	if os.Getenv("NEROCD_WORDPRESS_RUNTIME") != "true" || os.Getenv("NEROCD_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires approved WordPress catalog profile")
	}
	if (os.Getenv("NEROCD_WORDPRESS_MYSQL_RUNTIME") == "true") != mysql {
		t.Skip("requires exact WordPress terminal scenario")
	}
	f := newStorageRuntimeFixture(t, false)
	b := f.catalogBrowser
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	resource := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+resourceID, nil, 200)
	installed := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if original == nil || retried == nil || !reflect.DeepEqual(*original, installed) || !reflect.DeepEqual(*retried, installed) || installed.Request.TemplateID != "wordpress-with-mysql" || installed.Request.ImmutableVersion != wordpressFixtureVersion || installed.Binding.DestinationID != f.destinationID || installed.Request.PublicURL != "https://catalog-runtime.example.test:25443" || resource.Source.Compose == nil || len(installed.Credentials) != 3 {
		t.Fatal("WordPress install identity/retry changed")
	}
	definition, err := domain.ParseHostingStack(*resource.Source.Compose)
	if err != nil || definition.PublicComponent != "wordpress" || definition.ContainerPort != 80 {
		t.Fatal("WordPress public stack topology changed")
	}
	runtime := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	if runtime.Configuration.RuntimeGeneration != 1 || len(runtime.Configuration.Entries) != 4 {
		t.Fatal("WordPress runtime references missing")
	}
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	var sourcePort int
	var sourceOperation domain.HostingOperation
	var sourceWordPress domain.HostingStackComponent
	verify := func(op domain.HostingOperation) {
		t.Helper()
		if op.Result == nil || op.Result.Stack == nil || !op.Result.Ready || op.Result.Uncertain || op.MutationHold || len(op.Result.Stack.Components) != 2 || len(op.Result.Stack.Volumes) != 2 || op.Release == nil {
			t.Fatal("WordPress stack success missing")
		}
		for _, component := range op.Result.Stack.Components {
			if !component.Running || component.Health != "healthy" {
				t.Fatal("WordPress/MySQL health missing")
			}
			if component.Component == "mysql" {
				raw := bytes.TrimSpace(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{json .HostConfig.PortBindings}}"))
				if string(raw) != "null" && string(raw) != "{}" {
					t.Fatal("MySQL published a host port")
				}
			} else if component.Component != "wordpress" {
				t.Fatal("unexpected WordPress component")
			} else {
				sourceWordPress = component
			}
		}
		sourcePort = op.Snapshot.HostPort
		sourceOperation = op
	}
	verify(first)
	b.set("healthy", resourceID)
	b.await(t, "healthy")
	catalogRuntimeRoute(t, f, resourceID, first)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	var sourceProxy domain.HostingProxyOperation
	var proof string
	secretValues := map[string]string{}
	for _, credential := range installed.Credentials {
		if credential.Secret == nil {
			t.Fatal("WordPress credential reference missing")
		}
		revealed := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+credential.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: credential.Secret.Version}, 200)
		if revealed.Value == "" {
			t.Fatal("WordPress generated secret empty")
		}
		secretValues[credential.Name] = revealed.Value
	}
	finish := func(operations ...any) {
		f.stop()
		journal, e := os.ReadFile(filepath.Join(f.root, "journal", "journal.json"))
		if e != nil {
			t.Fatal(e)
		}
		ordinary, _ := json.Marshal(operations)
		for _, value := range secretValues {
			if bytes.Contains(journal, []byte(value)) || bytes.Contains(ordinary, []byte(value)) || strings.Contains(f.logs.String(), value) {
				t.Fatal("WordPress managed credential entered public operation/journal/log")
			}
		}
		clear(secretValues)
		clear(journal)
		clear(ordinary)
	}
	assertApplication := func() {
		t.Helper()
		parts := strings.SplitN(proof, "|", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "/wp-content/uploads/") {
			t.Fatal("WordPress browser identity missing")
		}
		for path, marker := range map[string][]byte{"/?p=" + parts[0]: []byte("NeroCD persistent WordPress post"), parts[1]: wordpressFixturePNG()} {
			kind := "post"
			if strings.HasPrefix(path, "/wp-content/uploads/") {
				kind = "upload"
			}
			url := installed.Request.PublicURL + path
			requestStarted := time.Now()
			var gotConnection, reused, wasIdle, tlsDone, wrote, wroteFailed, firstByte atomic.Bool
			var idleNanoseconds atomic.Int64
			trace := &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) {
					if gotConnection.CompareAndSwap(false, true) {
						reused.Store(info.Reused)
						wasIdle.Store(info.WasIdle)
						idleNanoseconds.Store(int64(info.IdleTime))
					}
				},
				TLSHandshakeDone: func(tls.ConnectionState, error) { tlsDone.Store(true) },
				WroteRequest: func(info httptrace.WroteRequestInfo) {
					wrote.Store(true)
					if info.Err != nil {
						wroteFailed.Store(true)
					}
				},
				GotFirstResponseByte: func() { firstByte.Store(true) },
			}
			request, requestErr := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, url, nil)
			if requestErr != nil {
				t.Fatal("WordPress assertion request construction failed")
			}
			r, e := client.Do(request)
			if e != nil {
				t.Logf("WordPress failed HTTPS trace kind=%s started=%s finished=%s elapsed_ms=%d connection=%t first_reused=%t first_was_idle=%t first_idle_ms=%d tls_done=%t wrote=%t wrote_failed=%t first_byte=%t", kind, requestStarted.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Since(requestStarted).Milliseconds(), gotConnection.Load(), reused.Load(), wasIdle.Load(), time.Duration(idleNanoseconds.Load()).Milliseconds(), tlsDone.Load(), wrote.Load(), wroteFailed.Load(), firstByte.Load())
				var controls sync.WaitGroup
				controls.Add(4)
				profileContext, cancelProfile := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancelProfile()
				freshWritten, freshDone := make(chan struct{}), make(chan struct{})
				var signalFreshWritten sync.Once
				var freshProfileFirstByte atomic.Bool
				beginControls := make(chan struct{})
				go func() {
					defer controls.Done()
					<-beginControls
					started := time.Now()
					defer func() {
						t.Logf("WordPress failure control timing request=proxy_observe started=%s finished=%s elapsed_ms=%d", started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Since(started).Milliseconds())
					}()
					// Observe the already committed source generation without changing it.
					profile, profileErr := storageRuntimeProxyProfileFor(f.root, f.proxyCleanupID, f.serverID, f.destinationID, "runner_storage_runtime_fixture", f.proxyCleanupDaemon)
					if profileErr == nil && sourceProxy.Plan != nil && sourceProxy.Preparation != nil && sourceProxy.Result != nil && domain.ValidateHostingProxyPlan(*sourceProxy.Plan) == nil {
						snapshot := sourceProxy.Plan.Snapshot
						identity := hostingproxy.Identity{ProxyID: snapshot.ProxyID, ServerID: snapshot.ServerID, DestinationID: snapshot.DestinationID, RunnerID: snapshot.RunnerID, DaemonID: sourceProxy.Plan.DaemonID}
						settings := hostingproxy.Settings{Directory: profile.directory, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Email: snapshot.Configuration.Email, CA: hostingproxy.CA(snapshot.Configuration.CA), ManagementPort: snapshot.Configuration.ManagementPort}
						engine, engineErr := hostingproxy.NewFixture(settings, "https://localhost:14000/dir", f.roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
						routes := make([]hostingproxy.Route, 0, len(snapshot.Routes))
						var sourceRoute string
						for _, route := range snapshot.Routes {
							routes = append(routes, hostingproxy.Route{ID: route.RouteID, Hostname: route.Hostname, PathPrefix: route.PathPrefix, HostPort: route.HostPort})
							if route.ResourceID == resourceID && int(route.HostPort) == sourcePort {
								sourceRoute = route.RouteID
							}
						}
						plan, planErr := hostingproxy.Prepare(identity, settings, uint64(snapshot.Generation), uint64(sourceProxy.Plan.PreviousGeneration), routes)
						if snapshot.VerifiedSwitch != nil {
							plan, planErr = hostingproxy.PrepareVerifiedSwitch(identity, settings, uint64(snapshot.Generation), uint64(sourceProxy.Plan.PreviousGeneration), routes, snapshot.VerifiedSwitch.RouteIDs())
						}
						matches := identity.ProxyID == f.proxyCleanupID && identity.ServerID == f.serverID && identity.DestinationID == f.destinationID && identity.RunnerID == "runner_storage_runtime_fixture" && identity.DaemonID == f.proxyCleanupDaemon && sourceRoute != "" && plan.Hash() == sourceProxy.Preparation.EngineHash && plan.StaticHash() == sourceProxy.Preparation.StaticHash
						if engineErr == nil && planErr == nil && matches {
							observeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							observed, observeErr := engine.Observe(observeContext, plan)
							cancel()
							sourceObserved, httpReady, httpsReady := false, false, false
							for _, route := range observed.Routes {
								if route.RouteID == sourceRoute {
									sourceObserved, httpReady, httpsReady = true, route.HTTPReady, route.HTTPSReady
								}
							}
							category := "none"
							if observeErr != nil {
								category = wordpressRuntimeHTTPErrorCategory(observeErr)
								switch observeErr.Error() {
								case "generation receipt not committed or mismatched":
									category = "proxy_receipt_mismatch"
								case "owned proxy not observable":
									category = "proxy_ownership_unavailable"
								case "traefik management unavailable", "traefik routers/services unavailable":
									category = "proxy_management_unavailable"
								case "loaded router count differs", "generation marker router not loaded", "loaded generation marker mismatch", "desired service is not loaded", "desired router is not loaded", "TLS router is not loaded":
									category = "proxy_loaded_config_mismatch"
								case "host/path generation probe failed":
									category = "proxy_backend_http_probe"
								case "selected route HTTP or trusted HTTPS verification failed; retain controller hold":
									category = "proxy_selected_route_probe"
								}
							}
							t.Logf("WordPress failure proxy observe_ok=%t owned_container_match=%t generation_match=%t loaded_config_match=%t source_seen=%t source_http=%t source_https=%t category=%s", observeErr == nil, observed.ContainerID != "" && observed.ContainerID == sourceProxy.Result.ContainerID, observed.LoadedGeneration == uint64(snapshot.Generation), observed.LoadedGeneration == uint64(snapshot.Generation) && observed.Hash == plan.Hash(), sourceObserved, httpReady, httpsReady, category)
						} else {
							t.Log("WordPress failure proxy expected_profile_match=false")
						}
					} else {
						t.Log("WordPress failure proxy expected_profile_available=false")
					}
				}()

				go func() {
					defer controls.Done()
					<-beginControls
					started := time.Now()
					category := "unavailable"
					defer func() {
						pending := true
						select {
						case <-freshDone:
							pending = false
						default:
						}
						t.Logf("WordPress private proxy profile category=%s started=%s finished=%s elapsed_ms=%d fresh_pending=%t fresh_first_byte=%t", category, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Since(started).Milliseconds(), pending, freshProfileFirstByte.Load())
					}()
					select {
					case <-freshWritten:
					case <-freshDone:
						category = "fresh_completed"
						return
					case <-profileContext.Done():
						category = "deadline"
						return
					}
					timer := time.NewTimer(300 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-freshDone:
						category = "fresh_completed"
						return
					case <-profileContext.Done():
						category = "deadline"
						return
					}
					select {
					case <-freshDone:
						category = "fresh_completed"
						return
					default:
					}
					if freshProfileFirstByte.Load() {
						category = "fresh_first_byte"
						return
					}
					profile, profileErr := storageRuntimeProxyProfileFor(f.root, f.proxyCleanupID, f.serverID, f.destinationID, "runner_storage_runtime_fixture", f.proxyCleanupDaemon)
					if profileErr != nil || sourceProxy.Result == nil || !storageRuntimeContainerID.MatchString(sourceProxy.Result.ContainerID) {
						category = "profile_unavailable"
						return
					}
					boundedCommand := func(cmd *exec.Cmd) ([]byte, error) {
						cmd.WaitDelay = time.Second
						output, err := cmd.StdoutPipe()
						if err != nil {
							return nil, err
						}
						if err = cmd.Start(); err != nil {
							return nil, err
						}
						raw, readErr := io.ReadAll(io.LimitReader(output, (64<<10)+1))
						_ = output.Close()
						waitErr := cmd.Wait()
						if readErr != nil {
							return nil, readErr
						}
						if waitErr != nil {
							return nil, waitErr
						}
						if len(raw) > 64<<10 {
							return nil, fmt.Errorf("bounded proxy inspection")
						}
						return raw, nil
					}
					command := func(args ...string) ([]byte, error) {
						return boundedCommand(exec.CommandContext(profileContext, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...))
					}
					daemon, daemonErr := command("info", "--format", "{{.ID}}")
					image, imageErr := command("image", "inspect", profile.image, "--format", "{{.Id}}")
					raw, inspectErr := command("container", "inspect", sourceProxy.Result.ContainerID, "--format", "{{json .}}")
					defer clear(raw)
					if daemonErr != nil || imageErr != nil || inspectErr != nil || strings.TrimSpace(string(daemon)) != profile.daemon {
						category = "ownership_unavailable"
						return
					}
					owned, ownedErr := storageRuntimeCheckProxy(raw, profile, sourceProxy.Result.ContainerID, strings.TrimSpace(string(image)))
					if ownedErr != nil || !owned.State.Running {
						category = "ownership_unavailable"
						return
					}
					select {
					case <-freshDone:
						category = "fresh_completed"
						return
					default:
					}
					if freshProfileFirstByte.Load() {
						category = "fresh_first_byte"
						return
					}
					profileTransport := &http.Transport{Proxy: nil}
					defer profileTransport.CloseIdleConnections()
					profileClient := &http.Client{Transport: profileTransport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					request, requestErr := http.NewRequestWithContext(profileContext, http.MethodGet, "http://127.0.0.1:19080/debug/pprof/goroutine?debug=2", nil)
					if requestErr != nil {
						category = "request_construction"
						return
					}
					response, profileErr := profileClient.Do(request)
					if profileErr != nil {
						category = "request_unavailable"
						return
					}
					body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
					closeErr := response.Body.Close()
					defer clear(body)
					if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
						category = "response_unavailable"
						return
					}
					truncated := len(body) > 2<<20
					if truncated {
						body = body[:2<<20]
					}
					file, fileErr := os.OpenFile("/fixture/wordpress-proxy-goroutines.private", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
					if fileErr != nil {
						category = "private_file_unavailable"
						return
					}
					_, writeErr := file.Write(body)
					fileCloseErr := file.Close()
					if writeErr != nil || fileCloseErr != nil {
						_ = os.Remove("/fixture/wordpress-proxy-goroutines.private")
						category = "private_write_failed"
						return
					}
					category = "captured"
					profilePendingAtSave := true
					select {
					case <-freshDone:
						profilePendingAtSave = false
					default:
					}
					t.Logf("WordPress private proxy profile bytes=%d sha256=%x truncated=%t captured_at=%s fresh_pending=%t fresh_first_byte=%t", len(body), sha256.Sum256(body), truncated, time.Now().UTC().Format(time.RFC3339Nano), profilePendingAtSave, freshProfileFirstByte.Load())
					// Socket evidence is a separate private artifact; its failure never discards the profile.
					const socketScript = `set -eu
if test "$1" = relay; then
 published=$2; current=$3
 gate='dockerd --host=unix:///var/run/docker.sock --feature=containerd-snapshotter=false --storage-driver=overlay2 '
 test "$(cat /proc/1/comm)" = docker-init; test "$(readlink /proc/1/exe)" = /usr/local/bin/docker-init
 test "$(tr '\000' ' ' </proc/1/cmdline)" = "docker-init -- $gate"
 children() {
  tasks=0; list=''
  for file in /proc/$1/task/*/children; do tasks=$((tasks+1)); test "$tasks" -le 128 || return 73; values=$(cat "$file") || return 73; list="$list $values"; done
  printf '%s\n' $list | sort -nu | awk 'NF {if(++n>128)exit 1;print}'
 }
 daemon=''; count=0
 child_ids=$(children 1) || exit 73
 for pid in $child_ids; do
  count=$((count+1)); test "$count" -le 128
  if test "$(cat /proc/$pid/comm)" = dockerd; then test -z "$daemon"; daemon=$pid; fi
 done
 test -n "$daemon"; test "$(readlink /proc/$daemon/exe)" = /usr/local/bin/dockerd
 test "$(tr '\000' ' ' </proc/$daemon/cmdline)" = "$gate"
 test "$(awk '/^PPid:/ {print $2}' /proc/$daemon/status)" = 1
 for ns in pid net; do test "$(readlink /proc/$daemon/ns/$ns)" = "$(readlink /proc/1/ns/$ns)"; done
 selected=''; count=0
 daemon_ticks=$(awk '{print $22}' /proc/$daemon/stat)
 child_ids=$(children "$daemon") || exit 73
 for pid in $child_ids; do
  count=$((count+1)); test "$count" -le 128
  test "$(cat /proc/$pid/comm)" = docker-proxy || continue
  test "$(readlink /proc/$pid/exe)" = /usr/local/bin/docker-proxy
  test "$(awk '/^PPid:/ {print $2}' /proc/$pid/status)" = "$daemon"
  for ns in pid net; do test "$(readlink /proc/$pid/ns/$ns)" = "$(readlink /proc/$daemon/ns/$ns)"; done
  args=$(tr '\000' '|' </proc/$pid/cmdline)
  prefix="/usr/local/bin/docker-proxy|-proto|tcp|-host-ip|127.0.0.1|-host-port|$published|-container-ip|"
  case "$args" in "$prefix"*) suffix=${args#"$prefix"};; *) continue;; esac
  backend=${suffix%%|*}
  case "$suffix" in "$backend|-container-port|80|"|"$backend|-container-port|80|-use-listen-fd|") ;; *) exit 74;; esac
  awk -v ip="$backend" 'BEGIN {n=split(ip,a,".");if(n!=4)exit 1;for(i=1;i<=4;i++)if(a[i]!~/^[0-9]+$/ || a[i]>255 || "x"sprintf("%d",a[i])!="x"a[i])exit 1}'
  test -z "$selected"; selected=$pid; configured=$backend; saved_args=$args
 done
 test -n "$selected"
 identity() {
  test "$(cat /proc/$selected/comm)" = docker-proxy; test "$(readlink /proc/$selected/exe)" = /usr/local/bin/docker-proxy
  test "$(awk '/^PPid:/ {print $2}' /proc/$selected/status)" = "$daemon"
  test "$(tr '\000' '|' </proc/$selected/cmdline)" = "$saved_args"
  for ns in pid net; do test "$(readlink /proc/$selected/ns/$ns)" = "$(readlink /proc/$daemon/ns/$ns)"; done
 }
 identity; ticks=$(awk '{print $22}' /proc/$selected/stat); mapping=''; count=0; sockets=0; limited=false; socket_limited=false
 for fd in /proc/$selected/fd/*; do
  if test "$count" -ge 1024; then limited=true; break; fi; count=$((count+1)); target=$(readlink "$fd") || exit 73
  case "$target" in socket:\[*\]) if test "$sockets" -ge 256; then socket_limited=true; break; fi; sockets=$((sockets+1)); inode=${target#socket:[}; inode=${inode%]}; mapping="$mapping $selected ${fd##*/} $inode";; esac
 done
 matches=false; if test "$configured" = "$current"; then matches=true; fi
 printf 'fd_limit_reached=%s\nsocket_limit_reached=%s\nfd_entries_read=%s socket_mappings=%s\nrelay_pid=%s start_ticks=%s parent=%s configured_backend=%s:80 backend_matches_current=%s\nowned_socket_fds%s\n' "$limited" "$socket_limited" "$count" "$sockets" "$selected" "$ticks" "$daemon" "$configured" "$matches" "$mapping"
 backend_hex=$(awk -v ip="$configured" 'BEGIN{split(ip,a,".");printf "%02X%02X%02X%02X:0050",a[4],a[3],a[2],a[1]}')
 frontend_hex=$(printf '0100007F:%04X' "$published")
 awk -v owned="$mapping" -v front="$frontend_hex" -v back="$backend_hex" 'BEGIN{n=split(owned,f," ");for(i=3;i<=n;i+=3)o[f[i]]=1} FNR>1 && ($10 in o){role="other_owned";if($2==front)role=($4=="0A"?"frontend_listener":"frontend_accepted");if($3==back)role="backend";print role,FILENAME,$0}' /proc/net/tcp /proc/net/tcp6
 identity; test "$(awk '{print $22}' /proc/$selected/stat)" = "$ticks"
 test "$(awk '{print $22}' /proc/$daemon/stat)" = "$daemon_ticks"
 exit
fi
command -v awk >/dev/null
if test "$1" = source; then
 printf 'attribution=verified_container_network_namespace source_fd_mapping=unavailable\n'
 awk -v port=":$2$" 'FNR>1 && $2 ~ port {print FILENAME, $0}' /proc/net/tcp /proc/net/tcp6
 exit
fi
command -v readlink >/dev/null
pids=1
mapping=''; processes=0
for pid in $pids; do
 processes=$((processes+1)); test "$processes" -le 32
 comm=$(cat "/proc/$pid/comm")
 case "$1:$comm" in proxy:traefik|source:apache2) ;; *) exit 72;; esac
 ls "/proc/$pid/fd" >/dev/null
 descriptors=0
 for fd in /proc/$pid/fd/*; do
  descriptors=$((descriptors+1)); test "$descriptors" -le 256
  target=$(readlink "$fd") || exit 73
  case "$target" in socket:\[*\]) inode=${target#socket:[}; inode=${inode%]}; mapping="$mapping $pid ${fd##*/} $inode";; esac
 done
done
printf 'owned_socket_fds%s\n' "$mapping"
awk -v owned="$mapping" -v port=":$2$" 'BEGIN {n=split(owned,fields," "); for(i=3;i<=n;i+=3) owners[fields[i]]=1} FNR>1 && ($10 in owners) && ($2 ~ port || $3 ~ port) {print FILENAME, $0}' /proc/net/tcp /proc/net/tcp6`
					socketCategory := "source_unavailable"
					defer func() { t.Logf("WordPress private proxy sockets category=%s", socketCategory) }()
					if !storageRuntimeContainerID.MatchString(sourceWordPress.ContainerID) || sourceOperation.Result == nil || sourceOperation.Result.Stack == nil || !storageRuntimeContainerID.MatchString(sourceOperation.Result.Stack.NetworkID) {
						return
					}
					sourceRaw, sourceErr := command("container", "inspect", sourceWordPress.ContainerID, "--format", "{{json .}}")
					defer clear(sourceRaw)
					var source struct {
						ID, Image       string
						Config          struct{ Labels map[string]string }
						State           struct{ Running bool }
						HostConfig      struct{ NetworkMode string }
						NetworkSettings struct {
							Ports    map[string][]struct{ HostIP, HostPort string }
							Networks map[string]struct{ NetworkID, IPAddress, Gateway, EndpointID string }
						}
					}
					if sourceErr != nil || json.Unmarshal(sourceRaw, &source) != nil || source.ID != sourceWordPress.ContainerID || source.Image != sourceWordPress.ImageID || !source.State.Running || source.Config.Labels["io.nerocd.binding"] != sourceOperation.Snapshot.BindingID || source.Config.Labels["io.nerocd.operation"] != sourceWordPress.OperationID || source.Config.Labels["io.nerocd.stack.component"] != "wordpress" {
						return
					}
					expectedNetwork := domain.HostingStackObjectID(sourceOperation.Snapshot.BindingID, "network", "private")
					network, networkPresent := source.NetworkSettings.Networks[expectedNetwork]
					if source.HostConfig.NetworkMode != expectedNetwork || len(source.NetworkSettings.Networks) != 1 || !networkPresent || network.NetworkID != sourceOperation.Result.Stack.NetworkID {
						return
					}
					ports := source.NetworkSettings.Ports["80/tcp"]
					if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(sourcePort) {
						return
					}
					if net.ParseIP(network.IPAddress).To4() == nil {
						return
					}
					snapshots := make([]map[string]any, 0, 3)
					for _, target := range []struct{ role, id, port string }{{"proxy", owned.ID, fmt.Sprintf("%04X", sourcePort)}, {"source", source.ID, "0050"}, {"relay", "", strconv.Itoa(sourcePort)}} {
						began := time.Now()
						pendingAtStart := true
						select {
						case <-freshDone:
							pendingAtStart = false
						default:
						}
						firstByteAtStart := freshProfileFirstByte.Load()
						var raw []byte
						var socketErr error
						if target.role == "relay" {
							raw, socketErr = boundedCommand(exec.CommandContext(profileContext, "/bin/sh", "-c", socketScript, "socket-capture", target.role, target.port, network.IPAddress))
						} else {
							raw, socketErr = command("exec", target.id, "sh", "-c", socketScript, "socket-capture", target.role, target.port)
						}
						defer clear(raw)
						pending := true
						select {
						case <-freshDone:
							pending = false
						default:
						}
						state := "available"
						if socketErr != nil {
							state = "capability_or_read_unavailable"
						} else if target.role == "relay" && (bytes.HasPrefix(raw, []byte("fd_limit_reached=true\n")) || bytes.Contains(raw, []byte("\nsocket_limit_reached=true\n"))) {
							state = "partial_limit"
						}
						snapshots = append(snapshots, map[string]any{"role": target.role, "container_id": target.id, "started": began.UTC().Format(time.RFC3339Nano), "finished": time.Now().UTC().Format(time.RFC3339Nano), "fresh_pending_at_start": pendingAtStart, "fresh_first_byte_at_start": firstByteAtStart, "fresh_pending": pending, "fresh_first_byte": freshProfileFirstByte.Load(), "category": state, "data": string(raw)})
					}
					socketBody, marshalErr := json.Marshal(map[string]any{"source_attribution": "verified_container_network_namespace", "source_fd_mapping": "unavailable", "proxy_attribution": "pid1_fd_inode_and_published_port", "relay_attribution": "unique_dockerd_child_exact_frontend_argv_fd_inode", "source_operation": sourceOperation.ID, "source_component": sourceWordPress, "source_networks": source.NetworkSettings.Networks, "snapshots": snapshots})
					defer clear(socketBody)
					if marshalErr != nil || len(socketBody) > 192<<10 {
						socketCategory = "bounded_encoding_unavailable"
						return
					}
					socketFile, socketFileErr := os.OpenFile("/fixture/wordpress-proxy-sockets.private", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
					if socketFileErr != nil {
						socketCategory = "private_file_unavailable"
						return
					}
					_, socketWriteErr := socketFile.Write(socketBody)
					socketCloseErr := socketFile.Close()
					if socketWriteErr != nil || socketCloseErr != nil {
						_ = os.Remove("/fixture/wordpress-proxy-sockets.private")
						socketCategory = "private_write_failed"
						return
					}
					socketCategory = "recorded"
					t.Logf("WordPress private proxy sockets bytes=%d sha256=%x", len(socketBody), sha256.Sum256(socketBody))

				}()
				for _, direct := range []bool{false, true} {
					go func() {
						defer controls.Done()
						<-beginControls
						started := time.Now()
						if !direct {
							defer close(freshDone)
						}
						controlTransport := transport.Clone()
						controlURL, controlKind := url, "fresh_tls"
						if direct {
							controlTransport = &http.Transport{Proxy: nil}
							controlURL, controlKind = fmt.Sprintf("http://127.0.0.1:%d%s", sourcePort, path), "direct_source"
						}
						defer func() {
							t.Logf("WordPress failure control timing request=%s started=%s finished=%s elapsed_ms=%d", controlKind, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Since(started).Milliseconds())
						}()
						control := &http.Client{Transport: controlTransport, Timeout: 5 * time.Second}
						controlRequest, controlErr := http.NewRequestWithContext(context.Background(), http.MethodGet, controlURL, nil)
						if controlErr != nil {
							controlTransport.CloseIdleConnections()
							t.Logf("WordPress failure control kind=%s request=%s category=request_construction", kind, controlKind)
							return
						}
						if direct {
							controlRequest.Host = "catalog-runtime.example.test:25443"
							controlRequest.Header.Set("X-Forwarded-Proto", "https")
							control.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
						}
						var connected, connectFailed, handshake, handshakeFailed, firstByte atomic.Bool
						if !direct {
							controlRequest = controlRequest.WithContext(httptrace.WithClientTrace(controlRequest.Context(), &httptrace.ClientTrace{
								ConnectDone:      func(_, _ string, err error) { connected.Store(true); connectFailed.Store(err != nil) },
								TLSHandshakeDone: func(_ tls.ConnectionState, err error) { handshake.Store(true); handshakeFailed.Store(err != nil) },
								WroteRequest: func(info httptrace.WroteRequestInfo) {
									if info.Err == nil {
										signalFreshWritten.Do(func() { close(freshWritten) })
									}
								},
								GotFirstResponseByte: func() { firstByte.Store(true); freshProfileFirstByte.Store(true) },
							}))
						}
						response, controlErr := control.Do(controlRequest)
						status, verified, matches, category := 0, false, false, "none"
						if controlErr != nil {
							category = wordpressRuntimeHTTPErrorCategory(controlErr)
						} else {
							data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
							closeErr := response.Body.Close()
							status, verified = response.StatusCode, response.TLS != nil && len(response.TLS.VerifiedChains) > 0
							matches = len(data) <= 2<<20 && bytes.Contains(data, marker)
							if kind == "upload" {
								matches = bytes.Equal(data, marker)
							}
							if readErr != nil || closeErr != nil {
								category = "body_read_or_close"
							}
						}
						controlTransport.CloseIdleConnections()
						if !direct {
							t.Logf("WordPress fresh HTTPS trace connect_done=%t connect_failed=%t tls_done=%t tls_failed=%t first_byte=%t", connected.Load(), connectFailed.Load(), handshake.Load(), handshakeFailed.Load(), firstByte.Load())
						}
						t.Logf("WordPress failure control kind=%s request=%s status=%d verified=%t matches=%t category=%s", kind, controlKind, status, verified, matches, category)
					}()
				}
				close(beginControls)
				controls.Wait()
				cancelProfile()
				t.Fatalf("WordPress trusted HTTPS failed kind=%s category=%s", kind, wordpressRuntimeHTTPErrorCategory(e))
			}
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				leaf := r.TLS.PeerCertificates[0]
				t.Logf("WordPress HTTPS kind=%s status=%d verified=%t leaf_not_before=%s leaf_not_after=%s", kind, r.StatusCode, len(r.TLS.VerifiedChains) > 0, leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
			}
			body, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			_ = r.Body.Close()
			t.Logf("WordPress HTTPS request kind=%s started=%s finished=%s elapsed_ms=%d connection=%t first_reused=%t first_was_idle=%t first_idle_ms=%d wrote_failed=%t", kind, requestStarted.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Since(requestStarted).Milliseconds(), gotConnection.Load(), reused.Load(), wasIdle.Load(), time.Duration(idleNanoseconds.Load()).Milliseconds(), wroteFailed.Load())
			matches := bytes.Contains(body, marker)
			if strings.HasPrefix(path, "/wp-content/uploads/") {
				matches = bytes.Equal(body, marker)
			}
			if e != nil || r.StatusCode != 200 || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || !matches {
				t.Fatal("WordPress trusted post/upload identity changed")
			}
		}
	}
	b.set("app_create", resourceID)
	proof = b.await(t, "app_created")
	assertApplication()
	f.stop()
	f.start()
	b.set("redeploy", resourceID)
	second := f.await(b.await(t, "redeployed"), "succeeded")
	verify(second)
	if !reflect.DeepEqual(first.Result.Stack.Volumes, second.Result.Stack.Volumes) || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration {
		t.Fatal("WordPress replacement changed data/runtime identities")
	}
	composeRuntimeWriterBarrier(t, first, second)
	continuity := f.awaitContinuity(resourceID, second.ID)
	sourceProxy = f.proxyAwait(continuity.SuccessorProxyOperationID)
	assertApplication()
	b.set("app_recheck", resourceID)
	if b.await(t, "app_checked") != proof {
		t.Fatal("fresh WordPress login lost post/media")
	}
	b.set("stop", resourceID)
	stopped := f.await(b.await(t, "stopped"), "succeeded")
	if stopped.Result == nil || !stopped.Result.StackStopped || stopped.MutationHold || stopped.StackStopPlan == nil || stopped.StackStopPlan.Target.OperationID != second.ID {
		t.Fatal("WordPress Stop authority missing")
	}
	for _, c := range second.Result.Stack.Components {
		if len(bytes.TrimSpace(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+c.ContainerID))) != 0 {
			t.Fatal("WordPress stopped writer remains")
		}
	}
	b.set("start", resourceID)
	started := f.await(b.await(t, "started"), "succeeded")
	verify(started)
	if started.Snapshot.StackStoppedOperationID != stopped.ID || !reflect.DeepEqual(second.Result.Stack.Volumes, started.Result.Stack.Volumes) {
		t.Fatal("WordPress Start changed volumes/stopped authority")
	}
	continuity = f.awaitContinuity(resourceID, started.ID)
	sourceProxy = f.proxyAwait(continuity.SuccessorProxyOperationID)
	assertApplication()
	b.set("app_start_recheck", resourceID)
	if b.await(t, "app_start_checked") != proof {
		t.Fatal("WordPress Start lost post/media")
	}
	if mysql {
		terminal := wordpressRuntimeDatabaseBackupImport(t, f, resource, installed, started, proof, assertApplication, secretValues)
		b.set("complete_mysql", terminal.ID)
		if b.await(t, "done") != resourceID {
			t.Fatal("WordPress MySQL completion resource mismatch")
		}
		finish(installed, first, second, stopped, started, terminal)
		t.Log("WORDPRESS_COMPOSE_MYSQL_RUNTIME_PASS")
		return
	}
	// An unavailable immutable image must fail before replacing either writer.
	source := resource.Source
	changed := *source.Compose
	changed.Original = strings.Replace(changed.Original, wordpressFixtureImage, "127.0.0.1:1/wordpress@sha256:"+strings.Repeat("a", 64), 1)
	if changed.Original == source.Compose.Original {
		t.Fatal("WordPress image preparation fixture did not change exact pin")
	}
	source.Compose = &changed
	resource = storageRuntimeJSON[domain.Resource](f, "PATCH", "/api/v1/resources/"+resourceID, app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source}, 200)
	deploy := func(key, path string) domain.HostingOperation {
		options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
		if options.StorageRouteContinuity == nil {
			t.Fatal("WordPress replacement continuity missing")
		}
		input := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: key, ContainerPort: 80, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: 3}, StorageRouteContinuity: options.StorageRouteContinuity}
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", input, 202)
	}
	failed := f.await(deploy("wordpress-image-prepare-failure", "/").ID, "failed")
	if failed.MutationHold || failed.Result != nil && failed.Result.Uncertain {
		t.Fatal("WordPress image preparation did not settle safely")
	}
	for _, c := range started.Result.Stack.Components {
		if string(bytes.TrimSpace(storageRuntimeDocker(t, "inspect", c.ContainerID, "--format", "{{.State.Running}}"))) != "true" {
			t.Fatal("failed image preparation replaced writer")
		}
	}
	assertApplication()
	b.set("app_failure_recheck", resourceID)
	if b.await(t, "app_failure_checked") != proof {
		t.Fatal("WordPress preparation failure lost data")
	}
	source.Compose = installed.Resource.Source.Compose
	resource = storageRuntimeJSON[domain.Resource](f, "PATCH", "/api/v1/resources/"+resourceID, app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source}, 200)
	held := f.await(deploy("wordpress-post-mutation-failure", "/nerocd-required-missing-readiness").ID, "reconciliation_required")
	if !held.MutationHold || held.Result == nil || !held.Result.Uncertain || held.Result.Ready || held.Result.Stack == nil || !held.Result.Stack.ObservationComplete {
		t.Fatal("WordPress post-mutation failure did not retain truthful hold")
	}
	if !reflect.DeepEqual(held.Result.Stack.Volumes, started.Result.Stack.Volumes) {
		t.Fatal("WordPress held attempt changed retained volumes")
	}
	composeRuntimeWriterBarrier(t, started, held)
	// The deliberately failing readiness path must not erase real records or
	// files. Inspect only the exact observed candidate, without clearing its hold.
	var wordpressContainer string
	currentIDs := map[string]bool{}
	for _, component := range held.Result.Stack.Components {
		currentIDs[component.ContainerID] = true
		if component.Component == "wordpress" {
			wordpressContainer = component.ContainerID
		}
	}
	if wordpressContainer == "" {
		t.Fatal("held WordPress candidate missing")
	}
	ids := strings.Fields(string(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project="+held.Snapshot.RuntimeNamespace)))
	if len(ids) != len(currentIDs) {
		t.Fatal("held WordPress stack has unexpected writer count")
	}
	for _, id := range ids {
		if !currentIDs[id] {
			t.Fatal("held WordPress stack has competing writer")
		}
	}
	php := `mysqli_report(MYSQLI_REPORT_OFF); $d=new mysqli(getenv("WORDPRESS_DB_HOST"),getenv("WORDPRESS_DB_USER"),getenv("WORDPRESS_DB_PASSWORD"),getenv("WORDPRESS_DB_NAME")); if($d->connect_errno)exit(41); $r=$d->query("SELECT COUNT(*) AS n FROM wp_posts WHERE post_title='NeroCD persistent WordPress post' AND post_status='publish'"); if(!$r || (int)$r->fetch_assoc()["n"]!==1)exit(42); echo "wordpress-record-retained\n";`
	if string(storageRuntimeDocker(t, "exec", wordpressContainer, "php", "-r", php)) != "wordpress-record-retained\n" {
		t.Fatal("held WordPress database record missing")
	}
	mediaPath := strings.SplitN(proof, "|", 2)[1]
	archive := tar.NewReader(bytes.NewReader(storageRuntimeDocker(t, "cp", wordpressContainer+":/var/www/html"+mediaPath, "-")))
	if _, err := archive.Next(); err != nil {
		t.Fatal("held WordPress upload missing")
	}
	file, err := io.ReadAll(io.LimitReader(archive, 1<<20))
	if err != nil || !bytes.Equal(file, wordpressFixturePNG()) {
		t.Fatal("held WordPress upload bytes changed")
	}
	options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
	competing := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: "wordpress-competing-while-held", ContainerPort: 80, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}, StorageRouteContinuity: options.StorageRouteContinuity}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", competing, 409)
	observed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/hosting-operations/"+held.ID+"/reconcile", map[string]any{}, 202)
	if observed.ID != held.ID || !observed.MutationHold || observed.RunID != held.RunID || !reflect.DeepEqual(observed.Snapshot, held.Snapshot) {
		t.Fatal("WordPress reconciliation changed held authority")
	}
	// Read-only reconciliation is not restoration or permission to mutate.
	b.set("complete", held.ID)
	if b.await(t, "done") != resourceID {
		t.Fatal("WordPress completion resource mismatch")
	}
	finish(installed, first, second, stopped, started, failed, held)
	t.Log("WORDPRESS_CATALOG_RUNTIME_PASS")
}

func wordpressFixturePNG() []byte {
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==")
	if err != nil {
		panic("invalid fixed PNG fixture")
	}
	return raw
}

// This cheap configuration probe uses the real Linux CLI and a real Unix socket.
// It never connects to a daemon or controller and cannot execute a workload.
func TestStorageRuntimeComposeRunnerConfiguration(t *testing.T) {
	binary := os.Getenv("NEROCD_RUNNER_CONFIGURATION_PROBE_BINARY")
	if binary == "" {
		t.Skip("requires explicitly selected actual CLI configuration probe")
	}
	root, err := os.MkdirTemp("/var/lib", "ncd-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"database-state", "database-secrets"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(root, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingOwnedStorageRuntimeCapability, domain.HostingRegistryPullCapability, domain.HostingStackCapability, domain.HostingStackLifecycleCapability, domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability}
	base := []string{"runner", "--server", "https://127.0.0.1:1", "--poll-interval", "0s", "--execute", "--hosting-runtime-env", "--hosting-registry-pull", "--hosting-runtime-logs", "--capabilities", strings.Join(caps, ","), "--proxy-state-root", filepath.Join(root, "proxy"), "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", socket, "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch", "--image-docker-endpoint", "unix://" + socket}
	for _, tc := range []struct {
		name     string
		extra    []string
		expected string
	}{
		{"old_wordpress", []string{"--hosting-storage-backup-download"}, "archive downloads require qualified storage archive configuration"},
		{"wordpress_temporary_database_roots_rejected", []string{"--hosting-database", "--hosting-database-backups", "--database-state-root", "/tmp/nerocd-invalid-state", "--database-secret-root", "/tmp/nerocd-invalid-secrets"}, "database roots require persistent system storage"},
		{"wordpress_mysql_backup", []string{"--image-docker-endpoint", "unix:///var/run/docker.sock", "--hosting-database", "--hosting-database-backups", "--database-state-root", filepath.Join(root, "database-state"), "--database-secret-root", filepath.Join(root, "database-secrets")}, "runner --poll-interval must be greater than zero"},
		{"wordpress_mysql_retention", []string{"--image-docker-endpoint", "unix:///var/run/docker.sock", "--hosting-database", "--hosting-database-backups", "--hosting-database-remote-backups", "--hosting-database-backup-retention", "--database-state-root", filepath.Join(root, "database-state"), "--database-secret-root", filepath.Join(root, "database-secrets")}, "runner --poll-interval must be greater than zero"},
		{"git_compose_basic", nil, "runner --poll-interval must be greater than zero"},
		{"managed_archive_requires_config", []string{"--hosting-storage-backup-download"}, "archive downloads require qualified storage archive configuration"},
		{"native_capabilities_require_explicit_config", []string{"--capabilities", strings.Join(append(append([]string{}, caps...), domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability), ",")}, "database capabilities require --hosting-database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, append(append([]string{}, base...), tc.extra...)...)
			command.Env = append(os.Environ(), "NEROCD_TOKEN=", "NEROCD_RUNNER_CREDENTIAL_FILE=")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.expected) {
				t.Fatalf("actual CLI validation category mismatch: %s", strings.TrimSpace(string(output)))
			}
			t.Logf("actual_cli profile=%s category=%s", tc.name, tc.expected)
		})
	}
}

// Direct target reads retain the saved source Host context. This proves database
// import through WordPress, not URL migration or HTTPS on another hostname.
func wordpressRuntimeDatabaseBackupImport(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, proof string, assertSource func(), secretValues map[string]string) domain.HostingDatabaseBackupOperation {
	t.Helper()
	target, deployed := wordpressRuntimeImportTarget(t, f, resource, installed, source, "b", 1)
	silo := hostingStorageRemoteRuntimeStartSilo(&hostingStorageRemoteRuntimeFixture{storageRuntimeFixture: f})
	secretValues["remote_access"] = silo.access
	secretValues["remote_secret"] = silo.secret
	remoteTarget := storageRuntimeJSON[domain.HostingDatabaseBackupTarget](f, "POST", "/api/v1/projects/"+resource.ProjectID+"/database-backup-targets", map[string]any{"request_id": storageRemoteRuntimeRequestID(t), "name": "WordPress MySQL remote", "endpoint": "http://127.0.0.1:9000", "bucket": "nerocd-storage-runtime", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": silo.access, "secret_key": silo.secret}}, 200)
	validation := storageRuntimeJSON[domain.HostingDatabaseBackupTransfer](f, "POST", "/api/v1/projects/"+resource.ProjectID+"/database-backup-targets/"+remoteTarget.ID+"/validations", domain.HostingDatabaseBackupTargetValidationInput{RequestID: storageRemoteRuntimeRequestID(t), ExpectedRevision: remoteTarget.Revision, DestinationID: f.destinationID}, 202)
	for deadline := time.Now().Add(60 * time.Second); ; {
		validation = storageRuntimeJSON[domain.HostingDatabaseBackupTransfer](f, "GET", "/api/v1/database-backup-transfers/"+validation.ID, nil, 200)
		if validation.State == "succeeded" {
			break
		}
		if validation.State == "failed" || time.Now().After(deadline) {
			t.Fatal("WordPress MySQL remote target validation failed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	b := f.catalogBrowser
	b.mu.Lock()
	b.scheduleResource = resource.ID
	b.mu.Unlock()
	b.set("database_backup", target.Resource.ID)
	backup := composeRuntimeAwaitDatabaseBackup(t, f, b.await(t, "database_backed_up"))
	if backup.Version != "mysql.compose-logical-local.v1" || backup.SourceResourceID != resource.ID || backup.SourceComponent != "mysql" || backup.SourceDeployedOperationID != source.ID || backup.Blob == nil || backup.Blob.Format != "mysql_sql" || backup.RemoteTarget == nil || backup.RemoteTarget.TargetID != remoteTarget.ID || backup.RemoteTarget.Revision != remoteTarget.Revision {
		t.Fatal("WordPress MySQL backup identity changed")
	}
	b.mu.Lock()
	original, retried := b.scheduleOriginal, b.scheduleRetried
	requestBytes, retryBytes := bytes.Clone(b.scheduleRequest), bytes.Clone(b.scheduleRetryRequest)
	b.scheduleResource = ""
	b.mu.Unlock()
	var scheduleInput domain.HostingDatabaseBackupScheduleInput
	if original == nil || retried == nil || !reflect.DeepEqual(original, retried) || !bytes.Equal(requestBytes, retryBytes) || json.Unmarshal(requestBytes, &scheduleInput) != nil || scheduleInput.Component != "mysql" || scheduleInput.ExpectedDeployedOperationID != source.ID || scheduleInput.ExpectedResourceGeneration != resource.Generation || !scheduleInput.Enabled || scheduleInput.ExpectedVersion != 0 || scheduleInput.Frequency != "every_minute" || scheduleInput.Timezone != "UTC" || !reflect.DeepEqual(scheduleInput.RemoteTarget, backup.RemoteTarget) || !reflect.DeepEqual(original.RemoteTarget, backup.RemoteTarget) {
		t.Fatal("WordPress MySQL schedule committed retry identity changed")
	}
	var scheduleVersion, revisionVersion, state string
	var revision, occurrenceRevision, historyCount int64
	var enabled bool
	if err := f.database.QueryRow(t.Context(), `SELECT s.component_backup_version,s.revision,s.enabled,v.component_backup_version,o.schedule_revision,o.state,(SELECT count(*) FROM hosting_database_backup_schedule_versions h WHERE h.schedule_id=s.id) FROM hosting_database_backup_schedule_occurrences o JOIN hosting_database_backup_schedules s ON s.id=o.schedule_id JOIN hosting_database_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=o.schedule_revision WHERE o.operation_id=$1 AND s.id=$2`, backup.ID, original.ID).Scan(&scheduleVersion, &revision, &enabled, &revisionVersion, &occurrenceRevision, &state, &historyCount); err != nil || scheduleVersion != domain.HostingDatabaseMySQLBackupComponentVersion || revisionVersion != scheduleVersion || revision != 3 || enabled || occurrenceRevision != 1 || state != "admitted" || historyCount != 3 {
		t.Fatal("WordPress MySQL due occurrence lost stored engine/revision identity")
	}
	versions, err := f.database.Query(t.Context(), `SELECT revision,frequency,timezone,enabled,component_backup_version FROM hosting_database_backup_schedule_versions WHERE schedule_id=$1 ORDER BY revision`, original.ID)
	if err != nil {
		t.Fatal("WordPress MySQL schedule history unavailable")
	}
	for i := int64(1); i <= 3; i++ {
		var got int64
		var frequency, zone, version string
		var on bool
		if !versions.Next() || versions.Scan(&got, &frequency, &zone, &on, &version) != nil || got != i || version != scheduleVersion || on != (i != 3) || i == 1 && (frequency != "every_minute" || zone != "UTC") || i != 1 && (frequency != "* * * * *" || zone != "Etc/UTC") {
			versions.Close()
			t.Fatal("WordPress MySQL schedule edit/disable history changed")
		}
	}
	if versions.Next() || versions.Err() != nil {
		versions.Close()
		t.Fatal("WordPress MySQL schedule history count changed")
	}
	versions.Close()
	var remoteRevisions int
	if err := f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backup_schedule_remote_targets WHERE schedule_id=$1 AND target_id=$2 AND target_revision=$3`, original.ID, remoteTarget.ID, remoteTarget.Revision).Scan(&remoteRevisions); err != nil || remoteRevisions != 3 {
		t.Fatal("WordPress MySQL schedule remote target history changed", err)
	}
	t.Log("COMPOSE_MYSQL_DATABASE_SCHEDULE_PASS real controller due; immutable MySQL engine and current started deployment; identical committed retry, edit/disable/history")
	var remoteID string
	if err := f.database.QueryRow(t.Context(), `SELECT id FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup' AND plan->'operation'->'remote_target'->>'target_id'=$2 AND id<>$3`, resource.ID, remoteTarget.ID, backup.ID).Scan(&remoteID); err != nil {
		t.Fatal("WordPress MySQL immediate remote backup missing")
	}
	remoteBackup := composeRuntimeAwaitDatabaseBackup(t, f, remoteID)
	if remoteBackup.ID == backup.ID || remoteBackup.Version != domain.HostingDatabaseMySQLBackupComponentVersion || remoteBackup.SourceDeployedOperationID != source.ID || remoteBackup.RemoteTarget == nil || remoteBackup.RemoteTarget.TargetID != remoteTarget.ID || remoteBackup.RemoteTarget.Revision != remoteTarget.Revision || remoteBackup.State != "succeeded" || remoteBackup.OwnershipHeld || remoteBackup.Blob == nil || remoteBackup.Blob.Format != "mysql_sql" {
		t.Fatal("WordPress MySQL remote local publication changed")
	}
	remoteBackups := []domain.HostingDatabaseBackupOperation{backup, remoteBackup}
	failedUploads := make([]domain.HostingDatabaseBackupTransfer, len(remoteBackups))
	failedPlans := make([]domain.HostingDatabaseBackupTransferPlan, len(remoteBackups))
	for i, saved := range remoteBackups {
		uploads := storageRuntimeJSON[[]domain.HostingDatabaseBackupTransfer](f, "GET", "/api/v1/database-backups/"+saved.ID+"/transfers", nil, 200)
		if len(uploads) != 1 || uploads[0].Mode != "upload" || uploads[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion || uploads[0].State != "failed" || uploads[0].FailureCode != "access_denied" || uploads[0].Blob == nil || *uploads[0].Blob != *saved.Blob {
			t.Fatal("WordPress MySQL failed upload lost successful local archive")
		}
		failedUploads[i] = uploads[0]
		if err := f.database.QueryRow(t.Context(), `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, uploads[0].ID).Scan(&failedPlans[i]); err != nil {
			t.Fatal("WordPress MySQL failed upload plan unavailable")
		}
	}
	var dumpsBefore int
	if err := f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup'`, resource.ID).Scan(&dumpsBefore); err != nil || dumpsBefore != 2 {
		t.Fatal("WordPress MySQL backup count changed", err)
	}
	silo.policy(true, false)
	b.set("database_mutate", resource.ID)
	later := b.await(t, "database_source_mutated")
	if _, err := strconv.ParseInt(later, 10, 64); err != nil {
		t.Fatal("WordPress later source record identity invalid")
	}
	for i, saved := range remoteBackups {
		uploads := storageRuntimeJSON[[]domain.HostingDatabaseBackupTransfer](f, "GET", "/api/v1/database-backups/"+saved.ID+"/transfers", nil, 200)
		var retriedUpload domain.HostingDatabaseBackupTransfer
		for _, upload := range uploads {
			if upload.ID != failedUploads[i].ID && upload.State == "succeeded" {
				retriedUpload = upload
			}
		}
		if len(uploads) != 2 || retriedUpload.ID == "" || retriedUpload.BackupID != saved.ID || retriedUpload.Version != failedUploads[i].Version || retriedUpload.Blob == nil || *retriedUpload.Blob != *saved.Blob {
			t.Fatal("WordPress MySQL same archive retry changed")
		}
		var retryPlan domain.HostingDatabaseBackupTransferPlan
		var predecessor string
		if err := f.database.QueryRow(t.Context(), `SELECT plan,predecessor_id FROM hosting_database_backup_transfers WHERE id=$1`, retriedUpload.ID).Scan(&retryPlan, &predecessor); err != nil || predecessor != failedUploads[i].ID || retryPlan.ObjectKey != failedPlans[i].ObjectKey || retryPlan.LocationID != failedPlans[i].LocationID || retryPlan.DaemonID != failedPlans[i].DaemonID || retryPlan.Target != failedPlans[i].Target {
			t.Fatal("WordPress MySQL retry redirected saved artifact or target", err)
		}
		localAfter := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "GET", "/api/v1/database-backups/"+saved.ID, nil, 200)
		if localAfter.State != "succeeded" || localAfter.OwnershipHeld || localAfter.Blob == nil || *localAfter.Blob != *saved.Blob {
			t.Fatal("WordPress MySQL upload retry changed local success")
		}
		objectDigest := strings.Fields(string(silo.command("mc cat fixture/nerocd-storage-runtime/" + retryPlan.ObjectKey + " | sha256sum")))
		if len(objectDigest) != 2 || "sha256:"+objectDigest[0] != saved.Blob.Digest {
			t.Fatal("WordPress MySQL remote object digest changed")
		}
	}
	var dumpsAfter int
	if err := f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup'`, resource.ID).Scan(&dumpsAfter); err != nil || dumpsAfter != dumpsBefore {
		t.Fatal("WordPress MySQL upload retry admitted another dump", err)
	}
	t.Log("COMPOSE_MYSQL_DATABASE_REMOTE_SCHEDULE_PASS real due archive retains local success on upload denial; same saved schedule artifact retried without another dump")
	t.Log("COMPOSE_MYSQL_DATABASE_REMOTE_PASS immediate upload failure retains local SQL; retry publishes exact archive without second dump")
	retentionPath := "/api/v1/resources/" + resource.ID + "/database-backup-retention"
	policy := storageRuntimeJSON[*domain.HostingDatabaseBackupRetentionPolicy](f, "GET", retentionPath, nil, 200)
	if policy == nil || policy.SourceScope == nil || policy.SourceScope.BackupVersion != domain.HostingDatabaseMySQLBackupComponentVersion || policy.SourceScope.Component != "mysql" || policy.Version != domain.HostingDatabaseMySQLBackupRetentionVersion || policy.Revision != 1 || !policy.Enabled || !policy.Automatic || policy.Local.KeepCount != 1 || policy.Local.KeepDays != 0 || policy.Local.MaxBytes != 0 || policy.Remote != (domain.BackupRetentionLimits{}) {
		t.Fatal("WordPress MySQL automatic retention policy lost exact independent source authority")
	}
	publish := func(title, body string) (domain.HostingDatabaseBackupOperation, domain.HostingDatabaseBackupTransfer, string) {
		written := wordpressRuntimePost(t, source, "create", title, body)
		if written.ID < 1 || wordpressRuntimePost(t, source, "count", title, "").Count != 1 {
			t.Fatal("WordPress retention source mutation unavailable")
		}
		admitted := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "POST", "/api/v1/resources/"+resource.ID+"/database-backups", domain.HostingDatabaseBackupInput{RequestID: storageRemoteRuntimeRequestID(t), ExpectedResourceGeneration: resource.Generation, Component: "mysql", ExpectedDeployedOperationID: source.ID, RemoteTarget: backup.RemoteTarget}, 202)
		settled := composeRuntimeAwaitDatabaseBackup(t, f, admitted.ID)
		if settled.State != "succeeded" || settled.OwnershipHeld || settled.Version != domain.HostingDatabaseMySQLBackupComponentVersion || settled.SourceComponent != "mysql" || settled.SourceDeployedOperationID != source.ID || settled.Blob == nil || settled.Blob.Format != "mysql_sql" || !reflect.DeepEqual(settled.RemoteTarget, backup.RemoteTarget) {
			t.Fatal("WordPress retention publication changed source or archive identity")
		}
		local, err := os.ReadFile(filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(settled.Blob.Digest, "sha256:")))
		if err != nil || int64(len(local)) != settled.Blob.Bytes || "sha256:"+fmt.Sprintf("%x", sha256.Sum256(local)) != settled.Blob.Digest {
			t.Fatal("WordPress retention newest local publication unavailable")
		}
		clear(local)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		for {
			transfers := storageRuntimeJSON[[]domain.HostingDatabaseBackupTransfer](f, "GET", "/api/v1/database-backups/"+settled.ID+"/transfers", nil, 200)
			for _, upload := range transfers {
				if upload.Mode != "upload" {
					continue
				}
				if upload.State == "failed" || upload.State == "uncertain" {
					t.Fatal("WordPress retention publication upload did not succeed")
				}
				if upload.State == "succeeded" {
					var plan domain.HostingDatabaseBackupTransferPlan
					if upload.Blob == nil || *upload.Blob != *settled.Blob || upload.TargetID != remoteTarget.ID || upload.TargetRevision != remoteTarget.Revision || upload.Version != domain.HostingDatabaseMySQLBackupTransferVersion {
						t.Fatal("WordPress retention uploaded archive identity changed")
					}
					if err := f.database.QueryRow(t.Context(), `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, upload.ID).Scan(&plan); err != nil || plan.DaemonID != failedPlans[0].DaemonID || plan.Target != failedPlans[0].Target || plan.ObjectKey != domain.BackupRemoteObjectKey(settled.ID, *settled.Blob) {
						t.Fatal("WordPress retention publication redirected remote authority", err)
					}
					digest := strings.Fields(string(silo.command("mc cat fixture/nerocd-storage-runtime/" + plan.ObjectKey + " | sha256sum")))
					if len(digest) != 2 || "sha256:"+digest[0] != settled.Blob.Digest {
						t.Fatal("WordPress retention remote publication bytes changed")
					}
					assertSource()
					return settled, upload, plan.ObjectKey
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("WordPress retention upload did not settle")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	const mutationA = "WordPress MySQL retention mutation A"
	const mutationB = "WordPress MySQL retention mutation B"
	third, thirdUpload, thirdObject := publish(mutationA, "This record belongs to the retained remote restore snapshot")
	fourth, fourthUpload, fourthObject := publish(mutationB, "This later record must remain absent from the restored third snapshot")
	if third.ID == fourth.ID || third.Blob.Digest == fourth.Blob.Digest {
		t.Fatal("WordPress retention later mutations did not produce distinct archives")
	}
	if err := f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup'`, resource.ID).Scan(&dumpsAfter); err != nil || dumpsAfter != 4 {
		t.Fatal("WordPress retention did not publish exactly two additional dumps", err)
	}
	var automatic domain.HostingDatabaseBackupRetentionExecution
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	for automatic.ID == "" {
		executions := storageRuntimeJSON[[]domain.HostingDatabaseBackupRetentionExecution](f, "GET", retentionPath+"/executions", nil, 200)
		for _, execution := range executions {
			if execution.PolicyRevision != policy.Revision || execution.State != "succeeded" || execution.ReservationHeld || execution.LocalState != "succeeded" || execution.RemoteState != "not_selected" {
				continue
			}
			for _, item := range execution.Items {
				if item.BackupID == third.ID && item.Location == "local" && (item.State == "deleted" || item.State == "absent") {
					automatic = execution
				}
			}
		}
		if automatic.ID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("WordPress automatic retention did not delete the third local archive")
		case <-time.After(100 * time.Millisecond):
		}
	}
	linked := false
	for _, publication := range []string{"local_" + third.ID, "remote_" + thirdUpload.ID, "local_" + fourth.ID, "remote_" + fourthUpload.ID} {
		if automatic.ID != "dbre_auto_"+domain.HostingDocumentHash(publication)[7:39] {
			continue
		}
		var state string
		if err := f.database.QueryRow(t.Context(), `SELECT state FROM hosting_database_backup_retention_publications WHERE id=$1 AND resource_id=$2 AND policy_revision=$3`, publication, resource.ID, policy.Revision).Scan(&state); err != nil || state != "admitted" {
			t.Fatal("WordPress automatic retention lost real publication linkage", err)
		}
		linked = true
	}
	if !linked {
		t.Fatal("WordPress automatic retention was not reconciled from a real later publication")
	}
	cas := filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(third.Blob.Digest, "sha256:"))
	if _, err := os.Lstat(cas); !os.IsNotExist(err) {
		t.Fatal("WordPress automatic retention third local CAS remains")
	}
	if _, err := os.Stat(filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(fourth.Blob.Digest, "sha256:"))); err != nil {
		t.Fatal("WordPress automatic retention deleted newest fourth local archive")
	}
	observerTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second}
	defer observerTransport.CloseIdleConnections()
	observer := databaseRetentionObservationClient(silo.access, silo.secret, &http.Client{Transport: observerTransport, Timeout: 3 * time.Second})
	if databaseRetentionObserveRemote(t.Context(), observer, "nerocd-storage-runtime", thirdObject) != "present" || databaseRetentionObserveRemote(t.Context(), observer, "nerocd-storage-runtime", fourthObject) != "present" {
		t.Fatal("WordPress independent automatic local pruning changed surviving remote objects")
	}
	t.Log("COMPOSE_MYSQL_DATABASE_RETENTION_AUTOMATIC_PASS saved scoped automatic policy; real later publications; third local absent, remote survivor and newest fourth preserved")
	b.set("database_import", target.Resource.ID)
	imported := composeRuntimeAwaitDatabaseBackup(t, f, b.await(t, "database_imported"))
	if imported.BackupID != third.ID || imported.TargetResourceID != target.Resource.ID || imported.TargetComponent != "mysql" || imported.TargetDeployedOperationID != deployed.ID || imported.Blob == nil || *imported.Blob != *third.Blob {
		t.Fatal("WordPress import changed exact target or SQL archive")
	}
	var downloadVersion, downloadState, downloadRestore, downloadParent string
	if err := f.database.QueryRow(t.Context(), `SELECT plan->'operation'->>'version',state,restore_operation_id,predecessor_id FROM hosting_database_backup_transfers WHERE restore_operation_id=$1 AND mode='download'`, imported.ID).Scan(&downloadVersion, &downloadState, &downloadRestore, &downloadParent); err != nil || downloadVersion != domain.HostingDatabaseMySQLBackupTransferVersion || downloadState != "succeeded" || downloadRestore != imported.ID || downloadParent != thirdUpload.ID {
		t.Fatal("remote-only component import lost exact recorded successful upload", err)
	}
	t.Log("COMPOSE_MYSQL_DATABASE_REMOTE_IMPORT_PASS absent local CAS; exact recorded retained survivor upload downloaded and imported into separate target")
	parts := strings.SplitN(proof, "|", 2)
	if len(parts) != 2 {
		t.Fatal("WordPress source post proof missing")
	}
	restored := wordpressRuntimePost(t, deployed, "read", parts[0], "")
	if !strings.Contains(restored.Title, "NeroCD persistent WordPress post") || !strings.Contains(restored.Body, "NeroCD persistent WordPress body") {
		t.Fatal("WordPress snapshot post missing after import")
	}
	if wordpressRuntimePost(t, deployed, "count", "Target row before MySQL import", "").Count != 0 || wordpressRuntimePost(t, deployed, "count", "New write after compose_backup", "").Count != 1 || wordpressRuntimePost(t, deployed, "count", mutationA, "").Count != 1 || wordpressRuntimePost(t, deployed, "count", mutationB, "").Count != 0 {
		t.Fatal("WordPress import retained prior target or later source record")
	}
	written := wordpressRuntimePost(t, deployed, "create", "Independent target MySQL import write", "Only restored WordPress database contains this write")
	if written.ID < 1 || wordpressRuntimePost(t, source, "count", written.Title, "").Count != 0 {
		t.Fatal("WordPress target write reached source")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/?p=%d", deployed.Snapshot.HostPort, written.ID)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(t.Context(), "GET", endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "catalog-runtime.example.test:25443"
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("exact target HTTP unavailable")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	closed := response.Body.Close()
	if readErr != nil || closed != nil || len(body) > 2<<20 || response.StatusCode != 200 || !bytes.Contains(body, []byte(written.Title)) || !bytes.Contains(body, []byte(written.Body)) {
		t.Fatal("WordPress target HTTP did not serve independent record")
	}
	assertSource()
	externalSamples := wordpressRuntimeExternalSQLSamples()
	if len(externalSamples) != 3 || wordpressRuntimeExternalSQL(t, source, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='nerocd_external_import_proof'") != "0" || wordpressRuntimeExternalSQL(t, deployed, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='nerocd_external_import_proof'") != "0" {
		t.Fatal("external SQL fixture proof table already exists or samples unavailable")
	}
	externalIdentities := map[string]domain.HostingDatabaseImportSourceIdentity{}
	for _, sample := range externalSamples {
		silo.command("printf '%s' '" + base64.StdEncoding.EncodeToString(sample.Encoded) + "' | base64 -d | mc pipe fixture/nerocd-storage-runtime/" + sample.Key + " >/dev/null\n")
		object, err := observer.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String("nerocd-storage-runtime"), Key: aws.String(sample.Key)})
		if err != nil {
			t.Fatal("owned external SQL fixture GET unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(object.Body, int64(len(sample.Encoded))+1))
		closeErr := object.Body.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(raw, sample.Encoded) || aws.ToString(object.ETag) == "" {
			t.Fatal("owned external SQL fixture GET identity or bytes changed")
		}
		externalIdentities[sample.Key] = domain.HostingDatabaseImportSourceIdentity{ETag: aws.ToString(object.ETag), VersionID: aws.ToString(object.VersionId)}
	}
	var externalBackupsBefore, externalUploadsBefore int
	if err := f.database.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_transfers WHERE mode='upload')`).Scan(&externalBackupsBefore, &externalUploadsBefore); err != nil {
		t.Fatal("external SQL provenance baseline unavailable", err)
	}
	b.set("database_check", target.Resource.ID)
	if b.await(t, "database_checked") != parts[0] {
		t.Fatal("WordPress source browser preservation proof changed")
	}
	manualPolicy := storageRuntimeJSON[*domain.HostingDatabaseBackupRetentionPolicy](f, "GET", retentionPath, nil, 200)
	if manualPolicy == nil || manualPolicy.SourceScope == nil || !reflect.DeepEqual(manualPolicy.SourceScope, policy.SourceScope) || manualPolicy.Revision != policy.Revision+1 || !manualPolicy.Enabled || manualPolicy.Automatic || manualPolicy.Local != policy.Local || manualPolicy.Remote != (domain.BackupRetentionLimits{KeepCount: 1}) {
		t.Fatal("WordPress reviewed manual retention policy changed saved source or independent limits")
	}
	executions := storageRuntimeJSON[[]domain.HostingDatabaseBackupRetentionExecution](f, "GET", retentionPath+"/executions", nil, 200)
	var manual domain.HostingDatabaseBackupRetentionExecution
	manualCount := 0
	for _, execution := range executions {
		if execution.PolicyRevision == manualPolicy.Revision {
			manual = execution
			manualCount++
		}
	}
	if manualCount != 1 || manual.State != "succeeded" || manual.ReservationHeld || manual.RemoteState != "succeeded" || strings.HasPrefix(manual.ID, "dbre_auto_") {
		t.Fatal("WordPress reviewed execution/API replay admitted multiple batches or failed remote deletion")
	}
	var manualRows int
	if err := f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backup_retention_executions WHERE resource_id=$1 AND (body->>'policy_revision')::bigint=$2`, resource.ID, manualPolicy.Revision).Scan(&manualRows); err != nil || manualRows != 1 {
		t.Fatal("WordPress retention exact replay changed persisted batch count", err)
	}
	var preview domain.HostingDatabaseBackupRetentionPreview
	var plan domain.HostingDatabaseBackupRetentionPlan
	if err := f.database.QueryRow(t.Context(), `SELECT p.body,e.plan FROM hosting_database_backup_retention_executions e JOIN hosting_database_backup_retention_previews p ON p.id=e.preview_id WHERE e.id=$1`, manual.ID).Scan(&preview, &plan); err != nil || plan.Validate() != nil || preview.ID != manual.PreviewID || preview.PolicyRevision != manualPolicy.Revision || preview.PlanHash != manual.PlanHash || !reflect.DeepEqual(preview.SourceScope, policy.SourceScope) || plan.Operation.ID != manual.ID || len(plan.Copies) != len(manual.Items) {
		t.Fatal("WordPress retention preview/execution lost exact immutable plan", err)
	}
	known := map[string]domain.HostingDatabaseBackupOperation{backup.ID: backup, remoteBackup.ID: remoteBackup, third.ID: third, fourth.ID: fourth}
	deleted := map[string]bool{}
	for _, item := range manual.Items {
		if item.ReservationHeld || item.State != "deleted" && item.State != "absent" {
			t.Fatal("WordPress retention selected copy did not settle safely")
		}
		deleted[item.CopyID] = true
	}
	for _, copy := range plan.Copies {
		if !deleted[copy.Item.CopyID] || copy.DaemonID != failedPlans[0].DaemonID || copy.DestinationID != f.destinationID || copy.ServerID != f.serverID {
			t.Fatal("WordPress retention changed daemon, placement or exact selected copies")
		}
	}
	selectedCount, remoteDeleted := 0, 0
	newestProtected := false
	for _, item := range preview.Items {
		saved, ok := known[item.BackupID]
		if !ok || saved.Blob == nil || item.Digest != saved.Blob.Digest || item.Bytes != saved.Blob.Bytes {
			t.Fatal("WordPress retention inventory escaped owned exact archive identities")
		}
		if item.Selected {
			selectedCount++
			if len(item.ProtectedReasons) != 0 || !deleted[item.CopyID] {
				t.Fatal("WordPress retention deleted protected or unreviewed copy")
			}
		} else if deleted[item.CopyID] {
			t.Fatal("WordPress retention execution broadened reviewed selection")
		}
		for _, reason := range item.ProtectedReasons {
			if item.BackupID == fourth.ID && reason == "newest" {
				newestProtected = true
			}
		}
		if item.Location == "local" {
			path := filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(item.Digest, "sha256:"))
			if item.Selected {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("WordPress selected local copy survived manual retention")
				}
			} else if item.Availability == "available" {
				raw, err := os.ReadFile(path)
				sum := sha256.Sum256(raw)
				clear(raw)
				if err != nil || "sha256:"+fmt.Sprintf("%x", sum) != item.Digest {
					t.Fatal("WordPress protected local copy changed")
				}
			}
		} else if item.Location == "remote" && (item.Selected || item.Availability == "available") {
			observation := databaseRetentionObserveRemote(t.Context(), observer, "nerocd-storage-runtime", domain.BackupRemoteObjectKey(saved.ID, *saved.Blob))
			if item.Selected {
				remoteDeleted++
				if observation != "missing" {
					t.Fatal("WordPress selected remote copy absence unproven")
				}
			} else if observation != "present" {
				t.Fatal("WordPress protected remote copy disappeared")
			}
		}
	}
	if selectedCount != len(manual.Items) || remoteDeleted == 0 || !newestProtected {
		t.Fatal("WordPress retention exact selection or newest protection missing")
	}
	if databaseRetentionObserveRemote(t.Context(), observer, "nerocd-storage-runtime", fourthObject) != "present" {
		t.Fatal("WordPress newest remote archive unavailable for failure qualification")
	}
	assertSource()
	t.Log("COMPOSE_MYSQL_DATABASE_RETENTION_MANUAL_PASS reviewed scoped exact batch; identical API replay admitted one execution; selected physical copies deleted and actual protections/newest fourth preserved")
	wordpressRuntimeVerifyExternalSQL(t, f, target, deployed, source, externalSamples, externalIdentities, externalBackupsBefore, externalUploadsBefore, failedPlans[0].DaemonID)
	if wordpressRuntimePost(t, deployed, "read", strconv.FormatInt(written.ID, 10), "") != written {
		t.Fatal("external SQL import or rejected inspection changed unrelated target WordPress data")
	}
	assertSource()
	t.Log("COMPOSE_MYSQL_EXTERNAL_S3_IMPORT_PASS independently seeded plain and magic-detected gzip SQL; immutable receipts; application-user target imports and identical API replay; truncated gzip rejected before SQL; source and writers preserved without backup provenance")
	t.Log("COMPOSE_MYSQL_DATABASE_RETENTION_SURVIVOR_PASS third local removed by real automatic publication; exact surviving remote imported; mutation A retained and later B excluded")
	terminal := wordpressRuntimeImportFailures(t, f, resource, installed, source, fourth, fourthUpload, fourthObject, silo, assertSource)
	t.Log("COMPOSE_MYSQL_DATABASE_IMPORT_PASS snapshot target write exact HTTP source Host; original HTTPS/uploads preserved; no filesystem or URL migration")
	return terminal
}

type wordpressRuntimeExternalSQLSample struct {
	Key         string
	Encoded     []byte
	SQL         []byte
	Compression string
}

func wordpressRuntimeExternalSQLSamples() []wordpressRuntimeExternalSQLSample {
	plain := []byte("CREATE TABLE IF NOT EXISTS nerocd_external_import_proof(id INT PRIMARY KEY, marker VARCHAR(64)); INSERT INTO nerocd_external_import_proof VALUES(1,'plain') ON DUPLICATE KEY UPDATE marker='plain';\n")
	update := []byte("UPDATE nerocd_external_import_proof SET marker='gzip' WHERE id=1;\n")
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(update); err != nil {
		return nil
	}
	if err := writer.Close(); err != nil {
		return nil
	}
	gzipSQL := append([]byte(nil), encoded.Bytes()...)
	return []wordpressRuntimeExternalSQLSample{
		{Key: "nerocd/operator-import/plain.sql", Encoded: plain, SQL: plain, Compression: "plain"},
		{Key: "nerocd/operator-import/gzip.data", Encoded: gzipSQL, SQL: update, Compression: "gzip"},
		{Key: "nerocd/operator-import/broken.sql", Encoded: append([]byte(nil), gzipSQL[:len(gzipSQL)-8]...), Compression: "gzip"},
	}
}

func wordpressRuntimeExternalSQL(t *testing.T, deployed domain.HostingOperation, query string) string {
	t.Helper()
	container := wordpressRuntimeComponent(t, deployed, "mysql", true).ContainerID
	const script = `set -eu
case "$MYSQL_USER" in ''|root|*[!A-Za-z0-9_-]*) exit 64;; esac
case "$MYSQL_PASSWORD" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
case "$MYSQL_DATABASE" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
umask 077
client=$(mktemp /tmp/nerocd-external-proof.XXXXXX)
trap 'rm -f "$client"' EXIT HUP INT TERM
printf '[client]\nuser=%s\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_USER" "$MYSQL_PASSWORD" >"$client"
/usr/bin/mysql --defaults-extra-file="$client" --batch --skip-column-names --database="$MYSQL_DATABASE" -e "$1"
`
	return strings.TrimSpace(string(storageRuntimeDocker(t, "exec", container, "sh", "-c", script, "fixture", query)))
}

func wordpressRuntimeVerifyExternalSQL(t *testing.T, f *storageRuntimeFixture, installed domain.HostingCatalogInstallation, deployed, source domain.HostingOperation, samples []wordpressRuntimeExternalSQLSample, identities map[string]domain.HostingDatabaseImportSourceIdentity, backupsBefore, uploadsBefore int, daemonID string) {
	t.Helper()
	var stageCount, importCount, backupsAfter, uploadsAfter int
	if err := f.database.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_database_import_stages WHERE target_resource_id=$1),(SELECT count(*) FROM hosting_database_imports WHERE target_resource_id=$1),(SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_transfers WHERE mode='upload')`, installed.Resource.ID).Scan(&stageCount, &importCount, &backupsAfter, &uploadsAfter); err != nil || stageCount != 3 || importCount != 2 || backupsAfter != backupsBefore || uploadsAfter != uploadsBefore {
		t.Fatal("external import fabricated provenance, added a dump, or replay created a second operation", err)
	}
	for index, sample := range samples {
		var stage domain.HostingDatabaseImportStage
		var stagePlan domain.HostingDatabaseImportStagePlan
		var readyRaw []byte
		var stageRun string
		if err := f.database.QueryRow(t.Context(), `SELECT body,plan,COALESCE(ready_receipt,'null'::jsonb),COALESCE(run_id,'') FROM hosting_database_import_stages WHERE target_resource_id=$1 AND body->'s3_source'->>'object_key'=$2`, installed.Resource.ID, sample.Key).Scan(&stage, &stagePlan, &readyRaw, &stageRun); err != nil || stage.Validate() != nil || stagePlan.Validate() != nil || stage.ID != stagePlan.Stage.ID || stage.TargetResourceID != installed.Resource.ID || stage.Component != "mysql" || stage.ExpectedDeployedOperationID != deployed.ID || stage.ExpectedResourceGeneration != installed.Resource.Generation || stagePlan.Target.Release.DaemonID != daemonID || stagePlan.Target.Snapshot.DestinationID != f.destinationID || stage.S3Source.TargetID != stagePlan.S3Target.Target.ID || stage.S3Source.Revision != stagePlan.S3Target.Target.Revision || stagePlan.S3Target.Target.Bucket != "nerocd-storage-runtime" || stageRun != "db_import_stage_"+stage.ID {
			t.Fatal("external inspection lost exact source, deployment, placement or stage task", err)
		}
		read := storageRuntimeJSON[domain.HostingDatabaseImportStage](f, "GET", "/api/v1/database-import-stages/"+stage.ID, nil, 200)
		if !reflect.DeepEqual(read, stage) {
			t.Fatal("external stage GET changed persisted facts")
		}
		var stageTasks, stageAttempts int
		if err := f.database.QueryRow(t.Context(), `SELECT count(*),COALESCE(max(next_attempt-1),0) FROM task_runs WHERE id=$1 AND run_spec->>'type'=$2`, stageRun, domain.RunTypeDatabaseImportStage).Scan(&stageTasks, &stageAttempts); err != nil || stageTasks != 1 || stageAttempts != 1 {
			t.Fatal("external inspection task or execution was duplicated", err)
		}
		if index == 2 {
			var imports, held int
			if err := f.database.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE ownership_held) FROM hosting_database_imports WHERE stage_id=$1`, stage.ID).Scan(&imports, &held); err != nil || imports != 0 || held != 0 || stage.State != "failed" || stage.FailureCode != "unsupported_format" || string(readyRaw) != "null" || stage.StagedBlob != nil || stage.StageHash != "" {
				t.Fatal("truncated gzip escaped inspection into SQL ownership or import", err)
			}
			continue
		}
		var receipt domain.HostingDatabaseImportReadyReceipt
		if json.Unmarshal(readyRaw, &receipt) != nil || receipt.ValidateFor(stagePlan) != nil || stage.State != "consumed" || stage.SourceBytes != int64(len(sample.Encoded)) || stage.Compression != sample.Compression || stage.StagedBlob == nil || stage.StagedBlob.Digest != "sha256:"+fmt.Sprintf("%x", sha256.Sum256(sample.SQL)) || stage.StagedBlob.Bytes != int64(len(sample.SQL)) || stage.StagedBlob.Format != "mysql_sql" || stage.StageHash != receipt.Hash || stage.SourceIdentity == nil || !reflect.DeepEqual(*stage.SourceIdentity, identities[sample.Key]) || stage.ExpiresAt == nil || !stage.ExpiresAt.Equal(receipt.ExpiresAt) {
			t.Fatal("external immutable receipt lost actual GET identity, encoded size, decoded SQL or expiry")
		}
		var imported domain.HostingDatabaseImportOperation
		var importPlan domain.HostingDatabaseImportPlan
		var importRun string
		if err := f.database.QueryRow(t.Context(), `SELECT body,plan,COALESCE(run_id,'') FROM hosting_database_imports WHERE stage_id=$1`, stage.ID).Scan(&imported, &importPlan, &importRun); err != nil || imported.Validate() != nil || importPlan.Validate() != nil || imported.State != "succeeded" || imported.OwnershipHeld || !imported.WritersResumed || imported.TargetResourceID != installed.Resource.ID || imported.TargetComponent != "mysql" || imported.TargetDeployedOperationID != deployed.ID || importPlan.Receipt.Hash != receipt.Hash || importPlan.StagePlan.Hash != stagePlan.Hash || importPlan.Operation.ID != imported.ID || importRun != "db_import_"+imported.ID {
			t.Fatal("external SQL import failed exact receipt consumption or writer resume", err)
		}
		readImport := storageRuntimeJSON[domain.HostingDatabaseImportOperation](f, "GET", "/api/v1/database-imports/"+imported.ID, nil, 200)
		if !reflect.DeepEqual(readImport, imported) {
			t.Fatal("external import GET changed persisted facts")
		}
		var importTasks, importAttempts int
		if err := f.database.QueryRow(t.Context(), `SELECT count(*),COALESCE(max(next_attempt-1),0) FROM task_runs WHERE id=$1 AND run_spec->>'type'=$2`, importRun, domain.RunTypeDatabaseImport).Scan(&importTasks, &importAttempts); err != nil || importTasks != 1 || importAttempts != 1 {
			t.Fatal("external SQL API replay duplicated a task or execution", err)
		}
	}
	if wordpressRuntimeExternalSQL(t, deployed, "SELECT marker FROM nerocd_external_import_proof WHERE id=1") != "gzip" || wordpressRuntimeExternalSQL(t, source, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='nerocd_external_import_proof'") != "0" {
		t.Fatal("external plain creation followed by gzip update did not stay on separate target")
	}
	for _, application := range []domain.HostingOperation{source, deployed} {
		for _, component := range []string{"wordpress", "mysql"} {
			container := wordpressRuntimeComponent(t, application, component, true).ContainerID
			if strings.TrimSpace(string(storageRuntimeDocker(t, "container", "inspect", "--format", "{{.State.Running}}", container))) != "true" {
				t.Fatal("external import or rejected inspection left a source/target writer stopped")
			}
		}
	}
}

func wordpressRuntimeImportTarget(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, key string, offset int) (domain.HostingCatalogInstallation, domain.HostingOperation) {
	t.Helper()
	target := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "POST", "/api/v1/hosting-catalog/installations", domain.HostingCatalogInstallInput{RequestID: strings.Repeat(key, 32), ProjectID: resource.ProjectID, ProjectEnvironmentID: resource.ProjectEnvironmentID, DestinationID: f.destinationID, Name: "WordPress database import target " + key, TemplateID: "wordpress-with-mysql", ImmutableVersion: wordpressFixtureVersion, PublicURL: installed.Request.PublicURL}, 201)
	if target.Resource.ID == resource.ID || target.Binding.ID == installed.Binding.ID || target.Request.ImmutableVersion != wordpressFixtureVersion {
		t.Fatal("WordPress import target is not distinct")
	}
	for _, a := range installed.Credentials {
		for _, b := range target.Credentials {
			if a.Secret != nil && b.Secret != nil && a.Secret.SecretID == b.Secret.SecretID {
				t.Fatal("WordPress target reused source credentials")
			}
		}
	}
	deployed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+target.Resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: target.Resource.Generation, ExpectedRuntimeConfigurationGeneration: 1, ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, IdempotencyKey: "wordpress-component-import-target-" + key, ContainerPort: 80, HostPort: source.Snapshot.HostPort + offset, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}}, 202)
	deployed = f.await(deployed.ID, "succeeded")
	if deployed.Result == nil || deployed.Result.Stack == nil || !deployed.Result.Ready || deployed.MutationHold {
		t.Fatal("WordPress target deployment unavailable")
	}
	for _, a := range source.Result.Stack.Volumes {
		for _, b := range deployed.Result.Stack.Volumes {
			if a.VolumeName == b.VolumeName {
				t.Fatal("WordPress target shares source volume")
			}
		}
	}
	wordpressRuntimePost(t, deployed, "seed", "", "")
	return target, deployed
}

type wordpressRuntimePostResult struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Count int    `json:"count"`
}

func wordpressRuntimePost(t *testing.T, operation domain.HostingOperation, action, value, body string) wordpressRuntimePostResult {
	t.Helper()
	container := wordpressRuntimeComponent(t, operation, "wordpress", true).ContainerID
	const script = `$nerocdFixtureArgs=$argv;$_SERVER['HTTP_HOST']='catalog-runtime.example.test:25443';$_SERVER['HTTPS']='on';$_SERVER['REQUEST_URI']='/';$nerocdFixtureAction=$nerocdFixtureArgs[1];if($nerocdFixtureAction==='seed'){define('WP_INSTALLING',true);}require '/var/www/html/wp-load.php';if($nerocdFixtureAction==='seed'){if(is_blog_installed()){exit(2);}require_once ABSPATH.'wp-admin/includes/upgrade.php';add_filter('pre_wp_mail','__return_true');wp_install('Import target','targetbeforeimport','target@example.test',true,'',wp_generate_password(48));$nerocdFixtureAction='create';$nerocdFixtureArgs[2]='Target row before MySQL import';$nerocdFixtureArgs[3]='This target-only row must be replaced';}if($nerocdFixtureAction==='create'){$nerocdFixtureID=wp_insert_post(['post_title'=>$nerocdFixtureArgs[2],'post_content'=>$nerocdFixtureArgs[3],'post_status'=>'publish','post_author'=>1],true);if(is_wp_error($nerocdFixtureID)||!$nerocdFixtureID){exit(3);}$nerocdFixturePost=get_post($nerocdFixtureID);echo json_encode(['id'=>$nerocdFixturePost->ID,'title'=>$nerocdFixturePost->post_title,'body'=>$nerocdFixturePost->post_content]);}elseif($nerocdFixtureAction==='read'){$nerocdFixturePost=get_post((int)$nerocdFixtureArgs[2]);if(!$nerocdFixturePost){exit(4);}echo json_encode(['id'=>$nerocdFixturePost->ID,'title'=>$nerocdFixturePost->post_title,'body'=>$nerocdFixturePost->post_content]);}elseif($nerocdFixtureAction==='count'){$nerocdFixturePosts=get_posts(['post_type'=>'post','post_status'=>'any','title'=>$nerocdFixtureArgs[2],'numberposts'=>2]);echo json_encode(['count'=>count($nerocdFixturePosts)]);}else{exit(5);}`
	raw := storageRuntimeDocker(t, "exec", "--user", "www-data", container, "php", "-r", script, "--", action, value, body)
	var result wordpressRuntimePostResult
	if len(raw) > 8192 || json.Unmarshal(raw, &result) != nil {
		t.Fatal("WordPress core API result unavailable")
	}
	return result
}

// Failure targets are separate stacks. A held import is never reused or released
// by this fixture, and source application files are never part of the SQL dump.
func wordpressRuntimeImportFailures(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, backup domain.HostingDatabaseBackupOperation, upload domain.HostingDatabaseBackupTransfer, objectKey string, silo *hostingStorageRemoteRuntimeSilo, assertSource func()) domain.HostingDatabaseBackupOperation {
	t.Helper()
	restore := func(target domain.HostingCatalogInstallation, deployed domain.HostingOperation, key string, remote *domain.BackupRemoteSource) domain.HostingDatabaseBackupOperation {
		current := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+target.Resource.ID, nil, 200)
		in := domain.HostingDatabaseBackupRestoreInput{RequestID: strings.Repeat(key, 32), ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: deployed.ID, ExpectedResourceGeneration: current.Generation, AcknowledgeReplacement: true}, RemoteSource: remote}
		op := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", in, 202)
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
			op = storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "GET", "/api/v1/database-backups/"+op.ID, nil, 200)
			if op.State == "uncertain" || op.State == "failed" {
				if !op.OwnershipHeld || op.BackupID != backup.ID || op.TargetResourceID != target.Resource.ID || op.TargetComponent != "mysql" || op.TargetDeployedOperationID != deployed.ID || op.Blob == nil || *op.Blob != *backup.Blob {
					t.Fatal("failed MySQL import lost exact held target/archive")
				}
				in.RequestID = strings.Repeat(key, 31) + "0"
				storageRuntimeJSON[map[string]any](f, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", in, 409)
				return op
			}
			if op.State == "succeeded" {
				t.Fatal("injected MySQL import unexpectedly succeeded")
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("failed MySQL import deadline")
		return op
	}
	corruptTarget, corruptDeployment := wordpressRuntimeImportTarget(t, f, resource, installed, source, "c", 2)
	assertSource()
	cas := filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(backup.Blob.Digest, "sha256:"))
	original, err := os.ReadFile(cas)
	if err != nil || int64(len(original)) != backup.Blob.Bytes || "sha256:"+fmt.Sprintf("%x", sha256.Sum256(original)) != backup.Blob.Digest {
		t.Fatal("original component CAS unavailable")
	}
	restored := false
	restoreCAS := func() {
		if !restored {
			if err := os.WriteFile(cas, original, 0600); err != nil {
				t.Error("component CAS restoration failed")
			} else {
				restored = true
			}
		}
	}
	t.Cleanup(restoreCAS)
	if upload.BackupID != backup.ID || upload.State != "succeeded" || upload.Blob == nil || *upload.Blob != *backup.Blob || objectKey != domain.BackupRemoteObjectKey(backup.ID, *backup.Blob) {
		t.Fatal("corrupt remote import object identity changed")
	}
	if os.Remove(cas) != nil {
		t.Fatal("corrupt remote import CAS removal failed")
	}
	if _, err := os.Lstat(cas); !os.IsNotExist(err) {
		t.Fatal("corrupt remote import local CAS remains")
	}
	silo.command("printf 'corrupt-owned-fixture-object' | mc pipe fixture/nerocd-storage-runtime/" + objectKey + " >/dev/null\n")
	corrupt := restore(corruptTarget, corruptDeployment, "d", &domain.BackupRemoteSource{TransferID: upload.ID, ExpectedTargetRevision: upload.TargetRevision})
	var importRuns, intents int
	var downloadFailure string
	if err := f.database.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_database_backups WHERE id=$1 AND run_id IS NOT NULL),(SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1 AND phase='intent'),(SELECT failure_code FROM hosting_database_backup_transfers WHERE restore_operation_id=$1 AND mode='download')`, corrupt.ID).Scan(&importRuns, &intents, &downloadFailure); err != nil || importRuns != 0 || intents != 0 || downloadFailure != "object_mismatch" || corrupt.FailureCode != "execution_failed" {
		t.Fatal("corrupt remote archive escaped download barrier", err)
	}
	writer := wordpressRuntimeComponent(t, corruptDeployment, "wordpress", true)
	if strings.TrimSpace(string(storageRuntimeDocker(t, "container", "inspect", "--format", "{{.State.Running}}", writer.ContainerID))) != "true" {
		t.Fatal("pre-import remote corruption stopped target writer")
	}
	restoreCAS()
	if !restored {
		t.Fatal("component CAS restoration failed")
	}
	// No SQL reached the target, so its pre-import row must still exist.
	if wordpressRuntimePost(t, corruptDeployment, "count", "Target row before MySQL import", "").Count != 1 {
		t.Fatal("corrupt archive changed target SQL")
	}
	assertSource()
	t.Log("COMPOSE_MYSQL_CORRUPT_ARCHIVE_PASS absent CAS and corrupt recorded remote object rejected before SQL; target held with writers running")
	partialTarget, partialDeployment := wordpressRuntimeImportTarget(t, f, resource, installed, source, "e", 3)
	database := wordpressRuntimeComponent(t, partialDeployment, "mysql", true)
	// These two static names are confined to this exact newly installed target.
	// The recovery account is necessary because root cannot regrant a privilege
	// that it no longer owns. Passwords stay in the container environment/files.
	const setup = `set -eu
case "$MYSQL_ROOT_PASSWORD" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
umask 077
mkdir /tmp/nerocd-import-fault
printf '[client]\nuser=root\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_ROOT_PASSWORD" >/tmp/nerocd-import-fault/root.cnf
printf '[client]\nuser=nerocd_fixture_recovery\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_ROOT_PASSWORD" >/tmp/nerocd-import-fault/recovery.cnf
chmod 0400 /tmp/nerocd-import-fault/*.cnf
/usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM mysql.user WHERE User='nerocd_fixture_recovery'" | /usr/bin/grep -qx 0
/usr/bin/touch /tmp/nerocd-import-fault/owned
/usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf <<SQL
CREATE USER 'nerocd_fixture_recovery'@'localhost' IDENTIFIED BY '$MYSQL_ROOT_PASSWORD';
GRANT ALL PRIVILEGES ON *.* TO 'nerocd_fixture_recovery'@'localhost' WITH GRANT OPTION;
GRANT SYSTEM_USER ON *.* TO 'nerocd_fixture_recovery'@'localhost';
REVOKE CREATE ON *.* FROM 'root'@'localhost';
SQL`
	const cleanup = `set -eu
if test -f /tmp/nerocd-import-fault/owned; then
 /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf -e "GRANT CREATE ON *.* TO 'root'@'localhost'" 2>/dev/null || /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/recovery.cnf -e "GRANT CREATE ON *.* TO 'root'@'localhost'"
 /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf -e "DROP USER IF EXISTS 'nerocd_fixture_recovery'@'localhost'"
 /usr/bin/rm -- /tmp/nerocd-import-fault/root.cnf /tmp/nerocd-import-fault/recovery.cnf /tmp/nerocd-import-fault/owned
 /usr/bin/rmdir /tmp/nerocd-import-fault
fi`
	cleaned := false
	clean := func() {
		if !cleaned {
			storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "sh", "-ec", cleanup)
			cleaned = true
		}
	}
	t.Cleanup(clean)
	storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "sh", "-ec", setup)
	claimedRuns := func() []string {
		f.logs.mu.Lock()
		defer f.logs.mu.Unlock()
		var ids []string
		for _, line := range strings.Split(f.logs.String(), "\n") {
			var event struct {
				Event string `json:"event"`
				RunID string `json:"run_id"`
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Event == "claimed_run" {
				ids = append(ids, event.RunID)
			}
		}
		return ids
	}
	beforeClaims := claimedRuns()
	partial := restore(partialTarget, partialDeployment, "f", nil)
	wordpressRuntimeComponent(t, partialDeployment, "wordpress", false)
	count := storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "/usr/bin/mysql", "--defaults-extra-file=/tmp/nerocd-import-fault/root.cnf", "--batch", "--skip-column-names", "-e", "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='wordpress' AND table_name='wp_commentmeta'")
	if strings.TrimSpace(string(count)) != "0" {
		t.Fatal("partial MySQL import did not demonstrate DROP before denied CREATE")
	}
	clean()
	// Repairing fixture privileges does not release the operation or start writers.
	wordpressRuntimeComponent(t, partialDeployment, "wordpress", false)
	assertSource()
	// The deliberately uncertain import is terminal for this runner and journal.
	for deadline := time.Now().Add(10 * time.Second); ; {
		exited, category, code := storageRuntimeRunnerExit(f.process)
		if exited {
			if category != "process_exit" || code != 1 {
				t.Fatal("uncertain import runner terminal outcome changed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uncertain import runner did not exit")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.stop()
	journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		t.Fatal("held import journal unavailable")
	}
	entries := journal.Snapshot().DatabaseBackup
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	var pair []runner.JournalDatabaseBackup
	for _, entry := range entries {
		if entry.Request.OperationID == partial.ID {
			pair = append(pair, entry)
		}
	}
	if len(pair) != 2 {
		t.Fatal("held import lost intent or uncertain receipt")
	}
	intent, uncertain := pair[0], pair[1]
	if intent.Request.Phase != "intent" {
		intent, uncertain = uncertain, intent
	}
	if intent.Request.Phase != "intent" || uncertain.Request.Phase != "uncertain" || uncertain.Request.FailureCode != "execution_failed" || uncertain.Request.WritersResumed || !reflect.DeepEqual(intent.Request.Writers, uncertain.Request.Writers) || intent.Request.HostingAuthority != uncertain.Request.HostingAuthority || !reflect.DeepEqual(intent.Plan, uncertain.Plan) {
		t.Fatal("held import journal authority or writer evidence changed")
	}
	for _, entry := range pair {
		plan, authority := entry.Plan, entry.Request.HostingAuthority
		if plan.Validate() != nil || entry.Request.PlanHash != plan.Hash || authority.OperationID != partial.ID || authority.RunID != entry.Attempt.RunID || authority.LeaseID != entry.Attempt.LeaseID || authority.Attempt != entry.Attempt.Attempt || authority.Fence != entry.Attempt.Fence || plan.Operation.Version != "mysql.compose-logical-local.v1" || plan.Operation.BackupID != backup.ID || plan.Operation.TargetResourceID != partialTarget.Resource.ID || plan.Operation.TargetComponent != "mysql" || plan.Operation.TargetDeployedOperationID != partialDeployment.ID || plan.Operation.Blob == nil || *plan.Operation.Blob != *backup.Blob {
			t.Fatal("held import journal lost exact target or archive")
		}
	}
	afterClaims := claimedRuns()
	if len(afterClaims) != len(beforeClaims)+1 || !reflect.DeepEqual(beforeClaims, afterClaims[:len(beforeClaims)]) || afterClaims[len(afterClaims)-1] != uncertain.Request.RunID {
		t.Fatal("uncertain import acquired an additional run")
	}
	current := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "GET", "/api/v1/database-backups/"+partial.ID, nil, 200)
	if current.State != "uncertain" || !current.OwnershipHeld || current.TargetResourceID != partial.TargetResourceID || current.TargetDeployedOperationID != partial.TargetDeployedOperationID || current.Blob == nil || *current.Blob != *backup.Blob {
		t.Fatal("terminal runner released held import")
	}
	wordpressRuntimeComponent(t, partialDeployment, "wordpress", false)
	t.Log("WordPress MySQL terminal journal retained intent and uncertain authority; expected runner exit; no additional acquired claim")
	t.Log("COMPOSE_MYSQL_PARTIAL_IMPORT_HELD_PASS actual DROP then denied CREATE; exact target writers stopped and ownership held; source preserved")
	return current
}

func wordpressRuntimeComponent(t *testing.T, operation domain.HostingOperation, name string, running bool) domain.HostingStackComponent {
	t.Helper()
	if operation.Result == nil || operation.Result.Stack == nil || operation.Release == nil {
		t.Fatal("component operation identity absent")
	}
	for _, component := range operation.Result.Stack.Components {
		if component.Component != name {
			continue
		}
		var observed []struct {
			ID              string `json:"Id"`
			Image           string
			Config          struct{ Labels map[string]string }
			State           struct{ Running bool }
			NetworkSettings struct {
				Ports map[string][]struct{ HostIP, HostPort string }
			}
		}
		raw := storageRuntimeDocker(t, "inspect", component.ContainerID)
		if json.Unmarshal(raw, &observed) != nil || len(observed) != 1 || observed[0].ID != component.ContainerID || observed[0].Image != component.ImageID || observed[0].State.Running != running || observed[0].Config.Labels["io.nerocd.binding"] != operation.Snapshot.BindingID || observed[0].Config.Labels["io.nerocd.operation"] != component.OperationID || observed[0].Config.Labels["io.nerocd.stack.component"] != name {
			t.Fatal("exact component identity/running state changed")
		}
		if name == "wordpress" && running {
			ports := observed[0].NetworkSettings.Ports["80/tcp"]
			if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(operation.Snapshot.HostPort) {
				t.Fatal("WordPress exact target endpoint changed")
			}
		}
		return component
	}
	t.Fatal("expected component absent")
	return domain.HostingStackComponent{}
}

func TestWordPressComponentBackupEnrollment(t *testing.T) {
	t.Setenv("NEROCD_CATALOG_BROWSER_ORIGIN", "https://127.0.0.1:25444")
	_, pg, _ := newHostingBuildAPIFixture(t)
	server := catalogRuntimeServer(t, pg)
	control := httptest.NewTLSServer(server)
	t.Cleanup(control.Close)
	f := &storageRuntimeFixture{t: t, control: control, client: control.Client()}
	f.token = createTestSession(t, server)
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingOwnedStorageRuntimeCapability, domain.HostingRegistryPullCapability, domain.HostingStackCapability, domain.HostingStackLifecycleCapability, domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability, domain.HostingDatabaseBackupComponentCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseMySQLBackupCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupComponentCapability}
	created := storageRuntimeJSON[app.CreatedRunnerEnrollment](f, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_wordpress_backup", RunnerName: "WordPress component backup", Capabilities: caps}, 201)
	credential := "ncr_wordpress_component_backup_fixture"
	consume := databaseRunnerEnrollmentConsumeInput(credential)
	raw, err := json.Marshal(consume)
	var decoded app.RunnerEnrollmentConsumeInput
	if err != nil || !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/runner-enrollments/consume", bytes.NewReader(raw)), &decoded) || !reflect.DeepEqual(decoded, consume) {
		t.Fatal("actual enrollment consume fixture decoder changed")
	}
	enrolled := storageRuntimeAuthorizedJSON[app.ConsumedRunnerEnrollment](f, created.Token, "POST", "/api/v1/runner-enrollments/consume", decoded, 200)
	client := runnerclient.New(control.URL, credential)
	client.HTTP = control.Client()
	id, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: true, RuntimeEnvironment: true, Proxy: true, ProxyVerifiedSwitch: true, RegistryPull: true, RuntimeLogs: true})
	if err != nil || id != enrolled.Runner.ID {
		t.Fatal("actual WordPress backup enrollment/profile rejected")
	}
	if _, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{}); err == nil {
		t.Fatal("component backup capabilities accepted without configured execution")
	}
}
