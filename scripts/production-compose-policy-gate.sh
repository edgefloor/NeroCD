#!/usr/bin/env bash
set -Eeuo pipefail

root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
compose_file="$root/compose.production.yaml"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'

# Keep all source-secret ingress and the long-lived PostgreSQL/server consumers
# deliberately frozen. These blocks must change only with an explicit policy
# review and fixture update.
IFS= read -r -d '' expected_secret_init_block <<'EOF' || true
  secret-init:
    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"
    user: "0:0"
    entrypoint: [/bin/sh, -ec]
    command: ["umask 077; install -d -m 0700 /runtime-owner /runtime-app; install -m 0400 /run/secrets/owner_database_url /runtime-owner/owner_database_url; install -m 0400 /run/secrets/app_database_url /runtime-app/app_database_url; chown 10001:10001 /runtime-owner/owner_database_url /runtime-app/app_database_url /runtime-owner /runtime-app"]
    secrets: [owner_database_url, app_database_url]
    volumes: [runtime-owner-secrets:/runtime-owner, runtime-app-secrets:/runtime-app]
    network_mode: none
    read_only: true
    tmpfs: [/tmp]
    security_opt: [no-new-privileges:true]
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]
    restart: "no"
    pids_limit: 32
    mem_limit: 64m
    cpus: 0.25
    logging: {driver: local, options: {max-size: 1m, max-file: "2"}}
EOF
expected_secret_init_block=${expected_secret_init_block%$'\n'}

IFS= read -r -d '' expected_postgres_secret_init_block <<'EOF' || true
  postgres-secret-init:
    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"
    user: "0:0"
    entrypoint: [/bin/sh, -ec]
    command: ["umask 077; install -d -m 0700 /runtime-postgres; install -m 0400 /run/secrets/postgres_password /runtime-postgres/postgres_password; chown 70:70 /runtime-postgres/postgres_password /runtime-postgres"]
    secrets: [postgres_password]
    volumes: [runtime-postgres-secrets:/runtime-postgres]
    network_mode: none
    read_only: true
    tmpfs: [/tmp]
    security_opt: [no-new-privileges:true]
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]
    restart: "no"
    pids_limit: 32
    mem_limit: 64m
    cpus: 0.25
    logging: {driver: local, options: {max-size: 1m, max-file: "2"}}
EOF
expected_postgres_secret_init_block=${expected_postgres_secret_init_block%$'\n'}

IFS= read -r -d '' expected_postgres_block <<'EOF' || true
  postgres:
    image: postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94
    user: "70:70"
    environment: {POSTGRES_DB: nerocd, POSTGRES_USER: "${NEROCD_OWNER_DATABASE_USER:?set migration owner role}", POSTGRES_PASSWORD_FILE: /runtime-postgres/postgres_password, POSTGRES_HOST_AUTH_METHOD: scram-sha-256, PGDATA: /var/lib/postgresql/data/pgdata}
    depends_on: {postgres-secret-init: {condition: service_completed_successfully}, pgdata-init: {condition: service_completed_successfully}}
    volumes: [postgres-data:/var/lib/postgresql/data, postgres-run:/var/run/postgresql, runtime-postgres-secrets:/runtime-postgres:ro]
    networks: [private]
    read_only: true
    tmpfs: [/tmp]
    security_opt: [no-new-privileges:true]
    cap_drop: [ALL]
    restart: unless-stopped
    pids_limit: 128
    mem_limit: 512m
    cpus: 0.50
    logging: {driver: local, options: {max-size: 10m, max-file: "3"}}
    healthcheck: {test: [CMD-SHELL, 'pg_isready -U "$${POSTGRES_USER}" -d "$${POSTGRES_DB}"'], interval: 5s, timeout: 3s, retries: 12}
EOF
expected_postgres_block=${expected_postgres_block%$'\n'}

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

