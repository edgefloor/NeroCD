#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"
umask 077
fixture_dir=$(mktemp -d /tmp/nerocd-proxy-ui.XXXXXXXX)
chmod 0700 "$fixture_dir"
suffix=$(openssl rand -hex 6)
fixture_label="io.nerocd.browser.fixture=$suffix"
browser_pid=''
proxy_fixture_id=''
image_id_a=''
image_id_b=''
image_ref_b=''
image_tag_b=''
tag_a="nerocd-proxy-ui-a:$suffix"
tag_b="nerocd-proxy-ui-b:$suffix"
source "$root/scripts/local-image-registry.sh"
# The accepted helper owns an exact registry ID. Add this wrapper's ownership
# label at creation without changing the shared helper or its pinned image.
docker() {
  if [[ "${1:-}" == run ]]; then
    shift
    command docker run --label "$fixture_label" "$@"
  else command docker "$@"; fi
}
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [[ -n "$browser_pid" ]]; then
    kill -TERM -- "-$browser_pid" 2>/dev/null || true
    wait "$browser_pid" 2>/dev/null || true
  fi
  for tool_cid_file in "$fixture_dir/build-test.cid" "$fixture_dir/build-cli.cid"; do
    if [[ -f "$tool_cid_file" ]]; then
      tool_cid=$(cat "$tool_cid_file")
      if [[ "$tool_cid" =~ ^[a-f0-9]{64}$ ]] && tool_identity=$(command docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.proxy.browser.build"}}' "$tool_cid" 2>/dev/null); then
        if [[ "$tool_identity" == "$tool_cid $suffix" ]]; then command docker rm -f "$tool_cid" >/dev/null || status=1
        else printf '%s\n' 'build container ownership changed; preserving it' >&2; status=1; fi
      fi
    fi
  done
  if [[ -n "$proxy_fixture_id" ]]; then
    identity=$(command docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.proxy.browser"}}' "$proxy_fixture_id" 2>/dev/null || true)
    if [[ "$identity" == "$proxy_fixture_id $suffix" ]]; then
      command docker rm -f -v "$proxy_fixture_id" >/dev/null || status=1
      printf 'removed owned proxy fixture id=%s label=%s\n' "$proxy_fixture_id" "$suffix"
    else printf '%s\n' 'proxy fixture ownership changed; preserving it' >&2; status=1; fi
  fi
  # Each row was captured from this test's accepted operation response. Verify
  # the exact ID and both workload ownership labels before removing anything.
  if [[ -f "$fixture_dir/operations" ]]; then
    while IFS=' ' read -r binding operation; do
      [[ "$binding" =~ ^binding_[a-zA-Z0-9_-]+$ && "$operation" =~ ^hop_[a-zA-Z0-9_-]+$ ]] || { status=1; continue; }
      while IFS= read -r container; do
        [[ "$container" =~ ^[a-f0-9]{64}$ ]] || continue
        actual=$(command docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.binding"}} {{index .Config.Labels "io.nerocd.operation"}}' "$container" 2>/dev/null) || continue
        if [[ "$actual" == "$container $binding $operation" ]]; then command docker rm -f "$container" >/dev/null || status=1; fi
      done < <(command docker ps -aq --no-trunc --filter "label=io.nerocd.binding=$binding" --filter "label=io.nerocd.operation=$operation")
    done < "$fixture_dir/operations"
  fi
  if [[ -n "$local_registry_container_id" ]]; then
    actual=$(command docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.browser.fixture"}}' "$local_registry_container_id" 2>/dev/null || true)
    if [[ "$actual" != "$local_registry_container_id $suffix" ]]; then
      printf '%s\n' 'registry ownership check failed; preserving resources' >&2
      local_registry_container_id=''; local_registry_image_ref=''; local_registry_source_tag=''; local_registry_tag=''; status=1
    fi
  fi
  for reference_variable in local_registry_image_ref local_registry_tag local_registry_source_tag; do
    reference=${!reference_variable}
    if [[ -n "$reference" ]] && command docker image inspect "$reference" >/dev/null 2>&1; then
      actual=$(command docker image inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.browser.fixture"}}' "$reference")
      if [[ "$actual" != "$image_id_a $suffix" ]]; then
        printf '%s\n' 'fixture image ownership changed; preserving reference' >&2
        printf -v "$reference_variable" '%s' ''; status=1
      fi
    fi
  done
  # Non-forced image removal preserves references still in use by other work.
  for reference in "$image_ref_b" "$image_tag_b"; do
    if [[ -n "$reference" ]] && command docker image inspect "$reference" >/dev/null 2>&1; then
      actual=$(command docker image inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.browser.fixture"}}' "$reference")
      if [[ "$actual" == "$image_id_b $suffix" ]]; then command docker image rm "$reference" >/dev/null || status=1
      else printf '%s\n' 'fixture B reference ownership changed; preserving reference' >&2; status=1; fi
    fi
  done
  local_registry_cleanup || status=1
  for source_tag in "$tag_a" "$tag_b"; do
    if [[ "$source_tag" == "$tag_a" ]]; then expected_image=$image_id_a; else expected_image=$image_id_b; fi
    if command docker image inspect "$source_tag" >/dev/null 2>&1; then
      actual=$(command docker image inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.browser.fixture"}}' "$source_tag")
      if [[ "$actual" == "$expected_image $suffix" ]]; then command docker image rm "$source_tag" >/dev/null || status=1
      else printf '%s\n' 'fixture source image ownership changed; preserving reference' >&2; status=1; fi
    fi
  done
  rm -rf -- "$fixture_dir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
arch=$(command docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) exit 1 ;; esac
cp acceptance/image-hosting/fixture/Dockerfile "$fixture_dir/Dockerfile"
for version in A B; do
  env CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$fixture_dir/server" ./acceptance/image-hosting/fixture
  if [[ "$version" == A ]]; then tag=$tag_a; else tag=$tag_b; fi
  command docker build --quiet --label "$fixture_label" --tag "$tag" "$fixture_dir"
  if [[ "$version" == A ]]; then image_id_a=$(command docker image inspect --format '{{.Id}}' "$tag")
  else image_id_b=$(command docker image inspect --format '{{.Id}}' "$tag"); fi
done
local_registry_publish "$tag_a" "$suffix"
image_tag_b="${local_registry_tag%:candidate}:b"
command docker tag "$tag_b" "$image_tag_b"
command docker push "$image_tag_b" >/dev/null
image_ref_b=$(command docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image_tag_b" | head -n 1)
[[ "$image_ref_b" == "${local_registry_tag%:candidate}@sha256:"* ]] || exit 1
export NEROCD_IMAGE_TEST_TAG="$local_registry_tag"
export NEROCD_IMAGE_TEST_A="$local_registry_image_ref"
export NEROCD_IMAGE_TEST_B="$image_ref_b"
export NEROCD_HOSTING_UI_FIXTURE_DIR="$fixture_dir"
if [[ -n "${DOCKER_HOST:-}" ]]; then image_endpoint=$DOCKER_HOST
else image_endpoint=$(command docker context inspect --format '{{(index .Endpoints "docker").Host}}'); fi
[[ "$image_endpoint" == unix:///* ]] || { printf '%s\n' 'fixture requires a local Docker Unix socket' >&2; exit 1; }
export NEROCD_HOSTING_UI_DOCKER_ENDPOINT="$image_endpoint"
# Give this disposable server an operator-controlled range distinct from the
# default range used by other test databases. Probe availability without touching
# foreign containers; admission/runtime still fail closed if a later race occurs.
auto_port_min=$(node --input-type=module -e '
import { createServer } from "node:net";
import { randomInt } from "node:crypto";
for (let attempt = 0; attempt < 32; attempt++) {
  const first = randomInt(35000, 55000);
  const listeners = [];
  try {
    for (let offset = 0; offset < 8; offset++) {
      const listener = createServer();
      await new Promise((resolve, reject) => { listener.once("error", reject); listener.listen(first + offset, "127.0.0.1", resolve); });
      listeners.push(listener);
    }
    process.stdout.write(String(first));
    break;
  } catch { if (attempt === 31) process.exitCode = 1; }
  finally { await Promise.all(listeners.map((listener) => new Promise((resolve) => listener.close(resolve)))); }
}
')
[[ "$auto_port_min" =~ ^[0-9]{5}$ ]] || exit 1
export NEROCD_HOSTING_AUTO_PORT_MIN="$auto_port_min"
export NEROCD_HOSTING_AUTO_PORT_MAX="$((auto_port_min + 7))"
# Separate Linux Docker namespace for real proxy ports, DNS and test-only ACME.
# The overlay only adds this temporary tagged test harness; production sources,
# CLI trust flags and central generated contracts remain unchanged.
python3 -c 'import pathlib,sys; pathlib.Path(sys.argv[1]).write_text(r"""//go:build linux && nerocd_proxy_fixture
package runnerruntime
import (
 "context"
 "crypto/tls"
 "crypto/x509"
 "io"
 "net/http"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "testing"
 "time"
 "nerocd/internal/domain"
 "nerocd/internal/hostingproxy"
 "nerocd/internal/runnerclient"
)
func TestProxyBrowserHarness(t *testing.T) {
 root := "/var/lib/proxy-ui"
 if err := os.MkdirAll(root, 0700); err != nil { t.Fatal(err) }
 if os.Getenv("NEROCD_PROXY_BROWSER_ACTION") == "setup" {
  roots, _ := controlTrust(t, root)
  client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, Timeout: 5*time.Second}
  response, err := client.Get("https://localhost:15000/roots/0"); if err != nil { t.Fatal(err) }; defer response.Body.Close()
  issued, err := io.ReadAll(io.LimitReader(response.Body, 1<<20)); if err != nil || response.StatusCode != 200 { t.Fatal("fixture issuance root unavailable") }
  if err := os.WriteFile(filepath.Join(root,"issuance.pem"), issued, 0600); err != nil { t.Fatal(err) }
  if err := os.WriteFile(filepath.Join(root,"ready"), []byte("ready"), 0600); err != nil { t.Fatal(err) }
  <-t.Context().Done(); return
 }
 directory := os.Getenv("NEROCD_PROXY_BROWSER_AGENT")
 if !strings.HasPrefix(directory,"/var/lib/agents/agent-") || filepath.Clean(directory) != directory { t.Fatal("invalid owned agent directory") }
 server := os.Getenv("NEROCD_PROXY_BROWSER_SERVER")
 credentialFile := filepath.Join(directory,"runner.credential")
 if _, err := os.Stat(credentialFile); os.IsNotExist(err) {
  args := []string{"runner", "--server",server,"--enrollment-file",filepath.Join(directory,"enrollment.token"),"--credential-file",credentialFile,"--work-dir",filepath.Join(directory,"work"),"--journal-dir",filepath.Join(directory,"journal"),"--once","--execute=true","--proxy-state-root",filepath.Join(directory,"proxy"),"--proxy-docker-binary","/usr/local/bin/docker","--proxy-docker-socket","/var/run/docker.sock","--proxy-protocol",domain.HostingProxyCompilerVersion}
  command := exec.CommandContext(t.Context(),"/tmp/nerocd",args...)
  command.Env = append(os.Environ(), "NEROCD_TOKEN=", "NEROCD_MODE=development")
  if output, err := command.CombinedOutput(); err != nil { _ = os.WriteFile(filepath.Join(directory,"enrollment-error"),output,0600); t.Fatal("real CLI enrollment failed; protected output withheld") }
  return
 }
 credential, err := os.ReadFile(credentialFile); if err != nil { t.Fatal("credential file unavailable") }
 token := strings.TrimSpace(string(credential))
 identity, err := runnerclient.New(server,token).ProxyIdentity(context.Background()); if err != nil { t.Fatal("configured agent identity failed") }
 roots := x509.NewCertPool()
 for _, name := range []string{"minica.pem","issuance.pem"} { pem, err := os.ReadFile(filepath.Join(root,name)); if err != nil || !roots.AppendCertsFromPEM(pem) { t.Fatal("fixture CA unavailable") } }
 publicRoot, err := os.ReadFile(filepath.Join(root,"minica.pem")); if err != nil { t.Fatal(err) }
 config := ProxyConfig{Directory:filepath.Join(directory,"proxy"),DockerBinary:"/usr/local/bin/docker",DockerSocket:"/var/run/docker.sock",Protocol:domain.HostingProxyCompilerVersion,engineFactory:func(settings hostingproxy.Settings)(managedProxyEngine,error){
  if err := os.MkdirAll(filepath.Join(settings.Directory,"certificates"),0700); err != nil { return nil,err }
  if err := os.WriteFile(filepath.Join(settings.Directory,"certificates/root.pem"),publicRoot,0600); err != nil { return nil,err }
  return hostingproxy.NewFixture(settings,"https://localhost:14000/dir",roots,[]string{"--env","LEGO_CA_CERTIFICATES=/certificates/root.pem"})
 }}
 if err := Run(Config{Proxy:config,ImageDockerEndpoint:"unix:///var/run/docker.sock",Server:server,Token:token,RunnerID:identity,WorkDir:filepath.Join(directory,"work"),JournalDir:filepath.Join(directory,"journal"),ImagePolicy:"pull",Execute:true,Once:true,CancelPollInterval:100*time.Millisecond}); err != nil { t.Fatal(err) }
}
""")' "$fixture_dir/proxy_browser_test.go"
python3 -c 'import json,sys; json.dump({"Replace":{"/workspace/internal/runnerruntime/hosting_proxy_browser_fixture_test.go":"/output/proxy_browser_test.go"}},open(sys.argv[1],"w"))' "$fixture_dir/overlay.json"
modules=$(go env GOMODCACHE)
mkdir -p "$fixture_dir/cache"
tool_image='golang@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af'
command docker run --rm --cidfile "$fixture_dir/build-test.cid" --label "io.nerocd.proxy.browser.build=$suffix" --network none --workdir /workspace --env GOTOOLCHAIN=local --env GOMODCACHE=/modules --env GOCACHE=/output/cache --env CGO_ENABLED=0 --mount "type=bind,src=$root,dst=/workspace,readonly" --mount "type=bind,src=$modules,dst=/modules,readonly" --mount "type=bind,src=$fixture_dir,dst=/output" "$tool_image" go test -tags netgo,osusergo,nerocd_proxy_fixture -overlay /output/overlay.json -c -o /output/proxy-browser.test ./internal/runnerruntime
command docker run --rm --cidfile "$fixture_dir/build-cli.cid" --label "io.nerocd.proxy.browser.build=$suffix" --network none --workdir /workspace --env GOTOOLCHAIN=local --env GOMODCACHE=/modules --env GOCACHE=/output/cache --env CGO_ENABLED=0 --mount "type=bind,src=$root,dst=/workspace,readonly" --mount "type=bind,src=$modules,dst=/modules,readonly" --mount "type=bind,src=$fixture_dir,dst=/output" "$tool_image" go build -tags netgo,osusergo -o /output/nerocd-linux ./cmd/nerocd
proxy_fixture_id=$(command docker run --detach --privileged --label "io.nerocd.proxy.browser=$suffix" --env DOCKER_TLS_CERTDIR= -p 127.0.0.1::80 -p 127.0.0.1::443 --mount "type=bind,src=$root,dst=/workspace,readonly" docker.io/library/docker@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 --host=unix:///var/run/docker.sock)
export NEROCD_PROXY_BROWSER_CONTAINER="$proxy_fixture_id"
printf 'owned proxy browser fixture id=%s label=%s\n' "$proxy_fixture_id" "$suffix"
for attempt in {1..60}; do if command docker exec "$proxy_fixture_id" docker info >/dev/null 2>&1; then break; fi; sleep 1; done
command docker exec "$proxy_fixture_id" docker info >/dev/null
command docker exec -i "$proxy_fixture_id" sh -c 'cat > /tmp/proxy-browser.test; chmod 700 /tmp/proxy-browser.test' < "$fixture_dir/proxy-browser.test"
command docker exec -i "$proxy_fixture_id" sh -c 'cat > /tmp/nerocd; chmod 700 /tmp/nerocd' < "$fixture_dir/nerocd-linux"
command docker exec "$proxy_fixture_id" mkdir -p /var/lib/agents
command docker exec "$proxy_fixture_id" chmod 700 /tmp/proxy-browser.test /tmp/nerocd
command docker exec "$proxy_fixture_id" docker pull ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199
command docker exec --detach --env NEROCD_PROXY_BROWSER_ACTION=setup "$proxy_fixture_id" /tmp/proxy-browser.test -test.run '^TestProxyBrowserHarness$' -test.timeout 1200s
for attempt in {1..60}; do if command docker exec "$proxy_fixture_id" test -f /var/lib/proxy-ui/ready; then break; fi; sleep 1; done
command docker exec "$proxy_fixture_id" test -f /var/lib/proxy-ui/ready
command docker exec "$proxy_fixture_id" cat /var/lib/proxy-ui/issuance.pem > "$fixture_dir/proxy-issuance.pem"
export NEROCD_PROXY_BROWSER_HTTP_PORT=$(command docker port "$proxy_fixture_id" 80/tcp | sed 's/.*://')
export NEROCD_PROXY_BROWSER_HTTPS_PORT=$(command docker port "$proxy_fixture_id" 443/tcp | sed 's/.*://')
# Load the already owned scratch image into only this disposable daemon.
command docker pull "$local_registry_tag" >/dev/null
command docker tag "$local_registry_tag" "$tag_a"
command docker save "$tag_a" | command docker exec -i "$proxy_fixture_id" docker load >/dev/null
command docker exec "$proxy_fixture_id" docker tag "$tag_a" proxy-browser-a:fixture
# A private registry makes image pull policy exercise the real image path.
command docker exec "$proxy_fixture_id" docker run -d --name proxy-browser-registry --network host -e REGISTRY_HTTP_ADDR=127.0.0.1:25000 registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
command docker exec "$proxy_fixture_id" docker tag proxy-browser-a:fixture 127.0.0.1:25000/proxy-browser-a:fixture
command docker exec "$proxy_fixture_id" docker push 127.0.0.1:25000/proxy-browser-a:fixture
export NEROCD_PROXY_IMAGE_A=127.0.0.1:25000/proxy-browser-a:fixture
# Isolate the browser/fixture descendants in a newly created session. Only this
# captured process group is signaled during cleanup; the browser server retains
# its own existing cleanup for PostgreSQL and API/OIDC child PIDs.
python3 -c 'import os; os.setsid(); os.execvp("bun", ["bun", "run", "--cwd", "web/app", "smoke:browser", "hosting-proxy.spec.ts"])' &
browser_pid=$!
printf 'proxy UI browser process group: %s\n' "$browser_pid"
wait "$browser_pid"
