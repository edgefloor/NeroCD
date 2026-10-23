# SSH and systemd target fixture

This fixture qualifies a native Linux target for later SSH-assisted hosting onboarding tests.
It also exercises the bounded owned installer adapter. It does not test complete product onboarding or qualify a production network.

The target runs real systemd as PID 1, OpenSSH, and its own Docker 29.4 daemon with API 1.54.
The host generates ephemeral Ed25519 host and client keys outside the image.
The SSH client trusts the independently generated host public key before it authenticates.
The wrong-host test requires rejection before the client offers its public key.
The wrong-client test requires public-key authentication denial.

The authenticated SSH session starts, checks, restarts, and stops a fixed marker service.
It also checks that an unrelated sentinel file, service process, and running container survive those operations.
No fixture operation targets a live server or a product-managed resource.

The qualifier also compiles and hashes the fixed `internal/serverbootstrap` test binary.
It invokes the fixed `TestPreparedHostFixture` and `TestInstallerPreparedHostFixture` entry points against the same target and ephemeral key files.
That integration test calls the real Go SSH probe and checks its prerequisite observations, host mismatch, and wrong-client denial.
Fixed snapshots before and after prove that the probe leaves the unrelated file, service process, container, and marker unchanged.
The installer transfers a separately compiled exact Linux agent artifact through the real SSH adapter.
An additional PostgreSQL container on the same internal bridge supports a real registered HTTPS API inside the target.
`TestInstallerControlFixture` creates a real authorized enrollment and installs an ephemeral fixture CA in the target trust bundle.
The first successful consume response is deliberately lost after the real database effect; a successful exact replay is required.
The test checks enrollment-file transition, credential-mode restart, exact owned cleanup, partial-transfer cleanup, and a symlink collision.
An exact-unit drop-in collision must preserve the foreign configuration and never execute its replacement command.
Replacing an own runtime root with a symlink must block restart and cleanup without stopping the agent or removing the link or sentinel target.
Closed unit-property parser tests also reject reported global or type-wide drop-ins and a foreign loaded fragment.
It also checks that cleanup retains the own unit's stop failure in its receipt while leaving systemd running with no retained failed own unit.
An additional genuinely enrolled agent tests SSH response loss with a restricted ephemeral client key.
Its forced command admits only the exact frozen command hash and exact nonsecret restart request hash.
Unknown commands or requests fail before execution. Stdin, keys, and tokens are not logged or stored by the wrapper.
The wrapper records only the bounded public protocol response and child exit status, then delays delivery five seconds.
The caller has a ten-second deadline and cancels after a separate fixed fixture read observes actual completion.
Inspection and exact retry must recover the same operation and invocation ID. Wrapper process IDs receive explicit absence checks.
The wrapper's process group is bounded to 132 seconds plus a one-second kill grace; its children are reaped before cleanup.
The fixture compares unrelated sentinels after installation and cleanup as well as after the read-only probe.
Source and agent/test binary hashes bind the evidence to the candidate; this remains a bounded adapter fixture, not complete onboarding.

## Run the fixture

Run these commands from the repository root with Bash, Python 3.11 or newer, OpenSSH, and Docker available:

```sh
bash acceptance/runtime-server-bootstrap/guard-test.sh
python3 -B acceptance/runtime-server-bootstrap/boundary_test.py
python3 -B acceptance/runtime-server-bootstrap/relay_test.py
bash acceptance/runtime-server-bootstrap/qualify.sh
```

The qualification creates a unique image, internal network, two labelled containers, and two labelled named data volumes.
PostgreSQL has no published port and uses trust authentication only inside this disposable internal fixture network.
It writes public evidence to `.cache/nerocd-server-bootstrap.<random>/` and deletes ephemeral keys on exit.
A successful run ends with the fixture-only pass message, relay shutdown, and successful resource absence checks.
The evidence includes exact resource identities, Docker configuration, package versions, source hashes, and SSH rejection logs.

