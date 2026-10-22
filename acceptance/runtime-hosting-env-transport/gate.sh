#!/usr/bin/env bash
# Disposable Linux Docker-in-Docker qualification. No host ports, PID sharing,
# bind mounts, PostgreSQL, production secrets, or global Docker pruning.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/runnerruntime/hosting_env_transport.go"
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
 aarch64|arm64) goarch=arm64 ;;
 x86_64|amd64) goarch=amd64 ;;
 *) echo 'unsupported daemon architecture' >&2; exit 1 ;;
esac
fixture_dir=$(mktemp -d /tmp/nerocd-runtime-hosting-env-transport.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.runtime-hosting-env-transport.owner
name="$owner-dind"
image="$owner:fixture"
container_id=''
image_id=''
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   if ! docker rm --force --volumes "$container_id" >/dev/null; then result=1; fi
   if docker inspect "$container_id" >/dev/null 2>&1; then result=1; else printf 'owned container removed: %s\n' "$container_id"; fi
  else echo 'owned fixture container identity unavailable' >&2; result=1; fi
 fi
 if [[ -n "$image_id" ]]; then
  if [[ $(docker image inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$image" 2>/dev/null || true) == "$image_id $owner" ]]; then
   if ! docker image rm "$image" >/dev/null; then result=1; fi
  else echo 'owned fixture image identity unavailable' >&2; result=1; fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/hosting-env-transport.test" ./internal/runnerruntime
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'COPY hosting-env-transport.test /fixture/hosting-env-transport.test' > "$fixture_dir/Dockerfile"
docker build --label "$label=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
container_id=$(docker run --detach --privileged --name "$name" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock)
ready=false
for _ in $(seq 1 60); do
 if docker exec "$container_id" docker info >/dev/null 2>&1; then ready=true; break; fi
 sleep 1
done
if [[ "$ready" != true ]]; then echo 'owned Linux Docker daemon did not become ready' >&2; exit 1; fi
docker exec "$container_id" docker version --format 'qualification Docker client={{.Client.Version}} server={{.Server.Version}}'
docker exec --env NEROCD_HOSTING_ENV_TRANSPORT_RUNTIME=disposable-dind "$container_id" /fixture/hosting-env-transport.test -test.v -test.run '^TestHostingEnvTransport' -test.timeout=90s
printf 'runtime hosting environment transport qualification passed\n'
