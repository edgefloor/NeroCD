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

for line in {1..82}; do
  printf 'compose-up-line-%03d\n' "$line" >>"$input"
done
printf 'compose-up-line-083 owner=%s app=%s\n' "$owner_password" "$app_password" >>"$input"
bash "$gate" --redact-tail-stdin "$owner_password" "$app_password" <"$input" >"$output"
! rg -Fq 'compose-up-line-001' "$output"
! rg -Fq 'compose-up-line-003' "$output"
rg -Fq 'compose-up-line-004' "$output"
rg -Fqx 'compose-up-line-083 owner=[REDACTED_OWNER_PASSWORD] app=[REDACTED_APP_PASSWORD]' "$output"
if rg -Fq "$owner_password" "$output" || rg -Fq "$app_password" "$output"; then
  printf '%s\n' 'production profile redaction test: bounded diagnostics retained a sentinel secret' >&2
  exit 1
fi

mock_bin="$temp_root/mock-bin"
cleanup_trace="$temp_root/cleanup-trace.txt"
mkdir "$mock_bin"
cat >"$mock_bin/docker" <<'EOF'
#!/bin/sh
printf 'docker %s\n' "$*" >>"$NEROCD_CLEANUP_TEST_TRACE"
case "${NEROCD_CLEANUP_TEST_MODE:-success}:$*" in
  survivor:ps\ -aq\ --filter\ *) printf '%s\n' surviving-container ;;
  survivor:rm\ -f\ surviving-container) exit 1 ;;
esac
exit 0
EOF
chmod +x "$mock_bin/docker"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: pre-compose failure unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'local_registry_cleanup' "$cleanup_trace"
rg -Fq 'docker image rm -f nerocd-production-server:' "$cleanup_trace"
! rg -Fq 'docker compose' "$cleanup_trace"
rg -Fqx 'cleanup_complete=true' /tmp/nerocd-production-profile.txt

: >"$cleanup_trace"
if PATH="$mock_bin:$PATH" NEROCD_CLEANUP_TEST_TRACE="$cleanup_trace" NEROCD_CLEANUP_TEST_MODE=survivor bash "$gate" --cleanup-pre-compose-test "$cleanup_trace"; then
  printf '%s\n' 'production profile cleanup test: surviving resource unexpectedly succeeded' >&2
  exit 1
fi
rg -Fqx 'local_registry_cleanup' "$cleanup_trace"
rg -Fqx 'docker rm -f surviving-container' "$cleanup_trace"
rg -Fqx 'cleanup_complete=false' /tmp/nerocd-production-profile.txt