IFS= read -r -d '' expected_secret_volume_block <<'EOF' || true
secrets:
  owner_database_url: {file: "${NEROCD_DATABASE_URL_SECRET:?set owner secret file}"}
  app_database_url: {file: "${NEROCD_APP_DATABASE_URL_SECRET:?set app secret file}"}
  postgres_password: {file: "${NEROCD_POSTGRES_PASSWORD_SECRET:?set secret file}"}
volumes:
  postgres-data: {}
  postgres-run: {}
  runtime-owner-secrets: {}
  runtime-app-secrets: {}
  runtime-postgres-secrets: {}
  backup-data: {}
EOF
expected_secret_volume_block=${expected_secret_volume_block%$'\n'}

[[ -f "$compose_file" ]] || { printf '%s\n' 'production compose policy: compose file is missing' >&2; exit 1; }
service_block() {
  local service=$1
  awk -v target="$service" '
  $0 == "services:" { in_services = 1; next }
  in_services && /^[^[:space:]#][^:]*:/ { in_services = 0 }
  in_services && $0 == "  " target ":" { capture = 1 }
  capture && $0 ~ /^  [a-z0-9-]+:$/ && $0 != "  " target ":" { exit }
  capture { print }
  ' "$compose_file"
}
require_exact_service() {
  local service=$1 expected_block=$2 actual_block
  actual_block=$(service_block "$service")
  if [[ "$actual_block" != "$expected_block" ]]; then
    printf 'production compose policy: %s service block differs from the approved canonical configuration\n' "$service" >&2
    exit 1
  fi
}
require_exact_service secret-init "$expected_secret_init_block"
require_exact_service postgres-secret-init "$expected_postgres_secret_init_block"
require_exact_service postgres "$expected_postgres_block"
require_exact_service server "$expected_server_block"
actual_secret_volume_block=$(awk '
  $0 == "secrets:" { capture=1 }
  capture && $0 == "networks:" { exit }
  capture { print }
' "$compose_file")
if [[ "$actual_secret_volume_block" != "$expected_secret_volume_block" ]]; then
  printf '%s\n' 'production compose policy: source descriptors or private volume declarations differ from the approved canonical configuration' >&2
  exit 1
fi

awk -v expected="$expected" '
  BEGIN {
    services = "secret-init postgres-secret-init backup-data-init migrate role-init server probe database-tools backup-scheduler"
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
  in_services && /^    secrets:[[:space:]]/ {
    if (service == "secret-init" && $0 == "    secrets: [owner_database_url, app_database_url]") {
      source_secret_fields++
    } else if (service == "postgres-secret-init" && $0 == "    secrets: [postgres_password]") {
      source_secret_fields++
    } else {
      print "production compose policy: only the two ingress services may mount source secrets" > "/dev/stderr"
      exit 1
    }
  }
  in_services && /DAC_OVERRIDE/ {
    if (!((service == "secret-init" || service == "postgres-secret-init") && $0 == "    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]")) {
      print "production compose policy: DAC_OVERRIDE is confined to exact ingress capability sets" > "/dev/stderr"
      exit 1
    }
    dac_override_fields++
  }
  in_services && /^    cap_add:/ {
    if ((service == "secret-init" || service == "postgres-secret-init") && $0 == "    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]") {
      cap_add_fields++
    } else if ((service == "pgdata-init" || service == "backup-data-init") && $0 == "    cap_add: [CHOWN, FOWNER]") {
      cap_add_fields++
    } else {
      print "production compose policy: capability additions differ from the approved initializer sets" > "/dev/stderr"
      exit 1
    }
  }
  in_services && /^    volumes:/ {
    allowed_volume = 0
    if (service == "secret-init" && $0 == "    volumes: [runtime-owner-secrets:/runtime-owner, runtime-app-secrets:/runtime-app]") allowed_volume = 1
    if (service == "postgres-secret-init" && $0 == "    volumes: [runtime-postgres-secrets:/runtime-postgres]") allowed_volume = 1
    if (service == "pgdata-init" && $0 == "    volumes: [postgres-data:/var/lib/postgresql/data, postgres-run:/postgres-run]") allowed_volume = 1
    if (service == "backup-data-init" && $0 == "    volumes: [backup-data:/backups]") allowed_volume = 1
    if (service == "postgres" && $0 == "    volumes: [postgres-data:/var/lib/postgresql/data, postgres-run:/var/run/postgresql, runtime-postgres-secrets:/runtime-postgres:ro]") allowed_volume = 1
    if (service == "migrate" && $0 == "    volumes: [runtime-owner-secrets:/runtime-owner:ro]") allowed_volume = 1
    if (service == "role-init" && $0 == "    volumes: [runtime-owner-secrets:/runtime-owner:ro, runtime-app-secrets:/runtime-app:ro]") allowed_volume = 1
    if (service == "server" && $0 == "    volumes: [runtime-app-secrets:/runtime-app:ro]") allowed_volume = 1
    if (service == "database-tools" && $0 == "    volumes: [runtime-owner-secrets:/runtime-owner:ro]") allowed_volume = 1
    if (service == "backup-scheduler" && $0 == "    volumes: [runtime-owner-secrets:/runtime-owner:ro, backup-data:/backups]") allowed_volume = 1
    if (!allowed_volume) {
      print "production compose policy: " service " volume set differs from the approved least-privilege mount" > "/dev/stderr"
      exit 1
    }
    volume_fields++
  }
  in_services && /\/run\/secrets\// && service != "secret-init" && service != "postgres-secret-init" {
    print "production compose policy: source-secret paths are confined to ingress services" > "/dev/stderr"
    exit 1
  }
  in_services && /runtime-postgres/ && service != "postgres-secret-init" && service != "postgres" {
    print "production compose policy: PostgreSQL runtime secret volume is confined to its ingress and consumer" > "/dev/stderr"
    exit 1
  }
  in_services && /runtime-owner/ && service != "secret-init" && service != "migrate" && service != "role-init" && service != "database-tools" && service != "backup-scheduler" {
    print "production compose policy: owner runtime secret volume reached an unapproved service" > "/dev/stderr"
    exit 1
  }
  in_services && /runtime-app/ && service != "secret-init" && service != "role-init" && service != "server" {
    print "production compose policy: app runtime secret volume reached an unapproved service" > "/dev/stderr"
    exit 1
  }
  in_services && (service == "postgres" || service == "migrate" || service == "role-init" || service == "server" || service == "backup-scheduler") {
    if ($0 == "    cap_drop: [ALL]") long_lived_cap_drop[service]++
    if ($0 ~ /^    cap_add:/) long_lived_cap_add[service]++
  }
  END {
    if (!services_seen) {
      print "production compose policy: top-level services mapping is missing" > "/dev/stderr"
      exit 1
    }
    if (image_count != 9) {
      print "production compose policy: expected exactly 9 NEROCD_IMAGE image fields" > "/dev/stderr"
      exit 1
    }
    if (quoted_count != 9) {
      print "production compose policy: every NEROCD_IMAGE image field must be quoted" > "/dev/stderr"
      exit 1
    }
    if (source_secret_fields != 2 || dac_override_fields != 2 || cap_add_fields != 4) {
      print "production compose policy: expected exactly two isolated source-secret ingress services" > "/dev/stderr"
      exit 1
    }
    if (volume_fields != 10) {
      print "production compose policy: expected exact least-privilege volume fields for ten services" > "/dev/stderr"
      exit 1
    }
    long_lived = "postgres migrate role-init server backup-scheduler"
    long_lived_count = split(long_lived, long_lived_names, " ")
    for (position = 1; position <= long_lived_count; position++) {
      name = long_lived_names[position]
      if (long_lived_cap_drop[name] != 1 || long_lived_cap_add[name] != 0) {
        print "production compose policy: " name " must drop ALL and add no capabilities" > "/dev/stderr"
        exit 1
      }
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
