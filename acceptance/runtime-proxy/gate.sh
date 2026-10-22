#!/usr/bin/env bash
# Disposable Linux namespace, never publish ports or attach user workloads.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd -P)
cd "$root"
name="nerocd-proxy-fixture-$$"
fixture_id=''
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/nerocd-proxy-build.XXXXXX")
cleanup() {
  if [[ -n "$fixture_id" ]]; then
    label=$(docker inspect --format '{{index .Config.Labels "io.nerocd.fixture"}}' "$fixture_id" 2>/dev/null || true)
    if [[ "$label" == "$name" ]]; then docker rm -f -v "$fixture_id" >/dev/null; fi
  fi
  rm -rf "$build_dir"
}
trap cleanup EXIT INT TERM
# Public immutable tooling images. The module cache is read-only and networking is disabled for compilation.
modules=$(go env GOMODCACHE)
docker run --rm --network none --workdir "$root" --env GOTOOLCHAIN=local --env GOMODCACHE=/modules \
  --mount "type=bind,src=$root,dst=$root,readonly" \
  --mount "type=bind,src=$modules,dst=/modules,readonly" \
  --mount "type=bind,src=$build_dir,dst=/output" \
  golang@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af \
  go test -race -tags netgo,osusergo -ldflags "-linkmode external -extldflags -static" -c -o /output/proxy.test ./internal/hostingproxy
fixture_id=$(docker run --detach --privileged --name "$name" --label "io.nerocd.fixture=$name" \
  --env DOCKER_TLS_CERTDIR= --mount "type=bind,src=$root,dst=/workspace,readonly" \
  docker.io/library/docker@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 \
  --host=unix:///var/run/docker.sock)
for attempt in {1..60}; do
  if docker exec "$fixture_id" docker --host unix:///var/run/docker.sock info >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$fixture_id" docker --host unix:///var/run/docker.sock info --format '{{.ID}} {{.OSType}} {{.Architecture}}'
docker exec "$fixture_id" docker --host unix:///var/run/docker.sock pull \
  ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199
# DinD mounts /tmp after creation. Transfer through exec into that live mount namespace.
docker exec -i "$fixture_id" sh -c 'cat > /tmp/proxy.test && chmod 700 /tmp/proxy.test' < "$build_dir/proxy.test"
docker exec --env NEROCD_PROXY_FIXTURE=1 "$fixture_id" /tmp/proxy.test -test.v -test.timeout 240s
