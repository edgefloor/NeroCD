# Same-host artifact and private Git qualification

Run from the assigned repository root with Docker, Go, Bun, Node.js, Python 3,
and the existing Playwright Chromium installation available.

```sh
# Focused actual API/store fixture policy check, with no builds.
NEROCD_PRIVATE_GIT_POLICY_CHECK=1 bash acceptance/runtime-hosting-local-build/gate.sh

# Existing compiled CLI Dockerfile and Railpack private HTTPS regressions,
# plus the original public local-artifact deployment case.
NEROCD_PRIVATE_GIT_RUNTIME=1 bash acceptance/runtime-hosting-local-build/gate.sh

# Private Railpack browser qualification.
NEROCD_PRIVATE_GIT_BROWSER=1 NEROCD_PRIVATE_GIT_STRATEGY=railpack \
  bash acceptance/runtime-hosting-local-build/gate.sh

# The existing private Dockerfile browser selection remains the default.
NEROCD_PRIVATE_GIT_BROWSER=1 bash acceptance/runtime-hosting-local-build/gate.sh
```

The browser mode builds the UI before compiling the embedded Go fixture. It
records the deterministic manifest at `/tmp/nerocd-private-git-browser-assets.json`
and prints its hash plus compiled CLI/test identities. Set
`NEROCD_PRIVATE_GIT_BROWSER_MANIFEST` to retain the manifest elsewhere. The browser
checks every served asset against it, excluding the unserved `.gitkeep` embedding
placeholder. Browser traces and automatic screenshots are disabled. Explicit
320/390/430/1440 screenshots contain the application/build view, with no secret
value form or secret reveal request.

The owner creates a private HTTPS Git resource and selects the fixed build
strategy through the UI. An edit changes its requested ref and verifies resource
and source generations, saved strategy, and private selection across strategy
switches. The owner saves placement, selects an existing secret's exact version,
and creates a source grant. A lost response is injected only after the real API
commits; explicit retry sends the identical body and receives the same grant.
The browser then builds with the compiled production CLI, inspects typed build
output, deploys the exact recorded local artifact, and saves a managed route.
The admitted snapshot must retain the Railpack strategy. Actual Docker events
confirm the pinned planner image started, and the fixture validates its generated
plan and records its hash. Typed BuildKit rows must show the Railpack plan and
Railpack-generated work. A quiet planner emits no fabricated `railpack` row;
components depend on actual tool output. Separate engine tests retain planner
output transport coverage.

A fixture-only stage independently probes the serving HTTPS application with the
fixture CA before committing failing source. Dockerfile uses `RUN exit 9`.
Railpack uses malformed `railpack.json`, which reaches the genuine pinned planner
failure path. A second UI build must fail without an artifact or deployment
control. An independent HTTPS probe after failure must still return the original
`local-A` application bytes. Known repository credentials, their Basic encoding,
runner credentials, and the fixture API token are scanned in persisted logs and
audits, journal files, typed output, and captured CLI output. Both build checkouts
must be removed. The Git transport trace separately checks argv and environment.
These checks establish absence of those known values, not universal redaction.

The infrastructure is one new owned Docker-in-Docker container with PID/cgroup
support and an inner owned PostgreSQL. No host database port is used. Browser
mode publishes only an ephemeral `127.0.0.1` port to inner port 18080. DinD,
PostgreSQL, BuildKit, builder frontends, proxy, test CA, and builder tools retain
the existing pins. The gate records full IDs, ownership labels, pre/post container,
image and volume inventories, and inner database inventories. Cleanup checks exact
IDs/labels and image references before removing only invocation-owned containers,
volumes, image, cgroup, and temporary files. It never prunes or operates on shared
32918 or unrelated fixtures.

The fixed policy-only selector runs only `TestPrivateFixtureResourcePolicyPostgres`.
It checks real source edits and name-only edits before the full browser journey.
The test repository adapter preserves its original trusted loopback policy when
configuration changes; production public HTTPS policy remains unchanged.

The private source, completion and failure controls exist only in the opted-in
test binary. Fixed bridge paths require the exact seeded owner cookie session and
Origin. The failure stage is ordered and accepts no shell, URL, path, or arbitrary
source input. A viewer can read builds but cannot create a grant, request a build,
deploy, or invoke fixture stages. Repository credentials are seeded into an
existing managed secret; their plaintext never enters browser metadata.

This qualifies the existing private Railpack static provider on a trusted Linux
build host, not arbitrary language providers, Nixpacks, hostile-code isolation,
complete account/secret onboarding, external GitHub, or production ACME. Managed
HTTPS uses the local Pebble CA. Runner control uses trusted fixture loopback HTTP
with the explicit source-credential fixture option; Git transport uses verified
test-CA HTTPS. This is not a production control-transport qualification. Preserve failed attempts separately from passes,
including their owned-resource cleanup evidence.
