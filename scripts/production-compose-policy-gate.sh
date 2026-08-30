#!/usr/bin/env bash
set -Eeuo pipefail

root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
compose_file="$root/compose.production.yaml"
expected='    image: "${NEROCD_IMAGE:?set canonical digest NEROCD_IMAGE}"'
expected_services=(secret-init backup-data-init migrate role-init server probe database-tools backup-scheduler)

[[ -f "$compose_file" ]] || { printf '%s\n' 'production compose policy: compose file is missing' >&2; exit 1; }
image_count=$(rg -e '^\s*image:\s+.*NEROCD_IMAGE' "$compose_file" | wc -l | tr -d ' ')
quoted_count=$(rg -F -x "$expected" "$compose_file" | wc -l | tr -d ' ')
[[ "$image_count" == 8 ]] || { printf '%s\n' 'production compose policy: expected exactly 8 NEROCD_IMAGE image fields' >&2; exit 1; }
[[ "$quoted_count" == 8 ]] || { printf '%s\n' 'production compose policy: every NEROCD_IMAGE image field must be quoted' >&2; exit 1; }

for service in "${expected_services[@]}"; do
  if ! awk -v service="$service" -v expected="$expected" '
    $0 == "  " service ":" { inside=1; next }
    inside && /^  [a-z0-9-]+:$/ { exit }
    inside && $0 == expected { found=1; exit }
    END { exit(found ? 0 : 1) }
  ' "$compose_file"; then
    printf 'production compose policy: %s lacks the exact quoted image field\n' "$service" >&2
    exit 1
  fi
done
