# Runtime log viewer browser gate

Run from the repository root after building the UI:

```sh
bun run --cwd web/app build
bash acceptance/runtime-hosting-logs-browser/gate.sh
```

Prerequisites are Go with race support, Docker, Python 3, Node.js, Bun, the existing
Playwright Chromium installation, and unused loopback port 32963. The gate owns a
PostgreSQL container pinned by digest, compiles the API test binary after the UI
build, and prints its SHA-256 identity. It runs the runtime-log control contract
tests before the browser fixture. It does not use PostgreSQL 32918, 32961, or
32962 and does not start a Docker log collector.

The browser uses actual registered API responses and the Go-embedded UI over an
ephemeral host-loopback HTTP port. A simulated authorized producer uses runner
claims and events through a verified test-CA TLS transport. Seeded accounts,
resource placement, and settled deployment receipts are fixture setup. The gate does not collect actual Docker output or qualify public TLS or
complete onboarding.

The fixture-only stage and completion endpoints require the exact seeded owner
session and Origin. They accept only fixed action names for the fixture resource.
They are not registered in production. The browser logs in as a project viewer
without Task setup or Task mutation/operation permission and checks explicit demand, literal output,
actor-bound sessions, retention/gaps, stop, demand expiry, replacement, and
membership revocation. Fault injection may forward an actual response and
withhold it to exercise deadlines and late completion; it fabricates no log
body or successful API result.

Traces and automatic failure screenshots are disabled. Explicit screenshots
contain the log section with simulated text only. Interaction and overflow
checks use widths 320, 390, 430, and 1440 with a 1000-pixel height. Full-section
captures use a taller viewport and scroll below the sticky app header.

The browser completion bridge and bounded test context release the fixture after
failure. Preserve complete command output, including failures, contract results,
binary hashes, container IDs/owner labels, and schema inventories. Cleanup checks
the full PostgreSQL container ID and exact invocation label before removal. The
test drops its own schema; before and after inventories should contain only
`public`. No global prune or unrelated container lifecycle operation is used.

The project viewer role also permits legacy Task reads. This gate does not
claim a separate denied Task-read permission. Actual Docker collector coverage
is exercised by `NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER=1 bash
acceptance/runtime-hosting-logs/gate.sh`; this simulated-producer gate remains
useful for deterministic fault and response-lifecycle coverage.
