#!/usr/bin/env bash
set -euo pipefail
repo_root=$(pwd -P)
test -f "$repo_root/internal/api/hosting_runtime_logs_browser_integration_test.go"
python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",32963));s.close()'
fixture_dir=$(mktemp -d /tmp/nerocd-runtime-logs-browser.XXXXXX)
owner=$(basename "$fixture_dir" | tr '[:upper:]' '[:lower:]')
label=io.nerocd.runtime-logs-browser.owner
pg_id=''
inventory(){ docker ps -a --no-trunc --format '{{.ID}} {{.Names}}'; }
cleanup(){
 result=$?;trap - EXIT INT TERM
 if [[ -n "$pg_id" ]];then
  if [[ $(docker inspect --format "{{.Id}} {{index .Config.Labels \"$label\"}}" "$pg_id" 2>/dev/null || true) == "$pg_id $owner" ]];then
   printf 'owned PostgreSQL schemas after fixture\n'
   schemas=$(docker exec "$pg_id" psql -U nerocd -d nerocd --no-psqlrc -Atc "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY nspname") || result=1
   printf '%s\n' "$schemas";[[ "$schemas" == public ]] || result=1
   docker rm --force --volumes "$pg_id" >/dev/null || result=1
   if docker inspect "$pg_id" >/dev/null 2>&1;then result=1;else printf 'owned PostgreSQL removed %s\n' "$pg_id";fi
  else result=1;fi
 fi
 printf 'container inventory after\n';inventory || result=1
 rm -rf "$fixture_dir"
 exit "$result"
}
trap cleanup EXIT INT TERM
printf 'container inventory before\n';inventory
go test -race -c -o "$fixture_dir/api.test" ./internal/api
go build -trimpath -o "$fixture_dir/nerocd" ./cmd/nerocd
shasum -a 256 "$fixture_dir/api.test" "$fixture_dir/nerocd"
"$fixture_dir/nerocd" contract -openapi openapi.yaml
pg_id=$(docker run --detach --name "$owner-pg" --label "$label=$owner" --publish 127.0.0.1:32963:5432 --env POSTGRES_USER=nerocd --env POSTGRES_PASSWORD=isolated_logs_browser --env POSTGRES_DB=nerocd postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94)
printf 'owned PostgreSQL id=%s owner=%s port=127.0.0.1:32963\n' "$pg_id" "$owner"
ready=false
for _ in $(seq 1 60);do if docker exec "$pg_id" pg_isready --host=127.0.0.1 --username=nerocd --dbname=nerocd >/dev/null 2>&1;then ready=true;break;fi;sleep 1;done
[[ "$ready" == true ]]
printf 'owned PostgreSQL schemas before fixture\n'
docker exec "$pg_id" psql -U nerocd -d nerocd --no-psqlrc -Atc "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema' ORDER BY nspname"
export NEROCD_TEST_DATABASE_URL='postgres://nerocd:isolated_logs_browser@127.0.0.1:32963/nerocd?sslmode=disable'
NEROCD_RUNTIME_LOGS_BROWSER=0 go test -race -v ./internal/domain ./internal/app ./internal/store ./internal/runnerclient ./internal/api -run 'HostingRuntimeLog|RuntimeLog' -count=1
node --input-type=module -e '
import { spawn } from "node:child_process";
import { once } from "node:events";
const fixture = spawn(process.argv[1], ["-test.v", "-test.run", "^TestHostingRuntimeLogsBrowser$", "-test.timeout=5m"], { cwd: process.cwd()+"/internal/api", env: {...process.env,NEROCD_RUNTIME_LOGS_BROWSER:"1"}, stdio:["ignore","pipe","inherit"] });
let browser, browserResult, buffer="", ready=false;
const completed=once(fixture,"exit");
const stop=()=>{if(browser&&browser.exitCode===null)browser.kill("SIGTERM");if(fixture.exitCode===null)fixture.kill("SIGTERM");};
process.once("SIGTERM",stop);process.once("SIGINT",stop);
const watchdog=setTimeout(stop,6*60_000);
fixture.stdout.on("data",chunk=>{
 buffer+=chunk.toString();let newline;
 while((newline=buffer.indexOf("\n"))!==-1){
  const line=buffer.slice(0,newline);buffer=buffer.slice(newline+1);const prefix="RUNTIME_LOGS_BROWSER_READY ";
  if(!line.startsWith(prefix)){process.stdout.write(line+"\n");continue;}
  if(ready){stop();continue;}ready=true;
  let meta;try{meta=JSON.parse(line.slice(prefix.length));}catch{stop();continue;}
  if(!/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(meta.origin)){stop();continue;}
  process.stdout.write(`runtime logs simulated-producer browser ready at ${meta.origin}\n`);
  browser=spawn("bun",["run","--cwd","web/app","smoke:browser","hosting-runtime-logs.spec.ts","--project","chromium","--no-deps"],{cwd:process.cwd(),env:{...process.env,PLAYWRIGHT_BASE_URL:meta.origin,NEROCD_RUNTIME_LOGS_BROWSER_META:JSON.stringify(meta)},stdio:"inherit"});
  browserResult=once(browser,"exit").then(([code])=>code);
 }
});
try{const[code]=await completed;if(code!==0)stop();const browserCode=browserResult?await browserResult:1;if(!ready||code!==0||browserCode!==0)process.exitCode=1;}finally{clearTimeout(watchdog);stop();}
' "$fixture_dir/api.test"
printf 'runtime logs real API/browser simulated-producer gate passed\n'
