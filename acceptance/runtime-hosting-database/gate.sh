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

OWNER = "nerocd-database-runtime-20261024-db7319"
LABEL = "io.nerocd.database-runtime.owner"
NAME, VOLUME = OWNER + "-dind", OWNER + "-docker-data"
ENDPOINT = "unix:///var/run/docker.sock"
DAEMON = "e37dd219-b13f-47dd-99ed-21d79442ed0c"
IMAGE = "docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489"
evidence = Path(tempfile.mkdtemp(prefix="nerocd-database-runtime-20261024.", dir="/tmp"))
log = (evidence / "gate.log").open("ab", buffering=0)
volume_attempted = outer_attempted = False
volume_pin = image = None
outer = ""
head = tree = None


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


def main():
    global head, tree, image, volume_attempted, outer_attempted, outer
    if not Path("internal/runnerruntime/hosting_database_runtime_integration_test.go").is_file():
        raise Failure("run from the assigned repository root")
    if run(["git", "status", "--porcelain"]).strip():
        raise Failure("gate requires a clean committed checkout")
    head = run(["git", "rev-parse", "HEAD"]).strip()
    tree = run(["git", "rev-parse", "HEAD^{tree}"]).strip()
    say("head=" + head + "\ntree=" + tree + "\nowner=" + OWNER)
    profile = document(docker("info", "--format", "{{json .}}", output="host-profile"))
    if (profile.get("ID"), profile.get("OSType"), profile.get("Architecture")) != (DAEMON, "linux", "aarch64"):
        raise Failure("host daemon/native architecture mismatch")
    # Only successful complete inventories establish absence. Inspect errors never do.
    containers = inventory("container", "host-containers-before")
    volumes = inventory("volume", "host-volumes-before")
    if NAME in containers or VOLUME in volumes:
        raise Failure("exact fixture name already exists; no mutation permitted")
    run(["git", "ls-files", "-s", "acceptance/runtime-hosting-database/",
         "internal/runnerruntime/hosting_database_runtime_integration_test.go",
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
    docker("exec", outer, "mkdir", "-p", "/fixture")
    docker("cp", str(evidence / "runtime.test"), outer + ":/fixture/runtime.test")
    say(docker("exec", "--env", "NEROCD_DATABASE_RUNTIME=disposable-dind", outer, "/fixture/runtime.test",
               "-test.v", "-test.run", "^TestHostingDatabaseNativeRuntime$", "-test.timeout=20m",
               timeout=1210, output="runtime-test"))
    say("native Linux arm64 private PostgreSQL/Redis engine runtime gate passed")


GUEST_JOIN = r"""
for exe in /proc/[0-9]*/exe; do
 case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/usr/local/bin/docker)
  pid=$(basename "$(dirname "$exe")"); kill -TERM "$pid";; esac
done
for tick in $(seq 1 100); do
 live=false
 for exe in /proc/[0-9]*/exe; do
  case $(readlink "$exe" 2>/dev/null || true) in /fixture/runtime.test|/usr/local/bin/docker) live=true;; esac
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
    try:
        inventory("container", "host-containers-after")
        inventory("volume", "host-volumes-after")
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
