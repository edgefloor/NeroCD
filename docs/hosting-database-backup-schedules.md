# Hosted PostgreSQL backup schedules

A project owner can create, edit and disable logical backup schedules from the
PostgreSQL database's **Database backups** panel. Project viewers can inspect
schedules and occurrence history. A schedule uses the same local custom-format
backup, protected execution limits, fenced runner and new-database restore path
as [immediate PostgreSQL backups](hosting-database-backups.md).

The supported source is the pinned PostgreSQL 17.11 recipe on its authorized
stable destination. [Remote backup targets](hosting-database-remote-backups.md)
can now be selected explicitly by target ID and revision for immediate and
scheduled backups. A target edit does not redirect an existing schedule. Redis and user-supplied commands are unsupported. Configure
[retention](hosting-database-backup-retention.md) and
[failure alerts](hosting-backup-notifications.md) separately.
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
`0 0 * * MON#2` are not. These calendar forms are outside the supported grammar. The pinned upstream
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

The [schedule domain](../internal/domain/hosting_database_backup_schedule.go)
defines the accepted input. The hosting controller runs discovery under
`StartHostingExecution`; River jobs carry operation IDs. PostgreSQL transactions,
immutable revisions, and runner leases replace process-local scheduling authority.
The controller does not run arbitrary commands.

<a id="physical-qualification-and-retained-evidence"></a>

## Tests and supported environments

The [compiled-runner gate](../acceptance/runtime-hosting-postgresql-backup-runner/gate.sh)
checks UI create, identical retry after response loss, edit, disable, occurrence
history, controller stop/join/restart, one admitted dump, and new-target restore
with application reads and writes. It also checks viewer denial and widths 320,
390, 430, and 1440 pixels.

For dump timeout, the fixture holds a source-table DDL lock and tracks the exact
guest Docker ExecID until exit 124. The guest process and PostgreSQL session must
be absent while the lock remains held, before fixture shutdown. Runner restart
keeps the uncertain source operation held.

The tested profile is native Linux arm64, PostgreSQL 17.11, trusted rootful
classic-store Docker, one destination, and private fixture-CA HTTPS. Broader cron
grammar, other engines, production ACME, cross-host recovery, and disk loss are
unqualified.
