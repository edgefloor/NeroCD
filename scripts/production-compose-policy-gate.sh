#!/usr/bin/env bash
set -Eeuo pipefail

root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
compose_file="$root/compose.production.yaml"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'

# Keep server source deliberately frozen: its process and secret-adjacent settings
# must change only with an explicit policy review and fixture update.
IFS= read -r -d '' expected_server_block <<'EOF' || true
  server:
    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"
    command: [server, --addr, ":8080"]
    environment: {NEROCD_MODE: production, NEROCD_IMAGE_REF: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}", NEROCD_PUBLIC_ORIGIN: "${NEROCD_PUBLIC_ORIGIN:?set HTTPS public origin}", NEROCD_TRUSTED_PROXY_CIDRS: "${NEROCD_TRUSTED_PROXY_CIDRS:-}", NEROCD_DATABASE_CREDENTIAL: app, NEROCD_APP_DATABASE_USER: "${NEROCD_APP_DATABASE_USER:?set application database role}", NEROCD_DATABASE_URL_FILE: /runtime-app/app_database_url}
    depends_on: {secret-init: {condition: service_completed_successfully}, postgres: {condition: service_healthy}, migrate: {condition: service_completed_successfully}, role-init: {condition: service_completed_successfully}}
    volumes: [runtime-app-secrets:/runtime-app:ro]
    networks: [private, proxy]
    read_only: true
    tmpfs: [/tmp]
    security_opt: [no-new-privileges:true]
    cap_drop: [ALL]
    restart: unless-stopped
    stop_grace_period: 35s
    pids_limit: 128
    mem_limit: 512m
    cpus: 1.0
    logging: {driver: local, options: {max-size: 10m, max-file: "3"}}
    healthcheck: {test: [CMD, nerocd, ready, --addr, http://127.0.0.1:8080], interval: 10s, timeout: 3s, retries: 6}
EOF
expected_server_block=${expected_server_block%$'\n'}

[[ -f "$compose_file" ]] || { printf '%s\n' 'production compose policy: compose file is missing' >&2; exit 1; }
actual_server_block=$(awk '
  $0 == "services:" { in_services = 1; next }
  in_services && /^[^[:space:]#][^:]*:/ { in_services = 0 }
  in_services && $0 == "  server:" { capture = 1 }
  capture && $0 == "  probe:" { exit }
  capture { print }
' "$compose_file")
if [[ "$actual_server_block" != "$expected_server_block" ]]; then
  printf '%s\n' 'production compose policy: server service block differs from the approved canonical configuration' >&2
  exit 1
fi

awk -v expected="$expected" '
  BEGIN {
    services = "secret-init backup-data-init migrate role-init server probe database-tools backup-scheduler"
    count = split(services, names, " ")
    for (position = 1; position <= count; position++) required[names[position]] = 1
  }
  $0 == "services:" {
    in_services = 1
    services_seen = 1
    service = ""
    next
  }
  in_services && /^[^[:space:]#][^:]*:/ {
    in_services = 0
    service = ""
  }
  in_services && /^  [a-z0-9-]+:$/ {
    service = substr($0, 3, length($0) - 3)
    next
  }
  /^[[:space:]]*image:[[:space:]].*NEROCD_IMAGE/ { image_count++ }
  $0 == expected {
    quoted_count++
  }
  in_services && service in required && /^    <<[[:space:]]*:/ {
    merge_keys[service]++
  }
  in_services && service in required && /^    image:[[:space:]]/ {
    direct_images[service]++
    if ($0 == expected) exact_images[service]++
  }
  END {
    if (!services_seen) {
      print "production compose policy: top-level services mapping is missing" > "/dev/stderr"
      exit 1
    }
    if (image_count != 8) {
      print "production compose policy: expected exactly 8 NEROCD_IMAGE image fields" > "/dev/stderr"
      exit 1
    }
    if (quoted_count != 8) {
      print "production compose policy: every NEROCD_IMAGE image field must be quoted" > "/dev/stderr"
      exit 1
    }
    for (position = 1; position <= count; position++) {
      if (merge_keys[names[position]] != 0) {
        print "production compose policy: " names[position] " must not use YAML image merges" > "/dev/stderr"
        exit 1
      }
      if (direct_images[names[position]] != 1 || exact_images[names[position]] != 1) {
        print "production compose policy: " names[position] " lacks exactly one direct quoted image field" > "/dev/stderr"
        exit 1
      }
    }
  }
' "$compose_file"
