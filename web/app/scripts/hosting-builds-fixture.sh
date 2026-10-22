#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"
umask 077
fixture_dir=$(mktemp -d /tmp/nerocd-hosting-ui.XXXXXXXX)
chmod 0700 "$fixture_dir"
suffix=$(openssl rand -hex 6)
fixture_label="io.nerocd.browser.fixture=$suffix"
browser_pid=''
image_id_a=''
image_id_b=''
image_ref_b=''
image_tag_b=''
tag_a="nerocd-hosting-ui-a:$suffix"
tag_b="nerocd-hosting-ui-b:$suffix"
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
# Trusted admission-only policy: no fixture agent executes a build with these
# documented example identities. All files remain under the owner-only fixture.
python3 -c 'import pathlib,sys; text=pathlib.Path("docs/hosting-build-control.md").read_text(); policy=text.split("```json\n",1)[1].split("```",1)[0]; pathlib.Path(sys.argv[1]).write_text(policy)' "$fixture_dir/build-policy.json"
chmod 600 "$fixture_dir/build-policy.json"
export NEROCD_HOSTING_BUILD_POLICY_FILE="$fixture_dir/build-policy.json"
# Isolate the browser/fixture descendants in a newly created session. Only this
# captured process group is signaled during cleanup; the browser server retains
# its own existing cleanup for PostgreSQL and API/OIDC child PIDs.
python3 -c 'import os; os.setsid(); os.execvp("bun", ["bun", "run", "--cwd", "web/app", "smoke:browser", "hosting-builds.spec.ts", "hosting-deployments.spec.ts", "hosting-git-resources.spec.ts"])' &
browser_pid=$!
printf 'hosting UI browser process group: %s\n' "$browser_pid"
wait "$browser_pid"
