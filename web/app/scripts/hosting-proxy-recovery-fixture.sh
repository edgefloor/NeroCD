#!/usr/bin/env bash
# Two separately owned Linux daemons: unchanged controller gate, then real UI gate.
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_proxy_recovery_browser_integration_test.go"
fixture_dir=$(mktemp -d /tmp/nerocd-proxy-recovery.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.proxy-recovery.owner
container_id=''
owned_volumes=''
cleanup_container(){
 if [[ -n "$container_id" ]];then
  local identity volumes volume volume_identity remaining
  if ! identity=$(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$container_id") || [[ "$identity" != "$container_id $owner" ]];then
   printf 'owned fixture identity unavailable or mismatched; cleanup refused\n' >&2
   return 1
  fi
  if ! volumes=$(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}' "$container_id") || [[ "$volumes" != "$owned_volumes" ]];then
   printf 'owned volume inventory unavailable or mismatched; cleanup refused\n' >&2
   return 1
  fi
  for volume in $owned_volumes;do
   if ! volume_identity=$(docker volume inspect --format '{{.Name}}' "$volume") || [[ "$volume_identity" != "$volume" ]];then
    printf 'owned volume identity unavailable or mismatched; cleanup refused\n' >&2
    return 1
   fi
  done
  if ! docker inspect --format 'owned final mounts={{json .Mounts}} ports={{json .NetworkSettings.Ports}}' "$container_id";then return 1;fi
  if ! docker rm --force --volumes "$container_id" >/dev/null;then return 1;fi
  if ! remaining=$(docker container ls --all --quiet --no-trunc --filter "id=$container_id") || [[ -n "$remaining" ]];then return 1;fi
  for volume in $owned_volumes;do
   if ! remaining=$(docker volume ls --quiet --filter "name=$volume") || [[ -n "$remaining" ]];then return 1;fi
   printf 'owned anonymous volume absent %s\n' "$volume"
  done
  printf 'owned DinD removed %s owner=%s\n' "$container_id" "$owner"
  container_id=''
  owned_volumes=''
 fi
}
cleanup(){
 result=$?;trap - EXIT INT TERM
 cleanup_container || result=1
 rm -rf "$fixture_dir"
 printf 'proxy recovery fixture cleanup exit=%s\n' "$result"
 exit "$result"
}
trap cleanup EXIT INT TERM
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) goarch=arm64;;x86_64|amd64) goarch=amd64;;*) exit 1;;esac
printf 'fixture source SHA=%s owner=%s arch=%s\n' "$(git rev-parse HEAD)" "$owner" "$goarch"
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -tags nerocd_proxy_fixture -c -o "$fixture_dir/api.test" ./internal/api
GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -tags nerocd_proxy_fixture -o "$fixture_dir/nerocd-proxy-fixture" ./cmd/nerocd
for version in A B;do GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$version" -o "$fixture_dir/backend-$version" ./acceptance/runtime-proxy-control/backend;done
shasum -a 256 "$fixture_dir/api.test" "$fixture_dir/nerocd-proxy-fixture" "$fixture_dir/backend-A" "$fixture_dir/backend-B"
for stage in controller browser;do
 container_id=$(docker run --detach --privileged --name "$owner-$stage" --label "$label=$owner" --publish 127.0.0.1::18082 --env DOCKER_TLS_CERTDIR= --mount "type=bind,src=$repo_root,dst=/workspace,readonly" --mount "type=bind,src=$fixture_dir,dst=/fixture,readonly" docker.io/library/docker@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489 --host=unix:///var/run/docker.sock)
 owned_volumes=$(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}' "$container_id")
 printf 'owned %s id=%s owner=%s\n' "$stage" "$container_id" "$owner"
 ready=false
 for _ in $(seq 1 60);do if docker exec "$container_id" docker info >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
 [[ "$ready" == true ]]
 docker inspect --format 'owned mounts={{json .Mounts}} ports={{json .NetworkSettings.Ports}}' "$container_id"
 docker exec "$container_id" docker version --format 'Docker client={{.Client.Version}} server={{.Server.Version}}'
 docker exec "$container_id" docker run -d --name fixture-registry --network host -e REGISTRY_HTTP_ADDR=127.0.0.1:25000 registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
 docker exec "$container_id" sh -ec 'mkdir /payload;printf "FROM scratch\nCOPY server /server\nUSER 65532:65532\nENTRYPOINT [\"/server\"]\n" >/payload/Dockerfile'
 for version in A B;do
  repository=$(printf '%s' "$version" | tr '[:upper:]' '[:lower:]')
  docker exec "$container_id" sh -ec "cp /fixture/backend-$version /payload/server;chmod 755 /payload/server"
  docker exec --env DOCKER_BUILDKIT=0 "$container_id" docker build --quiet -t "127.0.0.1:25000/backend-$repository:fixture" /payload
  docker exec "$container_id" docker push "127.0.0.1:25000/backend-$repository:fixture"
 done
 docker exec "$container_id" docker run -d --name fixture-postgres --network host -e POSTGRES_PASSWORD=proxy-fixture-only -e POSTGRES_DB=proxy_fixture postgres@sha256:b8faeff7712663c2773a190181e1246e37e364b887611c04658c28b44e2408fe
 ready=false
 for _ in $(seq 1 60);do if docker exec "$container_id" docker exec fixture-postgres pg_isready -U postgres -d proxy_fixture >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
 [[ "$ready" == true ]]
 if [[ "$stage" == controller ]];then
  docker exec --workdir /workspace/internal/api --env NEROCD_PROXY_COMPILED_FIXTURE=disposable-dind --env 'NEROCD_TEST_DATABASE_URL=postgres://postgres:proxy-fixture-only@127.0.0.1:5432/proxy_fixture?sslmode=disable' "$container_id" /fixture/api.test -test.v -test.run '^TestHostingProxyMetadataRecoveryCompiledRunner$' -test.timeout=3m
 else
  browser_addr=$(docker port "$container_id" 18082/tcp);[[ "$browser_addr" =~ ^127\.0\.0\.1:[0-9]+$ ]]
  export NEROCD_PROXY_RECOVERY_BROWSER_ORIGIN="http://$browser_addr"
  node --input-type=module -e '
import {spawn} from "node:child_process";
import {once} from "node:events";
const env=process.env;
const fixture=spawn("docker",["exec","--workdir","/workspace/internal/api","--env","NEROCD_PROXY_COMPILED_FIXTURE=disposable-dind","--env","NEROCD_PROXY_RECOVERY_BROWSER=1","--env",`NEROCD_PROXY_RECOVERY_BROWSER_ORIGIN=${env.NEROCD_PROXY_RECOVERY_BROWSER_ORIGIN}`,"--env","NEROCD_TEST_DATABASE_URL=postgres://postgres:proxy-fixture-only@127.0.0.1:5432/proxy_fixture?sslmode=disable",process.argv[1],"/fixture/api.test","-test.v","-test.run","^TestHostingProxyMetadataRecoveryCompiledRunner$","-test.timeout=7m"],{stdio:["ignore","pipe","inherit"]});
let browser,browserResult,buffer="",ready=false;
const completed=once(fixture,"exit");
const stop=()=>{if(browser&&browser.exitCode===null)browser.kill("SIGTERM");if(fixture.exitCode===null)fixture.kill("SIGTERM")};
process.once("SIGTERM",stop);process.once("SIGINT",stop);const timer=setTimeout(stop,8*60_000);
fixture.stdout.on("data",chunk=>{buffer+=chunk.toString();let newline;while((newline=buffer.indexOf("\n"))!==-1){const line=buffer.slice(0,newline);buffer=buffer.slice(newline+1);const prefix="PROXY_RECOVERY_BROWSER_READY ";if(!line.startsWith(prefix)){process.stdout.write(line+"\n");continue}if(ready){stop();continue}ready=true;let metadata;try{metadata=JSON.parse(line.slice(prefix.length))}catch{stop();continue}if(metadata.origin!==env.NEROCD_PROXY_RECOVERY_BROWSER_ORIGIN){stop();continue}
process.stdout.write(`authenticated recovery browser ready at ${metadata.origin}\n`);
// Dedicated actual-agent fixture has its own authenticated setup, independent of
// the generic OIDC fixture dependency. No application API response is fabricated.
browser=spawn("bun",["run","--cwd","web/app","smoke:browser","hosting-proxy-recovery.spec.ts","--project","chromium","--no-deps","--output","../../.cache/proxy-recovery-ui/browser-results"],{cwd:process.cwd(),env:{...env,PLAYWRIGHT_BASE_URL:metadata.origin,NEROCD_PROXY_RECOVERY_BROWSER_META:JSON.stringify(metadata)},stdio:"inherit"});browserResult=once(browser,"exit").then(([code])=>code)}});
try{const [fixtureCode]=await completed;if(fixtureCode!==0)stop();const browserCode=browserResult?await browserResult:1;if(!ready||fixtureCode!==0||browserCode!==0)process.exitCode=1}finally{clearTimeout(timer);stop()}
' "$container_id"
 fi
 docker exec "$container_id" docker ps -a --no-trunc --format '{{.ID}} {{.Names}} {{.Labels}}'
 docker exec "$container_id" docker volume ls
 cleanup_container
done
printf 'original controller and actual-agent browser recovery gates passed\n'
