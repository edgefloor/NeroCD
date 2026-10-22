# Managed Linux proxy engine

`internal/hostingproxy` manages a single Traefik instance on an enrolled Linux
host. It is a standalone component. Public route APIs, database authorization,
runner commands, fenced proxy holds, deployment binding selection, and WebUI
integration are separate work. Calling this package does not authorize a route.
The caller must first hold the controller's fenced proxy lease and derive each
upstream from a settled healthy binding.

## Typed contract

Construct trusted operator `Settings` with an absolute persistent state
directory, the absolute Docker CLI path, one exact Unix Docker socket, an ACME
email, a closed `CA` choice, and an unprivileged loopback management port.
`New` refuses non-Linux hosts. The package still compiles on other platforms.
Project input must never populate Settings.

`Prepare(identity, settings, generation, previous, routes)` returns an opaque
immutable `Plan`. Identity contains the proxy, server, destination, runner, and
exact Docker daemon IDs. A route contains only an ID, a lower-case DNS hostname,
a literal canonical origin path, and an unprivileged host port. Upstreams are
always `http://127.0.0.1:<port>`. The package accepts no URL, raw router rule,
Docker labels, certificate key, or user configuration document. Copies and
stable ordering prevent callers from mutating a prepared plan.

Plans have deterministic dynamic and static hashes. Static settings and immutable
identity remain separate from the dynamic generation. Container labels bind the
static hash, because labels do not change when the file provider reloads routes.
The caller can reconstruct a plan from persisted typed inputs. Do not expose
operator paths or email settings as a downloadable project plan.

- `Install(ctx, plan)` creates an absent proxy, or verifies an existing matching
  proxy and generation. It refuses a foreign named container, changed static
  settings, or occupied port 80, 443, or the management port. It never adopts,
  replaces, stops, or deletes a foreign container.
- `Apply(ctx, plan)` requires the exact existing owned container and previous
  committed generation. A replay of the same generation and hash verifies runtime
  state and acknowledges it. Stale generations or a changed hash cannot overwrite
  the current receipt.
- `Observe(ctx, plan)` requires a matching committed receipt, owned container,
  loaded configuration, and Host/path probes. It returns nonsecret certificate
  observations, including hostname, issuer, SHA-256 fingerprint, expiry, and status.

Every Docker command includes the exact `--host unix://...` and an empty protected
Docker config directory. The engine removes ambient Docker settings and verifies
Linux daemon identity and architecture directly over that Unix socket before
each command. The CLI has bounded output, a command deadline, and Linux process
group cancellation. This check cannot provide atomic fencing against a daemon
replacement between verification and execution. The upstream controller lease
remains mandatory.

## Routing, TLS, and runtime verification

The official pinned Traefik image uses host networking, public ports 80 and 443,
a file provider, and an API bound only to `127.0.0.1:<management port>`. The
public dashboard is disabled. Traefik has no Docker provider or Docker socket
mount. Its configuration mount is read-only, its filesystem is read-only, and
only persistent certificate storage is writable. It uses `unless-stopped`.

A path `/api` matches exactly `/api` and the segment prefix `/api/`. `/apix`
continues to match a broader route. The proxy preserves the request path and
orders longer paths first. ACME paths are reserved. The management entry point
is separate from public application `/api` routes.

Readiness checks Traefik's actual raw router and service state, including rules,
entry points, middleware, priorities, and loopback target ports. It also performs
Host/path requests and verifies the generated route ID and generation response
headers. An application's 401 or 404 does not mean that configuration failed.
A response from a different router does not prove the desired route loaded.

HTTP routing can be applied while TLS is pending. TLS readiness additionally
requires successful system trust and SNI hostname verification, an unexpired
non-CA leaf certificate, and the matching route/generation response. Default,
self-signed, and untrusted certificates never count as HTTPS ready. An ACME
failure for a new hostname does not roll back valid loaded routes or erase an
existing certificate.

Production code accepts only Let's Encrypt production and staging directory
choices. HTTP-01 uses port 80. Traefik owns persistent account and certificate
storage and renews certificates itself. There is no DNS-01 or wildcard certificate
support in this component. A separately authorized wildcard route can later be
expanded into individually authorized HTTP-01 hostnames. The local fixture's CA
and trust-root injection is unexported and inaccessible to project input.

## Persistent state and uncertain outcomes

The state root and subdirectories require mode 0700 and the effective user's
ownership. Certificate state and receipts require mode 0600. Existing symlinks
and unsafe modes are rejected. File operations use `os.Root`; the state directory
must have trusted ancestors, and the host administrator must not replace those
ancestors or mounts during an operation. The Linux file lock serializes engine
processes sharing this directory. It does not establish global exactly-once
execution or protect against a privileged host administrator.

