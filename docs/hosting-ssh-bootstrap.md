# SSH prerequisite probe and owned installer

`internal/serverbootstrap.Probe` checks a prepared Linux host before installation.
It returns individual prerequisites, not an enrolled runner identity, production TLS evidence, or aggregate server readiness.
`Probe` remains read-only. Separate typed installer methods manage only a new, exactly owned NeroCD agent.
The package exposes no arbitrary remote command method.

## Contract

`Probe(ctx, Target, privateKey)` requires a caller deadline and limits the entire operation to 15 seconds.
`Target` contains a `netip.Addr`, nonzero port, user `root`, and an independently trusted canonical SHA256 host-key fingerprint.
Unspecified, multicast, broadcast, invalid, and zoned addresses are rejected before dialing.
IPv4-mapped literals are normalized before validation and connection.
No DNS, SSH agent, password, proxy configuration, forwarding, or host-key learning is supported.
Private and loopback IPs are explicit trusted operator inputs, not a public endpoint policy or sandbox guarantee.

Private keys are limited to 64 KiB and parsed with the existing pinned `golang.org/x/crypto/ssh` dependency.
Unencrypted forms accepted by `ssh.ParsePrivateKey`, including OpenSSH Ed25519 and supported PEM keys, are accepted.
RSA moduli above 8192 bits return `InvalidPrivateKey` before dialing, limiting subsequent client signing work.
Encrypted or malformed keys return `InvalidPrivateKey`. The package does not log or retain keys after the call.
Go does not guarantee erasure of key bytes or parsed key material from memory.
The dependency's synchronous key parser is not context-preemptible. The 64 KiB input limit bounds its input,
but the deadline does not provide a hard CPU-time guarantee for parsing or other synchronous cryptographic work.
Callers must supply trusted operator key material; this package is not a public untrusted-key parsing endpoint.

The host-key callback compares the trusted fingerprint before client authentication.
The TCP socket has the effective deadline, and a joined cancellation helper closes it during handshake, channel creation, or execution.
Combined stdout and stderr share a synchronized 16 KiB limit. Overflow immediately closes the transport.
Malformed or incomplete protocol output returns zero observations.

Errors expose only fixed `ErrorCode` values:

- `InvalidTarget`, `DeadlineRequired`, and `InvalidPrivateKey` describe rejected inputs.
- `HostKeyMismatch` describes failed pinned trust.
- `HandshakeFailed` covers SSH handshake and authentication failure without parsing dependency error strings.
- `TransportFailed` covers dial, session, or recipe execution failure.
- `Canceled` and `DeadlineExceeded` support `errors.Is` with the corresponding context error.
- `OutputLimitExceeded` and `InvalidOutput` reject unsafe output.

No underlying SSH error, raw remote output, target details, or key material appears in these errors.

## Observations and trust

The constant recipe reports OS metadata and normalized architecture, real systemd PID 1 and running state,
the local Docker API version, Compose and Buildx versions, and root filesystem total and available bytes.
Each observation has an explicit `Available`, `Missing`, `Unavailable`, or `Unsupported` state as applicable.
Systemd `degraded`, `starting`, and `offline` remain unavailable prerequisites.
Disk block-to-byte conversion rejects overflow, zero total capacity, and available capacity above total capacity.

The recipe reads `/etc/os-release` as data and never evaluates its contents.
Version and metadata tokens are bounded and validated before they enter public observations.
No returned value is evaluated as a shell command.
The recipe runs beneath `env -i` with a fixed system `PATH` and locale.
Docker uses the explicit local Unix socket and `/proc/self/fd` as a non-user config directory.
Inherited Docker host, TLS, context, and config environment variables cannot select another daemon.
Compose and Buildx are executed directly from fixed system plugin directories rather than user client configuration.

The SSH account, SSH server, operating system, and installed system binaries remain trusted host-admin inputs.
An administrator can replace a binary, change the account's SSH startup behavior, or falsify reported metadata.
The probe observes prerequisites through that trust boundary; it does not attest the host or authorize an installation.

## Qualification

Run the package's real loopback SSH tests with `go test -race ./internal/serverbootstrap`.
They cover host mismatch before authentication, wrong client keys, invalid inputs before dialing,
actual stalled-handshake cancellation and the 15-second limit, command cancellation, and bounded protocol output.

`bash acceptance/runtime-server-bootstrap/qualify.sh` builds and hashes the package test binaries and agent,
then invokes fixed probe and installer tests against its owned systemd target and ephemeral keys.
The fixture checks actual probe values and compares unrelated sentinels before and after both operations.
Its Docker-exec TCP relay remains a local fixture transport, not production SSH reachability evidence.
See the [fixture contract](../acceptance/runtime-server-bootstrap/README.md) for privileges, pins, cleanup, and limitations.

## Exact owned installation

`Install`, `InspectOwned`, `RestartOwned`, and `CleanupOwned` accept the same explicitly trusted SSH target and key policy.
They require a caller deadline. Network execution is capped at 120 seconds, with a fixed remote GNU `timeout`
process group receiving TERM at 120 seconds and KILL five seconds later, independently of the SSH socket.
Local artifact reads and synchronous key parsing remain bounded by input size rather than hard CPU time.
Each action uses a fixed stdin-framed protocol and fixed system tools beneath an empty environment and system `PATH`.
Returned errors contain only safe codes; combined remote output is bounded to 16 KiB.
An accepted command whose response cannot be verified returns `Uncertain`, including cancellation after submission.

