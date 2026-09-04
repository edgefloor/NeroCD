#!/usr/bin/env bash
# Docker-free mutation checks for the release evidence pair lifecycle. These
# assertions intentionally cover ordering and explicit joins, not artifact
# contents; the artifact verifier and synthetic post-gate test cover contents.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
subject="$root/scripts/release-evidence.sh"
tmp=$(mktemp -d /tmp/nerocd-release-evidence-concurrency.XXXXXXXX)
cleanup(){ rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM
fail(){ printf 'release evidence concurrency mutation: %s\n' "$*" >&2; exit 1; }

line_of(){
  local file=$1 needle=$2
  awk -v needle="$needle" 'index($0, needle) { print NR; exit }' "$file"
}
require_order(){
  local file=$1 phase=$2 launch_a=$3 capture_a=$4 launch_b=$5 capture_b=$6 join=$7 compare=$8
  local launch_a_line capture_a_line launch_b_line capture_b_line join_line compare_line
  launch_a_line=$(line_of "$file" "$launch_a")
  capture_a_line=$(line_of "$file" "$capture_a")
  launch_b_line=$(line_of "$file" "$launch_b")
  capture_b_line=$(line_of "$file" "$capture_b")
  join_line=$(line_of "$file" "$join")
  compare_line=$(line_of "$file" "$compare")
  [[ -n "$launch_a_line" && -n "$capture_a_line" && -n "$launch_b_line" && -n "$capture_b_line" && -n "$join_line" && -n "$compare_line" ]] || return 1
  (( launch_a_line < capture_a_line && capture_a_line < launch_b_line && launch_b_line < capture_b_line && capture_b_line < join_line && join_line < compare_line )) || return 1
  [[ $(sed -n "${launch_a_line}p" "$file") == *' &' ]] || return 1
  [[ $(sed -n "${launch_b_line}p" "$file") == *' &' ]] || return 1
  [[ $(sed -n "${capture_a_line}p" "$file") == *'=$!' ]] || return 1
  [[ $(sed -n "${capture_b_line}p" "$file") == *'=$!' ]] || return 1
  printf '%s pair ordering PASS\n' "$phase"
}
check_subject(){
  local file=$1
  [[ -n $(line_of "$file" 'if wait "$first_pid"') ]] || return 1
  [[ -n $(line_of "$file" 'if wait "$second_pid"') ]] || return 1
  [[ -n $(line_of "$file" '$(jobs -pr)') ]] || return 1
  [[ -n $(line_of "$file" 'kill -TERM -- "-$pid"') ]] || return 1
  [[ $(awk '$0 == "set -m" { count += 1 } END { print count + 0 }' "$file") -eq 2 ]] || return 1
  [[ $(awk '$0 == "set +m" { count += 1 } END { print count + 0 }' "$file") -eq 2 ]] || return 1
  require_order "$file" binary \
    'build_binary_copy "$out/a" &' 'binary_a_pid=$!' \
    'build_binary_copy "$out/b" &' 'binary_b_pid=$!' \
    "wait_for_pair 'Go binary build'" 'cmp "$out/a/nerocd-linux-$arch" "$out/b/nerocd-linux-$arch"' || return 1
  require_order "$file" OCI \
    'build_oci "$out/a/nerocd.oci.tar" &' 'oci_a_pid=$!' \
    'build_oci "$out/b/nerocd.oci.tar" &' 'oci_b_pid=$!' \
    "wait_for_pair 'OCI build'" 'mkdir "$out/oci-expanded-a" "$out/oci-expanded-b"' || return 1
}

check_subject "$subject" || fail 'valid pair lifecycle was rejected'

cp "$subject" "$tmp/no-background.sh"
sed 's#build_binary_copy "$out/a" \&#build_binary_copy "$out/a"#' "$tmp/no-background.sh" >"$tmp/next"
mv "$tmp/next" "$tmp/no-background.sh"
if check_subject "$tmp/no-background.sh" >/dev/null 2>&1; then fail 'foreground copy mutation passed'; fi

cp "$subject" "$tmp/no-join.sh"
sed "/wait_for_pair 'OCI build'/d" "$tmp/no-join.sh" >"$tmp/next"
mv "$tmp/next" "$tmp/no-join.sh"
if check_subject "$tmp/no-join.sh" >/dev/null 2>&1; then fail 'missing join mutation passed'; fi

cp "$subject" "$tmp/compare-before-join.sh"
sed "/wait_for_pair 'Go binary build'/i\\
cmp \"\$out/a/nerocd-linux-\$arch\" \"\$out/b/nerocd-linux-\$arch\"" "$tmp/compare-before-join.sh" >"$tmp/next"
mv "$tmp/next" "$tmp/compare-before-join.sh"
if check_subject "$tmp/compare-before-join.sh" >/dev/null 2>&1; then fail 'comparison-before-join mutation passed'; fi

printf 'release evidence concurrency mutation PASS\n'
