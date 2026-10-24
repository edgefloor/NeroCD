#!/usr/bin/env bash
# Fixed-target disposable launcher; join each task-owned command process group.
set -euo pipefail
exec python3 -c 'import sys; source = open(sys.argv[1]).read().split("# PYTHON SOURCE\n", 1)[1].rsplit("\nPY\n", 1)[0]; exec(compile(source, sys.argv[1], "exec"))' "$0"
: <<'PY'
# PYTHON SOURCE
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

OWNER = "nerocd-database-origin-upgrade-20261024-b084"
LABEL = "io.nerocd.database-origin-upgrade.owner"
NAME, VOLUME = OWNER + "-dind", OWNER + "-docker-data"
ENDPOINT = "unix:///var/run/docker.sock"
DAEMON = "e37dd219-b13f-47dd-99ed-21d79442ed0c"
IMAGE = "docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489"
# Complete signed v3.23/main/aarch64 dependency closure, including musl. The
# pinned outer image's BusyBox ps cannot supply the required numeric uid column.
PROCPS_REPOSITORY = "https://dl-cdn.alpinelinux.org/alpine/v3.23/main"
PROCPS_PACKAGES = (
    ("libintl", "0.24.1-r1", "291a89bbb0e511e9875ff7aa9d6d07dc9a2fec8bf94c2a686d1a9153b8c58903"),
    ("libncursesw", "6.5_p20251123-r0", "7fe332c8af8b97579e89df56466f83f4abda5040a18e1842c07d684320bbef4b"),
    ("libproc2", "4.0.5-r0", "9f3b7b5f3f27cd15b471ac8e14915d7f686968f53b9249168f0adf318edb1687"),
    ("musl", "1.2.5-r24", "d7043c549029c98d438e9bc94213b006e5c88540eb58d1f5b0f0d1d56dbf9c98"),
    ("ncurses-terminfo-base", "6.5_p20251123-r0", "6952a6b39abaf7bbc498cb085f0f59bf23619b53b3f7328b08fe50c0198a2bd4"),
    ("procps-ng", "4.0.5-r0", "be8701f46de45aa1876f0b50748c54d47d216e8d3f54793c350818c669e64bc8"),
    ("skalibs-libs", "2.14.4.0-r0", "f1c33c73d842c98d99033833eaf681819b0f5b51310a6e286c2eb88b9a88ed97"),
    ("utmps-libs", "0.1.3.1-r0", "dbaa260e4328738d51b2ec77fdda9037cdbd67047465de5b2a30dce9a66f8fff"),
)
# Run in an isolated, deadline-owned helper process. Its only input URLs are the
# eight fixed official archive identities above; no repository solver is used.
PROCPS_DOWNLOAD = r'''
import hashlib
from pathlib import Path
import re
import ssl
import sys
import urllib.request

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("redirect refused")

def download_archive(url, destination, digest):
    prefix = "https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/"
    filename = Path(destination).name
    if (url != prefix + filename or not re.fullmatch(r"[a-z0-9][a-z0-9_.+-]*\.apk", filename)
            or not re.fullmatch(r"[a-f0-9]{64}", digest)):
        raise ValueError("archive identity refused")
    context = ssl.create_default_context()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                        urllib.request.HTTPSHandler(context=context), NoRedirect())
    with opener.open(url, timeout=10) as response:
        if response.status != 200 or response.geturl() != url:
            raise ValueError("response refused")
        raw = response.read((1 << 20) + 1)
    if len(raw) > 1 << 20:
        raise ValueError("archive exceeds byte bound")
    if hashlib.sha256(raw).hexdigest() != digest:
        raise ValueError("archive hash mismatch")
    # Never publish an unverified archive or replace an existing evidence file.
    with open(destination, "xb") as output:
        output.write(raw)

if __name__ == "__main__":
    try:
        download_archive(*sys.argv[1:])
    except Exception as error:
        reasons = {"redirect refused", "archive identity refused", "response refused",
                   "archive exceeds byte bound", "archive hash mismatch"}
        reason = str(error) if type(error) is ValueError and str(error) in reasons else "network or filesystem error"
        print("pinned archive fetch failed: " + reason, file=sys.stderr)
        sys.exit(1)
    print("pinned archive hash verified")
'''
evidence = Path(tempfile.mkdtemp(prefix="nerocd-database-origin-upgrade-20261024.", dir="/tmp"))
log = (evidence / "gate.log").open("ab", buffering=0)
volume_attempted = outer_attempted = False
volume_pin = image = None
outer = ""
head = tree = None
other_inventories = {}


