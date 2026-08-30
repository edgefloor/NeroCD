#!/bin/sh
set -eu

browser_port=${1:?browser port is required}
browser_origin=${2:?browser origin is required}
browser_runtime_dir=${3:?browser runtime directory is required}
browser_database="nerocd_browser_${browser_port}"
browser_container="nerocd-browser-postgres-${browser_port}"
browser_server_pid=""

cleanup() {
  browser_status=$?
  trap - EXIT HUP INT TERM
  if [ -n "$browser_server_pid" ]; then
    kill "$browser_server_pid" >/dev/null 2>&1 || true
    wait "$browser_server_pid" >/dev/null 2>&1 || true
  fi
  docker rm -f "$browser_container" >/dev/null 2>&1 || true
  rm -rf -- "$browser_runtime_dir"
  exit "$browser_status"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

umask 077
mkdir -p "$browser_runtime_dir"
browser_email="browser-$(od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')@example.invalid"
browser_password=$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')
printf %s "$browser_password" >"$browser_runtime_dir/password"
printf '%s\n%s\n' "$browser_email" "$browser_password" >"$browser_runtime_dir/credentials"

docker run -d --rm --name "$browser_container" \
  -e POSTGRES_DB="$browser_database" \
  -e POSTGRES_USER=nerocd \
  -e POSTGRES_PASSWORD=nerocd_browser \
  -p 127.0.0.1::5432 \
  postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94 >/dev/null

for _ in $(seq 1 30); do
  if docker exec "$browser_container" pg_isready -U nerocd -d "$browser_database" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$browser_container" pg_isready -U nerocd -d "$browser_database" >/dev/null

browser_database_port=$(docker port "$browser_container" 5432/tcp | tail -1 | sed 's/.*://')
browser_database_url="postgres://nerocd:nerocd_browser@127.0.0.1:${browser_database_port}/${browser_database}?sslmode=disable"
printf %s "$browser_database_url" >"$browser_runtime_dir/database-url"

browser_root=$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)
cd "$browser_root"
NEROCD_DATABASE_URL="$browser_database_url" GOCACHE=/private/tmp/nerocd-gocache go run ./cmd/nerocd migrate
NEROCD_DATABASE_URL="$browser_database_url" NEROCD_COOKIE_SECURE=false NEROCD_PUBLIC_ORIGIN="$browser_origin" GOCACHE=/private/tmp/nerocd-gocache go run ./cmd/nerocd server --addr "127.0.0.1:${browser_port}" &
browser_server_pid=$!
wait "$browser_server_pid"
