# SSH and systemd target fixture

This fixture qualifies a native Linux target for later SSH-assisted hosting onboarding tests.
It does not install NeroCD, test a product onboarding command, or qualify a production network.

The target runs real systemd as PID 1, OpenSSH, and its own Docker 29.4 daemon with API 1.54.
The host generates ephemeral Ed25519 host and client keys outside the image.
The SSH client trusts the independently generated host public key before it authenticates.
The wrong-host test requires rejection before the client offers its public key.
The wrong-client test requires public-key authentication denial.

The authenticated SSH session starts, checks, restarts, and stops a fixed marker service.
It also checks that an unrelated sentinel file, service process, and running container survive those operations.
No fixture operation targets a live server or a product-managed resource.

## Run the fixture

Run these commands from the repository root with Bash, Python 3.11 or newer, OpenSSH, and Docker available:

```sh
bash acceptance/runtime-server-bootstrap/guard-test.sh
python3 -B acceptance/runtime-server-bootstrap/boundary_test.py
python3 -B acceptance/runtime-server-bootstrap/relay_test.py
bash acceptance/runtime-server-bootstrap/qualify.sh
```

The qualification creates a unique image, internal network, Docker-data volume, and container.
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

Cleanup compares full container, network, and image IDs, exact names, and unique owner labels before deletion.
Docker volumes have names rather than independent immutable IDs, so cleanup checks their exact names and owner labels.
Deletion and absence checks fail explicitly even when the cleanup helper runs in a shell `||` list.
The guard tests inject inspect failures, identity changes, deletion failures, and incomplete absence checks into the actual helper.
Create-response-loss tests exercise exact-name and owner recovery without assuming a failed inspect proves absence.

The tested architecture is the Docker host's native architecture only.
Passing this fixture does not qualify NeroCD SSH onboarding, an installer, fresh TLS, data rollback, or production hosting parity.
