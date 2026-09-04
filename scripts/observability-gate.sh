#!/usr/bin/env bash
# AC15 verification. Focused tests establish renderer/store semantics; the two
# prerequisite runtime gates exercise actual PostgreSQL, server, enrolled
# runner, lifecycle rollback, and backup observation before authenticated
# scrapes. Make owns those dependencies so release aggregation can share their
# freshly produced evidence without executing either topology twice.
set -Eeuo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
evidence=/tmp/nerocd-observability.txt
: >"$evidence"
cd "$root"
GOCACHE="$root/.cache/go-build" go test ./internal/observability ./internal/store ./internal/app ./internal/api ./cmd/nerocd | tee -a "$evidence"
compose_evidence=/tmp/nerocd-compose-runtime.txt
backup_evidence=/tmp/nerocd-backup-restore.txt
[[ -s "$compose_evidence" && -s "$backup_evidence" ]]
rg -q '^observability_runtime_scrape authenticated=true anonymous_denied=true queue_claim_expiry_reclaim=true runner_telemetry=true deployment_rollback=true renewal_retry_delta=1 renewal_failure_delta=1 fixed_labels=true$' "$compose_evidence"
rg -q '^observability_backup_scrape authenticated=true anonymous_denied=true success_result=true fixed_labels=true$' "$backup_evidence"
printf 'reused prerequisite evidence: %s %s\n' "$compose_evidence" "$backup_evidence" | tee -a "$evidence"
GOCACHE="$root/.cache/go-build" go run ./cmd/nerocd contract | tee -a "$evidence"
printf 'PASS: observability render/auth/snapshot plus real runtime and backup scrapes\n' >>"$evidence"
printf 'observability evidence: %s\n' "$evidence"
