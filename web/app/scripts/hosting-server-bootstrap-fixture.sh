#!/usr/bin/env bash
# Run from the repository root. No host binds or published Docker ports.
set -euo pipefail
root=$(pwd -P)
if [[ ${1:-} != --drive-owned ]]; then
 bun run --cwd web/app build
 python3 -B scripts/skills/ui_manifest.py > /tmp/nerocd-bootstrap-ui-manifest.json
 NEROCD_BOOTSTRAP_BROWSER=1 NEROCD_BOOTSTRAP_ONBOARDING=owned-systemd-target bash acceptance/runtime-server-bootstrap/qualify.sh
 exit
fi
[[ $# == 7 ]]
container_id=$2; owner=$3; database_url=$4; fingerprint=$5; evidence=$6; port_file=$7
[[ "$container_id" =~ ^[a-f0-9]{64}$ && "$owner" =~ ^nerocd-server-bootstrap\.[a-z0-9]+$ ]]
[[ $(docker container inspect --format '{{.Id}} {{.Name}} {{index .Config.Labels "io.nerocd.server-bootstrap.owner"}}' "$container_id") == "$container_id /$owner $owner" ]]
node "$root/acceptance/runtime-server-bootstrap/browser-driver.mjs" "$container_id" "$owner" "$database_url" "$fingerprint" "$evidence" "$port_file"
