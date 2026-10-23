#!/usr/bin/env bash
# Disposable Docker-in-Docker only. Default mode publishes no host ports.
# Browser mode publishes its authenticated fixture on host loopback only. Never prunes Docker.
set -euo pipefail
repo_root=$(pwd -P)
case "$repo_root" in */NeroCD*) ;; *) echo 'run from assigned repository root' >&2; exit 1;; esac
source "$repo_root/acceptance/runtime-hosting-build-controller/helpers.sh"
browser_mode=${NEROCD_LOCAL_BUILD_BROWSER:-0}
case "$browser_mode" in 0|1) ;; *) echo 'invalid browser fixture option' >&2; exit 1;; esac
private_mode=${NEROCD_PRIVATE_GIT_RUNTIME:-0}
private_browser_mode=${NEROCD_PRIVATE_GIT_BROWSER:-0}
policy_check=${NEROCD_PRIVATE_GIT_POLICY_CHECK:-0}
case "$policy_check:$private_browser_mode" in 0:0|0:1|1:0) ;; *) echo 'invalid focused private policy option' >&2; exit 1;; esac
if [[ "$policy_check" == 1 ]]; then private_mode=1; fi
case "$private_mode:$private_browser_mode" in 0:0|1:0|0:1|1:1) ;; *) echo 'invalid private Git fixture option' >&2; exit 1;; esac
if [[ "$private_browser_mode" == 1 ]]; then private_mode=1; browser_mode=1; fi
export NEROCD_PRIVATE_GIT_BROWSER="$private_browser_mode"
private_strategy=${NEROCD_PRIVATE_GIT_STRATEGY:-dockerfile}
case "$private_strategy" in dockerfile|railpack) ;; *) echo 'invalid fixed private Git browser strategy' >&2; exit 1;; esac
export NEROCD_PRIVATE_GIT_STRATEGY="$private_strategy"
if [[ "$private_browser_mode" == 1 ]]; then
 bun run --cwd web/app build
 asset_manifest=${NEROCD_PRIVATE_GIT_BROWSER_MANIFEST:-/tmp/nerocd-private-git-browser-assets.json}
 python3 -B scripts/skills/ui_manifest.py > "$asset_manifest"
 shasum -a 256 "$asset_manifest"
 export NEROCD_PRIVATE_GIT_BROWSER_MANIFEST="$asset_manifest"
fi
preload_postgres=${NEROCD_BUILD_CONTROLLER_PRELOAD_POSTGRES:-0}
case "$preload_postgres" in 0|1) ;; *) echo 'invalid PostgreSQL preload option' >&2; exit 1;; esac
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
 aarch64|arm64) goarch=arm64; railarch=arm64; misearch=arm64; mise_sha=cf9a7c3e64688a7909fa69a4989d3b8daf6dd1b3e583fa0ab7c08139ae6d3dcd; buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a; rail_sha=c24a064b586b8f4f8c2fab44dd5ef19253e4c6cc4e1df793b3ae19cd87f7a5d4 ;;
 x86_64|amd64) goarch=amd64; railarch=x86_64; misearch=x64; mise_sha=e331f3fa3c360c63e996ef70bc183ce178cb16d21f8bf4926c61cf6fa6b9fafd; buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4; rail_sha=2842de93e68713af9037e0bc0a398d7da78f3b96aa4804303a638db2bc69bd30 ;;
 *) echo 'unsupported daemon architecture' >&2; exit 1 ;;
