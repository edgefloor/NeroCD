#!/usr/bin/env bash
# Disposable Docker-in-Docker only. Never publishes host ports or prunes Docker.
set -euo pipefail
repo_root=$(pwd -P)
case "$repo_root" in */NeroCD*) ;; *) echo 'run from assigned repository root' >&2; exit 1;; esac
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
 aarch64|arm64) goarch=arm64; railarch=arm64; misearch=arm64; mise_sha=cf9a7c3e64688a7909fa69a4989d3b8daf6dd1b3e583fa0ab7c08139ae6d3dcd; buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a; rail_sha=c24a064b586b8f4f8c2fab44dd5ef19253e4c6cc4e1df793b3ae19cd87f7a5d4 ;;
 x86_64|amd64) goarch=amd64; railarch=x86_64; misearch=x64; mise_sha=e331f3fa3c360c63e996ef70bc183ce178cb16d21f8bf4926c61cf6fa6b9fafd; buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4; rail_sha=2842de93e68713af9037e0bc0a398d7da78f3b96aa4804303a638db2bc69bd30 ;;
 *) echo 'unsupported daemon architecture' >&2; exit 1 ;;
esac
fixture_dir=$(mktemp -d /tmp/nerocd-runtime-hosting-build.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
name="$owner-dind"
image="$owner:fixture"
container_id=""
image_id=""
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.runtime-hosting-build.owner"}}' "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   if ! docker exec "$container_id" sh -ec 'if [ -d "$1" ]; then rmdir "$1"; fi' sh "/sys/fs/cgroup/$owner"; then result=1; fi
   if ! docker rm --force --volumes "$container_id" >/dev/null; then result=1; fi
   if docker inspect "$container_id" >/dev/null 2>&1; then result=1; else printf 'owned container removed: %s\n' "$container_id"; fi
  else printf 'owned container identity unavailable: %s\n' "$container_id" >&2; result=1; fi
 fi
 if [[ -n "$image_id" ]]; then
  if [[ $(docker image inspect --format '{{index .Config.Labels "io.nerocd.runtime-hosting-build.owner"}}' "$image_id" 2>/dev/null || true) == "$owner" ]]; then
   if ! docker image rm "$image" >/dev/null; then result=1; fi
  else result=1; fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
curl --fail --silent --show-error --location "https://github.com/docker/buildx/releases/download/v0.38.1/buildx-v0.38.1.linux-$goarch" --output "$fixture_dir/buildx"
printf '%s  %s\n' "$buildx_sha" "$fixture_dir/buildx" | shasum -a 256 --check
curl --fail --silent --show-error --location "https://github.com/railwayapp/railpack/releases/download/v0.40.1/railpack-v0.40.1-$railarch-unknown-linux-musl.tar.gz" --output "$fixture_dir/railpack.tgz"
printf '%s  %s\n' "$rail_sha" "$fixture_dir/railpack.tgz" | shasum -a 256 --check
tar -xzf "$fixture_dir/railpack.tgz" -C "$fixture_dir" railpack
curl --fail --silent --show-error --location "https://github.com/jdx/mise/releases/download/v2026.9.15/mise-v2026.9.15-linux-$misearch-musl.tar.gz" --output "$fixture_dir/mise.tgz"
printf '%s  %s\n' "$mise_sha" "$fixture_dir/mise.tgz" | shasum -a 256 --check
tar -xzf "$fixture_dir/mise.tgz" -C "$fixture_dir" mise/bin/mise
cp "$fixture_dir/mise/bin/mise" "$fixture_dir/mise-bin"
chmod 0755 "$fixture_dir/buildx" "$fixture_dir/railpack"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/hosting-executor.test" ./internal/runnerruntime
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'RUN apk add --no-cache git=2.52.0-r0 git-daemon=2.52.0-r0' 'COPY buildx railpack mise-bin hosting-executor.test /fixture/' > "$fixture_dir/Dockerfile"
docker build --label "io.nerocd.runtime-hosting-build.owner=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
container_id=$(docker run --detach --privileged --pid=host --name "$name" --label "io.nerocd.runtime-hosting-build.owner=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock)
ready=false
for _ in $(seq 1 90); do
 if docker exec "$name" docker info >/dev/null 2>&1; then ready=true; break; fi
 sleep 1
done
if [[ "$ready" != true ]]; then docker logs "$name"; exit 1; fi
docker exec "$name" sh -ec 'mkdir /planner; cp /fixture/railpack /planner/railpack; cp /fixture/mise-bin /planner/mise; printf "FROM alpine:3.23.2@sha256:865b95f46d98cf867a156fe4a135ad3fe50d2056aa3f25ed31662dff6da4eb62\nCOPY railpack /usr/local/bin/railpack\nCOPY mise /usr/local/bin/mise\n" > /planner/Dockerfile; docker build -t nerocd-runtime-planner:fixture /planner'
docker exec "$name" docker pull moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c
docker exec "$name" sh -ec 'mkdir "$1"; printf "+cpu +memory +pids" > "$1/cgroup.subtree_control"' sh "/sys/fs/cgroup/$owner"
planner_id=$(docker exec "$name" docker image inspect --format '{{.Id}}' nerocd-runtime-planner:fixture)
docker exec --env NEROCD_BUILD_EXECUTOR_RUNTIME=disposable-dind --env "NEROCD_CGROUP_ROOT=/sys/fs/cgroup/$owner" --env "NEROCD_PLANNER_IMAGE=$planner_id" --env "NEROCD_BUILDX_SHA256=sha256:$buildx_sha" "$name" /fixture/hosting-executor.test -test.v -test.run '^(TestBuild|TestHostingBuildRuntime)' -test.timeout=18m

docker exec "$name" rmdir "/sys/fs/cgroup/$owner"
printf 'runtime-hosting-build fixture cgroup cleanup passed\n'
