#!/usr/bin/env bash
set -euo pipefail
# Shares the established physical ownership and process-join guard. --catalog
# selects a distinct outer owner, sole Docker data volume and test family.
exec bash acceptance/runtime-hosting-storage/gate.sh --catalog