esac
fixture_dir=$(mktemp -d /tmp/nerocd-runtime-hosting-local-build.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
name="$owner-dind"
image="$owner:fixture"
container_id=""
image_id=""
pg_id=""
inventory(){
 printf 'inventory %s containers\n' "$1"; docker ps -a --no-trunc --format '{{.ID}} {{.Names}}'
 printf 'inventory %s images\n' "$1"; docker image ls --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}'
 printf 'inventory %s volumes\n' "$1"; docker volume ls --format '{{.Name}}'
}
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.runtime-hosting-local-build.owner"}}' "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   if [[ -n "$pg_id" ]]; then
    if [[ $(docker exec "$container_id" docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.build-controller.owner"}}' "$pg_id" 2>/dev/null || true) == "$pg_id $owner" ]]; then
     printf 'owned database inventory before cleanup\n'
     docker exec "$container_id" docker exec "$pg_id" psql -U nerocd -d postgres --no-psqlrc -Atc 'SELECT datname FROM pg_database ORDER BY datname' || result=1
     if ! docker exec "$container_id" docker rm --force --volumes "$pg_id" >/dev/null; then result=1; fi
     printf 'owned PostgreSQL removed %s\n' "$pg_id"
    else result=1; fi
   fi
   if ! docker exec "$container_id" sh -ec 'if [ -d "$1" ]; then rmdir "$1"; fi' sh "/sys/fs/cgroup/$owner"; then result=1; fi
   if ! docker rm --force --volumes "$container_id" >/dev/null; then result=1; fi
   if docker inspect "$container_id" >/dev/null 2>&1; then result=1; else printf 'owned container removed: %s\n' "$container_id"; fi
  else printf 'owned container identity unavailable: %s\n' "$container_id" >&2; result=1; fi
 fi
 if [[ -n "$image_id" ]]; then
  if [[ $(docker image inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.runtime-hosting-local-build.owner"}}' "$image" 2>/dev/null || true) == "$image_id $owner" && -z $(docker ps -aq --filter "ancestor=$image_id") ]]; then
   if ! docker image rm "$image" >/dev/null; then result=1; fi
  else result=1; fi
 fi
 inventory after || result=1
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
inventory before
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
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -tags nerocd_proxy_fixture -c -o "$fixture_dir/hosting-controller.test" ./internal/runnerruntime
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
shasum -a 256 "$fixture_dir/nerocd" "$fixture_dir/hosting-controller.test"
cp db/seeds/dev.sql "$fixture_dir/dev.sql"
printf '%s\n' 'FROM docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489' 'RUN apk add --no-cache git=2.52.0-r0 git-daemon=2.52.0-r0' 'COPY buildx railpack mise-bin hosting-controller.test nerocd dev.sql /fixture/' > "$fixture_dir/Dockerfile"
docker build --label "io.nerocd.runtime-hosting-local-build.owner=$owner" -t "$image" "$fixture_dir"
image_id=$(docker image inspect --format '{{.Id}}' "$image")
run_fixture_container() {
 # Nonempty positional arguments also work with Bash 3.2 and nounset enabled.
 set -- --detach --privileged --pid=host --mount "type=bind,src=$repo_root/acceptance/runtime-proxy-control/pebble.json,dst=/workspace/acceptance/runtime-proxy-control/pebble.json,readonly" --add-host example.com:127.0.0.1 --name "$name" --label "io.nerocd.runtime-hosting-local-build.owner=$owner" --env DOCKER_TLS_CERTDIR= "$image" dockerd --host=unix:///var/run/docker.sock
 if [[ "$browser_mode" == 1 ]]; then set -- --publish 127.0.0.1::18080 "$@"; fi
 docker run "$@"
}
container_id=$(run_fixture_container)
printf 'owned DinD id=%s owner=%s image=%s\n' "$container_id" "$owner" "$image_id"
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
pg_image=$(controller_postgres_image "$name" "$goarch" "$preload_postgres")
pg_id=$(docker exec "$name" docker run --detach --name "$owner-pg" --label "io.nerocd.build-controller.owner=$owner" --publish 127.0.0.1::5432 --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_fixture_only --env POSTGRES_DB=nerocd_build_controller "$pg_image")
ready=false
for _ in $(seq 1 60); do if controller_postgres_ready "$name" "$pg_id" >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
if [[ "$ready" != true ]]; then echo 'owned PostgreSQL not ready' >&2; exit 1; fi
printf 'owned PostgreSQL TCP query ready: container=%s owner=%s database=nerocd_build_controller\n' "$pg_id" "$owner"
docker exec "$name" docker exec "$pg_id" psql -U nerocd -d postgres --no-psqlrc -Atc 'SELECT datname FROM pg_database ORDER BY datname'
pg_addr=$(docker exec "$name" docker port "$pg_id" 5432/tcp)
if [[ "$browser_mode" == 0 ]]; then
test_pattern='^TestLocalBuildProductionHTTPS$'
if [[ "$private_mode" == 1 ]]; then test_pattern='^(TestPrivateFixtureSourceRevisionPolicy|TestPrivateFixtureResourcePolicyPostgres|TestPrivateBuildProductionHTTPS|TestLocalBuildProductionHTTPS)$'; fi
if [[ "$policy_check" == 1 ]]; then test_pattern='^TestPrivateFixtureResourcePolicyPostgres$'; fi
docker exec --env NEROCD_LOCAL_BUILD_RUNTIME=disposable-dind --env "NEROCD_BUILD_CONTROLLER_DATABASE_URL=postgres://nerocd:isolated_fixture_only@$pg_addr/nerocd_build_controller?sslmode=disable" --env "NEROCD_CGROUP_ROOT=/sys/fs/cgroup/$owner" --env "NEROCD_PLANNER_IMAGE=$planner_id" --env "NEROCD_BUILDX_SHA256=sha256:$buildx_sha" "$name" /fixture/hosting-controller.test -test.v -test.run "$test_pattern" -test.timeout=15m
else
 browser_addr=$(docker port "$container_id" 18080/tcp)
 [[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]] || exit 1
 export NEROCD_LOCAL_BUILD_BROWSER_ORIGIN="http://$browser_addr"
 export NEROCD_BROWSER_FIXTURE_DSN="postgres://nerocd:isolated_fixture_only@$pg_addr/nerocd_build_controller?sslmode=disable"
 export NEROCD_CGROUP_ROOT="/sys/fs/cgroup/$owner"
 export NEROCD_PLANNER_IMAGE="$planner_id"
 export NEROCD_BUILDX_SHA256="sha256:$buildx_sha"
 # Captured pipes carry fixture metadata directly. No coordination file is used.
 node --input-type=module -e '
import { spawn } from "node:child_process";
import { once } from "node:events";
import { readFileSync } from "node:fs";
const container = process.argv[1];
const env = process.env;
const privateMode = env.NEROCD_PRIVATE_GIT_BROWSER === "1";
const args = ["exec", "--env", `NEROCD_PRIVATE_GIT_BROWSER=${privateMode ? "1" : "0"}`, "--env", `NEROCD_PRIVATE_GIT_STRATEGY=${env.NEROCD_PRIVATE_GIT_STRATEGY}`, "--env", "NEROCD_LOCAL_BUILD_RUNTIME=disposable-dind", "--env", "NEROCD_LOCAL_BUILD_BROWSER=1", "--env", `NEROCD_LOCAL_BUILD_BROWSER_ORIGIN=${env.NEROCD_LOCAL_BUILD_BROWSER_ORIGIN}`, "--env", `NEROCD_BUILD_CONTROLLER_DATABASE_URL=${env.NEROCD_BROWSER_FIXTURE_DSN}`, "--env", `NEROCD_CGROUP_ROOT=${env.NEROCD_CGROUP_ROOT}`, "--env", `NEROCD_PLANNER_IMAGE=${env.NEROCD_PLANNER_IMAGE}`, "--env", `NEROCD_BUILDX_SHA256=${env.NEROCD_BUILDX_SHA256}`, container, "/fixture/hosting-controller.test", "-test.v", "-test.run", privateMode ? "^(TestControllerBrowserOrigin|TestPrivateFixtureSourceRevisionPolicy|TestPrivateFixtureResourcePolicyPostgres|TestPrivateGitBrowser)$" : "^(TestControllerBrowserOrigin|TestLocalBuildBrowser)$", "-test.timeout=10m"];
const fixture = spawn("docker", args, { stdio: ["ignore", "pipe", "inherit"] });
let browser;
let buffer = "";
let ready = false;
let browserResult;
const completed = once(fixture, "exit");
const stop = () => { if (browser && browser.exitCode === null) browser.kill("SIGTERM"); if (fixture.exitCode === null) fixture.kill("SIGTERM"); };
process.once("SIGTERM", stop); process.once("SIGINT", stop);
const timer = setTimeout(stop, 9 * 60_000);
fixture.stdout.on("data", (chunk) => {
 buffer += chunk.toString();
 let newline;
 while ((newline = buffer.indexOf("\n")) !== -1) {
  const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
  const prefix = privateMode ? "PRIVATE_GIT_BROWSER_READY " : "LOCAL_BUILD_BROWSER_READY ";
 if (!line.startsWith(prefix)) { process.stdout.write(line + "\n"); continue; }
  if (ready) { stop(); continue; }
  ready = true;
  let metadata;
  try { metadata = JSON.parse(line.slice(prefix.length)); } catch { stop(); continue; }
  if (metadata.origin !== env.NEROCD_LOCAL_BUILD_BROWSER_ORIGIN) { stop(); continue; }
  process.stdout.write(`authenticated browser fixture ready at ${metadata.origin}\n`);
  browser = spawn("bun", ["run", "--cwd", "web/app", "smoke:browser", privateMode ? "private-git-build-deployments.spec.ts" : "local-build-deployments.spec.ts", "--project", "chromium", "--no-deps"], { cwd: process.cwd(), env: { ...env, PLAYWRIGHT_BASE_URL: metadata.origin, NEROCD_LOCAL_BUILD_BROWSER_META: JSON.stringify(metadata), ...(privateMode ? { NEROCD_PRIVATE_GIT_BROWSER_META: JSON.stringify(metadata), NEROCD_PRIVATE_GIT_BROWSER_ASSETS: readFileSync(env.NEROCD_PRIVATE_GIT_BROWSER_MANIFEST,"utf8") } : {}) }, stdio: "inherit" });
  browserResult = once(browser, "exit").then(([code]) => { if (code !== 0) stop(); return code; });
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
if [[ $(docker exec "$name" docker inspect --format '{{.Id}} {{index .Config.Labels "io.nerocd.build-controller.owner"}}' "$pg_id") != "$pg_id $owner" ]]; then echo 'owned PostgreSQL identity mismatch' >&2; exit 1; fi
printf 'owned database inventory after tests\n'
remaining=$(docker exec "$name" docker exec "$pg_id" psql -U nerocd -d postgres --no-psqlrc -Atc 'SELECT datname FROM pg_database ORDER BY datname')
printf '%s\n' "$remaining"
if [[ "$remaining" == *private_runtime_* ]]; then echo 'private fixture database cleanup incomplete' >&2; exit 1; fi
docker exec "$name" docker rm --force --volumes "$pg_id"
printf 'owned PostgreSQL removed %s\n' "$pg_id"
pg_id=""

docker exec "$name" rmdir "/sys/fs/cgroup/$owner"
printf 'runtime-hosting-local-build fixture cgroup cleanup passed\n'
