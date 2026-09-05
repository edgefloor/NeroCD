#!/usr/bin/env bash
# Docker-free behavioral coverage for the accepted runtime gate scheduler. The
# harness executes the runtime recipe extracted from the real Makefile against
# traced stubs, preserving the real observability prerequisites.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
makefile="$root/Makefile"
tmp=$(mktemp -d /tmp/nerocd-release-runtime-scheduling.XXXXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM
fail() { printf 'release runtime scheduling: %s\n' "$*" >&2; exit 1; }

runtime_gates=(
  runtime-fencing-gate
  runtime-spool-gate
  runtime-enrollment-gate
  runtime-provenance-gate
  runtime-compose-gate
  runtime-web-operator-gate
  backup-restore-gate
  observability-gate
)
accepted_gates=$(sed -n '/^release-evidence-accepted-gates:/,/^release-evidence-gate:/p' "$makefile")
runtime_recipe=$(printf '%s\n' "$accepted_gates" | awk '/^\t\$\(MAKE\) .*runtime-fencing-gate/ { count++; line=$0 } END { if (count == 1) print line; else exit 1 }') || fail 'real Makefile must contain one runtime recipe'
runtime_recipe=${runtime_recipe#	}
read -r -a recipe_fields <<<"$runtime_recipe"
[[ ${#recipe_fields[@]} -eq $((${#runtime_gates[@]} + 2)) ]] || fail 'real runtime recipe has an unexpected field count'
[[ ${recipe_fields[0]} == '$(MAKE)' && ${recipe_fields[1]} == '--jobs=1' ]] || fail 'real runtime recipe is not an explicit serial recursive make'
for index in "${!runtime_gates[@]}"; do
  [[ ${recipe_fields[index + 2]} == "${runtime_gates[index]}" ]] || fail "real runtime recipe inventory or order differs at ${runtime_gates[index]}"
done
[[ $(sed -n '/^observability-gate:/p' "$makefile") == 'observability-gate: runtime-compose-gate backup-restore-gate' ]] || fail 'real observability prerequisites changed'

stub="$tmp/gate-stub.sh"
cat >"$stub" <<'STUB'
#!/usr/bin/env bash
set -Eeuo pipefail
name=$1
if ! mkdir "$ACTIVE" 2>/dev/null; then
  printf 'overlap %s\n' "$name" >>"$TRACE"
  exit 97
fi
cleanup() { rmdir "$ACTIVE" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM
printf 'start %s\n' "$name" >>"$TRACE"
if [[ ${FAIL_GATE:-} == "$name" ]]; then
  printf 'fail %s\n' "$name" >>"$TRACE"
  exit 42
fi
sleep 0.03
printf 'end %s\n' "$name" >>"$TRACE"
STUB
chmod +x "$stub"

harness="$tmp/Makefile"
{
  printf '.PHONY: run'
  for gate in "${runtime_gates[@]}"; do printf ' %s' "$gate"; done
  printf '\n\nrun:\n\t%s\n\n' "$runtime_recipe"
  for gate in "${runtime_gates[@]}"; do
    if [[ $gate == observability-gate ]]; then
      printf '%s: runtime-compose-gate backup-restore-gate\n' "$gate"
    else
      printf '%s:\n' "$gate"
    fi
    printf '\t@"%s" %s\n\n' "$stub" "$gate"
  done
} >"$harness"

line_of() {
  local trace=$1 event=$2 gate=$3
  awk -v event="$event" -v gate="$gate" '$1 == event && $2 == gate { print NR; exit }' "$trace"
}

success_trace="$tmp/success.trace"
: >"$success_trace"
(cd "$tmp" && TRACE="$success_trace" ACTIVE="$tmp/active" make --no-print-directory -f Makefile run >/dev/null) || fail 'serial runtime harness failed'
! rg -q '^overlap ' "$success_trace" || fail 'runtime gates overlapped'
for gate in "${runtime_gates[@]}"; do
  [[ $(awk -v gate="$gate" '$1 == "start" && $2 == gate { count++ } END { print count + 0 }' "$success_trace") -eq 1 ]] || fail "$gate did not start exactly once"
  [[ $(awk -v gate="$gate" '$1 == "end" && $2 == gate { count++ } END { print count + 0 }' "$success_trace") -eq 1 ]] || fail "$gate did not finish exactly once"
done
compose_end=$(line_of "$success_trace" end runtime-compose-gate)
backup_end=$(line_of "$success_trace" end backup-restore-gate)
observability_start=$(line_of "$success_trace" start observability-gate)
(( compose_end < observability_start && backup_end < observability_start )) || fail 'observability ran before both prerequisites completed'

failure_trace="$tmp/failure.trace"
: >"$failure_trace"
if (cd "$tmp" && TRACE="$failure_trace" ACTIVE="$tmp/active" FAIL_GATE=runtime-spool-gate make --no-print-directory -f Makefile run >/dev/null 2>&1); then
  fail 'failing runtime gate returned success'
fi
[[ $(awk '$1 == "start" && $2 == "runtime-fencing-gate" { count++ } END { print count + 0 }' "$failure_trace") -eq 1 ]] || fail 'failure run did not execute its prerequisite first gate exactly once'
[[ $(awk '$1 == "fail" && $2 == "runtime-spool-gate" { count++ } END { print count + 0 }' "$failure_trace") -eq 1 ]] || fail 'failure run did not stop at the selected failing gate'
for gate in "${runtime_gates[@]:2}"; do
  ! rg -q "^start ${gate}$" "$failure_trace" || fail "$gate ran after runtime-spool-gate failed"
done
! rg -q '^overlap ' "$failure_trace" || fail 'failure run overlapped runtime gates'

printf 'release runtime scheduling PASS gates=%s mode=serial failure=fail-closed\n' "${#runtime_gates[@]}"
