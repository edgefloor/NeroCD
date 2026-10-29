#!/usr/bin/env bash
set -euo pipefail
# Shares the established physical ownership and process-join guard. --catalog
# selects a distinct outer owner, sole Docker data volume and test family.
case ${1:-} in
 --wordpress|--wordpress-mysql|--wordpress-initial) exec bash acceptance/runtime-hosting-storage/gate.sh "$1";;
 '') exec bash acceptance/runtime-hosting-storage/gate.sh --catalog;;
 *) echo 'usage: gate.sh [--wordpress|--wordpress-mysql|--wordpress-initial]' >&2; exit 2;;
esac
