#!/bin/sh
set -eu

browser_stage=argument-validation
browser_port=${1:?browser port is required}
browser_origin=${2:?browser origin is required}
if [ "$#" -ne 2 ]; then
  echo "browser server accepts only a port and origin" >&2
  exit 2
fi

case "$browser_port" in
  ''|*[!0-9]*) echo "browser port must be numeric" >&2; exit 2 ;;
esac
if [ "$browser_port" -lt 1024 ] || [ "$browser_port" -gt 65535 ]; then
  echo "browser port must be between 1024 and 65535" >&2
  exit 2
fi

browser_run_id=${NEROCD_BROWSER_RUN_ID:-$(od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')}
case "$browser_run_id" in
  ????????*) ;;
  *) echo "browser run identifier is invalid" >&2; exit 2 ;;
esac
case "$browser_run_id" in
  *[!A-Za-z0-9_-]*) echo "browser run identifier is invalid" >&2; exit 2 ;;
esac

browser_runtime_base=/tmp
browser_database="nerocd_browser_${browser_port}_${browser_run_id}"
browser_container_name="nerocd-browser-postgres-${browser_port}-${browser_run_id}"
browser_container_label="nerocd.browser.fixture=${browser_run_id}"
browser_runtime_dir=""
browser_runtime_link="$browser_runtime_base/nerocd-browser-${browser_run_id}"
browser_container_id=""
browser_server_pid=""
browser_oidc_pid=""
browser_term_requested=false

browser_postgres_ready() {
  docker exec "$browser_container_id" pg_isready -h 127.0.0.1 -U nerocd -d "$browser_database" >/dev/null 2>&1
}

stop_server() {
  [ -n "$browser_server_pid" ] || return 0
  if kill -0 "$browser_server_pid" >/dev/null 2>&1; then
    kill -TERM "$browser_server_pid" >/dev/null 2>&1 || true
    browser_stop_attempt=0
    while kill -0 "$browser_server_pid" >/dev/null 2>&1 && [ "$browser_stop_attempt" -lt 5 ]; do
      sleep 1
      browser_stop_attempt=$((browser_stop_attempt + 1))
    done
    kill -0 "$browser_server_pid" >/dev/null 2>&1 && kill -KILL "$browser_server_pid" >/dev/null 2>&1 || true
  fi
  wait "$browser_server_pid" >/dev/null 2>&1 || true
  browser_server_pid=""
}

stop_oidc_provider() {
  [ -n "$browser_oidc_pid" ] || return 0
  if kill -0 "$browser_oidc_pid" >/dev/null 2>&1; then
    kill -TERM "$browser_oidc_pid" >/dev/null 2>&1 || true
  fi
  wait "$browser_oidc_pid" >/dev/null 2>&1 || true
  browser_oidc_pid=""
}

cleanup() {
  browser_status=$?
  trap - 0 HUP INT TERM
  stop_server
  stop_oidc_provider
  if [ -n "$browser_container_id" ]; then
    docker rm -f "$browser_container_id" >/dev/null 2>&1 || true
  fi
  if [ -L "$browser_runtime_link" ] && [ "$(readlink "$browser_runtime_link")" = "$browser_runtime_dir" ]; then
    rm -f -- "$browser_runtime_link"
  fi
  case "$browser_runtime_dir" in
    "$browser_runtime_base"/nerocd-browser-"$browser_port"-"$browser_run_id".*) rm -rf -- "$browser_runtime_dir" ;;
  esac
  if [ "$browser_status" -ne 0 ] && { [ "$browser_term_requested" != true ] || [ "$browser_stage" != server ] || [ "$browser_status" -ne 143 ]; }; then
    printf 'browser server failed: stage=%s status=%s\n' "$browser_stage" "$browser_status" >&2
  fi
  exit "$browser_status"
}

trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'browser_term_requested=true; exit 143' TERM