## Isolation and transport limits

The container uses a private cgroup namespace, `SYS_ADMIN`, `NET_ADMIN`, and an unconfined seccomp profile.
These are trusted local fixture privileges. They do not provide isolation for untrusted code.
The fixture does not use privileged mode, host PID sharing, device passthrough, host Docker sockets, or bind mounts.
It does not change the host Docker daemon, packages, cgroups, or networking configuration.

Before starting the container, `check-isolation.py` checks the Docker configuration.
The entrypoint requires cgroup v2 and namespace membership `0::/` before remounting Docker's provided cgroup mount writable.
It never mounts a host cgroup directory. Systemd receives only the container's namespace-provided hierarchy.
If these checks or real systemd fail, the fixture stops without adding privileges or using a replacement init system.

The target's only network is an owned internal bridge. It has no outbound package downloads or image pulls at runtime.
Docker binaries and the native sentinel image come from the pinned official Docker image during preparation.
The private daemon uses a Unix socket, the `vfs` driver, no bridge, no userland proxy, no iptables, and no IP forwarding.

Docker does not publish the requested SSH port on this internal network in the tested environment.
`relay.py` therefore binds an automatically assigned host loopback port and forwards bytes through a fixed Docker-exec argument vector.
The exec process runs `socat STDIO TCP:127.0.0.1:22,connect-timeout=5` inside the target.
The SSH handshake, host verification, authentication, and remote commands remain end to end.
This is a fixture Docker-exec TCP relay, not direct Docker NAT or production SSH reachability evidence.

The relay verifies the full container ID and owner label before listening and before each connection.
It permits at most four concurrent connections, limits each connection to 25 seconds, and stops after 180 seconds.
Shutdown closes the listener, cancels connections, and reaps its own Docker-exec children before container cleanup.

## Pins and evidence interpretation

The final base is the official Debian `bookworm-20261020-slim` image at index digest
`sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8`.
The Docker binary donor and preloaded sentinel use official Docker `29.4.0-dind` at index digest
`sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489`.
The fixture selects the native architecture and validates the saved sentinel's native config digest before running it.
The same donor supplies Compose `v5.1.1` (`version --short` reports `5.1.1`). The tested arm64 binary SHA256 is
`4b5c42952b7dd81f508d01a771df2a9e5dbffe9b8c5c7d983e738504ad38f056`.
It is initially preloaded outside plugin lookup paths to preserve the missing-plugin probe test.
Fixed fixture setup then copies it into the system plugin path before the installer test.
The positive profile requires Debian 12, matching native architecture, systemd running, Docker API 1.54,
Compose exactly `5.1.1`, and at least 1 GiB free on root. Buildx is not required; no arbitrary minimum is inferred.
The product installer never installs Docker or plugins.

Direct package pins are systemd `252.39-1~deb12u2`, OpenSSH server and client `1:9.2p1-2+deb12u10`, and socat `1.7.4.4-2`.
APT verifies Debian's signed repository metadata. Dependency versions are recorded in `packages.txt`.
The repository can remove old package versions, so a future build can fail rather than silently replacing a direct pin.
Dependency resolution is recorded evidence, not a promise of a fully reproducible package snapshot.

