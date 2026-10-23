#!/usr/bin/env bash
# Runs only on the explicitly authorized disposable local Docker target.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_storage_runtime_integration_test.go"
[[ -z $(git status --porcelain) ]] || { echo 'gate requires a clean committed checkout' >&2; exit 1; }
head_before=$(git rev-parse HEAD)
tree_before=$(git rev-parse 'HEAD^{tree}')
owner=nerocd-storage-runtime-20261024-16ace
label=io.nerocd.storage-runtime.owner
name=$owner-dind
volume=$owner-docker-data
docker_host=unix:///var/run/docker.sock
daemon=e37dd219-b13f-47dd-99ed-21d79442ed0c
outer=''
created_volume=false
evidence=$(mktemp -d /tmp/nerocd-storage-runtime-20261024.XXXXXX)
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
    case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd-proxy-fixture)
     pid=${exe#/proc/}; pid=${pid%/exe}; kill -TERM "$pid";; esac
   done
   for tick in $(seq 1 100); do
    live=false
    for exe in /proc/[0-9]*/exe; do
     case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd-proxy-fixture) live=true;; esac
    done
    if [ "$live" = false ]; then exit 0; fi
    sleep 0.1
   done
   exit 1'; then
   echo 'owned fixture processes did not join; preserving guest' >&2
   exit 1
  fi
  echo 'owned API and runner processes absent' | tee "$evidence/process-absence"
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
git ls-files -s acceptance/runtime-hosting-storage acceptance/image-hosting-storage-fixture internal/api/hosting_storage_runtime_integration_test.go > "$evidence/source-blobs"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -tags nerocd_proxy_fixture -c -o "$evidence/api.test" ./internal/api
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nerocd_proxy_fixture -trimpath -o "$evidence/nerocd-proxy-fixture" ./cmd/nerocd
mkdir "$evidence/a" "$evidence/b"
for version in a b; do
 if [[ "$version" == a ]]; then display=A; else display=B; fi
 GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$display" -o "$evidence/$version/app" ./acceptance/image-hosting-storage-fixture
 cp acceptance/image-hosting-storage-fixture/Dockerfile "$evidence/$version/Dockerfile"
done
shasum -a 256 "$evidence/api.test" "$evidence/nerocd-proxy-fixture" "$evidence/a/app" "$evidence/b/app" | tee "$evidence/binary-sha256"
dind=docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489
postgres=postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94
registry=registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
host_docker image inspect "$dind" "$postgres" "$registry" > "$evidence/pinned-host-images"
host_docker volume create --driver local --label "$label=$owner" "$volume"
created_volume=true
outer=$(host_docker run --detach --privileged --name "$name" --label "$label=$owner" --mount "type=volume,src=$volume,dst=/var/lib/docker" --env DOCKER_TLS_CERTDIR= "$dind" dockerd --host=unix:///var/run/docker.sock)
host_docker inspect "$outer" > "$evidence/outer-initial-inspect"
ready=false
for _ in $(seq 1 60); do if inner_docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
inner_docker info --format '{{.ID}} {{.OSType}} {{.Architecture}}' | tee "$evidence/inner-daemon"
host_docker exec "$outer" mkdir -p /fixture /workspace/db/seeds /workspace/internal/api /workspace/acceptance/runtime-proxy-control
for binary in api.test nerocd-proxy-fixture; do host_docker cp "$evidence/$binary" "$outer:/fixture/$binary"; done
host_docker cp db/seeds/dev.sql "$outer:/workspace/db/seeds/dev.sql"
host_docker cp acceptance/runtime-proxy-control/pebble.json "$outer:/workspace/acceptance/runtime-proxy-control/pebble.json"
host_docker image save "$postgres" "$registry" -o "$evidence/base-images.tar"
host_docker cp "$evidence/base-images.tar" "$outer:/fixture/base-images.tar"
inner_docker image load -i /fixture/base-images.tar
host_docker exec "$outer" rm /fixture/base-images.tar
for image in \
 docker.io/library/traefik@sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368 \
 ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199; do inner_docker pull "$image"; done
pg=$(inner_docker run --detach --name "$owner-pg" --label "$label=$owner" --network host --tmpfs /var/lib/postgresql/data:rw --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_fixture_only --env POSTGRES_DB=nerocd_storage_runtime "$postgres")
reg=$(inner_docker run --detach --name "$owner-registry" --label "$label=$owner" --network host --tmpfs /var/lib/registry:rw --env REGISTRY_HTTP_ADDR=127.0.0.1:25000 "$registry")
printf 'inner_pg=%s\ninner_registry=%s\n' "$pg" "$reg" | tee "$evidence/inner-services"
for version in a b; do
 host_docker cp "$evidence/$version" "$outer:/fixture/$version"
 inner_docker build --build-arg "FIXTURE_OWNER=$owner" -t "127.0.0.1:25000/$owner:$version" "/fixture/$version"
 inner_docker push "127.0.0.1:25000/$owner:$version"
 inner_docker image inspect "127.0.0.1:25000/$owner:$version" > "$evidence/app-$version-image"
done
inner_docker image ls --no-trunc --digests --format '{{json .}}' > "$evidence/inner-images-before-test"
ready=false
for _ in $(seq 1 60); do
 if [[ $(inner_docker exec --env PGPASSWORD=isolated_fixture_only "$pg" psql --host=127.0.0.1 --username=nerocd --dbname=nerocd_storage_runtime --no-psqlrc --tuples-only --no-align --command='SELECT current_database()' 2>/dev/null || true) == nerocd_storage_runtime ]]; then ready=true; break; fi
 sleep 1
done
[[ "$ready" == true ]]
host_docker exec --workdir /workspace/internal/api --env NEROCD_STORAGE_RUNTIME=disposable-dind --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_storage_runtime?sslmode=disable' "$outer" /fixture/api.test -test.v -test.run '^(TestStorageRuntimeRollback|TestHostingStorageCompiledRunnerHTTPS)$' -test.timeout=12m
echo 'compiled storage runner / authenticated public API / trusted fixture-CA HTTPS gate passed'
