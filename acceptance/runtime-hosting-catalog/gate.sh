#!/usr/bin/env bash
set -euo pipefail
# Shares the established physical ownership and process-join guard. --catalog
# selects a distinct outer owner, sole Docker data volume and test family.
case ${1:-} in
 --wordpress) exec bash acceptance/runtime-hosting-storage/gate.sh --wordpress;;
 '') exec bash acceptance/runtime-hosting-storage/gate.sh --catalog;;
 *) echo 'usage: gate.sh [--wordpress]' >&2; exit 2;;
esac
