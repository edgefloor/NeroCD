#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
gate="$root/scripts/production-profile-gate.sh"
temp_root=$(mktemp -d /tmp/nerocd-production-profile-redaction.XXXXXXXX)

cleanup() {
  local code=$?
  rm -rf -- "$temp_root"
  exit "$code"
}
trap cleanup EXIT

owner_password='owner-secret-sentinel-9f4c'
app_password='app-secret-sentinel-72bd'
input="$temp_root/diagnostics.txt"
output="$temp_root/redacted.txt"
printf 'up output postgres://owner:%s@postgres/nerocd\nserver received %s\n' "$owner_password" "$app_password" >"$input"

bash "$gate" --redact-stdin "$owner_password" "$app_password" <"$input" >"$output"

if rg -Fq "$owner_password" "$output" || rg -Fq "$app_password" "$output"; then
  printf '%s\n' 'production profile redaction test: sentinel secret remained in diagnostics' >&2
  exit 1
fi
rg -Fqx 'up output postgres://owner:[REDACTED_OWNER_PASSWORD]@postgres/nerocd' "$output"
rg -Fqx 'server received [REDACTED_APP_PASSWORD]' "$output"