The immutable v1 `InstallManifest` binds an installation ID, initial install operation ID, runner ID,
opaque credential reference, HTTPS controller origin, exact artifact inventory, and the fixed `hosting_image_v1` preset.
Installation and operation IDs are exactly 32 lowercase hexadecimal characters.
`CanonicalManifest` validates the manifest and returns its exact JSON bytes and SHA256 identity.
Enrollment tokens and SSH keys are separate inputs and never enter this identity or public receipts.
Transport ownership is not controller authorization or a secret grant.

The caller supplies a trusted local artifact path, exact size, SHA256, version, and `amd64` or `arm64` architecture.
The artifact must be a regular Linux ELF64 little-endian executable of at most 256 MiB.
No pathname component may be a symlink. The client opens the file once, validates it, and transfers from that descriptor.
Replacing its pathname cannot substitute another file. A host administrator who changes the opened inode can still cause
a failed transfer; the target independently checks exact size, SHA256, and ELF platform before executing any uploaded byte.
There are no download URLs, channels, or `latest` choices.

The fixed namespace is `/var/lib/nerocd-bootstrap`, with mode-0700 root-owned directories and mode-0600 metadata
and credentials. A new artifact is mode 0700. Each installation has an exact unit named
`nerocd-agent-<installation-id>.service`. Existing foreign, permissive, or symlink paths block installation.
The transport never adopts an existing installation or replaces another agent.
An existing exact unit drop-in directory, including a symlink, blocks admission.
Loaded systemd properties must identify the exact own fragment and no drop-ins, including global or type-wide drop-ins.
The helper verifies this after daemon reload and before starting the unit; restart and cleanup use the same check.
Foreign configuration is preserved and never deleted to make admission succeed.
Only the new own unit is created, enabled, started, restarted, disabled, or removed.
It does not install packages, Docker, plugins, firewall rules, or host tools; restart Docker; or modify workloads.
Install first observes Linux architecture, running systemd, and Docker API 1.54 through the unchanged probe.

The shipped fixed helper runs only as root with the inherited exact bootstrap lock descriptor.
The root-owned mode-0600 global lock inode is created exclusively and never unlinked or replaced.
Immutable ownership and durable per-operation receipts are synced before service side effects.
An incomplete receipt, a pending systemd job, or a changed owner blocks competing mutations.
Existing work, journal, and secret roots must remain root-owned mode-0700 directories, with no symlinks.
Loaded units and the launcher require all three roots. The lock coordinates this protocol; it does not constrain the host administrator.
Inspection reads evidence without installing, renewing, or clearing an unresolved receipt.
Database lease expiry cannot establish that a remote process stopped.
There is no generic unlock or recovery operation in this adapter.

The launcher delegates to the existing `runner` command with deployment and hosting-readiness capabilities only.
While the enrollment file exists, the existing runner persists a generated credential and stable consume request before consuming it.
After successful consumption it removes the enrollment file and uses credential mode on subsequent launches.
Systemd retries failed launches with the same persisted files, with a five-second delay and a fixed start-rate limit.
No enrollment token or generated credential appears in command arguments, unit contents, public results, or helper logs.
File presence and an active systemd process are observations; neither proves authenticated enrollment or server readiness.

Cleanup requires exact ownership, stops the own service, and removes only verified known files and empty own data directories.
Known entries are removed relative to opened no-follow directory descriptors; cleanup never traverses data directories.
It removes only its exact enabled unit link, preserving administrator-created aliases elsewhere.
Before removing the unit, cleanup preserves its bounded systemd stop result and exit status in the durable operation receipt.
It then clears only that verified stopped unit's retained failure record, so requested runner cancellation does not leave the host degraded.
Other units and unexpected failures are not reset, and the service's accepted exit statuses are unchanged.
Unknown or nonempty data is retained. A nonsecret tombstone preserves the exact manifest and cleanup operation identity.
An exact cleanup replay can read that result; a different action or operation cannot borrow it.
Partial transfers remain staged and cannot execute. Their exact claim supports inspection and bounded cleanup.
Interrupted mutations with unresolved receipts remain held rather than being silently retried.

## Installer qualification limits

The disposable target shares an internal-only bridge with a separate owned PostgreSQL container.
The target runs a registered NeroCD HTTPS API on `127.0.0.1:18443`, using an ephemeral fixture CA installed only in that target.
The fixture creates a real authorized enrollment and deliberately drops the first successful consume response after its database effect.
It then verifies an exact consume replay, one enrolled identity, credential-mode restart, owned cleanup, and unchanged sentinels.
The current prepared profile is Debian 12 on the tested native `arm64` host, systemd running, Docker API 1.54,
Compose exactly `5.1.1`, and at least 1 GiB free on the root filesystem. Buildx is not required.
The fixture first verifies Compose is missing, then prepares its pinned donor binary before the installer test.
This preparation is outside the product adapter and does not add a plugin installation feature.

A separate genuinely enrolled fixture agent exercises actual SSH mutation response loss.
A restricted test key permits only the exact frozen command and exact nonsecret restart request.
The fixture delays the successful public response; the caller cancels after observing the completed mutation marker.
Normal-key inspection recovers the original operation, and an exact retry must preserve its invocation ID.
Unknown commands and other requests are rejected before execution. No production failure switch is added.
This same-host fixture TLS topology does not qualify production network reachability or production TLS.
The SSH transport remains the bounded Docker-exec TCP relay described in the fixture contract.

The [SSH onboarding controller](hosting-ssh-onboarding.md) supplies API authorization,
secret grants, installation review, durable retry, and UI controls. This package
performs only the fixed remote probe and owned installation actions.
