# Same-host artifact and private Git qualification

Run from the assigned repository root with Docker, Go, Bun, Node.js, Python 3,
and the existing Playwright Chromium installation available.

```sh
# Focused actual API/store fixture policy check, with no builds.
NEROCD_PRIVATE_GIT_POLICY_CHECK=1 bash acceptance/runtime-hosting-local-build/gate.sh

# Isolated existing private CLI Dockerfile and Railpack regressions.
NEROCD_PRIVATE_GIT_CLI_CHECK=1 bash acceptance/runtime-hosting-local-build/gate.sh

# Existing combined compiled CLI Dockerfile and Railpack private HTTPS regressions,
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
These scans cover the named fixture credentials. Unknown application secrets
can still appear in output.

The infrastructure is one new owned Docker-in-Docker container with PID/cgroup
support and an inner owned PostgreSQL. No host database port is used. Browser
mode publishes only an ephemeral `127.0.0.1` port to inner port 18080. DinD,
PostgreSQL, BuildKit, builder frontends, proxy, test CA, and builder tools retain
the existing pins. The gate records full IDs, ownership labels, pre/post container,
image and volume inventories, and inner database inventories. Cleanup checks exact
IDs/labels and image references before removing only invocation-owned containers,
volumes, image, cgroup, and temporary files. It never prunes or operates on shared
32918 or unrelated fixtures.

The fixed private-CLI selector runs only both `TestPrivateBuildProductionHTTPS`
strategies. It leaves the original combined public/private mode unchanged.
A public-case timeout or combined-mode failure remains a failed check, even if
the isolated private cases pass. Failure diagnostics use bounded actual typed
output with known fixture credentials redacted.

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

This fixture covers the private Railpack static provider on a trusted Linux
build host. Arbitrary language providers, Nixpacks, hostile-code isolation,
complete account and secret onboarding, external GitHub, and production ACME
are outside its scope. Managed
HTTPS uses the local Pebble CA. Runner control uses trusted fixture loopback HTTP
with the explicit source-credential fixture option; Git transport uses verified
test-CA HTTPS. This is not a production control-transport qualification. Preserve failed attempts separately from passes,
including their owned-resource cleanup evidence.


## Compiled Vite SPA variant

```sh
NEROCD_PRIVATE_GIT_BROWSER=1 NEROCD_PRIVATE_GIT_STRATEGY=railpack \
NEROCD_PRIVATE_GIT_COMPILED_VITE=1 \
  bash acceptance/runtime-hosting-local-build/gate.sh
```

This opt-in variant commits `package.json`, a frozen `bun.lock`, valid
`railpack.json`, an HTML entrypoint, explicit Vite output configuration and JavaScript source into the private fixture
repository. It commits no `dist` directory or dependency tree. The source owns its
build script. Railpack must install dependencies and run Vite before copying the
resulting `dist` output into the Caddy runtime. Direct versions are Node 22.22.0,
Bun 1.3.6, Vite 8.2.2 and Caddy 2.10.2. The generated plan is checked against the
selected packages, frozen install, build command and deploy output. The lockfile
was generated once with verified Bun 1.3.6 and has SHA-256
`a5594b88702e5b041cd66e61be1dbcee013cf780eb32030a13e168820da16691`.

These settings follow pinned upstream source. Railpack 0.40.1's
[Node SPA provider](https://github.com/railwayapp/railpack/blob/v0.40.1/core/providers/node/spa.go)
selects the Vite output directory and Caddy deployment. Its
[default Caddy template](https://github.com/railwayapp/railpack/blob/v0.40.1/core/providers/node/Caddyfile.template)
falls back to the root index for unmatched paths. The test therefore expects
`/account/deep/link` and `/missing.js` to return status 200 with the same HTML as
`/`. This is the actual Node SPA default, including its missing-asset behavior.
It is distinct from NeroCD's prepared-assets-only static adapter. The fixture
uses no custom Caddyfile. Its `vite.config.js` is required for detection because
the explicit Node build command does not contain the literal `vite build` selector. Vite 8.2.2's
[package engines](https://github.com/vitejs/vite/blob/v8.2.2/packages/vite/package.json)
accept Node 22.22.0.

The browser repeats the existing source-grant, build, deployment, managed-route,
viewer-denial, typed-output and responsive keyboard journey. Independent HTTPS
probes use ordinary fixture-CA verification to check the generated index, bundle
marker and SHA-256, deep link and missing-asset response. The recorded deployment
must retain the exact local artifact, daemon identity and platform. The fixture reads
the built image's `PORT`, or derives port 80 from the pinned Caddy template when
that variable is absent. The browser explicitly supplies this effective application
port, and the deployment snapshot, readiness and managed route must use it. A second build
fails on a genuine JavaScript syntax error while its planner configuration and
lockfile remain valid. It must produce no artifact. The original HTTPS bundle and
deep link must remain unchanged afterward.

A separate fresh browser context executes the generated JavaScript and its
keyboard-operated counter through a fixed read-only loopback HTTP bridge. The
bridge reads only fixed allowed paths from the owned HTTPS application, with its
fixed Host, SNI and dial target. It verifies the fixture CA, follows no redirects,
uses no ambient proxy, and forwards no cookies, authorization or caller headers.
Responses are capped at 1 MiB and requests at five seconds. The browser compares
bundle bytes with the independent HTTPS probe. This establishes browser JavaScript
execution through an HTTP test bridge and separate real HTTPS evidence; it does
not establish browser HTTPS trust or production ACME. Only this opt-in mode adds
an ephemeral host-loopback mapping to inner port 18081. All existing Dockerfile
and Railpack prepared-static selections retain their defaults.

The optional `NEROCD_PRIVATE_GIT_COMPILED_DIAGNOSTIC=1` mode preserves any
unexpected CLI failure and captures bounded owned cgroup/container counters. It
may run one separate direct-engine diagnostic with the same public source, pins
and limits. That diagnostic is not CLI qualification. Only diagnostic mode extends
the first browser wait to 420 seconds; normal qualification retains 180 seconds.
Intermittent failures have occurred after OCI export and during dependency
installation without an identified cause. Retain failing output and cleanup
evidence even when a later run passes. Diagnostic success does not qualify the
compiled CLI path.
