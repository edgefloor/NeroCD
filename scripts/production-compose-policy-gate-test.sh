#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
gate="$root/scripts/production-compose-policy-gate.sh"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'
temp_root=$(mktemp -d /tmp/nerocd-production-compose-policy.XXXXXXXX)

cleanup() {
  local code=$?
  rm -rf -- "$temp_root"
  exit "$code"
}
trap cleanup EXIT

mkdir "$temp_root/positive" "$temp_root/merge"
cp "$root/compose.production.yaml" "$temp_root/positive/compose.production.yaml"
cp "$root/compose.production.yaml" "$temp_root/merge/compose.production.yaml"
PATH=/usr/bin:/bin bash "$gate" "$temp_root/positive"

awk -v expected="$expected" '
  BEGIN {
    print "x-evil-image: &evil_image"
    print "  image: ghcr.io/example/evil:latest"
  }
  /^  backup-scheduler:$/ { scheduler=1 }
  scheduler && $0 == expected {
    print "    <<: *evil_image"
    scheduler=0
    next
  }
  { print }
  END {
    print "x-padding:"
    print "  nested:"
    print expected
  }
' "$root/compose.production.yaml" >"$temp_root/merge/compose.production.yaml"

if PATH=/usr/bin:/bin bash "$gate" "$temp_root/merge"; then
  printf '%s\n' 'production compose policy test: merge-and-padding bypass was accepted' >&2
  exit 1
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  printf 'owner\n' >"$temp_root/owner"
  printf 'app\n' >"$temp_root/app"
  printf 'postgres\n' >"$temp_root/postgres"
  NEROCD_IMAGE='127.0.0.1:32769/nerocd-gate-0123456789ab@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
    NEROCD_PROXY_NETWORK=proxy NEROCD_PUBLIC_ORIGIN='https://nerocd.example.invalid' \
    NEROCD_OWNER_DATABASE_USER=owner NEROCD_APP_DATABASE_USER=app \
    NEROCD_DATABASE_URL_SECRET="$temp_root/owner" NEROCD_APP_DATABASE_URL_SECRET="$temp_root/app" \
    NEROCD_POSTGRES_PASSWORD_SECRET="$temp_root/postgres" \
    docker compose -f "$temp_root/merge/compose.production.yaml" config >"$temp_root/rendered.yaml"
  if ! awk '/image: ghcr.io\/example\/evil:latest/ { found=1 } END { exit(found ? 0 : 1) }' "$temp_root/rendered.yaml"; then
    printf '%s\n' 'production compose policy test: malicious merge did not render as expected' >&2
    exit 1
  fi
fi
