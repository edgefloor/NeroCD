# Hosted PostgreSQL backup schedules

A project owner can create, edit and disable logical backup schedules from the
PostgreSQL database's **Database backups** panel. Project viewers can inspect
schedules and occurrence history. A schedule uses the same local custom-format
backup, protected execution limits, fenced runner and new-database restore path
as [immediate PostgreSQL backups](hosting-database-backups.md).

The supported source is the pinned PostgreSQL 17.11 recipe on its authorized
stable destination. [Remote backup targets](hosting-database-remote-backups.md)
can now be selected explicitly by target ID and revision for immediate and
scheduled backups. A target edit does not redirect an existing schedule. Redis,
retention, notifications and user-supplied commands remain outside this path.
A local archive remains dependent on that destination. Remote acceptance uses
a loopback fixture and does not qualify off-host disaster recovery.

## Create or change a schedule

Review the database resource generation, frequency and effective IANA timezone.
The timezone is saved explicitly; changing the server's timezone does not change
an admitted schedule. Supported frequency aliases are `every_minute`, `hourly`,
`daily`, `weekly`, `monthly` and `yearly`. The corresponding `@` aliases are
accepted except `@every_minute`. A five-field minute/hour/day/month/weekday cron
expression is also accepted. Seconds, `@every` intervals and embedded timezone
prefixes are rejected.

