#!/usr/bin/env bash
# All host-network ports belong to this disposable Linux daemon's namespace.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd -P)
cd "$root"
name="nerocd-proxy-control-runtime-$$"
fixture_id=''
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/nerocd-proxy-control-build.XXXXXX")
mkdir -p "$build_dir/cache"
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ -n "$fixture_id" ]]; then
    label=$(docker inspect --format '{{index .Config.Labels "io.nerocd.fixture"}}' "$fixture_id" 2>/dev/null || true)
    if [[ "$label" == "$name" ]]; then
      if docker rm -f -v "$fixture_id" >/dev/null; then
        printf 'removed owned fixture id=%s label=%s\n' "$fixture_id" "$name"
        if docker inspect "$fixture_id" >/dev/null 2>&1; then status=1; fi
      else status=1; fi
    else
      printf 'owned fixture identity changed; cleanup refused\n' >&2
      status=1
    fi
  fi
  rm -rf "$build_dir"
  printf 'proxy control fixture cleanup exit=%s\n' "$status"
  exit "$status"
}
trap cleanup EXIT INT TERM
modules=$(go env GOMODCACHE)
tool_image='golang@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af'
docker run --rm --network none --workdir /workspace --env GOTOOLCHAIN=local --env GOMODCACHE=/modules --env GOCACHE=/output/cache \
  --mount "type=bind,src=$root,dst=/workspace,readonly" --mount "type=bind,src=$modules,dst=/modules,readonly" \
  --mount "type=bind,src=$build_dir,dst=/output" "$tool_image" \
  go test -race -tags netgo,osusergo,nerocd_proxy_fixture -ldflags '-linkmode external -extldflags -static' -c -o /output/proxy-control.test ./internal/runnerruntime
for version in A B; do
  docker run --rm --network none --workdir /workspace --env GOTOOLCHAIN=local --env GOMODCACHE=/modules --env GOCACHE=/output/cache --env CGO_ENABLED=0 \
    --mount "type=bind,src=$root,dst=/workspace,readonly" --mount "type=bind,src=$modules,dst=/modules,readonly" \
    --mount "type=bind,src=$build_dir,dst=/output" "$tool_image" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "/output/backend-$version" ./acceptance/runtime-proxy-control/backend
done
fixture_id=$(docker run --detach --privileged --name "$name" --label "io.nerocd.fixture=$name" \
  --env DOCKER_TLS_CERTDIR= --mount "type=bind,src=$root,dst=/workspace,readonly" \
  docker.io/library/docker@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 --host=unix:///var/run/docker.sock)
printf 'owned fixture id=%s label=%s published_ports=none\n' "$fixture_id" "$name"
for attempt in {1..60}; do
  if docker exec "$fixture_id" docker --host unix:///var/run/docker.sock info >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$fixture_id" docker --host unix:///var/run/docker.sock info --format '{{.ID}} {{.OSType}} {{.Architecture}}'
for image in \
  'postgres@sha256:b8faeff7712663c2773a190181e1246e37e364b887611c04658c28b44e2408fe' \
  'ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199' \
  'registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373'; do
  docker exec "$fixture_id" docker --host unix:///var/run/docker.sock pull "$image"
done
docker exec "$fixture_id" sh -c 'mkdir -p /var/lib/proxy-build; printf "FROM scratch\nCOPY server /server\nUSER 65532:65532\nENTRYPOINT [\"/server\"]\n" > /var/lib/proxy-build/Dockerfile'
docker exec "$fixture_id" docker run -d --name fixture-registry --network host -e REGISTRY_HTTP_ADDR=127.0.0.1:25000 \
  'registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373'
for version in A B; do
  if [[ "$version" == A ]]; then repository=backend-a; else repository=backend-b; fi
  docker exec -i "$fixture_id" sh -c 'cat > /var/lib/proxy-build/server; chmod 755 /var/lib/proxy-build/server' < "$build_dir/backend-$version"
  docker exec --env DOCKER_BUILDKIT=0 "$fixture_id" docker build --quiet -t "127.0.0.1:25000/$repository:fixture" /var/lib/proxy-build
  docker exec "$fixture_id" docker push "127.0.0.1:25000/$repository:fixture"
done
docker exec "$fixture_id" docker run -d --name fixture-postgres --network host \
  -e POSTGRES_PASSWORD=proxy-fixture-only -e POSTGRES_DB=proxy_fixture \
  'postgres@sha256:b8faeff7712663c2773a190181e1246e37e364b887611c04658c28b44e2408fe'
docker exec -i "$fixture_id" sh -c 'cat > /tmp/proxy-control.test; chmod 700 /tmp/proxy-control.test' < "$build_dir/proxy-control.test"
docker exec --env NEROCD_PROXY_CONTROL_FIXTURE=disposable-dind "$fixture_id" /tmp/proxy-control.test \
  -test.run '^TestProxyRuntimeCombined$' -test.v -test.timeout 600s
