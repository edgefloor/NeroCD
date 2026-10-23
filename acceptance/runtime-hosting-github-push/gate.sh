#!/usr/bin/env bash
# Task-owned DinD only. No host socket/PID sharing, global cleanup or live GitHub.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_github_push_runtime_integration_test.go"
pg_id=${NEROCD_GITHUB_PUSH_PG_ID:-42b2c7a54671a6c55a0387a909a122ded6d36a12776fa9859e1d21f4e4e1b7dd}
[[ "$pg_id" =~ ^[0-9a-f]{64}$ ]]
[[ $(docker inspect --format '{{.Id}} {{index .Config.Labels "nerocd.task"}}' "$pg_id") == "$pg_id github-push-builds-20261023" ]]
browser=${NEROCD_GITHUB_PUSH_BROWSER:-0}
case "$browser" in 0|1) ;; *) exit 1;; esac
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
 aarch64|arm64) goarch=arm64; buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a;;
 x86_64|amd64) goarch=amd64; buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4;;
 *) exit 1;;
esac
fixture_dir=$(mktemp -d /tmp/nerocd-github-push.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.github-push.owner
name="$owner-dind"
container_id=''
cleanup(){
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$container_id" ]]; then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$container_id" 2>/dev/null || true) == "$container_id $owner" ]]; then
   # On interruption, cancel only the verified test executable. Its signal
   # context joins the runner and API before this daemon can be removed.
   if ! docker exec "$container_id" sh -ec '
    for exe in /proc/[0-9]*/exe; do
     if [ "$(readlink "$exe" 2>/dev/null || true)" = /fixture/api.test ]; then pid=${exe#/proc/}; pid=${pid%/exe}; kill -TERM "$pid"; fi
    done
    for tick in $(seq 1 200); do
     live=false
     for exe in /proc/[0-9]*/exe; do
      case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd) live=true;; esac
     done
     if [ "$live" = false ]; then exit 0; fi
     sleep 0.1
    done
    exit 1'; then
    echo "owned fixture processes did not join; leaving exact daemon for inspection" >&2
    exit 1
   fi
   if ! docker exec "$container_id" sh -ec 'if [ -d "$1" ]; then rmdir "$1"; fi' sh "/sys/fs/cgroup/$owner"; then result=1; fi
   if ! docker rm --force --volumes "$container_id"; then result=1; fi
   if docker inspect "$container_id" >/dev/null 2>&1; then result=1; fi
  else result=1; fi
 fi
 if [[ -f "$fixture_dir/pg-before" ]]; then
  if ! docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY 1" > "$fixture_dir/pg-after" || ! diff -u "$fixture_dir/pg-before" "$fixture_dir/pg-after"; then result=1; fi
 fi
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
docker ps -a --no-trunc --format '{{.ID}} {{.Names}} {{.Labels}}' > "$fixture_dir/docker-before"
docker exec "$pg_id" psql -U nerocd -d nerocd -At -c "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY 1" > "$fixture_dir/pg-before"
curl --fail --silent --show-error --location "https://github.com/docker/buildx/releases/download/v0.38.1/buildx-v0.38.1.linux-$goarch" --output "$fixture_dir/buildx"
printf '%s  %s\n' "$buildx_sha" "$fixture_dir/buildx" | shasum -a 256 --check
chmod 0755 "$fixture_dir/buildx"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -o "$fixture_dir/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
shasum -a 256 "$fixture_dir/api.test" "$fixture_dir/nerocd"
run_container(){
 set -- --detach --privileged --add-host github.com:127.0.0.1 --name "$name" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 dockerd --host=unix:///var/run/docker.sock
 if [[ "$browser" == 1 ]]; then set -- --publish 127.0.0.1::18080 "$@"; fi
 docker run "$@"
}
container_id=$(run_container)
docker inspect --format '{{.Id}} pidmode={{.HostConfig.PidMode}} mounts={{json .Mounts}} labels={{json .Config.Labels}} ports={{json .NetworkSettings.Ports}}' "$container_id"
ready=false
for _ in $(seq 1 60); do if docker exec "$container_id" docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
docker exec "$container_id" sh -ec 'test "$(docker info --format "{{.CgroupVersion}}")" = 2; mkdir "$1"; printf "+cpu +memory +pids" > "$1/cgroup.subtree_control"; cat "$1/cgroup.subtree_control"' sh "/sys/fs/cgroup/$owner"
docker exec "$container_id" apk add --no-cache git=2.52.0-r0 git-daemon=2.52.0-r0
docker exec "$container_id" mkdir -p /fixture /workspace/db/seeds /workspace/internal/api
docker cp "$fixture_dir/api.test" "$container_id:/fixture/api.test"
docker cp "$fixture_dir/nerocd" "$container_id:/fixture/nerocd"
docker cp "$fixture_dir/buildx" "$container_id:/fixture/buildx"
docker cp db/seeds/dev.sql "$container_id:/workspace/db/seeds/dev.sql"
# Dockerfile strategy never invokes Railpack. Its otherwise unused planner
# identity is a pinned local image; this gate makes no Railpack execution claim.
docker exec "$container_id" docker pull alpine:3.23.2@sha256:865b95f46d98cf867a156fe4a135ad3fe50d2056aa3f25ed31662dff6da4eb62
planner_id=$(docker exec "$container_id" docker image inspect --format '{{.Id}}' alpine:3.23.2@sha256:865b95f46d98cf867a156fe4a135ad3fe50d2056aa3f25ed31662dff6da4eb62)
docker exec "$container_id" docker pull moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c
docker exec "$container_id" docker pull nginx:1.29.3-alpine@sha256:b3c656d55d7ad751196f21b7fd2e8d4da9cb430e32f646adcf92441b72f82b14
export NEROCD_BUILDX_SHA256="sha256:$buildx_sha" NEROCD_CGROUP_ROOT="/sys/fs/cgroup/$owner" NEROCD_PLANNER_IMAGE="$planner_id"
if [[ "$browser" == 0 ]]; then
 docker exec --workdir /workspace/internal/api --env NEROCD_GITHUB_PUSH_RUNTIME=disposable-dind --env "NEROCD_BUILDX_SHA256=$NEROCD_BUILDX_SHA256" --env "NEROCD_CGROUP_ROOT=$NEROCD_CGROUP_ROOT" --env "NEROCD_PLANNER_IMAGE=$planner_id" --env 'NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_test@host.docker.internal:32963/nerocd?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestHostingGitHubPushCompiledRunner$' -test.timeout=16m
