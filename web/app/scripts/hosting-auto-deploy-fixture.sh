#!/usr/bin/env bash
# Disposable trusted-source Linux qualification. Never uses the host Docker socket.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_auto_deploy_browser_integration_test.go"
[[ -z $(git status --porcelain) ]] || { echo 'gate requires a clean committed checkout' >&2; exit 1; }
head_before=$(git rev-parse HEAD)
tree_before=$(git rev-parse HEAD^{tree})
evidence=$(mktemp -d /tmp/nerocd-auto-deploy-browser.XXXXXX)
owner=$(basename "$evidence" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.auto-deploy.browser.owner
outer=''
pg=''
exec > >(tee "$evidence/gate.log") 2>&1
printf 'evidence=%s\nhead=%s\ntree=%s\n' "$evidence" "$head_before" "$tree_before"
git status --porcelain > "$evidence/status-before"
git ls-files -s internal/api/hosting_auto_deploy_browser_integration_test.go web/app/tests/browser/hosting-auto-deploy.spec.ts web/app/scripts/hosting-auto-deploy-fixture.sh web/app/src/api/hosting-github-push.ts web/app/src/pages/ResourceGithubPushBuilds.tsx web/app/src/pages/ResourceAutomaticDeployment.tsx > "$evidence/source-blobs"
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [[ -n "$outer" ]]; then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$outer" 2>/dev/null || true) == "$outer $owner" ]]; then
   if ! docker exec "$outer" sh -ec '
    for exe in /proc/[0-9]*/exe; do
     if [ "$(readlink "$exe" 2>/dev/null || true)" = /fixture/api.test ]; then pid=${exe#/proc/}; pid=${pid%/exe}; kill -TERM "$pid"; fi
    done
    for tick in $(seq 1 200); do
     live=false
     for exe in /proc/[0-9]*/exe; do
      case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd-proxy-fixture) live=true;; esac
     done
     if [ "$live" = false ]; then exit 0; fi
     sleep 0.1
    done
    exit 1'; then
    echo 'owned processes did not join; preserving exact daemon for inspection' >&2
    exit 1
   fi
   docker exec "$outer" docker ps -a --no-trunc --format '{{.ID}} {{.Names}} {{.Status}}' > "$evidence/inner-final" || result=1
   if [[ -n "$pg" ]]; then
    if [[ $(docker exec "$outer" docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$pg" 2>/dev/null || true) == "$pg $owner" ]]; then
     docker exec "$outer" docker rm --force --volumes "$pg" || result=1
    else result=1; fi
   fi
   docker exec "$outer" sh -ec 'if [ -d "$1" ]; then rmdir "$1"; fi' sh "/sys/fs/cgroup/$owner" || result=1
   owned_volumes=$(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}' "$outer")
   docker rm --force --volumes "$outer" || result=1
   for volume in $owned_volumes; do
    if docker volume inspect "$volume" >/dev/null 2>&1; then result=1; else echo "owned anonymous volume absent: $volume"; fi
   done
   if docker inspect "$outer" >/dev/null 2>&1; then result=1; else echo "owned outer absent: $outer"; fi
  else result=1; fi
 fi
 git status --porcelain > "$evidence/status-after"
 git rev-parse HEAD > "$evidence/head-after"
 git rev-parse HEAD^{tree} > "$evidence/tree-after"
 [[ $(cat "$evidence/head-after") == "$head_before" && $(cat "$evidence/tree-after") == "$tree_before" && ! -s "$evidence/status-after" ]] || result=1
 printf 'gate_exit=%s\nouter_id=%s\ninner_pg_id=%s\n' "$result" "$outer" "$pg" > "$evidence/result"
 echo "gate exit=$result evidence=$evidence"
 exit "$result"
}
trap cleanup EXIT INT TERM
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
 arm64|aarch64) goarch=arm64; buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a;;
 amd64|x86_64) goarch=amd64; buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4;;
 *) echo 'unsupported native Docker architecture' >&2; exit 1;;
