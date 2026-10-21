#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION="1.1.0"

for arg in "$@"; do
    case "$arg" in
        -h|--help)
            printf '%s\n' "check-interface-compliance.sh v$VERSION - Find likely missing compile-time interface compliance verifications

USAGE
    bash check-interface-compliance.sh [options] [path]

OPTIONS
    -h, --help       Show this help message
    -v, --version    Show version
    --json           Output results as JSON
    --include-test   Also scan _test.go files for interface definitions and implementations
    --limit N        Show at most N results (default: all)"
            exit 0
            ;;
        -v|--version)
            echo "check-interface-compliance.sh v$VERSION"
            exit 0
            ;;
    esac
done

REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
exec python3 -B "$REPO_ROOT/scripts/skills/run_analyzer.py" "$SCRIPT_DIR/check-interface-compliance.go" "$@"
