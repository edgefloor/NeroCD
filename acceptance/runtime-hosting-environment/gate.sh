#!/usr/bin/env bash
# Owned disposable DinD. The allocated PostgreSQL container is inspected only;
# the Go fixture creates and drops its own schema and preserves existing schemas.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/runnerruntime/hosting_runtime_environment.go"
pg_id=e63593f54627a4de76038d238a85b145fddf65d026cb472711c66994c5840aa1
[[ $(docker inspect --format '{{.Id}} {{index .Config.Labels "nerocd.task"}}' "$pg_id") == "$pg_id hosting-build-deployment-20261022" ]]
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) goarch=arm64; buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a;; x86_64|amd64) goarch=amd64; buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4;; *) exit 1;; esac
fixture_dir=$(mktemp -d /tmp/nerocd-runtime-hosting-environment.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.runtime-hosting-environment.owner
name="$owner-dind"
image="$owner:fixture"
container_id=''
image_id=''
cleanup(){
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   if ! docker exec "$container_id" sh -ec 'if [ -d "$1" ]; then rmdir "$1"; fi' sh "/sys/fs/cgroup/$owner"; then result=1; fi
   if ! docker rm --force --volumes "$container_id" >/dev/null; then result=1; fi
   if docker inspect "$container_id" >/dev/null 2>&1; then result=1; else printf 'owned container removed: %s\n' "$container_id"; fi
  else echo 'owned container identity changed' >&2; result=1; fi
 fi
 if [[ -n "$image_id" ]]; then
  if [[ $(docker image inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$image" 2>/dev/null || true) == "$image_id $owner" ]]; then
   if ! docker image rm "$image" >/dev/null; then result=1; fi
  else result=1; fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
curl --fail --silent --show-error --location "https://github.com/docker/buildx/releases/download/v0.38.1/buildx-v0.38.1.linux-$goarch" --output "$fixture_dir/buildx"
printf '%s  %s\n' "$buildx_sha" "$fixture_dir/buildx" | shasum -a 256 --check
chmod 0755 "$fixture_dir/buildx"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/probe" ./acceptance/runtime-hosting-environment/probe
shasum -a 256 "$fixture_dir/nerocd" "$fixture_dir/api.test"
cp db/seeds/dev.sql "$fixture_dir/dev.sql"
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'COPY api.test nerocd probe buildx /fixture/' 'COPY dev.sql /workspace/db/seeds/dev.sql' 'WORKDIR /workspace/internal/api' > "$fixture_dir/Dockerfile"
docker build --label "$label=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
container_id=$(docker run --detach --privileged --name "$name" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock)
printf 'owned runtime fixture container=%s owner=%s image=%s\n' "$container_id" "$owner" "$image_id"
ready=false
for _ in $(seq 1 60); do if docker exec "$container_id" docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
docker exec "$container_id" sh -ec 'mkdir "$1"; printf "+cpu +memory +pids" > "$1/cgroup.subtree_control"' sh "/sys/fs/cgroup/$owner"
docker exec "$container_id" docker version --format 'Docker client={{.Client.Version}} server={{.Server.Version}}'
docker exec "$container_id" sh -ec 'mkdir /payload; cp /fixture/probe /payload/probe; printf "FROM scratch\nCOPY probe /probe\nENTRYPOINT [\"/probe\"]\n" > /payload/Dockerfile; docker build -t environment-probe:fixture /payload'
docker exec "$container_id" docker run --detach --name runtime-environment-registry --label "$label=$owner" --publish 127.0.0.1::5000 registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
registry_addr=$(docker exec "$container_id" docker port runtime-environment-registry 5000/tcp)
[[ "$registry_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
registry_image="$registry_addr/runtime-environment:fixture"
docker exec "$container_id" docker tag environment-probe:fixture "$registry_image"
docker exec "$container_id" docker push "$registry_image"
docker exec --env "NEROCD_BUILDX_SHA256=sha256:$buildx_sha" --env "NEROCD_CGROUP_ROOT=/sys/fs/cgroup/$owner" --env NEROCD_HOSTING_ENVIRONMENT_RUNTIME=disposable-dind --env "NEROCD_HOSTING_ENVIRONMENT_IMAGE=$registry_image" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_build_deployment_test@host.docker.internal:32918/nerocd?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestHostingRuntimeEnvironmentCompiledRunner$' -test.timeout=6m
printf 'hosting runtime environment compiled-runner qualification passed\n'