esac
# An existing immutable fixture binary may be reused when DNS is unavailable.
# Both paths must match the architecture-specific pinned digest below.
if [[ -n "${NEROCD_BUILDX_FIXTURE_BINARY:-}" ]]; then
 [[ -f "$NEROCD_BUILDX_FIXTURE_BINARY" && ! -L "$NEROCD_BUILDX_FIXTURE_BINARY" ]]
 binary_bytes=$(wc -c < "$NEROCD_BUILDX_FIXTURE_BINARY")
 [[ "$binary_bytes" -gt 0 && "$binary_bytes" -le 134217728 ]]
 printf 'local buildx source=%s bytes=%s\n' "$NEROCD_BUILDX_FIXTURE_BINARY" "$binary_bytes" | tee "$evidence/buildx-source"
 cp "$NEROCD_BUILDX_FIXTURE_BINARY" "$evidence/buildx"
else
 curl --fail --silent --show-error --location --connect-timeout 15 --max-time 120 --header "Cache-Control: no-cache" --header "Pragma: no-cache" "https://github.com/docker/buildx/releases/download/v0.38.1/buildx-v0.38.1.linux-$goarch" --output "$evidence/buildx"
fi
printf '%s  %s\n' "$buildx_sha" "$evidence/buildx" | shasum -a 256 --check
chmod 0755 "$evidence/buildx"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -tags nerocd_proxy_fixture -c -o "$evidence/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -tags nerocd_proxy_fixture -trimpath -o "$evidence/nerocd-proxy-fixture" ./cmd/nerocd
shasum -a 256 "$evidence/api.test" "$evidence/nerocd-proxy-fixture" "$evidence/buildx" | tee "$evidence/binary-sha256"
name="$owner-dind"
[[ -z $(docker ps -aq --filter "name=^/$name$") ]]
outer=$(docker run --detach --privileged --publish 127.0.0.1::18080 --add-host github.com:127.0.0.1 --name "$name" --label "$label=$owner" --env DOCKER_TLS_CERTDIR= docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 dockerd --host=unix:///var/run/docker.sock)
docker inspect --format '{{.Id}} pidmode={{.HostConfig.PidMode}} mounts={{json .Mounts}} labels={{json .Config.Labels}} ports={{json .NetworkSettings.Ports}}' "$outer" | tee "$evidence/outer-identity"
ready=false
for _ in $(seq 1 60); do if docker exec "$outer" docker info >/dev/null 2>&1; then ready=true; break; fi; sleep 1; done
[[ "$ready" == true ]]
docker exec "$outer" sh -ec 'test "$(docker info --format "{{.CgroupVersion}}")" = 2; mkdir "$1"; printf "+cpu +memory +pids" > "$1/cgroup.subtree_control"' sh "/sys/fs/cgroup/$owner"
docker exec "$outer" apk add --no-cache git=2.52.0-r0 git-daemon=2.52.0-r0
docker exec "$outer" mkdir -p /fixture /workspace/db/seeds /workspace/internal/api /workspace/acceptance/runtime-proxy-control
docker cp "$evidence/api.test" "$outer:/fixture/api.test"
docker cp "$evidence/nerocd-proxy-fixture" "$outer:/fixture/nerocd-proxy-fixture"
docker cp "$evidence/buildx" "$outer:/fixture/buildx"
docker cp db/seeds/dev.sql "$outer:/workspace/db/seeds/dev.sql"
docker cp acceptance/runtime-proxy-control/pebble.json "$outer:/workspace/acceptance/runtime-proxy-control/pebble.json"
for image in \
 postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94 \
 alpine:3.23.2@sha256:865b95f46d98cf867a156fe4a135ad3fe50d2056aa3f25ed31662dff6da4eb62 \
 moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c \
 nginx:1.29.3-alpine@sha256:b3c656d55d7ad751196f21b7fd2e8d4da9cb430e32f646adcf92441b72f82b14 \
 docker.io/library/traefik@sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368 \
 ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199; do docker exec "$outer" docker pull "$image"; done
planner=$(docker exec "$outer" docker image inspect --format '{{.Id}}' alpine:3.23.2@sha256:865b95f46d98cf867a156fe4a135ad3fe50d2056aa3f25ed31662dff6da4eb62)
# The planner identity is required but Dockerfile strategy never invokes Railpack.
pg=$(docker exec "$outer" docker run --detach --name "$owner-pg" --label "$label=$owner" --network host --tmpfs /var/lib/postgresql/data:rw --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_fixture_only --env POSTGRES_DB=nerocd_auto_deploy postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94)
echo "owned inner PostgreSQL id=$pg owner=$owner private-port=5432 no-published-port"
ready=false
for _ in $(seq 1 60); do
 if [[ $(docker exec "$outer" docker exec --env PGPASSWORD=isolated_fixture_only "$pg" psql --host=127.0.0.1 --username=nerocd --dbname=nerocd_auto_deploy --no-psqlrc --tuples-only --no-align --command='SELECT current_database()' 2>/dev/null || true) == nerocd_auto_deploy ]]; then ready=true; break; fi
 sleep 1
done
[[ "$ready" == true ]]
browser_addr=$(docker port "$outer" 18080/tcp)
[[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
export NEROCD_AUTO_DEPLOY_BROWSER_ORIGIN="http://$browser_addr"
export NEROCD_BUILDX_SHA256="sha256:$buildx_sha" NEROCD_CGROUP_ROOT="/sys/fs/cgroup/$owner" NEROCD_PLANNER_IMAGE="$planner"
export NEROCD_AUTO_DEPLOY_BROWSER_OUTPUT="$evidence/browser-results"
node --input-type=module -e '
import {spawn} from "node:child_process";
import {once} from "node:events";
const env=process.env;
const fixture=spawn("docker",["exec","--workdir","/workspace/internal/api","--env","NEROCD_AUTO_DEPLOY_RUNTIME=disposable-dind","--env","NEROCD_AUTO_DEPLOY_BROWSER=1","--env",`NEROCD_AUTO_DEPLOY_BROWSER_ORIGIN=${env.NEROCD_AUTO_DEPLOY_BROWSER_ORIGIN}`,"--env",`NEROCD_BUILDX_SHA256=${env.NEROCD_BUILDX_SHA256}`,"--env",`NEROCD_CGROUP_ROOT=${env.NEROCD_CGROUP_ROOT}`,"--env",`NEROCD_PLANNER_IMAGE=${env.NEROCD_PLANNER_IMAGE}`,"--env","NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_auto_deploy?sslmode=disable",process.argv[1],"/fixture/api.test","-test.v","-test.run","^TestHostingAutomaticDeploymentBrowser$","-test.timeout=24m"],{stdio:["ignore","pipe","inherit"]});
let browser,browserResult,buffer="",ready=false;
const completed=once(fixture,"exit");
const stop=()=>{if(browser&&browser.exitCode===null)browser.kill("SIGTERM");if(fixture.exitCode===null)fixture.kill("SIGTERM")};
process.once("SIGTERM",stop);process.once("SIGINT",stop);const timer=setTimeout(stop,25*60_000);
fixture.stdout.on("data",chunk=>{buffer+=chunk.toString();let newline;while((newline=buffer.indexOf("\n"))!==-1){const line=buffer.slice(0,newline);buffer=buffer.slice(newline+1);const prefix="AUTO_DEPLOY_BROWSER_READY ";if(!line.startsWith(prefix)){process.stdout.write(line+"\n");continue}if(ready){stop();continue}ready=true;let metadata;try{metadata=JSON.parse(line.slice(prefix.length))}catch{stop();continue}if(metadata.base_url!==env.NEROCD_AUTO_DEPLOY_BROWSER_ORIGIN){stop();continue}
process.stdout.write(`authenticated automatic deployment browser ready at ${metadata.base_url}\n`);
browser=spawn("bun",["run","--cwd","web/app","smoke:browser","hosting-auto-deploy.spec.ts","--project","chromium","--no-deps","--output",env.NEROCD_AUTO_DEPLOY_BROWSER_OUTPUT],{cwd:process.cwd(),env:{...env,PLAYWRIGHT_BASE_URL:metadata.base_url,NEROCD_AUTO_DEPLOY_BROWSER_META:JSON.stringify(metadata)},stdio:"inherit"});browserResult=once(browser,"exit").then(([code])=>{if(code!==0)stop();return code})}});
try{const [fixtureCode]=await completed;if(fixtureCode!==0)stop();const browserCode=browserResult?await browserResult:1;if(!ready||fixtureCode!==0||browserCode!==0)process.exitCode=1}finally{clearTimeout(timer);stop()}
' "$outer"
printf 'actual automatic deployment owner browser/API/compiled agent/fixture-CA HTTPS gate passed\n'
