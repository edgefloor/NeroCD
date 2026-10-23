#!/usr/bin/env bash
# Explicitly owned PostgreSQL32961 and isolated DinD, no shared fixture changes.
set -euo pipefail
repo_root=$(pwd -P)
browser_mode=${NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER:-0}
case "$browser_mode" in 0|1) ;; *) echo "invalid actual logs browser option" >&2; exit 1;; esac
if [[ "$browser_mode" == 1 ]]; then
 bun run --cwd web/app build
 manifest_path=${NEROCD_RUNTIME_LOGS_ACTUAL_MANIFEST:-/tmp/nerocd-runtime-logs-actual-manifest.json}
 python3 -B scripts/skills/ui_manifest.py > "$manifest_path"
 shasum -a 256 "$manifest_path"
 export NEROCD_RUNTIME_LOGS_ACTUAL_MANIFEST="$manifest_path"
fi
test -f "$repo_root/internal/runnerruntime/hosting_runtime_logs.go"
python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",32961)); s.close()'
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) goarch=arm64;; x86_64|amd64) goarch=amd64;; *) exit 1;; esac
fixture_dir=$(mktemp -d /tmp/nerocd-hosting-logs.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.hosting-logs.owner
container_id='';image_id='';pg_id=''
image="$owner:fixture"
inventory(){
 printf 'inventory %s containers\n' "$1";docker ps -a --no-trunc --format '{{.ID}} {{.Names}}'
 printf 'inventory %s images\n' "$1";docker image ls --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}'
 printf 'inventory %s volumes\n' "$1";docker volume ls --format '{{.Name}}'
}
cleanup(){
 result=$?;trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   if ! docker rm --force --volumes "$container_id" >/dev/null;then result=1;fi
   if docker inspect "$container_id" >/dev/null 2>&1;then result=1;else printf 'owned DinD removed %s\n' "$container_id";fi
  else result=1;fi
 fi
 if [[ -n "$image_id" ]];then
  if [[ $(docker image inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$image" 2>/dev/null || true) == "$image_id $owner" ]];then
   if ! docker image rm "$image" >/dev/null;then result=1;fi
  else result=1;fi
 fi
 if [[ -n "$pg_id" ]];then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$pg_id" 2>/dev/null || true) == "$pg_id $owner" ]];then
   printf 'owned PostgreSQL schemas after fixture\n'
   schemas=$(docker exec "$pg_id" psql -U nerocd -d nerocd --no-psqlrc -Atc "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY nspname") || result=1
   printf '%s\n' "$schemas"; [[ "$schemas" == public ]] || result=1
   if ! docker rm --force --volumes "$pg_id" >/dev/null;then result=1;fi
   if docker inspect "$pg_id" >/dev/null 2>&1;then result=1;else printf 'owned PostgreSQL removed %s\n' "$pg_id";fi
  else result=1;fi
 fi
 inventory after || result=1
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
inventory before
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/probe" ./acceptance/runtime-hosting-logs/probe
shasum -a 256 "$fixture_dir/nerocd" "$fixture_dir/api.test"
cp db/seeds/dev.sql "$fixture_dir/dev.sql"
cp openapi.yaml "$fixture_dir/openapi.yaml"
mkdir -p "$fixture_dir/consumers/cmd/nerocd" "$fixture_dir/consumers/web/static" "$fixture_dir/consumers/web/app/src"
cp cmd/nerocd/main.go "$fixture_dir/consumers/cmd/nerocd/main.go"
cp web/static/app.js "$fixture_dir/consumers/web/static/app.js"
cp web/app/src/api.ts "$fixture_dir/consumers/web/app/src/api.ts"
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'COPY api.test nerocd probe openapi.yaml /fixture/' 'COPY dev.sql /workspace/db/seeds/dev.sql' 'COPY consumers /workspace/' 'WORKDIR /workspace/internal/api' > "$fixture_dir/Dockerfile"
docker build --label "$label=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
pg_id=$(docker run --detach --name "$owner-pg" --label "$label=$owner" --publish 127.0.0.1:32961:5432 --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_logs_fixture --env POSTGRES_DB=nerocd postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94)
printf 'owned PostgreSQL id=%s owner=%s port=127.0.0.1:32961\n' "$pg_id" "$owner"
ready=false
for _ in $(seq 1 60);do if docker exec "$pg_id" pg_isready --host=127.0.0.1 --username=nerocd --dbname=nerocd >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
[[ "$ready" == true ]]
printf 'owned PostgreSQL schemas before fixture\n'
docker exec "$pg_id" psql -U nerocd -d nerocd --no-psqlrc -Atc "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY nspname"
run_fixture_container(){
 set -- --detach --privileged --name "$owner-dind" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock
 if [[ "$browser_mode" == 1 ]]; then set -- --publish 127.0.0.1::18080 "$@"; fi
 docker run "$@"
}
container_id=$(run_fixture_container)
printf 'owned DinD id=%s owner=%s image=%s\n' "$container_id" "$owner" "$image_id"
ready=false
for _ in $(seq 1 60);do if docker exec "$container_id" docker info >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
[[ "$ready" == true ]]
docker exec --workdir /workspace "$container_id" /fixture/nerocd contract -openapi /fixture/openapi.yaml
docker exec "$container_id" docker version --format 'Docker client={{.Client.Version}} server={{.Server.Version}}'
docker exec "$container_id" sh -ec 'mkdir /payload;cp /fixture/probe /payload/probe;printf "FROM scratch\nCOPY probe /probe\nENTRYPOINT [\"/probe\"]\n" >/payload/Dockerfile;docker build -t logs-probe:fixture /payload'
docker exec "$container_id" docker run --detach --name logs-registry --publish 127.0.0.1::5000 registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
registry_addr=$(docker exec "$container_id" docker port logs-registry 5000/tcp)
[[ "$registry_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
registry_image="$registry_addr/logs:fixture"
docker exec "$container_id" docker tag logs-probe:fixture "$registry_image"
docker exec "$container_id" docker push "$registry_image"
if [[ "$browser_mode" == 0 ]]; then
 docker exec --env NEROCD_RUNTIME_LOGS_RUNTIME=disposable-dind --env "NEROCD_RUNTIME_LOGS_IMAGE=$registry_image" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_logs_fixture@host.docker.internal:32961/nerocd?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestHostingRuntimeLogsCompiledRunner$' -test.timeout=5m
else
 browser_addr=$(docker port "$container_id" 18080/tcp)
 [[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
 export NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_ORIGIN="http://$browser_addr"
 export NEROCD_RUNTIME_LOGS_IMAGE="$registry_image"
 # Reference-only metadata travels through the existing fixture pipe bridge.
 node --input-type=module -e '
import { spawn } from "node:child_process";
import { once } from "node:events";
import { readFileSync } from "node:fs";
const env = process.env;
const fixture = spawn("docker", ["exec", "--env", "NEROCD_RUNTIME_LOGS_RUNTIME=disposable-dind", "--env", "NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER=1", "--env", `NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_ORIGIN=${env.NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_ORIGIN}`, "--env", `NEROCD_RUNTIME_LOGS_IMAGE=${env.NEROCD_RUNTIME_LOGS_IMAGE}`, "--env", "NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_logs_fixture@host.docker.internal:32961/nerocd?sslmode=disable", process.argv[1], "/fixture/api.test", "-test.v", "-test.run", "^TestHostingRuntimeLogsCompiledRunner$", "-test.timeout=4m"], { stdio: ["ignore", "pipe", "inherit"] });
let browser, browserResult, buffer = "", ready = false;
const completed = once(fixture, "exit");
const stop = () => { if (browser && browser.exitCode === null) browser.kill("SIGTERM"); if (fixture.exitCode === null) fixture.kill("SIGTERM"); };
process.once("SIGTERM", stop); process.once("SIGINT", stop);
const timer = setTimeout(stop, 5 * 60_000);
fixture.stdout.on("data", (chunk) => {
 buffer += chunk.toString(); let newline;
 while ((newline = buffer.indexOf("\n")) !== -1) {
  const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
  const prefix = "ACTUAL_LOGS_BROWSER_READY ";
  if (!line.startsWith(prefix)) { process.stdout.write(line + "\n"); continue; }
  if (ready) { stop(); continue; } ready = true;
  let metadata; try { metadata = JSON.parse(line.slice(prefix.length)); } catch { stop(); continue; }
  if (metadata.origin !== env.NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_ORIGIN) { stop(); continue; }
  process.stdout.write(`actual Docker runtime logs browser fixture ready at ${metadata.origin}\n`);
  browser = spawn("bun", ["run", "--cwd", "web/app", "smoke:browser", "hosting-runtime-logs-actual.spec.ts", "--project", "chromium", "--no-deps"], { cwd: process.cwd(), env: { ...env, PLAYWRIGHT_BASE_URL: metadata.origin, NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER_META: JSON.stringify(metadata), NEROCD_RUNTIME_LOGS_ACTUAL_ASSETS: readFileSync(env.NEROCD_RUNTIME_LOGS_ACTUAL_MANIFEST,"utf8") }, stdio: "inherit" });
  browserResult = once(browser, "exit").then(([code]) => code);
 }
});
try {
 const [fixtureCode] = await completed; if (fixtureCode !== 0) stop();
 const browserCode = browserResult ? await browserResult : 1;
 if (!ready || fixtureCode !== 0 || browserCode !== 0) process.exitCode = 1;
} finally { clearTimeout(timer); stop(); }
' "$container_id"
fi
printf 'hosting runtime logs compiled runner gate passed\n'
