#!/usr/bin/env bash
# Owned disposable DinD. The allocated PostgreSQL container is inspected only;
# the Go fixture creates and drops its own schema and preserves existing schemas.
set -euo pipefail
repo_root=$(pwd -P)
browser_mode=${NEROCD_HOSTING_ENVIRONMENT_BROWSER:-0}
case "$browser_mode" in 0|1) ;; *) echo "invalid browser fixture option" >&2; exit 1;; esac
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
 if [[ -f "$fixture_dir/pg-schemas-before" ]]; then
  inventory_ok=true
  if ! pg_inventory > "$fixture_dir/pg-schemas-after" || ! diff -u "$fixture_dir/pg-schemas-before" "$fixture_dir/pg-schemas-after"; then result=1; inventory_ok=false; fi
  if ! docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT table_schema,table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name" > "$fixture_dir/pg-public-after" || ! diff -u "$fixture_dir/pg-public-before" "$fixture_dir/pg-public-after"; then result=1; inventory_ok=false; fi
  if ! docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT application_name,state,count(*) FROM pg_stat_activity WHERE datname='nerocd' AND application_name <> 'psql' GROUP BY application_name,state ORDER BY application_name,state" > "$fixture_dir/pg-sessions-after" || ! diff -u "$fixture_dir/pg-sessions-before" "$fixture_dir/pg-sessions-after"; then result=1; inventory_ok=false; fi
  if [[ "$inventory_ok" == true ]]; then echo 'allocated PostgreSQL schema/public/session inventory preserved'; fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
pg_inventory(){
 docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname"
}
pg_inventory > "$fixture_dir/pg-schemas-before"
docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT table_schema,table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY table_name" > "$fixture_dir/pg-public-before"
docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT application_name,state,count(*) FROM pg_stat_activity WHERE datname='nerocd' AND application_name <> 'psql' GROUP BY application_name,state ORDER BY application_name,state" > "$fixture_dir/pg-sessions-before"
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
run_fixture_container(){
 set -- --detach --privileged --name "$name" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock
 if [[ "$browser_mode" == 1 ]]; then set -- --publish 127.0.0.1::18080 "$@"; fi
 docker run "$@"
}
container_id=$(run_fixture_container)
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
if [[ "$browser_mode" == 0 ]]; then
 docker exec --env "NEROCD_BUILDX_SHA256=sha256:$buildx_sha" --env "NEROCD_CGROUP_ROOT=/sys/fs/cgroup/$owner" --env NEROCD_HOSTING_ENVIRONMENT_RUNTIME=disposable-dind --env "NEROCD_HOSTING_ENVIRONMENT_IMAGE=$registry_image" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_build_deployment_test@host.docker.internal:32918/nerocd?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestHostingRuntimeEnvironmentCompiledRunner$' -test.timeout=6m
else
 browser_addr=$(docker port "$container_id" 18080/tcp)
 [[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
 export NEROCD_HOSTING_ENVIRONMENT_BROWSER_ORIGIN="http://$browser_addr"
 export NEROCD_BUILDX_SHA256="sha256:$buildx_sha"
 export NEROCD_CGROUP_ROOT="/sys/fs/cgroup/$owner"
 export NEROCD_HOSTING_ENVIRONMENT_IMAGE="$registry_image"
 # Reuse the existing browser fixture pipe bridge. Metadata contains references,
 # not account passwords, credentials, managed values, or control files.
 node --input-type=module -e '
import { spawn } from "node:child_process";
import { once } from "node:events";
const env = process.env;
const args = ["exec", "--env", "NEROCD_HOSTING_ENVIRONMENT_RUNTIME=disposable-dind", "--env", "NEROCD_HOSTING_ENVIRONMENT_BROWSER=1", "--env", `NEROCD_HOSTING_ENVIRONMENT_BROWSER_ORIGIN=${env.NEROCD_HOSTING_ENVIRONMENT_BROWSER_ORIGIN}`, "--env", `NEROCD_BUILDX_SHA256=${env.NEROCD_BUILDX_SHA256}`, "--env", `NEROCD_CGROUP_ROOT=${env.NEROCD_CGROUP_ROOT}`, "--env", `NEROCD_HOSTING_ENVIRONMENT_IMAGE=${env.NEROCD_HOSTING_ENVIRONMENT_IMAGE}`, "--env", "NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_build_deployment_test@host.docker.internal:32918/nerocd?sslmode=disable", process.argv[1], "/fixture/api.test", "-test.v", "-test.run", "^TestHostingRuntimeEnvironmentCompiledRunner$", "-test.timeout=9m"];
const fixture = spawn("docker", args, { stdio: ["ignore", "pipe", "inherit"] });
let browser, browserResult, ready = false, buffer = "";
const completed = once(fixture, "exit");
const stop = () => { if (browser && browser.exitCode === null) browser.kill("SIGTERM"); if (fixture.exitCode === null) fixture.kill("SIGTERM"); };
process.once("SIGTERM", stop); process.once("SIGINT", stop);
const timer = setTimeout(stop, 8 * 60_000);
fixture.stdout.on("data", (chunk) => {
 buffer += chunk.toString();
 let newline;
 while ((newline = buffer.indexOf("\n")) !== -1) {
  const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
  const prefix = "RUNTIME_ENVIRONMENT_BROWSER_READY ";
  if (!line.startsWith(prefix)) { process.stdout.write(line + "\n"); continue; }
  if (ready) { stop(); continue; }
  ready = true;
  let metadata;
  try { metadata = JSON.parse(line.slice(prefix.length)); } catch { stop(); continue; }
  if (metadata.origin !== env.NEROCD_HOSTING_ENVIRONMENT_BROWSER_ORIGIN) { stop(); continue; }
  process.stdout.write(`authenticated runtime browser fixture ready at ${metadata.origin}\n`);
  browser = spawn("bun", ["run", "--cwd", "web/app", "smoke:browser", "runtime-environment-deployments.spec.ts", "--project", "chromium", "--no-deps"], { cwd: process.cwd(), env: { ...env, PLAYWRIGHT_BASE_URL: metadata.origin, NEROCD_RUNTIME_ENVIRONMENT_BROWSER_META: JSON.stringify(metadata) }, stdio: "inherit" });
  browserResult = once(browser, "exit").then(([code]) => code);
 }
});
try {
 const [fixtureCode] = await completed;
 if (fixtureCode !== 0) stop();
 const browserCode = browserResult ? await browserResult : 1;
 if (!ready || fixtureCode !== 0 || browserCode !== 0) process.exitCode = 1;
} finally { clearTimeout(timer); stop(); }
' "$container_id"
fi
printf 'hosting runtime environment compiled-runner qualification passed\n'
