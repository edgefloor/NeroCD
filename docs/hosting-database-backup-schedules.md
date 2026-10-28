# Hosted database backup schedules

A project owner can create, edit and disable logical backup schedules from the
PostgreSQL or MySQL database's **Database backups** panel. Project viewers can inspect
schedules and occurrence history. A schedule uses the same engine-specific local
backup format, protected execution limits, fenced runner, and new-database restore
path as [immediate backups](hosting-database-backups.md).

Supported sources are the pinned standalone PostgreSQL 17.11 and MySQL 8.4.13
recipes and the qualified managed Compose PostgreSQL 16 component on their authorized stable
destinations. Compose schedules create local database archives only. They do not
include application uploads or support remote backup targets. Standalone
PostgreSQL remote-target semantics are unchanged. [Remote backup targets](hosting-database-remote-backups.md)
can be selected explicitly by target ID and revision for standalone immediate
and scheduled backups. A target edit does not redirect an existing schedule. Redis and user-supplied commands are unsupported. For PostgreSQL, configure
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

## Schedule native MySQL backups

For `mysql-8.4.13-oraclelinux9.v1`, open **MySQL backup schedules** in the native
database panel. Create, edit, disable, and retry with the same fields and revision
checks described above. The response version is `mysql.logical-local.schedule.v1`.
A due occurrence creates an ordinary local `mysql_sql` backup through the existing
MySQL backup capability and authority checks. Select that exact completed backup
for [restore into a separate same-recipe target](hosting-database-backups.md#mysql-local-backup-and-restore).

MySQL schedules are local-only. They do not accept a remote target, `component`,
or `expected_deployed_operation_id`. Remote copies, retention controls, failure
notifications, and Compose MySQL schedules remain unsupported. The InnoDB snapshot
and concurrent-DDL limits of immediate MySQL backups still apply.

## Schedule a Compose PostgreSQL component

Use the application's **Database backups** panel and select its PostgreSQL
component. The same resource-scoped endpoints accept the additional `component`
and `expected_deployed_operation_id` fields. For example, add these fields to
the POST document above, using the operation ID from the reviewed deployment:

```json
{
  "component": "postgresql",
  "expected_deployed_operation_id": "<reviewed settled deployment operation ID>"
}
```

The component is immutable for the schedule's lifetime. Creation and enabling
require a fresh review of the current settled deployment. Each due occurrence
resolves that component from the current deployment, so a successful redeployment
does not leave the schedule attached to its predecessor. Admission retains the
exact deployment operation used for that backup.

You can disable a schedule, inspect history, and replay an identical accepted
request while the stack is unavailable. Current authorization still applies.
A disable stops future discovery without canceling admitted work. The archive
contains database records only. A separate-target database import does not restore
the application's uploaded files or provide coordinated application recovery.

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

The managed Compose PostgreSQL 16 schedule workflow passed the existing
`--managed-compose` gate at
`09cd2670154443f788963d9812aa6b77a2c919c8` on 2026-10-27. See the
[Compose qualification checklist](hosting-compose.md#qualification) for retained
evidence, check limits, and the tested profile.


## Native MySQL schedule qualification

At `8d85d70b` on 2026-10-28, the existing UI created a native MySQL schedule,
replayed a lost response identically, edited it, disabled it, and read occurrence
history. The background controller produced a due `mysql_sql` artifact. Restore
used that exact scheduled artifact in a separate resource with distinct binding,
volume, and credential identities. A routed application verified recovered data
and made a new write. Source-only and target-only writes remained isolated,
including during a direct source-database outage.

After disable, crossing another due minute admitted no new backup. Revoking the
destination grant produced a `skipped` occurrence with `source_unavailable` and no
operation. Revocation also withdrew the routes. Restoring permission left them
disabled; explicit current-version route updates recovered the known source and
target routes. Fresh source HTTPS writes and exact uploads passed afterward.
This is explicit recovery, not uninterrupted availability.

The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-mysql-schedules-20261028/8d85d70b7b5eada485ea72dd0680df445effd591/evidence-index.json)
records source checks, responsive owner/viewer assertions, exact artifact restore,
and cleanup. The profile is one native Linux arm64 destination, trusted rootful
Docker, and test-CA HTTPS. The full WebUI run retained 2,241 passing tests and one
unrelated timeout; that test passed separately without a timeout change, and the
separate build passed. MySQL remote copies, retention, notifications, Compose
MySQL scheduling, production ACME, and cross-host recovery remain unqualified.