The image and package choices follow the [official Debian image documentation](https://hub.docker.com/_/debian),
[Debian systemd package](https://packages.debian.org/bookworm/systemd),
[Debian OpenSSH package](https://packages.debian.org/bookworm/openssh-server), and
[Debian socat package](https://packages.debian.org/bookworm/socat).
The cgroup checks follow the [systemd container interface](https://systemd.io/CONTAINER_INTERFACE/).
Docker's [binary installation documentation](https://docs.docker.com/engine/install/binaries/) describes the testing limits of static binaries.

Each service is checked separately with `systemctl is-active`, followed by a successful private `docker info` and actual sentinel container operation.
A multi-unit `systemctl is-active` result alone cannot prove that every listed service is active.
Only a complete qualification exit of zero counts as a fixture pass.

## Complete API onboarding extension

`NEROCD_BOOTSTRAP_ONBOARDING=owned-systemd-target bash acceptance/runtime-server-bootstrap/qualify.sh`
also compiles the Linux API fixture and runs it after the adapter qualification.
It uses a separate database in the same owned PostgreSQL container. Production
HTTP handlers admit every operation and command. The normal River consumer calls
the configured production bootstrap processor, which connects to the target's
real OpenSSH server and starts the transferred agent through systemd. The agent
consumes enrollment over HTTPS independently of the test driver.

The extension requires native arm64. It covers explicit true and false project
grant choices, a later explicit grant, committed create and command response
loss with exact retries, enrollment response loss, a real expired review and
fresh probe, rejected artifact and receipt input, viewer denial, registration
binding, and a separately observed readiness report. It never seeds bootstrap
operations or submits SSH observations through a test endpoint. Its controller
and SSH target share the disposable container. The HTTPS test CA is trusted
explicitly by the API client and installed only inside that container for the
agent. This does not qualify production DNS, TLS issuance, or network reachability.
The original qualifier remains unchanged unless this opt-in is set.

The recovery case uses a transparent loopback TCP forwarder to the same real
sshd. It counts encrypted upload bytes and withholds server response bytes after
the artifact-sized transfer. It never decrypts traffic, changes SSH identity, or
substitutes a remote result. Independent exact ownership inspection must observe
the active service before the test stops and joins the hosting execution loop.
The restarted execution loop recovers from the durable intent after real lease
expiry. Exact manifest, remote operation and systemd InvocationID must survive
inspection with the hold retained. This proves an execution lifecycle restart
against the durable database. It does not restart the whole controller process.
The forwarder binds only loopback, permits four connections, applies 125-second
connection deadlines, and joins its connections and byte pumps on cancellation.
Separate loopback forwarded ports give the true/false grant and browser cases
separate owned fixture destinations. This respects durable endpoint ownership.
It is a test arrangement, not a recommendation for production cohosting.

Run `bash web/app/scripts/hosting-server-bootstrap-fixture.sh` from the repository
root for the additional browser journey. It builds the current UI before
compiling the embedded API test binary. The browser uses the real production
handlers for project, existing managed key version, qualified artifact,
create/probe/review/install and readiness. It drops one committed create response
and retries the identical body. It checks keyboard review at widths 320, 390,
430 and 1440, viewer denial, and served asset hashes against `web/dist`.

For this browser fixture only, the Docker-exec TCP relay has a fixed HTTPS
profile from host loopback port 19443 to target loopback port 19443. The original
SSH profile and its 180-second lifetime remain the default. The HTTPS profile
uses the same full ID/name/owner checks, four-connection admission and 25-second
per-connection deadline, with a 600-second whole-run lifetime. The browser's
Chromium process trusts the exact ephemeral certificate SPKI from this run.
It does not use `ignoreHTTPSErrors`, bypass CSP, or change authentication/CSRF
rules. This is fixture certificate pinning and same-host transport qualification.
It does not prove production TLS or public reachability. The four-capability
`hosting_image_v1` preset remains limited to image hosting and readiness; source
builds, proxy operations and runtime logs require their separate qualification.

Cleanup compares full container, network, and image IDs, exact names, and unique owner labels before deletion.
Docker volumes have names rather than independent immutable IDs, so cleanup checks their exact names and owner labels.
Deletion and absence checks fail explicitly even when the cleanup helper runs in a shell `||` list.
The guard tests inject inspect failures, identity changes, deletion failures, and incomplete absence checks into the actual helper.
Create-response-loss tests exercise exact-name and owner recovery without assuming a failed inspect proves absence.

The tested architecture is the Docker host's native architecture only.
Passing this fixture qualifies the bounded adapter on that native prepared target.
It does not qualify complete NeroCD SSH onboarding, production installation or networking, fresh TLS, data rollback, or production hosting parity.
