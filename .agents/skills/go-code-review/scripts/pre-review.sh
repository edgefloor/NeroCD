#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
BASE=""
TARGET="./cmd/... ./internal/..."
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            echo 'Usage: pre-review.sh [--base REV] [local-package-pattern]'
            echo 'Runs make fmt-check and pinned lint. Install the linter with make lint-install.'
            exit 0 ;;
        --base)
            [[ $# -ge 2 && -n "$2" ]] || { echo 'error: --base requires a revision' >&2; exit 2; }
            BASE="$2"; shift 2 ;;
        -*) echo "error: unknown option: $1" >&2; exit 2 ;;
        *)
            [[ "$1" =~ ^\./[A-Za-z0-9_./-]+$ ]] || {
                echo 'error: expected a local Go package pattern' >&2; exit 2;
            }
            TARGET="$1"; shift ;;
    esac
done
cd "$REPO_ROOT"
if [[ -n "$BASE" ]]; then
    BASE=$(git rev-parse --verify --end-of-options "$BASE^{commit}")
    [[ "$BASE" =~ ^[0-9a-f]{40}$ ]] || { echo 'error: invalid base commit' >&2; exit 2; }
fi
make --no-print-directory fmt-check
if [[ -n "$BASE" ]]; then
    exec make --no-print-directory lint-new "PRODUCT_PACKAGES=$TARGET" "LINT_BASE_REV=$BASE"
fi
exec make --no-print-directory lint "PRODUCT_PACKAGES=$TARGET"
