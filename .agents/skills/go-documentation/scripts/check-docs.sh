#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION="1.1.0"

for arg in "$@"; do
    case "$arg" in
        -h|--help)
            printf '%s\n' "check-docs.sh v$VERSION - Check for missing doc comments on exported Go symbols

USAGE
    bash check-docs.sh [options] [path]

OPTIONS
    -h, --help       Show this help message
    -v, --version    Show version
    --json           Output results as JSON
    --strict         Also check unexported functions, methods, types, constants, and variables
    --limit N        Show at most N results (default: all)"
            exit 0
            ;;
        -v|--version)
            echo "check-docs.sh v$VERSION"
            exit 0
            ;;
    esac
done

REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
exec python3 -B "$REPO_ROOT/scripts/skills/run_analyzer.py" "$SCRIPT_DIR/check-docs-ast.go" "$@"