Cron syntax follows the pinned `robfig/cron/v3` v3.0.1 parser, not the full
calendar grammar of upstream's `dragonmantank/cron-expression` v3.6.0. Numeric
bounds are minute 0–59, hour 0–23, day 1–31, month 1–12 and weekday 0–6. Three-letter
month and weekday names, lists, ranges, steps and wildcards are accepted; `?`
also acts as a wildcard in this parser. Use `0` or `SUN` for Sunday: weekday `7`,
`L`, `W` and `#` calendar expressions are rejected. For example, `0 0 * * SUN`
is supported, while `0 0 * * 7`, `0 0 L * *`, `0 0 15W * *` and
`0 0 * * MON#2` are not. This is an explicit compatibility limit, rather than
full upstream cron-expression parity. The pinned upstream
[weekday parser](https://github.com/dragonmantank/cron-expression/blob/d61a8a9604ec1f8c3d150d09db6ce98b32675013/src/Cron/DayOfWeekField.php)
and [month-day parser](https://github.com/dragonmantank/cron-expression/blob/d61a8a9604ec1f8c3d150d09db6ce98b32675013/src/Cron/DayOfMonthField.php)
define those additional calendar forms.

The resource-scoped endpoints are:

| Method | Path | Permission |
| --- | --- | --- |
| GET | `/api/v1/resources/{id}/database-backup-schedules` | Viewer |
| POST | `/api/v1/resources/{id}/database-backup-schedules` | Owner |
| PUT | `/api/v1/resources/{id}/database-backup-schedules/{schedule_id}` | Owner |
| GET | `/api/v1/resources/{id}/database-backup-schedules/{schedule_id}/occurrences` | Viewer |

POST and PUT accept the same closed document:

```json
{
  "request_id": "a127e2b179b44573bc9d69f9019a9088",
  "expected_resource_generation": 4,
  "expected_version": 0,
  "frequency": "daily",
  "timezone": "Europe/Budapest",
  "enabled": true
}
```

Creation requires version zero. An edit or disable request must use the reviewed
current schedule revision. Every accepted change increments that revision while
retaining the schedule ID and discovery cursor. Disable uses `enabled: false`;
it stops future discovery and does not cancel already admitted work.

If a response is lost, retry the identical request ID and document. The server
returns its historical acknowledgment under current access checks. A changed
request with the same ID is rejected. The UI retains the exact request after an
ambiguous transport failure. A version conflict requires an explicit refresh
and review before submitting another edit; it is never silently resubmitted.

## Due discovery and execution

The hosting execution controller makes bounded passes. Each pass captures one
discovery instant, considers at most 100 schedules, and chooses only the newest
due UTC minute within the inclusive preceding ten minutes. The current minute
is eligible. The first eligible minute can precede schedule creation within
that window. There is no backlog enumeration after a longer outage.

At a daylight-saving fall-back, fixed-hour expressions suppress the repeated
local wall-clock minute when it matches the saved cursor. Wildcard and step-hour
expressions may run in both UTC occurrences. A nonexistent spring-forward local
minute does not run. These rules follow the pinned upstream due-discovery
behavior; tests cover UTC boundaries and `Europe/Budapest` transitions.

Each occurrence retains its schedule revision, frequency, timezone, UTC minute
and optional exact backup operation ID. An occurrence, ordinary backup
admission and River operation-ID enqueue commit in one PostgreSQL transaction.
A failed queue publication rolls back the cursor and admission so a later pass
can retry that same minute. Concurrent passes cannot admit duplicate
`(schedule_id, scheduled_at)` occurrences.

The saved editing actor's current project ownership, destination grant and exact
runner capability are checked at admission. The normal operation checks them
again during execution. Revocation produces bounded denied/skipped history;
it does not grant a scheduler perpetual authority. An authorized owner may edit
the schedule, creating a new actor/configuration revision for future work.
Already admitted work retains its original authority and operation identity.

A source binding permits only one held backup at a time, including immediate
backups and other schedules. This is stronger than upstream's per-schedule
exclusion. Overlap is recorded as skipped rather than creating another writer.
Maintenance and credential changes remain subject to the existing source hold.
Restores own their new target and pin the immutable archive without holding the
live source.

## History and conservative recovery

History exposes admitted operation status, missed/skipped/denied occurrences,
finite reasons and whether source ownership remains held. It contains no
credentials, local archive paths, process commands or lease authority. Readers
receive at most 100 current schedules or occurrences per request.

The controller can republish a lost, provably unstarted queue item after sixty
minutes, using the same operation ID. Only the newest eligible occurrence within
24 hours is recovered. Older or superseded work is recorded as missed. Retirement
and any source-hold release require locked database proof that no run, attempt,
lease or mutation receipt was ever admitted, plus an exact immutable retirement
record. A delayed claim and retirement serialize against one another.

Once a run or attempt exists, expiry, timeout or uncertain execution never
becomes automatic rerun permission. A revoked or changed source can leave held
ownership with a visible failure. The controller does not infer completion from
an observation, release uncertain holds or repeat an import from an intent
without a durable completed receipt. This preserves the immediate backup and
restore replay limits documented in the linked guide.

## Implementation and qualification boundary

The scheduler adapts Coolify commit
`b12d1386f3d458618b4c579babcd1e339c6c40b3`:

- `app/Jobs/ScheduledJobManager.php` newest-due selection, timezone and overlap checks;
- `app/Services/ScheduledJobDeliveryService.php` durable occurrence identity and conservative
  stale-enqueue recovery;
- `app/Livewire/Project/Database/CreateScheduledBackup.php`,
  `app/Livewire/Project/Database/BackupEdit.php` and
  `app/Http/Controllers/Api/DatabasesController.php` owner configuration and history;
- `app/Jobs/DatabaseBackupJob.php`, `app/Console/Kernel.php` and
  `bootstrap/helpers/constants.php`
  logical-tool behavior and frequency aliases.

Go replaces upstream SSH/cache coordination with PostgreSQL transactions,
immutable occurrence/configuration records and the existing runner's
lease/attempt/fence and append-before-send journal. The controller runs under
`StartHostingExecution`; it does not add a general scheduler service or execute
arbitrary backup commands.

The opt-in compiled-runner fixture selects both the existing immediate workflow
and the schedule workflow. It requires real embedded-UI create/lost-response
replay/edit/disable, an actual controller stop/join/restart, one admitted dump,
new-target restore and distinct application reads/writes. Viewer denial and
320/390/430/1440-pixel layouts are separate assertions. A source-table DDL lock
exercises an actual guest `pg_dump` timeout, verifies the guest tool stops and
retains uncertain source ownership. Its `DATABASE_BACKUP_GUEST_DUMP_TIMEOUT_PASS`
marker is distinct from the existing `pg_restore` timeout marker. Fixture code
and unit tests alone do not qualify these runtime claims; exact-candidate
physical execution and independent review are required.

## Physical qualification and retained evidence

Source `c76019739ffbde0bcf7eb357c9740ddd35bf9a57` passed the full physical gate
on 2026-10-25. Accepted combined revision
`60b86e47504533280170028cbc0b9b632c7b67db` retains all 38 feature blobs and
modes exactly. Its only differences from that source are the six accepted
immediate-backup documentation files. All 13 combined integration checks passed,
and the Leader accepted this bounded feature. Acceptance does not upgrade the
existing user UI process.

The live embedded UI creates a schedule, retries its lost committed response,
edits it, disables it, reads occurrence history and restores the resulting
archive. The running hosting controller discovers the due occurrence. A real
controller stop, join and restart plus runner replay preserve one admitted dump
and its exact operation identity. A distinct application reads restored data
and writes new data to a distinct database resource, binding, volume and
credential identity. The original source retains its records and accepts new
writes. Viewer denial and 320, 390, 430 and 1440-pixel layouts are checked.

The dump-timeout proof is separate. A positively observed source-table DDL lock
blocks the guest `pg_dump`. The fixture tracks that same Docker ExecID until
it terminates with exit 124, with the guest process and PostgreSQL session absent
while the blocking lock remains held. Those assertions run before `f.stop`, so
fixture shutdown cannot satisfy the deadline proof. Restart leaves the uncertain
source operation held. This closes the earlier bounded backup-timeout evidence
gap without changing the historical restore-only timeout assessment.

Receipts are under
`/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-schedules-20261025/`:

| Receipt | Result and retained boundary |
| --- | --- |
| `first-runtime/driver.json` and `runtime-summary.json` | Exit 0, full workflow pass in 157.868 seconds. Three top-level Go groups and three failure subtests passed without skips; both live UI scenarios and all nine required markers passed. |
| `first-runtime/independent-cleanup.json` and `full-profile-preservation.json` | Existing 35-container inventory and 209-volume profile preserved. Owned outer containers and volumes absent. |
| `integration/sourceproof.json` and `checks.json` | Accepted combined revision retains all 38 source blobs/modes from `c76019739ffbde0bcf7eb357c9740ddd35bf9a57`; changes outside those paths match the prior accepted documentation snapshot. All 13 integration checks passed, including focused race checks, vet, API generation, frontend/product builds and embedded artifact checks. |
| `transaction-pg/round7-captured-tick.json` | Earlier focused PostgreSQL/store and domain race checks passed in 25.113 seconds. This remains labeled earlier evidence, rather than a new full physical run. |

The physical schedule group took 43.31 seconds. The live immediate and scheduled
browser scenarios took 17.4 and 17.0 seconds within the full gate. These nested
measurements do not establish total delivery time, total review rounds, token
usage or monetary cost. The existing user UI on port 18081 still runs its older
`09a432407c41671517fa342d28c07e9c79978aea` binary. Qualification of the embedded
fixture does not deploy this feature to that process.

The [checklist](parity-checklist.json) appends two subsequent bounded assessments:
R46-C02 gains supported scheduled-backup qualification, and R46-C06 gains actual
`pg_dump` timeout qualification. The prior documentation snapshot, its nine
validated outcomes and one partial timeout outcome remain historical. The
933-outcome audit and all parent gates remain unchanged. The unsupported cron
forms above, retention, notifications, other engines and full R46/R62 or
Coolify parity remain open. Subsequent [remote acceptance](hosting-database-remote-backups.md#accepted-qualification-and-durable-evidence)
adds bounded S3 targets and recovery; it does not change this historical
schedule receipt or add another R46-C02 credit.
