#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
gate="$root/scripts/production-compose-policy-gate.sh"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'
expected_server_command='    command: [server, --addr, ":8080"]'
temp_root=$(mktemp -d /tmp/nerocd-production-compose-policy.XXXXXXXX)

cleanup() {
  local code=$?
  rm -rf -- "$temp_root"
  exit "$code"
}
trap cleanup EXIT

mkdir "$temp_root/positive" "$temp_root/merge" "$temp_root/merge-whitespace" "$temp_root/unquoted-command" "$temp_root/server-entrypoint"
cp "$root/compose.production.yaml" "$temp_root/positive/compose.production.yaml"
awk -v expected_server_command="$expected_server_command" '
  $0 == expected_server_command { print "    command: [server, --addr, :8080]"; next }
  { print }
' "$root/compose.production.yaml" >"$temp_root/unquoted-command/compose.production.yaml"
PATH=/usr/bin:/bin bash "$gate" "$temp_root/positive"
if PATH=/usr/bin:/bin bash "$gate" "$temp_root/unquoted-command"; then
  printf '%s\n' 'production compose policy test: unquoted server command was accepted' >&2
  exit 1
fi
awk -v expected="$expected" '
  /^  server:$/ { server=1 }
  server && $0 == expected {
    print
    print "    entrypoint: [/bin/sh, -c, \"cat /runtime-app/app_database_url >&2; exec sleep 600\"]"
    server=0
    next
  }
  { print }
' "$root/compose.production.yaml" >"$temp_root/server-entrypoint/compose.production.yaml"
if PATH=/usr/bin:/bin bash "$gate" "$temp_root/server-entrypoint"; then
  printf '%s\n' 'production compose policy test: server entrypoint override was accepted' >&2
  exit 1
fi

write_merge_mutation() {
  local destination=$1 merge_key=$2
  awk -v expected="$expected" -v merge_key="$merge_key" '
  BEGIN {
    print "x-evil-image: &evil_image"
    print "  image: ghcr.io/example/evil:latest"
  }
  /^  backup-scheduler:$/ { scheduler=1 }
  scheduler && $0 == expected {
    print "    " merge_key " *evil_image"
    scheduler=0
    next
  }
  { print }
  END {
    print "x-padding:"
    print "  nested:"
    print expected
  }
  ' "$root/compose.production.yaml" >"$destination/compose.production.yaml"
}

write_merge_mutation "$temp_root/merge" '<<:'
write_merge_mutation "$temp_root/merge-whitespace" '<< :'

for variant in merge merge-whitespace; do
  if PATH=/usr/bin:/bin bash "$gate" "$temp_root/$variant"; then
    printf 'production compose policy test: %s merge-and-padding bypass was accepted\n' "$variant" >&2
    exit 1
  fi
done

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  printf 'owner\n' >"$temp_root/owner"
  printf 'app\n' >"$temp_root/app"
  printf 'postgres\n' >"$temp_root/postgres"
  render_compose() {
    NEROCD_IMAGE='127.0.0.1:32769/nerocd-gate-0123456789ab@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
      NEROCD_PROXY_NETWORK=proxy NEROCD_PUBLIC_ORIGIN='https://nerocd.example.invalid' \
      NEROCD_OWNER_DATABASE_USER=owner NEROCD_APP_DATABASE_USER=app \
      NEROCD_DATABASE_URL_SECRET="$temp_root/owner" NEROCD_APP_DATABASE_URL_SECRET="$temp_root/app" \
      NEROCD_POSTGRES_PASSWORD_SECRET="$temp_root/postgres" \
      docker compose -f "$1" config
  }
  for variant in positive merge merge-whitespace server-entrypoint; do
    render_compose "$temp_root/$variant/compose.production.yaml" >"$temp_root/rendered-$variant.yaml"
    if [[ "$variant" == positive ]]; then
      continue
    fi
    if [[ "$variant" == server-entrypoint ]]; then
      if ! awk '/entrypoint:/ { found=1 } /cat \/runtime-app\/app_database_url/ { payload=1 } END { exit(found && payload ? 0 : 1) }' "$temp_root/rendered-$variant.yaml"; then
        printf '%s\n' 'production compose policy test: server entrypoint override did not render as expected' >&2
        exit 1
      fi
      continue
    fi
    if ! awk '/image: ghcr.io\/example\/evil:latest/ { found=1 } END { exit(found ? 0 : 1) }' "$temp_root/rendered-$variant.yaml"; then
      printf 'production compose policy test: %s malicious merge did not render as expected\n' "$variant" >&2
      exit 1
    fi
  done
fi
