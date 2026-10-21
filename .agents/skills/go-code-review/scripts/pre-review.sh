#!/usr/bin/env bash
set -euo pipefail

VERSION="1.0.0"
SCRIPT_NAME="$(basename "$0")"

usage() {
    printf '%s\n' "$SCRIPT_NAME v$VERSION — Run automated pre-review checks on Go code

USAGE
    bash $SCRIPT_NAME [options] [path]

DESCRIPTION
    Runs gofmt and the repository-pinned lint target against the target path and
    reports any findings. Use before manual code review to catch
    mechanical issues early.

    Exits 0 if all checks pass, 1 if issues found, 2 on error.

OPTIONS
    -h, --help       Show this help message
    -v, --version    Show version
    --json           Output results as JSON
    --force          Allow standalone vet when the pinned linter is missing
    --base REV       Report only lint findings added since REV
    --limit N        Max items reported per section (0 = unlimited, default: 0)

ARGUMENTS
    path             Local package pattern (default: product packages)

EXAMPLES
    bash $SCRIPT_NAME
    bash $SCRIPT_NAME ./pkg/...
    bash $SCRIPT_NAME --json ./cmd/server/...
    bash $SCRIPT_NAME --force ./...
    bash $SCRIPT_NAME --json --limit 10 ./..."
}

json_escape() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\t'/\\t}"
    s="${s//$'\r'/}"
    s="${s//$'\n'/\\n}"
    printf '%s' "$s"
}

JSON_OUTPUT=false
FORCE=false
LIMIT=0
TARGET=""
BASE=""
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)    usage; exit 0 ;;
        -v|--version) echo "$SCRIPT_NAME v$VERSION"; exit 0 ;;
        --json)       JSON_OUTPUT=true; shift ;;
        --force)      FORCE=true; shift ;;
        --base)       BASE="${2:?error: --base requires a revision}"; shift 2 ;;
        --limit)
            if [[ $# -lt 2 ]]; then
                echo "error: --limit requires a number" >&2
                exit 2
            fi
            LIMIT="$2"
            shift 2
            ;;
        -*)           echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
        *)            TARGET="$1"; shift ;;
    esac
done

cd "$REPO_ROOT"
# Resolve the pinned executable through the same Makefile used by CI.
LINT_BIN=$(make --no-print-directory -s skill-lint-path)
if [[ ! -x "$LINT_BIN" ]] && ! $FORCE; then
    echo "error: pinned golangci-lint is missing; run make lint-install or use --force" >&2
    exit 2
fi
if [[ -n "$BASE" ]]; then
    BASE=$(git rev-parse --verify "$BASE^{commit}")
    [[ "$BASE" =~ ^[0-9a-f]{40}$ ]] || { echo "error: invalid base commit" >&2; exit 2; }
fi
TARGET="${TARGET:-./cmd/... ./internal/...}"
read -r -a PACKAGES < <(printf '%s\n' "$TARGET")
for package in "${PACKAGES[@]}"; do
    [[ "$package" =~ ^\./[A-Za-z0-9_./-]+$ ]] || {
        echo "error: expected a local Go package pattern" >&2
        exit 2
    }
done

if ! [[ "$LIMIT" =~ ^[0-9]+$ ]]; then
    echo "error: --limit must be a non-negative integer, got: $LIMIT" >&2
    exit 2
fi

if ! command -v go &>/dev/null; then
    echo "error: go is not installed or not in PATH" >&2
    exit 2
fi

if ! command -v gofmt &>/dev/null; then
    echo "error: gofmt is not installed or not in PATH" >&2
    exit 2
fi

GOFMT_STATUS="passed"
GOFMT_FINDINGS=()
GOFMT_DIRS=()
for package in "${PACKAGES[@]}"; do
    GOFMT_DIRS+=("${package%/...}")