class Failure(Exception):
    def __init__(self, message, status=1):
        super().__init__(message)
        self.status = status if 0 < status < 256 else 1


class Interrupted(BaseException):
    def __init__(self, number):
        self.status = 128 + number


def say(message):
    print(message, flush=True)
    log.write((message + "\n").encode())


def interrupt(number, _frame):
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    raise Interrupted(number)


signal.signal(signal.SIGINT, interrupt)
signal.signal(signal.SIGTERM, interrupt)
say("evidence=" + str(evidence))


def group_exists(pid):
    try:
        os.killpg(pid, 0)
        return True
    except ProcessLookupError:
        return False


def join_owned(process):
    # Only groups started by this launcher are signalled.
    if group_exists(process.pid):
        os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        if group_exists(process.pid):
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=2)
    if group_exists(process.pid):
        os.killpg(process.pid, signal.SIGKILL)
        deadline = time.monotonic() + 2
        while group_exists(process.pid) and time.monotonic() < deadline:
            time.sleep(0.02)
    if group_exists(process.pid):
        raise Failure("owned process group did not join: " + str(process.pid))
    say("owned process group joined: " + str(process.pid))


def run(args, output=None, timeout=60, env=None):
    capture = tempfile.TemporaryFile()
    # Do not lose a newly spawned child if cancellation arrives during Popen.
    previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=capture,
                                   stderr=log, start_new_session=True, env=env,
                                   # This launcher is single threaded; restore the child mask before exec.
                                   preexec_fn=lambda: signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask))
    except BaseException:
        capture.close()
        signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        raise
    try:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        deadline = time.monotonic() + timeout
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise Failure("command deadline exceeded: " + args[0])
            try:
                process.wait(timeout=min(0.1, remaining))
                break
            except subprocess.TimeoutExpired:
                if os.fstat(capture.fileno()).st_size > 1024 * 1024:
                    raise Failure("command output exceeds bound")
        if group_exists(process.pid):
            join_owned(process)
            raise Failure("command left an owned child process")
        capture.seek(0)
        data = capture.read(1024 * 1024 + 1)
        if len(data) > 1024 * 1024:
            raise Failure("command output exceeds bound")
        if output:
            (evidence / output).write_bytes(data)
        if process.returncode:
            raise Failure("command failed: " + args[0], process.returncode)
        return data.decode("utf-8")
    except BaseException:
        try:
            join_owned(process)
        except Exception as error:
            say(str(error))
        raise
    finally:
        capture.close()


def docker(*args, **kwargs):
    return run(["docker", "--host", ENDPOINT, *args], **kwargs)


def document(raw):
    try:
        return json.loads(raw)
    except (ValueError, TypeError) as error:
        raise Failure("malformed Docker JSON") from error


def inspect(kind, identity, tag):
    value = document(docker(kind, "inspect", identity, output=tag))
    if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
        raise Failure("Docker inspect must return exactly one object")
    return value[0]


def inventory(kind, tag, users=False):
    args = ["volume", "ls"] if kind == "volume" else ["container", "ls", "--all", "--no-trunc"]
    if users:
        args += ["--filter", "volume=" + VOLUME]
    lines = docker(*args, "--format", "{{json .}}", output=tag).splitlines()
    if len(lines) > 4096:
        raise Failure("Docker inventory exceeds bound")
    found = {}
    for line in lines:
        row = document(line)
        if not isinstance(row, dict):
            raise Failure("malformed inventory row")
        name = row.get("Name") if kind == "volume" else row.get("Names")
        if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,254}", name) or name in found:
            raise Failure("malformed or duplicate inventory name")
        if kind != "volume" and not re.fullmatch(r"[a-f0-9]{64}", row.get("ID", "")):
            raise Failure("inventory requires full container IDs")
        found[name] = row
    return found


def canonical_inventory(lines):
    if len(lines) > 4096:
        raise Failure("Docker inventory exceeds bound")
    rows = [document(line) for line in lines]
    if any(not isinstance(row, dict) for row in rows):
        raise Failure("malformed inventory row")
    return sorted(json.dumps(row, sort_keys=True) for row in rows)


