#!/usr/bin/env bash
# Owned PostgreSQL32965 and native Linux DinD fixture for the actual CLI collector.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/runnerruntime/hosting_readiness.go"
python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",32965)); s.close()'
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) goarch=arm64;; x86_64|amd64) goarch=amd64;; *) exit 1;; esac
fixture_dir=$(mktemp -d /tmp/nerocd-readiness-agent.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.readiness-agent.owner
image="$owner:fixture"
pg_id='';container_id='';image_id=''
cleanup(){
 result=$?;trap - EXIT INT TERM
 for id in "$container_id" "$pg_id";do
  if [[ -n "$id" ]];then
   if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$id" 2>/dev/null || true) == "$id $owner" ]];then
    docker rm --force --volumes "$id" >/dev/null || result=1
    if docker inspect "$id" >/dev/null 2>&1;then result=1;else printf 'owned fixture removed %s\n' "$id";fi
   else result=1;fi
  fi
 done
 if [[ -n "$image_id" ]];then
  if [[ $(docker image inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$image" 2>/dev/null || true) == "$image_id $owner" ]];then docker image rm "$image" >/dev/null || result=1;else result=1;fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
for name in "$owner-pg" "$owner-dind";do
 test -z "$(docker ps -a --filter "name=^/$name$" --format '{{.ID}}')"
done
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/runtime.test" ./internal/runnerruntime
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
shasum -a 256 "$fixture_dir/nerocd" "$fixture_dir/api.test" "$fixture_dir/runtime.test"
cp db/seeds/dev.sql "$fixture_dir/dev.sql"
printf '%s\n' '#!/bin/sh' 'echo executed >>/fixture/readiness-cli-executed' 'exit 77' >"$fixture_dir/docker"
chmod 0755 "$fixture_dir/docker"
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'COPY api.test runtime.test nerocd /fixture/' 'COPY docker /fixture/readiness-bin/docker' 'COPY dev.sql /workspace/db/seeds/dev.sql' 'WORKDIR /workspace/internal/api' >"$fixture_dir/Dockerfile"
docker build --label "$label=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
pg_id=$(docker run --detach --name "$owner-pg" --label "$label=$owner" --publish 127.0.0.1:32965:5432 --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_readiness_fixture --env POSTGRES_DB=nerocd postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94)
printf 'owned PostgreSQL id=%s label=%s=%s port=127.0.0.1:32965\n' "$pg_id" "$label" "$owner"
ready=false
for _ in $(seq 1 60);do if docker exec "$pg_id" pg_isready --host=127.0.0.1 --username=nerocd --dbname=nerocd >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
[[ "$ready" == true ]]
container_id=$(docker run --detach --privileged --name "$owner-dind" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock)
printf 'owned DinD id=%s label=%s=%s image=%s\n' "$container_id" "$label" "$owner" "$image_id"
ready=false
for _ in $(seq 1 60);do if docker exec "$container_id" /usr/local/bin/docker info >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
[[ "$ready" == true ]]
docker exec "$container_id" /usr/local/bin/docker version --format 'Docker client={{.Client.Version}} server={{.Server.Version}} API={{.Server.APIVersion}} arch={{.Server.Arch}}'
docker exec --workdir /workspace/internal/api "$container_id" /fixture/runtime.test -test.v -test.run '^TestReadiness' -test.timeout=2m
docker exec --env NEROCD_READINESS_RUNTIME=disposable-dind --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_readiness_fixture@host.docker.internal:32965/nerocd?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestServerReadinessCompiledAgent$' -test.timeout=3m
printf 'server readiness compiled native agent gate passed\n'