Before changing external state, the engine fsyncs an intent receipt. A complete
candidate configuration is written outside the watched directory, fsynced,
atomically renamed into it, and followed by a directory fsync. The previous
configuration remains outside the watched directory. Only verified runtime loading
commits the receipt. Rejected configuration restores the previous bytes and
verifies the previous routers and probes before reporting `rolled_back`.

`Result.Outcome` is `applied`, `rolled_back`, `refused`, or `uncertain`. An error
after a journaled effect, cancellation after publication, a lost Docker response,
an unverifiable rollback, or an unfinished intent remains `uncertain`. The caller
must retain its hold. The engine deliberately does not erase or automatically
acknowledge an interrupted intent. Host operator recovery must reconcile the
actual container, files, receipt, and loaded routers before future controller
work is admitted. A leftover candidate after a local crash also fails closed.
There is no automatic recovery API in this package.

Persist the whole state directory and preserve identity, absolute mount paths,
ownership, modes, and receipts across restarts. For backup, retain the controller
hold, stop the exact owned Traefik container, acquire the local engine lock, copy
the complete state into an owner-only backup location, preserve 0600 certificate
files, fsync the backup, release the lock, and restart that same container. Restore
only while the sole writer is stopped and the hold and lock are retained. Restore
to the same absolute operator path and identity, then verify loaded routes and
trusted TLS. A live copy of `acme.json` is not a coordinated backup. The engine
never returns ACME account data or private certificate keys in results or errors.

## Image provenance and licenses

The runtime image is `docker.io/library/traefik` at immutable OCI index
`sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368`,
resolved with `docker buildx imagetools inspect docker.io/library/traefik:v3.7.15`
on 2026-10-22. The Linux arm64 manifest is
`sha256:e4b3dc44af95a546106a89d5200cea77d897e1acbaf4dc8211875c0621bd192c`;
the amd64 manifest is
`sha256:10cc4f33663c9d71049b71775d0b2ac67c18f7ade489ce9d826acc292d7eb82a`.
Registry annotations identify version v3.7.15 and official image source revision
`faf4015ef956ac855b480c94cfeec96f691d2cf4`.

Sources are the [official signed release](https://github.com/traefik/traefik/releases/tag/v3.7.15),
[official image source](https://github.com/traefik/traefik-library-image/tree/faf4015ef956ac855b480c94cfeec96f691d2cf4),
[MIT license](https://github.com/traefik/traefik/blob/v3.7.15/LICENSE.md),
[file provider documentation](https://doc.traefik.io/traefik/v3.7/reference/install-configuration/providers/others/file/),
and [ACME documentation](https://doc.traefik.io/traefik/v3.7/reference/install-configuration/tls/certificate-resolvers/acme/).
The pinned upstream [renewal implementation](https://github.com/traefik/traefik/blob/v3.7.15/pkg/provider/acme/provider.go)
renews within its certificate window at startup and on its periodic timer.
Base-image and bundled dependency licenses remain governed by their respective
upstream notices. This package does not copy Coolify source.

## Run the isolated acceptance fixture

Run `acceptance/runtime-proxy/gate.sh` from this checkout with Docker available
and this module's dependencies already in the local Go cache. It compiles a Linux
race-enabled test binary in pinned Go 1.26.10 with networking disabled, then runs
a disposable privileged DinD daemon with no host ports published. Only this
fixture container is cleaned up by its exact ID and matching ownership label.
No production DNS, production ACME, or user containers participate.

The fixture pins Docker 29.4.0 DinD index
`sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`,
Go index `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`,
and [Pebble](https://github.com/letsencrypt/pebble), Apache-2.0, at
`sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199`.
Docker Engine is Apache-2.0; Go is BSD-3-Clause. The test-only DNS server handles
UDP and TCP in the isolated Linux namespace. Two real loopback HTTP backends
return deliberate 404 responses with their received paths. Pebble performs actual
HTTP-01 validation and issues deliberately short-lived certificates so Traefik's
startup or periodic renewal can be observed. The test-only CA uses a one-hour certificate duration setting to check renewal every minute; production retains Traefik's normal renewal policy.

The gate covers trusted issuance, routing semantics, concurrent generation CAS,
container restart and fresh-engine replay, foreign proxy and 80/443 collisions,
daemon mismatch, real malformed-file rollback, cancellation after publication,
lost Docker run response, ACME failure continuity, renewal, coordinated stopped-
writer backup/restore, and persistent permissions. Linux unit tests also cover
unsafe directories, symlinks, nonempty Docker config, atomic replacement, and
lock cancellation. Ordinary unit/race/vet checks target `./internal/hostingproxy`.
