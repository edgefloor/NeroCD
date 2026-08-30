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

mkdir "$temp_root/positive" "$temp_root/merge" "$temp_root/merge-whitespace"
cp "$root/compose.production.yaml" "$temp_root/positive/compose.production.yaml"
PATH=/usr/bin:/bin bash "$gate" "$temp_root/positive"

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
  for variant in merge merge-whitespace; do
    render_compose "$temp_root/$variant/compose.production.yaml" >"$temp_root/rendered-$variant.yaml"
    if ! awk '/image: ghcr.io\/example\/evil:latest/ { found=1 } END { exit(found ? 0 : 1) }' "$temp_root/rendered-$variant.yaml"; then
      printf 'production compose policy test: %s malicious merge did not render as expected\n' "$variant" >&2
      exit 1
    fi
  done
fi