done
UNFORMATTED=$(gofmt -l "${GOFMT_DIRS[@]}" 2>&1) || GOFMT_STATUS="failed"
if [[ -n "$UNFORMATTED" ]]; then
    GOFMT_STATUS="failed"
    while IFS= read -r f; do
        [[ -n "$f" ]] && GOFMT_FINDINGS+=("$f")
    done < <(printf '%s\n' "$UNFORMATTED")
fi

GOVET_STATUS="not_run"
GOVET_OUTPUT="Covered by the pinned linter's govet analyzer"
LINT_STATUS="not_run"
LINT_OUTPUT=""
if [[ -x "$LINT_BIN" ]]; then
    LINT_STATUS="passed"
    LINT_TARGET=lint
    [[ -z "$BASE" ]] || LINT_TARGET=lint-new
    if ! LINT_OUTPUT=$(GOCACHE="$REPO_ROOT/.cache/go-build" make --no-print-directory "$LINT_TARGET" \
        "PRODUCT_PACKAGES=$TARGET" "LINT_BASE_REV=$BASE" 2>&1); then
        LINT_STATUS="failed"
    fi
else
    GOVET_STATUS="passed"
    GOVET_OUTPUT=""
    if ! GOVET_OUTPUT=$(GOCACHE="$REPO_ROOT/.cache/go-build" go vet "${PACKAGES[@]}" 2>&1); then
        GOVET_STATUS="failed"
    fi
fi

FAILED=0
[[ "$GOFMT_STATUS" == "failed" ]] && FAILED=1
[[ "$GOVET_STATUS" == "failed" ]] && FAILED=1
[[ "$LINT_STATUS" == "failed" ]] && FAILED=1