def network_document(raw):
    def unique_object(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise Failure("duplicate network document key")
            value[key] = item
        return value
    try:
        return json.loads(raw, object_pairs_hook=unique_object)
    except (ValueError, TypeError) as error:
        raise Failure("malformed network document") from error


def network_inventory(tag):
    lines = docker("network", "ls", "--no-trunc", "--format", "{{json .}}", output=tag).splitlines()
    if len(lines) > 4096:
        raise Failure("network inventory exceeds bound")
    found, names = {}, set()
    # Validate the entire successful listing before inspecting exact IDs. Never
    # split its flattened Labels string: commas may be part of one label value.
    for line in lines:
        row = network_document(line)
        if not isinstance(row, dict):
            raise Failure("malformed network inventory row")
        identity, name = row.get("ID"), row.get("Name")
        if (not isinstance(identity, str) or not re.fullmatch(r"[a-f0-9]{64}", identity) or
                not isinstance(name, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,254}", name) or
                not isinstance(row.get("Labels"), str) or identity in found or name in names):
            raise Failure("malformed or duplicate network inventory identity")
        found[identity] = row
        names.add(name)
    result = []
    for identity, row in sorted(found.items()):
        value = network_document(docker("network", "inspect", identity, output=tag + "-inspect-" + identity))
        if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
            raise Failure("network inspect must return exactly one object")
        value = value[0]
        labels = value.get("Labels")
        if (value.get("Id") != identity or value.get("Name") != row["Name"] or "Labels" not in value or
                labels is not None and (not isinstance(labels, dict) or
                    any(not isinstance(key, str) or not isinstance(item, str) for key, item in labels.items()))):
            raise Failure("network inspect identity or labels mismatch")
        normalized = dict(row)
        normalized["Labels"] = labels if labels is not None else {}
        result.append(normalized)
    # Raw listing and each complete raw inspect are retained alongside the exact
    # listing metadata with structured labels used by the comparison.
    (evidence / (tag + "-structured")).write_text(json.dumps(result, sort_keys=True) + "\n")
    return result


def verified_volume(allowed_user=""):
    global volume_pin
    value = inspect("volume", VOLUME, "volume-latest-inspect")
    if (value.get("Name") != VOLUME or value.get("Driver") != "local" or
            value.get("Scope") != "local" or value.get("Labels") != {LABEL: OWNER} or
            value.get("Options") not in ({}, None) or not value.get("CreatedAt")):
        raise Failure("outer storage metadata mismatch; preserving volume")
    if volume_pin is not None and value != volume_pin:
        raise Failure("outer storage identity changed; preserving volume")
    if volume_pin is None:
        volume_pin = value
        (evidence / "volume-first-inspect").write_text(json.dumps(value, sort_keys=True))
    users = inventory("container", "volume-users", users=True)
    if any(row["ID"] != allowed_user for row in users.values()) or len(users) > bool(allowed_user):
        raise Failure("unexpected outer storage users; preserving volume")
    return value


def verified_outer(identity):
    value = inspect("container", identity, "outer-latest-inspect")
    host, config = value.get("HostConfig", {}), value.get("Config", {})
    expected_labels = dict((image.get("Config") or {}).get("Labels") or {})
    expected_labels[LABEL] = OWNER
    mounts = value.get("Mounts", [])
    if (value.get("Id") != identity or value.get("Name") != "/" + NAME or
            value.get("Image") != image["Id"] or config.get("Labels") != expected_labels or
            config.get("Cmd") != ["dockerd", "--host=unix:///var/run/docker.sock"] or
            host.get("Privileged") is not True or host.get("PidMode") or host.get("UsernsMode") or
            host.get("Binds") or host.get("PortBindings") or host.get("PublishAllPorts") or
            host.get("NetworkMode") not in ("default", "bridge") or len(mounts) != 1):
        raise Failure("outer identity/configuration mismatch; preserving resources")
    mount = mounts[0]
    if (mount.get("Type") != "volume" or mount.get("Name") != VOLUME or
            mount.get("Driver") != "local" or mount.get("Destination") != "/var/lib/docker" or
            mount.get("RW") is not True):
        raise Failure("outer mount mismatch; preserving resources")
    return value


def procps_numeric_profile(raw):
    if len(raw.encode("utf-8")) > 8192:
        return False
    lines = raw.strip().splitlines()
    if not 2 <= len(lines) <= 256 or lines[0].split() != ["PID", "UID"]:
        return False
    seen = set()
    for line in lines[1:]:
        fields = line.split()
        if len(fields) != 2 or any(not re.fullmatch(r"0|[1-9][0-9]{0,9}", value) for value in fields):
            return False
        pid = int(fields[0])
        if pid < 1 or pid in seen:
            return False
        seen.add(pid)
    return True


def provision_procps():
    # Download only fixed HTTPS identities to bounded evidence. The isolated
    # helper receives no ambient proxy, credentials, TLS or Python configuration.
    archives = evidence / "apk"
    archives.mkdir(mode=0o700)
    deadline = time.monotonic() + 120
    for name, version, digest in PROCPS_PACKAGES:
        filename = name + "-" + version + ".apk"
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise Failure("procps preflight failed: archive fetch deadline")
        try:
            run(["python3", "-I", "-c", PROCPS_DOWNLOAD, PROCPS_REPOSITORY + "/aarch64/" + filename,
                 str(archives / filename), digest], timeout=min(25, remaining),
                output="procps-fetch-" + name, env={"PATH": os.environ.get("PATH", os.defpath)})
        except Failure as error:
            raise Failure("procps preflight failed: archive fetch " + name, error.status) from error
    verified_outer(outer)
    try:
        docker("cp", str(archives) + "/.", outer + ":/fixture/apk", timeout=30, output="procps-copy")
    except Failure as error:
        raise Failure("procps preflight failed: archive copy", error.status) from error
    # APK mutations stay inside the exact disposable daemon, without ambient
    # configuration or repositories. Signatures remain independently required.
    environment = ["env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "APK_CONFIG=/dev/null"]
    apk = ["apk", "--arch", "aarch64", "--no-cache", "--repositories-file", "/dev/null", "--keys-dir", "/etc/apk/keys"]
    specs = [name + "=" + version for name, version, _ in PROCPS_PACKAGES]
    filenames = [name + "-" + version + ".apk" for name, version, _ in PROCPS_PACKAGES]
    files = ["/fixture/apk/" + filename for filename in filenames]
    stages = (
        ("signature", [*apk, "--no-network", "verify", *files], 20),
        ("install", [*apk, "--no-network", "add", *files], 60),
        ("version", [*apk, "--no-network", "info", "--exists", *specs], 20),
    )
    for stage, args, timeout in stages:
        verified_outer(outer)
        try:
            docker("exec", outer, *environment, *args, timeout=timeout, output="procps-" + stage)
        except Failure as error:
            raise Failure("procps preflight failed: " + stage, error.status) from error
    # The successful exact-version check is distinct from the intended manifest.
    (evidence / "procps-installed-closure").write_text(json.dumps(dict(
        repository=PROCPS_REPOSITORY, architecture="aarch64", keys="/etc/apk/keys",
        packages=[dict(name=name, version=version, sha256=digest) for name, version, digest in PROCPS_PACKAGES],
        exact_installed_versions_verified=True), sort_keys=True) + "\n")
    verified_outer(outer)
    try:
        raw = docker("exec", outer, *environment, "ps", "-eo", "pid,uid", timeout=20, output="procps-probe")
    except Failure as error:
        raise Failure("procps preflight failed: numeric PID/UID command", error.status) from error
    if not procps_numeric_profile(raw):
        raise Failure("procps preflight failed: malformed numeric PID/UID output")
    say("numeric PID/UID process preflight passed")


def main():
    global head, tree, image, volume_attempted, outer_attempted, outer
    if not Path("internal/runnerruntime/hosting_database_origin_upgrade_runtime_integration_test.go").is_file():
        raise Failure("run from the assigned repository root")
    if run(["git", "status", "--porcelain"]).strip():
        raise Failure("gate requires a clean committed checkout")
    head = run(["git", "rev-parse", "HEAD"]).strip()
    tree = run(["git", "rev-parse", "HEAD^{tree}"]).strip()
    say("head=" + head + "\ntree=" + tree + "\nowner=" + OWNER)
    profile = document(docker("info", "--format", "{{json .}}", output="host-profile"))
    if (profile.get("ID"), profile.get("OSType"), profile.get("Architecture")) != (DAEMON, "linux", "aarch64"):
        raise Failure("host daemon/native architecture mismatch")
    # These commands only list existing outer objects. No pulls or network writes
    # are part of this launcher; preserve the complete unrelated inventories.
    other_inventories["network"] = network_inventory("host-networks-before")
    rows = docker("image", "ls", "--no-trunc", "--digests", "--format", "{{json .}}", output="host-images-before").splitlines()
    other_inventories["image"] = canonical_inventory(rows)
    # Only successful complete inventories establish absence. Inspect errors never do.
    containers = inventory("container", "host-containers-before")
    volumes = inventory("volume", "host-volumes-before")
    if NAME in containers or VOLUME in volumes:
        raise Failure("exact fixture name already exists; no mutation permitted")
    run(["git", "ls-files", "-s", "acceptance/runtime-hosting-database-origin-upgrade/",
         "internal/runnerruntime/hosting_database_origin_upgrade_runtime_integration_test.go",
         "internal/runnerruntime/hosting_database_runtime_integration_test.go",
         "internal/runnerruntime/hosting_database_network_capture.go",
         "internal/runnerruntime/hosting_database_network_upgrade.go",
         "internal/runnerruntime/hosting_database_engine.go",
         "internal/runnerruntime/hosting_database_secrets.go",
         "internal/runnerruntime/hosting_database_network.go",
         "internal/runnerruntime/hosting_database_readiness.go",
         "internal/runnerruntime/hosting_owned_volume.go",
         "internal/domain/hosting_database.go", "internal/domain/hosting_database_execution.go"], output="source-blobs")
    environment = dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0")
    run(["go", "test", "-c", "-o", str(evidence / "runtime.test"), "./internal/runnerruntime"], timeout=300, env=environment)
    run(["go", "version", "-m", str(evidence / "runtime.test")], output="test-binary-build-info")
    (evidence / "binary-sha256").write_text(hashlib.sha256((evidence / "runtime.test").read_bytes()).hexdigest() + "\n")
    image = inspect("image", IMAGE, "pinned-outer-image")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image.get("Id", "")):
        raise Failure("invalid pinned image ID")
    volume_attempted = True
    if docker("volume", "create", "--driver", "local", "--label", LABEL + "=" + OWNER, VOLUME).strip() != VOLUME:
        raise Failure("unexpected volume creation response")
    verified_volume()
    outer_attempted = True
    response = docker("container", "create", "--privileged", "--name", NAME, "--label", LABEL + "=" + OWNER,
                      "--mount", "type=volume,src=" + VOLUME + ",dst=/var/lib/docker",
                      "--env", "DOCKER_TLS_CERTDIR=", IMAGE, "dockerd", "--host=unix:///var/run/docker.sock").strip()
    if not re.fullmatch(r"[a-f0-9]{64}", response):
        raise Failure("invalid outer creation response")
    outer = response
    verified_outer(outer)
    verified_volume(outer)
    docker("container", "start", outer)
    for attempt in range(60):
        try:
            inner = document(docker("exec", outer, "docker", "--host", ENDPOINT, "info", "--format", "{{json .}}",
                                    output="inner-profile"))
            break
        except Failure:
            if attempt == 59:
                raise
            time.sleep(1)
    if inner.get("OSType") != "linux" or inner.get("Architecture") not in ("arm64", "aarch64"):
        raise Failure("inner daemon/native architecture mismatch")
    (evidence / "native-profile").write_text("binary_goos=linux binary_goarch=arm64 emulation=false\n" + json.dumps(inner) + "\n")
    docker("exec", outer, "mkdir", "-p", "/fixture/apk")
    provision_procps()
    docker("cp", str(evidence / "runtime.test"), outer + ":/fixture/runtime.test")
    say(docker("exec", "--env", "NEROCD_DATABASE_ORIGIN_UPGRADE_RUNTIME=disposable-dind", outer, "/fixture/runtime.test",
               "-test.v", "-test.run", "^TestHostingDatabaseOriginUpgradeNativeRuntime$", "-test.timeout=20m",
               timeout=1210, output="runtime-test"))
    say("native Linux arm64 database origin/alias upgrade fixture passed")


GUEST_JOIN = r"""
for exe in /proc/[0-9]*/exe; do
 case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/tmp/origin-client.test|/usr/local/bin/docker)
  pid=$(basename "$(dirname "$exe")"); kill -TERM "$pid";; esac
done
for tick in $(seq 1 100); do
 live=false
 for exe in /proc/[0-9]*/exe; do
  case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/tmp/origin-client.test|/usr/local/bin/docker) live=true;; esac
 done
 if [ "$live" = false ]; then exit 0; fi
 sleep 0.1
done
exit 1
"""


def cleanup():
    global outer
    if outer_attempted:
        containers = inventory("container", "cleanup-containers")
        if NAME not in containers and not outer:
            # A cancelled request can still finish server-side. Keep its storage reserved.
            raise Failure("outer creation outcome unknown; preserving volume")
        if NAME in containers:
            identity = containers[NAME]["ID"]
            if outer and outer != identity:
                raise Failure("outer ID changed; preserving resources")
            outer = identity
            value = verified_outer(outer)
            try:
                if value.get("State", {}).get("Running"):
                    docker("exec", outer, "sh", "-ec", GUEST_JOIN, timeout=15)
                    (evidence / "process-absence").write_text("owned guest test and Docker CLI processes absent\n")
                    for args, tag in ((["ps", "-a", "--no-trunc"], "inner-containers-final"),
                                      (["volume", "ls"], "inner-volumes-final"),
                                      (["image", "ls", "--no-trunc", "--digests"], "inner-images-final")):
                        docker("exec", outer, "docker", "--host", ENDPOINT, *args, "--format", "{{json .}}", output=tag)
                verified_volume(outer)
                verified_outer(outer)
            except Exception:
                # A positively identified owned privileged process must not remain running.
                if verified_outer(outer).get("State", {}).get("Running"):
                    docker("container", "stop", "--time", "5", outer, timeout=10)
                    if verified_outer(outer).get("State", {}).get("Running"):
                        raise Failure("owned outer did not stop; preserving resources")
                raise
            docker("container", "rm", "--force", outer)
            if NAME in inventory("container", "outer-absence"):
                raise Failure("owned outer remains")
            say("owned outer absent: " + outer)
    if volume_attempted:
        if VOLUME in inventory("volume", "cleanup-volumes"):
            verified_volume()
            docker("volume", "rm", VOLUME)
        if VOLUME in inventory("volume", "volume-absence"):
            raise Failure("owned outer volume remains")
        say("owned outer volume absent: " + VOLUME)


status = 0
cleanup_failure = ""
try:
    main()
except Interrupted as error:
    status = error.status
    say("gate interrupted: " + str(status))
except Exception as error:
    status = getattr(error, "status", 1)
    say(str(error))
finally:
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    try:
        cleanup()
    except Exception as error:
        cleanup_failure = str(error)
        say("cleanup failed: " + cleanup_failure)
        status = status or 1
    # Capture every inventory kind even when another comparison fails. A failed
    # check remains failed; it must not erase evidence needed for reconciliation.
    for kind in ("container", "volume", "network", "image"):
        try:
            if kind in ("container", "volume"):
                after = inventory(kind, "host-" + ("containers" if kind == "container" else "volumes") + "-after")
                before = evidence / ("host-containers-before" if kind == "container" else "host-volumes-before")
                if before.exists():
                    if kind == "container":
                        original = {document(line)["Names"]: document(line)["ID"] for line in before.read_text().splitlines()}
                        actual = {name: row["ID"] for name, row in after.items() if name != NAME}
                    else:
                        original = {document(line)["Name"] for line in before.read_text().splitlines()}
                        actual = set(after) - {VOLUME}
                    if original != actual:
                        raise Failure("unrelated outer " + kind + " inventory changed")
            else:
                if kind == "network":
                    actual = network_inventory("host-networks-after")
                else:
                    rows = docker("image", "ls", "--no-trunc", "--digests", "--format", "{{json .}}", output="host-images-after").splitlines()
                    actual = canonical_inventory(rows)
                if kind in other_inventories and other_inventories[kind] != actual:
                    raise Failure("unrelated outer " + kind + " inventory changed")
        except Exception as error:
            say("final inventory/check failed: " + str(error))
            status = status or 1
    try:
        if head is not None and (run(["git", "rev-parse", "HEAD"]).strip() != head or
                                 run(["git", "rev-parse", "HEAD^{tree}"]).strip() != tree or
                                 run(["git", "status", "--porcelain"], output="status-after").strip()):
            raise Failure("checkout changed during gate")
    except Exception as error:
        say("final inventory/check failed: " + str(error))
        status = status or 1
    (evidence / "result").write_text("gate_exit=" + str(status) + "\nouter_id=" + outer +
                                   "\nouter_volume=" + VOLUME + "\ncleanup_failure=" + cleanup_failure + "\n")
    say("gate exit=" + str(status) + " evidence=" + str(evidence))
    log.close()
sys.exit(status)
PY
