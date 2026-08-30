#!/usr/bin/env bash
set -Eeuo pipefail

root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
compose_file="$root/compose.production.yaml"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'

[[ -f "$compose_file" ]] || { printf '%s\n' 'production compose policy: compose file is missing' >&2; exit 1; }
awk -v expected="$expected" '
  BEGIN {
    services = "secret-init backup-data-init migrate role-init server probe database-tools backup-scheduler"
    count = split(services, names, " ")
    for (position = 1; position <= count; position++) required[names[position]] = 1
  }
  /^  [a-z0-9-]+:$/ {
    service = substr($0, 3, length($0) - 3)
    next
  }
  /^[[:space:]]*image:[[:space:]].*NEROCD_IMAGE/ { image_count++ }
  $0 == expected {
    quoted_count++
    if (service in required) service_images[service]++
  }
  END {
    if (image_count != 8) {
      print "production compose policy: expected exactly 8 NEROCD_IMAGE image fields" > "/dev/stderr"
      exit 1
    }
    if (quoted_count != 8) {
      print "production compose policy: every NEROCD_IMAGE image field must be quoted" > "/dev/stderr"
      exit 1
    }
    for (position = 1; position <= count; position++) {
      if (service_images[names[position]] != 1) {
        print "production compose policy: " names[position] " lacks exactly one quoted image field" > "/dev/stderr"
        exit 1
      }
    }
  }
' "$compose_file"