if $JSON_OUTPUT; then
    GOFMT_TRUNCATED=false
    GOFMT_DISPLAY=("${GOFMT_FINDINGS[@]+"${GOFMT_FINDINGS[@]}"}")
    if [[ $LIMIT -gt 0 && ${#GOFMT_DISPLAY[@]} -gt $LIMIT ]]; then
        GOFMT_DISPLAY=("${GOFMT_FINDINGS[@]:0:$LIMIT}")
        GOFMT_TRUNCATED=true
    fi

    GOFMT_JSON="["
    first=true
    for f in "${GOFMT_DISPLAY[@]+"${GOFMT_DISPLAY[@]}"}"; do
        $first || GOFMT_JSON+=","
        first=false
        GOFMT_JSON+="\"$(json_escape "$f")\""
    done
    GOFMT_JSON+="]"

    GOVET_TRUNCATED=false
    GOVET_DISPLAY="$GOVET_OUTPUT"
    if [[ $LIMIT -gt 0 && -n "$GOVET_OUTPUT" ]]; then
        GOVET_ARR=()
        while IFS= read -r line; do
            GOVET_ARR+=("$line")
        done < <(printf '%s\n' "$GOVET_OUTPUT")
        if [[ ${#GOVET_ARR[@]} -gt $LIMIT ]]; then
            GOVET_DISPLAY=""
            for (( i=0; i<LIMIT; i++ )); do
                [[ -n "$GOVET_DISPLAY" ]] && GOVET_DISPLAY+=$'\n'
                GOVET_DISPLAY+="${GOVET_ARR[$i]}"
            done
            GOVET_TRUNCATED=true
        fi
    fi
    GOVET_ESC="$(json_escape "$GOVET_DISPLAY")"

    LINT_TRUNCATED=false
    LINT_DISPLAY="$LINT_OUTPUT"
    if [[ $LIMIT -gt 0 && -n "$LINT_OUTPUT" ]]; then
        LINT_ARR=()
        while IFS= read -r line; do
            LINT_ARR+=("$line")
        done < <(printf '%s\n' "$LINT_OUTPUT")
        if [[ ${#LINT_ARR[@]} -gt $LIMIT ]]; then
            LINT_DISPLAY=""
            for (( i=0; i<LIMIT; i++ )); do
                [[ -n "$LINT_DISPLAY" ]] && LINT_DISPLAY+=$'\n'
                LINT_DISPLAY+="${LINT_ARR[$i]}"
            done
            LINT_TRUNCATED=true
        fi
    fi
    LINT_ESC="$(json_escape "$LINT_DISPLAY")"

    GOFMT_TRUNC=""
    $GOFMT_TRUNCATED && GOFMT_TRUNC=',"truncated":true'
    GOVET_TRUNC=""
    $GOVET_TRUNCATED && GOVET_TRUNC=',"truncated":true'
    LINT_TRUNC=""
    $LINT_TRUNCATED && LINT_TRUNC=',"truncated":true'

    printf '%s\n' "{\"gofmt\":{\"status\":\"$GOFMT_STATUS\",\"files\":$GOFMT_JSON$GOFMT_TRUNC},\"govet\":{\"status\":\"$GOVET_STATUS\",\"output\":\"$GOVET_ESC\"$GOVET_TRUNC},\"golangci_lint\":{\"status\":\"$LINT_STATUS\",\"output\":\"$LINT_ESC\"$LINT_TRUNC},\"exit_code\":$FAILED,\"status\":\"$( [[ $FAILED -eq 0 ]] && echo passed || echo failed )\",\"passed\":$( [[ $FAILED -eq 0 ]] && echo true || echo false )}"
else
    echo "=== gofmt ==="
    if [[ "$GOFMT_STATUS" == "failed" ]]; then
        echo "Unformatted files:"
        GOFMT_COUNT=0
        for f in "${GOFMT_FINDINGS[@]}"; do
            GOFMT_COUNT=$((GOFMT_COUNT + 1))
            if [[ $LIMIT -gt 0 && $GOFMT_COUNT -gt $LIMIT ]]; then
                echo "  ... ($(( ${#GOFMT_FINDINGS[@]} - LIMIT )) more items truncated)"
                break
            fi
            echo "  $f"
        done
    else
        echo "OK"
    fi

    echo ""
    echo "=== go vet ==="
    if [[ "$GOVET_STATUS" == "not_run" ]]; then
        echo "$GOVET_OUTPUT"
    elif [[ "$GOVET_STATUS" == "failed" ]]; then
        if [[ $LIMIT -gt 0 ]]; then
            GOVET_ARR=()
            while IFS= read -r line; do
                GOVET_ARR+=("$line")
            done < <(printf '%s\n' "$GOVET_OUTPUT")
            for (( i=0; i<${#GOVET_ARR[@]} && i<LIMIT; i++ )); do
                echo "${GOVET_ARR[$i]}"
            done
            if [[ ${#GOVET_ARR[@]} -gt $LIMIT ]]; then
                echo "... ($(( ${#GOVET_ARR[@]} - LIMIT )) more items truncated)"
            fi
        else
            echo "$GOVET_OUTPUT"
        fi
    else
        echo "OK"
    fi

    echo ""
    echo "=== golangci-lint ==="
    if [[ "$LINT_STATUS" == "not_run" ]]; then
        echo "Skipped (not installed)"
    elif [[ "$LINT_STATUS" == "failed" ]]; then
        if [[ $LIMIT -gt 0 ]]; then
            LINT_ARR=()
            while IFS= read -r line; do
                LINT_ARR+=("$line")
            done < <(printf '%s\n' "$LINT_OUTPUT")
            for (( i=0; i<${#LINT_ARR[@]} && i<LIMIT; i++ )); do
                echo "${LINT_ARR[$i]}"
            done
            if [[ ${#LINT_ARR[@]} -gt $LIMIT ]]; then
                echo "... ($(( ${#LINT_ARR[@]} - LIMIT )) more items truncated)"
            fi
        else
            echo "$LINT_OUTPUT"
        fi
    else
        echo "OK"
    fi

    echo ""
    if [[ $FAILED -eq 1 ]]; then
        echo "Pre-review checks FAILED — fix issues before manual review."
    else
        echo "All pre-review checks passed."
    fi
fi

exit $FAILED
