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
  in_services && service in required && /^    <<:/ {
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
