#!/usr/bin/env bash
# Runs only on the explicitly authorized disposable local Docker target.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/runnerruntime/hosting_database_runtime_integration_test.go"
[[ -z $(git status --porcelain) ]] || { echo 'gate requires a clean committed checkout' >&2; exit 1; }
head_before=$(git rev-parse HEAD)
tree_before=$(git rev-parse 'HEAD^{tree}')
owner=nerocd-database-runtime-20261024-db7319
label=io.nerocd.database-runtime.owner
name=$owner-dind
volume=$owner-docker-data
docker_host=unix:///var/run/docker.sock
daemon=e37dd219-b13f-47dd-99ed-21d79442ed0c
outer=''
created_volume=false
evidence=$(mktemp -d /tmp/nerocd-database-runtime-20261024.XXXXXX)
exec > >(tee "$evidence/gate.log") 2>&1
printf 'evidence=%s\nhead=%s\ntree=%s\nowner=%s\n' "$evidence" "$head_before" "$tree_before" "$owner"
host_docker() { docker --host "$docker_host" "$@"; }
inner_docker() { host_docker exec "$outer" docker --host unix:///var/run/docker.sock "$@"; }
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$outer" ]]; then
  if [[ $(host_docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$outer" 2>/dev/null || true) != "$outer $owner" ]]; then
   echo 'outer identity changed; preserving resources' >&2
   exit 1
  fi
  # Stop only binaries copied into this owned guest, then join before deleting it.
  if ! host_docker exec "$outer" sh -ec '
   for exe in /proc/[0-9]*/exe; do
    case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/usr/local/bin/docker)
     pid=${exe#/proc/}; pid=${pid%/exe}; kill -TERM "$pid";; esac
   done
   for tick in $(seq 1 100); do
    live=false
    for exe in /proc/[0-9]*/exe; do
     case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/usr/local/bin/docker) live=true;; esac
    done
    if [ "$live" = false ]; then exit 0; fi
    sleep 0.1
   done
   exit 1'; then
   echo 'owned fixture processes did not join; preserving guest' >&2
   exit 1
  fi
  echo 'owned database test and Docker CLI processes absent' | tee "$evidence/process-absence"
  inner_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/inner-containers-final" || result=1
  inner_docker volume ls --format '{{json .}}' > "$evidence/inner-volumes-final" || result=1
  inner_docker image ls --no-trunc --digests --format '{{json .}}' > "$evidence/inner-images-final" || result=1
  host_docker inspect "$outer" > "$evidence/outer-final-inspect"
  if [[ $(host_docker inspect --format '{{range .Mounts}}{{.Type}} {{.Name}} {{.Destination}}{{end}}' "$outer") != "volume $volume /var/lib/docker" ]]; then
   echo 'unexpected outer mount; preserving guest' >&2; exit 1
  fi
  host_docker rm --force "$outer" || result=1
  if host_docker inspect "$outer" >/dev/null 2>&1; then result=1; else echo "owned outer absent: $outer"; fi
 fi
 if [[ "$created_volume" == true ]]; then
  if [[ $(host_docker volume inspect --format "{{.Name}} {{.Driver}} {{index .Labels \"$label\"}}" "$volume" 2>/dev/null || true) != "$volume local $owner" || -n $(host_docker ps -aq --filter "volume=$volume") ]]; then
   echo 'outer storage ownership/users changed; preserving volume' >&2; exit 1
  fi
  host_docker volume rm "$volume" || result=1
  if host_docker volume inspect "$volume" >/dev/null 2>&1; then result=1; else echo "owned outer volume absent: $volume"; fi
 fi
 host_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/host-containers-after" || result=1
 host_docker volume ls --format '{{json .}}' > "$evidence/host-volumes-after" || result=1
 git status --porcelain > "$evidence/status-after"
 [[ $(git rev-parse HEAD) == "$head_before" && $(git rev-parse 'HEAD^{tree}') == "$tree_before" && ! -s "$evidence/status-after" ]] || result=1
 printf 'gate_exit=%s\nouter_id=%s\nouter_volume=%s\n' "$result" "$outer" "$volume" > "$evidence/result"
 echo "gate exit=$result evidence=$evidence"
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[[ $(host_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}}') == "$daemon linux aarch64" ]]
[[ -z $(host_docker ps -aq --filter "name=^/$name$") ]]
if host_docker volume inspect "$volume" >/dev/null 2>&1; then echo 'owned outer volume already exists' >&2; exit 1; fi
host_docker ps -a --no-trunc --format '{{json .}}' > "$evidence/host-containers-before"
host_docker volume ls --format '{{json .}}' > "$evidence/host-volumes-before"
git ls-files -s acceptance/runtime-hosting-database/ internal/runnerruntime/hosting_database_runtime_integration_test.go internal/runnerruntime/hosting_database_engine.go internal/runnerruntime/hosting_database_secrets.go internal/runnerruntime/hosting_database_network.go internal/runnerruntime/hosting_database_readiness.go internal/runnerruntime/hosting_owned_volume.go internal/domain/hosting_database.go internal/domain/hosting_database_execution.go > "$evidence/source-blobs"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o "$evidence/runtime.test" ./internal/runnerruntime
go version -m "$evidence/runtime.test" > "$evidence/test-binary-build-info"
shasum -a 256 "$evidence/runtime.test" | tee "$evidence/binary-sha256"
dind=docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489
host_docker image inspect "$dind" > "$evidence/pinned-outer-image"
host_docker volume create --driver local --label "$label=$owner" "$volume"
created_volume=true
outer=$(host_docker run --detach --privileged --name "$name" --label "$label=$owner" --mount "type=volume,src=$volume,dst=/var/lib/docker" --env DOCKER_TLS_CERTDIR= "$dind" dockerd --host=unix:///var/run/docker.sock)
host_docker inspect "$outer" > "$evidence/outer-initial-inspect"
ready=false
for _ in $(seq 1 60); do if inner_docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
inner_profile=$(inner_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}}')
printf 'binary_goos=linux binary_goarch=arm64 daemon=%s emulation=false\n' "$inner_profile" | tee "$evidence/native-profile"
[[ "$inner_profile" == *' linux aarch64' || "$inner_profile" == *' linux arm64' ]]
host_docker exec "$outer" mkdir -p /fixture
host_docker cp "$evidence/runtime.test" "$outer:/fixture/runtime.test"
# Production PrepareDatabase pulls only its domain-pinned native child manifests:
# PostgreSQL arm64 sha256:f97ca6038b7641359fef23126949d824ac68edd8a7081b424d6a78e1f7076f8e
# Redis arm64 sha256:b9fe60acc13c30e9a4ed29b9022ceea7b468fa2f032a173787a55bb8f8998f03
host_docker exec --env NEROCD_DATABASE_RUNTIME=disposable-dind "$outer" /fixture/runtime.test -test.v -test.run '^TestHostingDatabaseNativeRuntime$' -test.timeout=20m
echo 'native Linux arm64 private PostgreSQL/Redis engine runtime gate passed'
