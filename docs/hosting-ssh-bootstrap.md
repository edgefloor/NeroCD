# SSH prerequisite probe

`internal/serverbootstrap.Probe` observes a prepared Linux host before a future reviewed installation.
It returns individual prerequisites, not an enrolled runner identity, production TLS evidence, or aggregate server readiness.
The package does not expose an arbitrary command method, install software, create files, or change services.

## Contract

`Probe(ctx, Target, privateKey)` requires a caller deadline and limits the entire operation to 15 seconds.
`Target` contains a `netip.Addr`, nonzero port, user `root`, and an independently trusted canonical SHA256 host-key fingerprint.
Unspecified, multicast, broadcast, invalid, and zoned addresses are rejected before dialing.
IPv4-mapped literals are normalized before validation and connection.
No DNS, SSH agent, password, proxy configuration, forwarding, or host-key learning is supported.
Private and loopback IPs are explicit trusted operator inputs, not a public endpoint policy or sandbox guarantee.

Private keys are limited to 64 KiB and parsed with the existing pinned `golang.org/x/crypto/ssh` dependency.
Unencrypted forms accepted by `ssh.ParsePrivateKey`, including OpenSSH Ed25519 and supported PEM keys, are accepted.
Encrypted or malformed keys return `InvalidPrivateKey`. The package does not log or retain keys after the call.
Go does not guarantee erasure of key bytes or parsed key material from memory.

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

`bash acceptance/runtime-server-bootstrap/qualify.sh` builds and hashes the package test binary,
then invokes only `TestPreparedHostFixture` against its owned systemd target and ephemeral keys.
The fixture checks actual probe values and compares unrelated sentinels before and after the probe.
Its Docker-exec TCP relay remains a local fixture transport, not production SSH reachability evidence.
See the [fixture contract](../acceptance/runtime-server-bootstrap/README.md) for privileges, pins, cleanup, and limitations.

Application API authorization, bootstrap secret grants, installation review, durable retry, and UI controls remain outside this package.