else
 browser_addr=$(docker port "$container_id" 18080/tcp)
 [[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
 export NEROCD_GITHUB_PUSH_BROWSER_ORIGIN="http://$browser_addr"
 node --input-type=module -e '
import {spawn} from "node:child_process";
import {once} from "node:events";
const env=process.env;
const fixture=spawn("docker",["exec","--workdir","/workspace/internal/api","--env","NEROCD_GITHUB_PUSH_RUNTIME=disposable-dind","--env","NEROCD_GITHUB_PUSH_BROWSER=1","--env",`NEROCD_GITHUB_PUSH_BROWSER_ORIGIN=${env.NEROCD_GITHUB_PUSH_BROWSER_ORIGIN}`,"--env",`NEROCD_BUILDX_SHA256=${env.NEROCD_BUILDX_SHA256}`,"--env",`NEROCD_CGROUP_ROOT=${env.NEROCD_CGROUP_ROOT}`,"--env",`NEROCD_PLANNER_IMAGE=${env.NEROCD_PLANNER_IMAGE}`,"--env","NEROCD_TEST_DATABASE_URL=postgres://nerocd:nerocd_test@host.docker.internal:32963/nerocd?sslmode=disable",process.argv[1],"/fixture/api.test","-test.v","-test.run","^TestHostingGitHubPushCompiledRunner$","-test.timeout=16m"],{stdio:["ignore","pipe","inherit"]});
const complete=once(fixture,"exit");let browser,browserExit,ready=false,buffer="";
const stop=()=>{if(browser&&browser.exitCode===null)browser.kill("SIGTERM");if(fixture.exitCode===null)fixture.kill("SIGTERM");};
process.once("SIGTERM",stop);process.once("SIGINT",stop);const timer=setTimeout(stop,15*60_000);
fixture.stdout.on("data",chunk=>{buffer+=chunk;let at;while((at=buffer.indexOf("\n"))!==-1){const line=buffer.slice(0,at);buffer=buffer.slice(at+1);const prefix="GITHUB_PUSH_BROWSER_READY ";if(!line.startsWith(prefix)){process.stdout.write(line+"\n");continue;}if(ready){stop();continue;}let meta;try{meta=JSON.parse(line.slice(prefix.length));}catch{stop();continue;}if(meta.base_url!==env.NEROCD_GITHUB_PUSH_BROWSER_ORIGIN){stop();continue;}ready=true;browser=spawn("bun",["run","--cwd","web/app","smoke:browser","hosting-github-push-builds.spec.ts","--project","chromium","--no-deps"],{cwd:process.cwd(),env:{...env,PLAYWRIGHT_BASE_URL:meta.base_url,NEROCD_GITHUB_PUSH_BROWSER_META:JSON.stringify(meta)},stdio:"inherit"});browserExit=once(browser,"exit").then(([code])=>code);browserExit.then(code=>{if(code!==0)stop();});}});
try{const [code]=await complete;if(code!==0)stop();const result=browserExit?await browserExit:1;if(!ready||code!==0||result!==0)process.exitCode=1;}finally{clearTimeout(timer);stop();}
' "$container_id"
fi
printf 'GitHub push real Git/runner/build/manual-deployment fixture passed\n'