browser_stage=runtime-setup
browser_runtime_dir=$(mktemp -d "$browser_runtime_base/nerocd-browser-${browser_port}-${browser_run_id}.XXXXXXXX")
case "$browser_runtime_dir" in
  "$browser_runtime_base"/nerocd-browser-"$browser_port"-"$browser_run_id".*) ;;
  *) echo "browser runtime directory is outside the approved temporary prefix" >&2; exit 2 ;;
esac
[ -d "$browser_runtime_dir" ] || exit 2
ln -s "$browser_runtime_dir" "$browser_runtime_link"

umask 077
browser_email="browser-$(od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')@example.invalid"
browser_password=$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')
printf %s "$browser_password" >"$browser_runtime_dir/password"
printf '%s\n%s\n' "$browser_email" "$browser_password" >"$browser_runtime_dir/credentials"
browser_oidc_secret=$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')
printf %s "$browser_oidc_secret" >"$browser_runtime_dir/oidc-client-secret"

browser_stage=postgres-start
browser_container_id=$(docker run -d --rm --name "$browser_container_name" --label "$browser_container_label" \
  -e POSTGRES_DB="$browser_database" \
  -e POSTGRES_USER=nerocd \
  -e POSTGRES_PASSWORD=nerocd_browser \
  -p 127.0.0.1::5432 \
  postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94)
browser_inspected_id=$(docker inspect --format '{{.Id}}' "$browser_container_id")
[ "$browser_inspected_id" = "$browser_container_id" ] || exit 1

browser_stage=postgres-readiness
for _ in $(seq 1 30); do
  if browser_postgres_ready; then break; fi
  sleep 1
done
browser_postgres_ready

browser_stage=postgres-port
browser_database_port=$(docker port "$browser_container_id" 5432/tcp | tail -1 | sed 's/.*://')
browser_database_url="postgres://nerocd:nerocd_browser@127.0.0.1:${browser_database_port}/${browser_database}?sslmode=disable"
printf %s "$browser_database_url" >"$browser_runtime_dir/database-url"

browser_root=$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)
cd "$browser_root"
browser_stage=oidc-provider-start
node web/app/scripts/oidc-provider.mjs "$browser_origin" "$browser_runtime_dir/oidc-provider.json" "$browser_runtime_dir/oidc-client-secret" &
browser_oidc_pid=$!
for _ in $(seq 1 30); do
  [ -s "$browser_runtime_dir/oidc-provider.json" ] && break
  kill -0 "$browser_oidc_pid" >/dev/null 2>&1 || exit 1
  sleep 1
done
[ -s "$browser_runtime_dir/oidc-provider.json" ] || exit 1
browser_oidc_issuer=$(node -e 'const fs=require("fs"); const value=JSON.parse(fs.readFileSync(process.argv[1],"utf8")).issuer; if(typeof value!=="string") process.exit(2); process.stdout.write(value)' "$browser_runtime_dir/oidc-provider.json")
browser_stage=build
GOCACHE="$browser_runtime_dir/go-cache" go build -o "$browser_runtime_dir/nerocd" ./cmd/nerocd
browser_stage=migrate
NEROCD_DATABASE_URL="$browser_database_url" "$browser_runtime_dir/nerocd" migrate
browser_stage=oidc-provision
NEROCD_MODE=development NEROCD_DATABASE_URL="$browser_database_url" "$browser_runtime_dir/nerocd" oidc-provision --issuer "$browser_oidc_issuer" --subject nerocd-browser-oidc-subject --email oidc-browser@example.invalid --name "OIDC Browser User"
browser_stage=server
NEROCD_MODE=development NEROCD_DATABASE_URL="$browser_database_url" NEROCD_COOKIE_SECURE=false NEROCD_PUBLIC_ORIGIN="$browser_origin" NEROCD_OIDC_ISSUER_URL="$browser_oidc_issuer" NEROCD_OIDC_CLIENT_ID=nerocd-browser NEROCD_OIDC_CLIENT_SECRET_FILE="$browser_runtime_dir/oidc-client-secret" "$browser_runtime_dir/nerocd" server --addr "127.0.0.1:${browser_port}" &
browser_server_pid=$!
wait "$browser_server_pid"
