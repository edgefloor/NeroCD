# Resource runtime log viewer

Project hosting exposes runtime logs directly for a resource. It requires no
legacy Task setup or mutation/operation permission. Current principal and resource authorization gate the view,
and the resource/session APIs authorize every request. A project viewer can
request logs but cannot read another actor's session.
The current model uses the same project viewer role for legacy Task reads.
There is no separate Task-read permission to remove. The browser fixture proves
runtime-log access for a viewer denied Task mutation/operation permission; a
source regression separately proves legacy-read failure does not gate this view.

## Sensitive output and explicit demand

Initial rendering opens no session and reads no plaintext. Choose **Follow
runtime logs** to admit collection demand. Application output can contain secrets.
The viewer keeps plaintext only in the actor/project/resource mounted component;
it does not put rows in query caches, browser storage, URLs, notifications, or
exports. **Hide output**, access loss, and scope changes clear rows and session
state. Unmounting aborts requests but does not promise server-side closure.

An open attempt freezes a cryptographic 32-character lowercase hexadecimal
request ID and a canonical UTC admission deadline. Explicit retry after a lost
response sends the identical body. It never opens another session automatically.
A new Follow is a separate explicit attempt and can conflict with demand already
admitted by an ambiguous earlier request.

Every open, read, renewal, and close has a five-second client deadline. Timeout
retires request ownership before abort, clears busy state, and pauses reads and
renewals. Late responses cannot replace a subsequent attempt, move its cursor,
restore cleared output, or extend its demand. Network, malformed-response, and
conflict errors require explicit retry. HTTP 401, 403, and 404 clear all sensitive
state. HTTP 410 clears expired cached output. A 409 does not establish a quota,
placement, or target diagnosis.

## Following and history

While explicitly following and visible in both the viewport and document, the
viewer reads at most one page of 200 rows every three seconds and renews demand
every ten seconds. Reads do not renew demand. Hiding or scrolling away pauses
collection requests and aborts in-flight work. Returning requires explicit
Resume while the original demand is live, or a new Follow after expiry.

The local demand clock subtracts conservative request duration and the greater
of monotonic or wall-clock elapsed time from the server deadline. Clock rollback
and system suspension cannot extend the apparent thirty-second demand. A page
read cannot extend the accepted local lease; only a successful authorized renew
can do that.

**Stop following** disables renewal immediately, aborts reads and renewals, and
requests DELETE for a known session. Retained rows remain historical. An
unconfirmed close can leave already admitted collection until demand expires.
Closed, unsupported-driver, and old-target history can drain bounded pages while
visible. Sessions retain their original target and never retarget themselves.

Rows show server sequence, component, stream, source time, receipt time, and the
immutable operation/container identity. Output renders as text; HTML is escaped
and terminal control characters are displayed as escapes. The viewer distinguishes
source gaps, removed retention prefixes, additional pages, clipped messages, and
local trimming. The local buffer retains at most 500 complete rows and 1 MiB of
UTF-8 encoded row data. Empty pages advance the cursor only through a validated
server deletion watermark.

## Verification boundary

The browser gate in `acceptance/runtime-hosting-logs-browser/` uses the real
registered API, PostgreSQL, cookie sessions, and embedded built UI. Its producer
simulates an authorized runner through verified test-CA TLS claim/event calls.
Accounts, resources, and settled deployment receipts are fixture setup. This
qualifies the viewer/API workflow, not account onboarding, real Docker log
collection, production TLS, or A29. Combined actual-collector qualification is a
separate acceptance gate.

The optional actual-collector browser path is
`NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER=1 bash acceptance/runtime-hosting-logs/gate.sh`.
It follows fresh Docker stdout/stderr only after a registered deployment settles
through the compiled runner. It uses the same embedded viewer and real session
API, then exercises Stop/new Follow, burst retention, compiled collector reconnect,
real demand expiry, and membership revocation. The fixture seeds accounts and
placement, and uses a local test CA and disposable Docker daemon. This bounded
qualification does not establish onboarding, public TLS, universal redaction, or
exactly-once log capture. The simulated-producer gate remains the deterministic
fault and late-response qualification.
