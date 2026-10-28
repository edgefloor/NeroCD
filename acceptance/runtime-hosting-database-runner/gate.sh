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

mysql_mode = os.environ.get("NEROCD_DATABASE_MYSQL_RUNTIME", "0")
backup_mode = os.environ.get("NEROCD_DATABASE_BACKUP_RUNTIME", "0")
schedule_mode = os.environ.get("NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME", "0")
remote_mode = os.environ.get("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME", "0")
retention_mode = os.environ.get("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME", "0")
notifications_mode = os.environ.get("NEROCD_DATABASE_BACKUP_NOTIFICATIONS_RUNTIME", "0")
SILO_IMAGE = "pgsty/silo@sha256:87585bb783b7822318b3e1bcc4497c8427c98e2b021826d20b5f888713299f95"
OWNER = "nerocd-postgresql-backup-runtime-20261025-pbr7319" if backup_mode == "1" else "nerocd-database-runner-20261024-dbr7319"
LABEL = "io.nerocd.postgresql-backup-runtime.owner" if backup_mode == "1" else "io.nerocd.database-runner.owner"
OWNER_LABEL = "20261025-pbr7319" if backup_mode == "1" else OWNER
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
evidence = Path(tempfile.mkdtemp(prefix="nerocd-database-runner-20261024.", dir="/tmp"))
log = (evidence / "gate.log").open("ab", buffering=0)
volume_attempted = outer_attempted = False
volume_pin = image = None
outer = ""
outer_deadline = None
cleaning = False
head = tree = None
browser_origin = ""
browser_mode = os.environ.get("NEROCD_DATABASE_CONNECTION_BROWSER", "0")
routes_mode = os.environ.get("NEROCD_DATABASE_CONNECTION_ROUTES", "0")
storage_mode = os.environ.get("NEROCD_DATABASE_CONNECTION_STORAGE", "0")
gate_mode = os.environ.get("NEROCD_DATABASE_RUNNER_MODE", "full")
STORAGE_CUTOVER_DIAGNOSTIC_SELECTOR = "^TestHostingApplicationDatabaseStorageFailuresCompiledRunner$/^postgresql$/^cutover$"
full_workflow_pass = diagnostic_pass = False
PEBBLE_IMAGE = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"


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
        process.wait(timeout=10 if browser_mode == "1" else 2)
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
        if outer_deadline is not None and not cleaning:
            deadline = min(deadline, outer_deadline)
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


def backup_baseline_capture(containers, volumes):
    if backup_mode != "1":
        return
    ids = [v["ID"] for v in containers.values()]
    values = document(docker("container", "inspect", *ids, output="backup-baseline-private")) if ids else []
    volume_values = document(docker("volume", "inspect", *volumes, output="backup-baseline-volumes-private")) if volumes else []
    (evidence / "backup-baseline-identities.json").write_text(json.dumps({"containers": values, "volumes": volumes, "volume_profiles": volume_values}, sort_keys=True))


def backup_baseline_verify():
    if backup_mode != "1" or not (evidence / "backup-baseline-identities.json").exists():
        return
    baseline = json.loads((evidence / "backup-baseline-identities.json").read_text())
    ids = [v["Id"] for v in baseline["containers"]]
    current = document(docker("container", "inspect", *ids, output="backup-final-private")) if ids else []
    def selected(value, telemetry=True):
        value = json.loads(json.dumps(value))
        value["Mounts"] = sorted(value.get("Mounts", []), key=lambda v: json.dumps(v, sort_keys=True))
        if not telemetry:
            value.get("State", {}).get("Health", {}).pop("Log", None)
        return {k: value.get(k) for k in ("Id", "Image", "Config", "HostConfig", "Mounts", "State", "NetworkSettings")}
    old = {v["Id"]: v for v in baseline["containers"]}
    strict = [v["Id"] for v in current if selected(v) != selected(old[v["Id"]])]
    changed = [v["Id"] for v in current if selected(v, False) != selected(old[v["Id"]], False)]
    now_volumes = inventory("volume", "backup-volumes-preserved")
    if not set(baseline["volumes"]).issubset(now_volumes):
        changed.append("missing_baseline_volume")
    else:
        volume_profiles = document(docker("volume", "inspect", *baseline["volumes"], output="backup-final-volumes-private")) if baseline["volumes"] else []
        if {v["Name"]: v for v in volume_profiles} != {v["Name"]: v for v in baseline["volume_profiles"]}:
            changed.append("baseline_volume_configuration_changed")
    (evidence / "backup-preservation-raw.json").write_text(json.dumps({"status": "failed" if strict else "passed", "changed_ids": strict}))
    (evidence / "backup-preservation-reconciled.json").write_text(json.dumps({"status": "failed" if changed else "passed", "actual_changes": changed, "health_log_only_ids": strict if not changed else [], "baseline_container_count": len(ids), "baseline_volume_count": len(baseline["volumes"])}))
    if changed:
        raise Failure("unrelated baseline identity/configuration/state/network changed")


def verified_volume(allowed_user=""):
    global volume_pin
    value = inspect("volume", VOLUME, "volume-latest-inspect")
    if (value.get("Name") != VOLUME or value.get("Driver") != "local" or
            value.get("Scope") != "local" or value.get("Labels") != {LABEL: OWNER_LABEL} or
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
    global browser_origin
    value = inspect("container", identity, "outer-latest-inspect")
    host, config = value.get("HostConfig", {}), value.get("Config", {})
    expected_labels = dict((image.get("Config") or {}).get("Labels") or {})
    expected_labels[LABEL] = OWNER_LABEL
    mounts = value.get("Mounts", [])
    if (value.get("Id") != identity or value.get("Name") != "/" + NAME or
            value.get("Image") != image["Id"] or config.get("Labels") != expected_labels or
            config.get("Cmd") != ["dockerd", "--host=unix:///var/run/docker.sock"] or
            host.get("Privileged") is not True or host.get("PidMode") or host.get("UsernsMode") or
            host.get("Binds") or host.get("PublishAllPorts") or
            host.get("NetworkMode") not in ("default", "bridge") or len(mounts) != 1):
        raise Failure("outer identity/configuration mismatch; preserving resources")
    if backup_mode == "1" and (host.get("NanoCpus") != 4000000000 or host.get("Memory") != 4294967296 or host.get("PidsLimit") != 1024):
        raise Failure("backup fixture resource profile mismatch")
    ports = host.get("PortBindings")
    ports = {} if ports is None else ports
    network = value.get("NetworkSettings", {})
    if not isinstance(ports, dict) or not isinstance(network, dict):
        raise Failure("outer port metadata malformed; preserving resources")
    assigned = network.get("Ports")
    assigned = {} if assigned is None else assigned
    if not isinstance(assigned, dict) or any(
            not isinstance(key, str) or not re.fullmatch(r"[1-9][0-9]{0,4}/(?:tcp|udp|sctp)", key) or
            int(key.split("/")[0]) > 65535 or (bindings is not None and not isinstance(bindings, list))
            for key, bindings in assigned.items()):
        raise Failure("outer assigned port metadata malformed; preserving resources")
    # Docker includes exposed image ports with null/empty assignments. Only
    # nonempty bindings publish host ports, in both admission and cleanup.
    assigned = {key: bindings for key, bindings in assigned.items() if bindings}
    if browser_mode == "1":
        publication = ports.get("18080/tcp", [])
        if len(ports) != 1 or not isinstance(publication, list) or len(publication) != 1 or publication[0] != {"HostIp": "127.0.0.1", "HostPort": ""}:
            raise Failure("browser bridge publication mismatch; preserving resources")
        live = assigned.get("18080/tcp", [])
        if assigned or value.get("State", {}).get("Running"):
            if len(assigned) != 1 or len(live) != 1 or not isinstance(live[0], dict) or set(live[0]) != {"HostIp", "HostPort"} or live[0]["HostIp"] != "127.0.0.1" or not isinstance(live[0]["HostPort"], str) or not re.fullmatch(r"[1-9][0-9]{3,4}", live[0]["HostPort"]):
                raise Failure("browser bridge assigned port mismatch; preserving resources")
            port = int(live[0]["HostPort"])
            if not 1024 <= port <= 65535:
                raise Failure("browser bridge port out of bounds")
            observed = "http://127.0.0.1:" + str(port)
            if browser_origin and browser_origin != observed:
                raise Failure("browser bridge port changed; preserving resources")
            browser_origin = observed
    elif ports or assigned:
        raise Failure("unexpected outer publication; preserving resources")
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


def backup_runtime_profile(schedule, remote="0", retention="0", notifications="0", mysql="0"):
    if mysql not in ("0", "1"):
        raise Failure("invalid backup engine profile")
    if mysql == "1":
        if (schedule, remote, retention, notifications) == ("1", "1", "0", "0"):
            return "^TestHostingDatabaseBackupRemoteCompiledRunner$", ["TestHostingDatabaseBackupRemoteCompiledRunner"], ["DATABASE_BACKUP_REMOTE_PASS mysql", "DATABASE_BACKUP_REMOTE_REPLAY_PASS mysql", "DATABASE_BACKUP_REMOTE_SCHEDULE_PASS mysql", "DATABASE_BACKUP_REMOTE_BROWSER_PASS mysql"]
        if (schedule, remote, retention, notifications) != ("1", "0", "0", "0"):
            raise Failure("MySQL requires the exact local or remote schedule profile")
        return "^TestHostingDatabaseBackupScheduleCompiledRunner$", ["TestHostingDatabaseBackupScheduleCompiledRunner"], ["DATABASE_BACKUP_SCHEDULE_PASS mysql", "DATABASE_BACKUP_SCHEDULE_BROWSER_PASS mysql", "DATABASE_BACKUP_SCHEDULE_ADMISSION_FAILURE_PASS mysql source_unavailable"]
    if schedule not in ("0", "1") or remote not in ("0", "1") or retention not in ("0", "1") or retention == "1" and remote != "1":
        raise Failure("invalid backup schedule profile")
    if notifications not in ("0", "1") or notifications == "1" and remote != "1":
        raise Failure("notifications require the reviewed remote backup profile")
    names = ["TestHostingDatabaseBackupCompiledRunner", "TestHostingDatabaseBackupFailuresCompiledRunner"]
    markers = ["DATABASE_BACKUP_RESTORE_APP_PASS postgresql", "DATABASE_BACKUP_BROWSER_PASS postgresql", "DATABASE_BACKUP_GUEST_TIMEOUT_PASS postgresql", "DATABASE_BACKUP_FAILURE_PASS postgresql corrupt", "DATABASE_BACKUP_FAILURE_PASS postgresql truncated", "DATABASE_BACKUP_FAILURE_PASS postgresql revoked"]
    if schedule == "1":
        names.append("TestHostingDatabaseBackupScheduleCompiledRunner")
        markers += ["DATABASE_BACKUP_SCHEDULE_PASS postgresql", "DATABASE_BACKUP_SCHEDULE_BROWSER_PASS postgresql", "DATABASE_BACKUP_GUEST_DUMP_TIMEOUT_PASS postgresql"]
    if remote == "1":
        names.append("TestHostingDatabaseBackupRemoteCompiledRunner")
        markers += ["DATABASE_BACKUP_REMOTE_PASS postgresql", "DATABASE_BACKUP_REMOTE_REPLAY_PASS postgresql", "DATABASE_BACKUP_REMOTE_GET_RESTORE_APP_PASS postgresql", "DATABASE_BACKUP_REMOTE_BROWSER_PASS postgresql"]
    if retention == "1":
        names += ["TestHostingDatabaseBackupRetentionCompiledRunner", "TestHostingDatabaseBackupRetentionReplayCompiledRunner", "TestHostingDatabaseBackupRetentionFailuresCompiledRunner", "TestHostingDatabaseBackupRetentionProtectionsCompiledRunner", "TestHostingDatabaseBackupRetentionUnknownDeleteCompiledRunner"]
        markers += ["DATABASE_BACKUP_RETENTION_PASS postgresql", "DATABASE_BACKUP_RETENTION_BROWSER_PASS postgresql", "DATABASE_BACKUP_RETENTION_RECOVERY_BROWSER_PASS postgresql", "DATABASE_BACKUP_RETENTION_RECOVERY_PASS postgresql", "DATABASE_BACKUP_RETENTION_STALE_AUTHORITY_PASS postgresql", "DATABASE_BACKUP_RETENTION_RESTORE_APP_PASS postgresql", "DATABASE_BACKUP_RETENTION_REPLAY_PASS postgresql", "DATABASE_BACKUP_RETENTION_FAILURE_PASS postgresql delete_denied", "DATABASE_BACKUP_RETENTION_FAILURE_PASS postgresql conditional_unsupported", "DATABASE_BACKUP_RETENTION_FAILURE_PASS postgresql authorization_revoked", "DATABASE_BACKUP_RETENTION_PROTECTION_PASS postgresql newest_shared_active_failed_upload_external", "DATABASE_BACKUP_RETENTION_INDEPENDENT_LIMITS_PASS postgresql", "DATABASE_BACKUP_RETENTION_UNKNOWN_DELETE_HELD_PASS postgresql"]
    if notifications == "1":
        names.append("TestHostingBackupNotificationsCompiledRunner")
        markers += ["DATABASE_BACKUP_NOTIFICATIONS_PASS postgresql " + proof for proof in ("dump_email_webhook", "upload_email_webhook", "source_replay_restart", "retry_exhaustion", "current_authority", "local_restore_app")]
        markers.append("DATABASE_BACKUP_NOTIFICATIONS_BROWSER_PASS postgresql")
    return "^(?:" + "|".join(names) + ")$", names, markers


def verify_backup_runtime_output(output, schedule, remote="0", retention="0", notifications="0", mysql="0"):
    _, names, markers = backup_runtime_profile(schedule, remote, retention, notifications, mysql)
    for marker in markers:
        if output.splitlines().count(marker) != 1:
            raise Failure("required real backup restore proof missing: " + marker)
    for name in names:
        if not re.search(r"^--- PASS: " + name + r" ", output, re.MULTILINE):
            raise Failure("backup runtime test missing or skipped")
    if re.search(r"^\s*--- (?:SKIP|FAIL): ", output, re.MULTILINE):
        raise Failure("backup runtime test missing or skipped")


def retention_failure_diagnostic_profile(backup, remote, retention, schedule, browser, routes, storage):
    if (backup, remote, retention, schedule, browser, routes, storage) != ("1", "1", "1", "0", "0", "0", "0"):
        raise Failure("retention failure diagnostic requires its exact non-browser backup profile")
    names = ["TestHostingDatabaseBackupRetentionFailuresCompiledRunner", "TestHostingDatabaseBackupRetentionProtectionsCompiledRunner"]
    markers = ["DATABASE_BACKUP_RETENTION_FAILURE_PASS postgresql " + kind for kind in
               ("delete_denied", "conditional_unsupported", "authorization_revoked")]
    markers += ["DATABASE_BACKUP_RETENTION_PROTECTION_PASS postgresql newest_shared_active_failed_upload_external",
                "DATABASE_BACKUP_RETENTION_INDEPENDENT_LIMITS_PASS postgresql"]
    return "^(?:" + "|".join(names) + ")$", names, markers


def verify_retention_failure_diagnostic_output(output):
    _, names, markers = retention_failure_diagnostic_profile("1", "1", "1", "0", "0", "0", "0")
    required = set(names + [names[0] + "/" + kind for kind in
                           ("delete_denied", "conditional_unsupported", "authorization_revoked")])
    passed = re.findall(r"^\s*--- PASS: ([^ ]+) ", output, re.MULTILINE)
    if set(passed) != required or len(passed) != len(required) or re.search(r"^\s*--- (?:SKIP|FAIL): ", output, re.MULTILINE):
        raise Failure("retention failure diagnostic cases missing, repeated, skipped, failed or unrelated")
    for marker in markers:
        if output.splitlines().count(marker) != 1:
            raise Failure("retention failure diagnostic proof missing or repeated")
    if any(re.match(r"^DATABASE_[A-Z_]+_PASS ", line) and line not in markers for line in output.splitlines()):
        raise Failure("retention failure diagnostic contains unrelated qualification markers")


def verify_mysql_runtime_output(output):
    names = ["TestHostingApplicationDatabaseStorageCompiledRunner", "TestHostingApplicationDatabaseStorageFailuresCompiledRunner"]
    markers = ["DATABASE_CONNECTION_STORAGE_PASS mysql", "DATABASE_CONNECTION_STORAGE_BROWSER_PASS mysql", "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS mysql success", "DATABASE_BACKUP_RESTORE_APP_PASS mysql", "DATABASE_BACKUP_BROWSER_PASS mysql"]
    for case in ("readiness", "cutover"):
        markers += ["DATABASE_CONNECTION_STORAGE_RECOVERY_PASS mysql " + case, "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS mysql " + case]
    for marker in markers:
        if output.splitlines().count(marker) != 1:
            raise Failure("required MySQL storage/backup proof missing or repeated: " + marker)
    for name in names:
        if not re.search(r"^--- PASS: " + name + r" ", output, re.MULTILINE):
            raise Failure("required MySQL physical test did not pass")
    if re.search(r"^\s*--- (?:SKIP|FAIL): ", output, re.MULTILINE):
        raise Failure("MySQL qualification contains a skipped or failed case")
    if any(re.match(r"^DATABASE_[A-Z_]+_PASS ", line) and line not in markers for line in output.splitlines()):
        raise Failure("MySQL qualification contains unrelated proof markers")


def database_workflow_pebble_config(raw):
    config = json.loads(raw)
    try:
        profile = config["pebble"]["profiles"]["default"]
        if profile["validityPeriod"] != 600:
            raise ValueError("unexpected shared certificate validity")
    except (KeyError, TypeError, ValueError) as error:
        raise Failure("database workflow requires the pinned short-lived CA source") from error
    # Database persistence is not the dedicated certificate-renewal workflow.
    # A day-long leaf stays outside the fixture Traefik one-hour renewal window.
    profile["validityPeriod"] = 86400
    return (json.dumps(config, sort_keys=True, indent=2) + "\n").encode()


def main():
    global head, tree, image, volume_attempted, outer_attempted, outer, full_workflow_pass, diagnostic_pass, outer_deadline
    mysql_schedule = (mysql_mode, backup_mode, schedule_mode, remote_mode, retention_mode, notifications_mode, storage_mode, routes_mode, browser_mode, gate_mode) in (("1", "1", "1", "0", "0", "0", "1", "1", "1", "full"), ("1", "1", "1", "1", "0", "0", "1", "1", "1", "full"))
    if mysql_mode not in ("0", "1") or mysql_mode == "1" and not mysql_schedule and (backup_mode != "0" or schedule_mode != "0" or storage_mode != "1" or routes_mode != "1" or browser_mode != "1" or gate_mode != "full"):
        raise Failure("MySQL requires exact connected storage/browser profile with appended local backup")
    if notifications_mode not in ("0", "1") or notifications_mode == "1" and (remote_mode != "1" or browser_mode != "1" or gate_mode != "full"):
        raise Failure("notifications mode requires exact full remote backup/browser profile")
    if retention_mode not in ("0", "1") or retention_mode == "1" and remote_mode != "1":
        raise Failure("retention mode requires the reviewed remote backup profile")
    if remote_mode not in ("0", "1") or remote_mode == "1" and backup_mode != "1":
        raise Failure("remote mode requires the reviewed backup profile")
    if schedule_mode not in ("0", "1") or schedule_mode == "1" and backup_mode != "1":
        raise Failure("schedule mode requires the reviewed backup profile")
    if backup_mode not in ("0", "1") or backup_mode == "1" and not mysql_schedule and (browser_mode != ("0" if gate_mode == "retention-failures-diagnostic" else "1") or routes_mode != "0" or storage_mode != "0" or gate_mode not in ("full", "retention-failures-diagnostic")):
        raise Failure("backup mode requires its exact private database/browser profile")
    if browser_mode not in ("0", "1"):
        raise Failure("invalid browser mode")
    if routes_mode not in ("0", "1"):
        raise Failure("invalid route mode")
    if storage_mode not in ("0", "1") or storage_mode == "1" and (routes_mode != "1" or browser_mode != "1"):
        raise Failure("storage mode requires reviewed route and live browser profiles")
    if gate_mode not in ("full", "storage-cutover-diagnostic", "retention-failures-diagnostic"):
        raise Failure("invalid fixed runtime mode")
    if gate_mode == "retention-failures-diagnostic":
        retention_failure_diagnostic_profile(backup_mode, remote_mode, retention_mode, schedule_mode, browser_mode, routes_mode, storage_mode)
        say("diagnostic-only retention failures and protections; full workflow qualification disabled")
    if gate_mode == "storage-cutover-diagnostic" and storage_mode != "1":
        raise Failure("cutover diagnostic requires the reviewed storage profile")
    if gate_mode == "storage-cutover-diagnostic":
        say("diagnostic-only PostgreSQL cutover; full workflow qualification disabled")
    if not Path("internal/api/hosting_database_runtime_integration_test.go").is_file():
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
    backup_baseline_capture(containers, volumes)
    if NAME in containers or VOLUME in volumes:
        raise Failure("exact fixture name already exists; no mutation permitted")
    run(["git", "ls-files", "-s", "acceptance/runtime-hosting-database-runner/",
         "acceptance/runtime-hosting-database-client/", "internal/api/hosting_database_runtime_integration_test.go",
         "internal/api/hosting_database_capture_runtime_integration_test.go",
         "internal/api/hosting_database_upgrade_runtime_integration_test.go",
         "internal/api/hosting_application_database_runtime_integration_test.go",
         "internal/api/hosting_application_database_route_runtime_integration_test.go",
         "internal/api/hosting_application_database_storage_runtime_integration_test.go",
         "acceptance/runtime-proxy-control/pebble.json", "internal/runnerruntime/hosting_proxy_engine_fixture.go",
         "db/migrations/0073_hosting_application_database_route_continuity.sql", "internal/domain/hosting_application_database_route_continuity.go",
         "db/migrations/0074_hosting_application_database_storage.sql", "internal/domain/hosting_proxy_storage_switch.go",
         "internal/store/sql/hosting_application_database_storage.sql", "internal/store/sqlcgen/hosting_application_database_storage.sql.go",
         "internal/hostingjobs/application_database_storage*.go", "internal/hostingproxy/",
         "internal/hostingjobs/application_database_route.go", "internal/store/postgres_hosting_application_database_route_continuity.go",
         "internal/store/postgres_hosting_database_connection_authority.go", "internal/store/postgres_hosting_database_connection_complete.go",
         "internal/store/postgres_hosting_database_connection_execution.go", "internal/store/postgres_hosting_database_connection_runner.go",
         "internal/store/postgres_hosting_proxy.go", "internal/store/postgres_hosting_route_switch.go", "internal/hostingjobs/jobs.go",
         "internal/domain/hosting_route_switch.go", "internal/api/routes.go", "openapi.yaml", "web/app/src/api/generated.ts",
         "internal/api/hosting_database_connection*.go", "web/app/src/pages/ResourceDatabaseConnections.tsx",
         "web/app/src/components/hosting/ConnectedStorageRecovery.tsx",
         "web/app/tests/browser/database-connection-live.spec.ts", "web/app/src/api/hosting-database-connections.ts", "web/app/package.json", "web/app/bun.lock",
         "internal/app/hosting_database_connection_service.go", "internal/store/postgres_hosting_database_connection*.go",
         "internal/store/sql/hosting_database_connection.sql", "internal/store/sqlcgen/hosting_database_connection.sql.go",
         "db/migrations/0072_hosting_application_database_connections.sql",
         "cmd/nerocd/runner_application_database_config.go", "internal/runner/journal_application_database.go",
         "internal/runnerclient/hosting_database_connection.go", "internal/domain/hosting_database_connection.go",
         "internal/domain/hosting_application_database*.go", "internal/domain/hosting_database_maintenance.go",
         "internal/runnerruntime/hosting_application_database_execution.go",
         "internal/runnerruntime/hosting_application_database_engine.go", "internal/runnerruntime/hosting_database_maintenance_execution.go",
         "internal/runnerruntime/hosting_application_database_storage.go", "internal/runner/journal_application_database_storage.go",
         "internal/store/postgres_hosting_application_database_storage*.go", "internal/runnerclient/hosting_application_database_storage.go",
         "internal/runnerruntime/hosting_database_member_maintenance.go", "internal/runnerruntime/runtime.go",
         "cmd/nerocd/runner.go", "cmd/nerocd/runner_database_config.go",
         "internal/runnerruntime/hosting_database_network_capture_execution.go",
         "internal/runnerruntime/hosting_database_network_capture.go", "internal/runner/journal_database_network_capture.go",
         "internal/runnerclient/hosting_database_network_capture.go",
         "internal/runnerruntime/hosting_database_execution.go", "internal/runnerruntime/hosting_database_engine.go",
         "internal/runnerruntime/hosting_database_execution_config.go", "internal/runnerruntime/hosting_database_replay.go",
         "internal/runnerruntime/hosting_database_upgrade_execution.go", "internal/runnerruntime/hosting_database_network_upgrade.go",
         "internal/runnerruntime/hosting_runtime_environment.go",
         "internal/runnerruntime/hosting_database_secrets.go", "internal/runnerruntime/hosting_database_network.go",
         "internal/runnerruntime/hosting_database_readiness.go", "internal/runnerruntime/hosting_owned_volume.go",
         "internal/runner/journal.go", "internal/runner/journal_database.go",
         "internal/runner/journal_hosting.go", "internal/runner/journal_storage.go",
         "internal/runnerclient/hosting_execution.go", "internal/runnerclient/hosting_proxy.go",
         "internal/runnerclient/hosting_database_delivery.go", "internal/domain/hosting_database.go",
         "internal/domain/hosting_database_execution.go", "internal/domain/hosting_database_upgrade_execution.go",
         "internal/domain/hosting_database_network_plan.go", "internal/domain/hosting_execution.go",
         "internal/app/hosting_database_upgrade_service.go", "internal/store/hosting_database_upgrade_repository.go",
         "internal/store/postgres_hosting_database_delivery.go", "internal/store/postgres_hosting_database_execution.go",
         "internal/store/postgres_hosting_database_upgrade.go", "internal/store/postgres_hosting_execution.go",
         "internal/store/postgres_hosting_storage_materialization.go",
         "internal/store/sql/hosting_database_network_upgrades.sql", "internal/store/sqlcgen/hosting_database_network_upgrades.sql.go",
         "internal/store/sqlcgen/models.go", "db/migrations/0071_hosting_database_network_upgrades.sql"], output="source-blobs")
    if browser_mode == "1":
        if not Path("web/app/src/pages/ResourceDatabaseConnections.tsx").is_file():
            raise Failure("browser mode requires the reviewed connected UI source")
        run(["bun", "run", "--cwd", "web/app", "build"], timeout=300, output="browser-ui-build")
        files = sorted(path for path in Path("web/dist").rglob("*") if path.is_file())
        if not Path("web/dist/index.html").is_file() or len(files) > 2048 or any(path.is_symlink() for path in files):
            raise Failure("browser UI artifact unavailable or unsafe")
        manifest = [{"path": str(path.relative_to("web/dist")), "size": path.stat().st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()} for path in files]
        (evidence / "browser-ui-manifest.json").write_text(json.dumps(manifest, sort_keys=True) + "\n")
    environment = dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0")
    fixture_tags = ["-tags", "nerocd_proxy_fixture"] if routes_mode == "1" else []
    run(["go", "test", "-c", *fixture_tags, "-o", str(evidence / "api.test"), "./internal/api"], timeout=300, env=environment)
    run(["go", "build", "-trimpath", *fixture_tags, "-o", str(evidence / "nerocd"), "./cmd/nerocd"], timeout=300, env=environment)
    client_context = evidence / "client"
    client_context.mkdir()
    run(["go", "build", "-trimpath", "-o", str(client_context / "client"), "./acceptance/runtime-hosting-database-client"], timeout=300, env=environment)
    (client_context / "Dockerfile").write_bytes(Path("acceptance/runtime-hosting-database-client/Dockerfile").read_bytes())
    (client_context / "Dockerfile.http").write_bytes(Path("acceptance/runtime-hosting-database-client/Dockerfile.http").read_bytes())
    if storage_mode == "1":
        (client_context / "Dockerfile.http-storage").write_bytes(Path("acceptance/runtime-hosting-database-client/Dockerfile.http-storage").read_bytes())
    for name, binary in (("api.test", evidence / "api.test"), ("nerocd", evidence / "nerocd"), ("client", client_context / "client")):
        run(["go", "version", "-m", str(binary)], output=name + "-build-info")
        (evidence / (name + "-sha256")).write_text(hashlib.sha256(binary.read_bytes()).hexdigest() + "\n")
    image = inspect("image", IMAGE, "pinned-outer-image")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image.get("Id", "")):
        raise Failure("invalid pinned image ID")
    volume_attempted = True
    if docker("volume", "create", "--driver", "local", "--label", LABEL + "=" + OWNER_LABEL, VOLUME).strip() != VOLUME:
        raise Failure("unexpected volume creation response")
    verified_volume()
    outer_attempted = True
    if backup_mode == "1":
        outer_deadline = time.monotonic() + 3600
    publication = ["--publish", "127.0.0.1::18080"] if browser_mode == "1" else []
    limits = ["--cpus", "4", "--memory", "4g", "--pids-limit", "1024"] if backup_mode == "1" else []
    response = docker("container", "create", *publication, *limits, "--privileged", "--name", NAME, "--label", LABEL + "=" + OWNER_LABEL,
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
    docker("exec", outer, "mkdir", "-p", "/fixture", "/workspace/db/seeds", "/workspace/internal/api")
    for binary in ("api.test", "nerocd"):
        docker("cp", str(evidence / binary), outer + ":/fixture/" + binary)
    docker("cp", "db/seeds/dev.sql", outer + ":/workspace/db/seeds/dev.sql")
    if routes_mode == "1":
        # Copy bytes only into owned DinD. No outer-host bind or socket mount.
        original = Path("acceptance/runtime-proxy-control/pebble.json").read_bytes()
        config = evidence / "pebble.json"
        config.write_bytes(database_workflow_pebble_config(original))
        (evidence / "pebble-source-config-sha256").write_text(hashlib.sha256(original).hexdigest() + "\n")
        (evidence / "pebble-config-sha256").write_text(hashlib.sha256(config.read_bytes()).hexdigest() + "\n")
        docker("cp", str(config), outer + ":/fixture/pebble.json")
        docker("exec", outer, "docker", "--host", ENDPOINT, "pull", PEBBLE_IMAGE, timeout=180, output="fixture-pebble-pull")
    docker("cp", str(client_context), outer + ":/fixture/client")
    docker("exec", outer, "docker", "--host", ENDPOINT, "build", "--build-arg", "FIXTURE_OWNER=" + OWNER,
           "--tag", "nerocd-database-client:" + OWNER, "/fixture/client", timeout=120, output="client-image-build")
    # This registry and its loopback publication exist only in the owned DinD.
    registry_image = "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
    docker("exec", outer, "docker", "--host", ENDPOINT, "pull", registry_image, timeout=180, output="registry-pull")
    registry = docker("exec", outer, "docker", "--host", ENDPOINT, "run", "--detach", "--name", OWNER + "-registry",
                      "--label", LABEL + "=" + OWNER_LABEL, "--publish", "127.0.0.1::5000", "--tmpfs", "/var/lib/registry:rw", registry_image).strip()
    if not re.fullmatch(r"[a-f0-9]{64}", registry):
        raise Failure("invalid inner registry identity")
    registry_state = document(docker("exec", outer, "docker", "--host", ENDPOINT, "container", "inspect", registry,
                                     "--format", "{{json .}}", output="inner-registry-profile"))
    if not isinstance(registry_state, dict):
        raise Failure("inner registry inspection is not an object")
    ports = registry_state.get("HostConfig", {}).get("PortBindings", {})
    publication = ports.get("5000/tcp", [])
    if (registry_state.get("Id") != registry or registry_state.get("Name") != "/" + OWNER + "-registry"
            or registry_state.get("Config", {}).get("Labels", {}).get(LABEL) != OWNER_LABEL
            or registry_state.get("Config", {}).get("Image") != registry_image
            or registry_state.get("HostConfig", {}).get("Privileged") is not False
            or not registry_state.get("State", {}).get("Running") or len(ports) != 1 or len(publication) != 1
            or publication[0].get("HostIp") != "127.0.0.1"):
        raise Failure("inner registry ownership/publication mismatch")
    assigned = registry_state.get("NetworkSettings", {}).get("Ports", {}).get("5000/tcp", [])
    if len(assigned) != 1 or assigned[0].get("HostIp") != "127.0.0.1" or not re.fullmatch(r"[0-9]{4,5}", assigned[0].get("HostPort", "")):
        raise Failure("inner registry dynamic loopback port missing")
    port = int(assigned[0]["HostPort"])
    if not 1024 <= port <= 65535:
        raise Failure("inner registry port out of bounds")
    app_repository = "127.0.0.1:" + str(port) + "/nerocd-http-app"
    app_tag = app_repository + ":" + OWNER
    for attempt in range(20):
        try:
            if docker("exec", outer, "wget", "-q", "-O", "-", "http://127.0.0.1:" + str(port) + "/v2/", timeout=5).strip() == "{}":
                break
        except Failure:
            pass
        if attempt == 19:
            raise Failure("owned inner registry did not become ready")
        time.sleep(1)
    docker("exec", outer, "docker", "--host", ENDPOINT, "build", "--file", "/fixture/client/Dockerfile.http",
           "--build-arg", "FIXTURE_OWNER=" + OWNER, "--tag", app_tag, "/fixture/client", timeout=120, output="http-app-image-build")
    docker("exec", outer, "docker", "--host", ENDPOINT, "push", app_tag, timeout=120, output="http-app-image-push")
    digests = document(docker("exec", outer, "docker", "--host", ENDPOINT, "image", "inspect", app_tag,
                              "--format", "{{json .RepoDigests}}", output="http-app-digests"))
    if not isinstance(digests, list) or len(digests) != 1 or not re.fullmatch(re.escape(app_repository) + r"@sha256:[a-f0-9]{64}", digests[0]):
        raise Failure("HTTP app registry digest missing")
    app_image = digests[0]
    (evidence / "http-app-image").write_text(app_image + "\n")
    docker("exec", outer, "docker", "--host", ENDPOINT, "image", "rm", app_tag, output="http-app-remove-local-tag")
    if docker("exec", outer, "docker", "--host", ENDPOINT, "image", "ls", "--quiet", "--no-trunc", "--filter", "reference=" + app_repository + "*").strip():
        raise Failure("HTTP fixture image remained local before production pull")
    storage_image = ""
    if storage_mode == "1":
        storage_tag = app_repository + ":" + OWNER + "-storage"
        docker("exec", outer, "docker", "--host", ENDPOINT, "build", "--file", "/fixture/client/Dockerfile.http-storage",
               "--build-arg", "FIXTURE_OWNER=" + OWNER, "--tag", storage_tag, "/fixture/client", timeout=120, output="storage-http-app-image-build")
        docker("exec", outer, "docker", "--host", ENDPOINT, "push", storage_tag, timeout=120, output="storage-http-app-image-push")
        digests = document(docker("exec", outer, "docker", "--host", ENDPOINT, "image", "inspect", storage_tag,
                                  "--format", "{{json .RepoDigests}}", output="storage-http-app-digests"))
        if not isinstance(digests, list) or len(digests) != 1 or not re.fullmatch(re.escape(app_repository) + r"@sha256:[a-f0-9]{64}", digests[0]) or digests[0] == app_image:
            raise Failure("distinct storage HTTP app registry digest missing")
        storage_image = digests[0]
        (evidence / "storage-http-app-image").write_text(storage_image + "\n")
        docker("exec", outer, "docker", "--host", ENDPOINT, "image", "rm", storage_tag, output="storage-http-app-remove-local-tag")
        if docker("exec", outer, "docker", "--host", ENDPOINT, "image", "ls", "--quiet", "--no-trunc", "--filter", "reference=" + app_repository + "*").strip():
            raise Failure("storage HTTP fixture image remained local before production pull")
    postgres = "postgres:17.6-alpine@sha256:ef257d85f76e48da1c64832459b59fcaba1a4dac97bf5d7450c77753542eee94"
    docker("exec", outer, "docker", "--host", ENDPOINT, "pull", postgres, timeout=180, output="control-postgres-pull")
    pg = docker("exec", outer, "docker", "--host", ENDPOINT, "run", "--detach", "--name", OWNER + "-control-pg",
                "--label", LABEL + "=" + OWNER_LABEL, "--network", "host", "--tmpfs", "/var/lib/postgresql/data:rw",
                "--env", "POSTGRES_USER=nerocd", "--env", "POSTGRES_PASSWORD=isolated_fixture_only",
                "--env", "POSTGRES_DB=nerocd_database_runner", postgres).strip()
    if not re.fullmatch(r"[a-f0-9]{64}", pg):
        raise Failure("invalid control PostgreSQL container identity")
    (evidence / "control-postgres-id").write_text(pg + "\n")
    for attempt in range(60):
        try:
            ready = docker("exec", outer, "docker", "--host", ENDPOINT, "exec", "--env", "PGPASSWORD=isolated_fixture_only", pg,
                           "psql", "--host=127.0.0.1", "--username=nerocd", "--dbname=nerocd_database_runner", "--no-psqlrc",
                           "--tuples-only", "--no-align", "--command=SELECT current_database()", timeout=5).strip()
            if ready == "nerocd_database_runner":
                break
        except Failure:
            pass
        if attempt == 59:
            raise Failure("control PostgreSQL did not become ready")
        time.sleep(1)
    if remote_mode == "1":
        docker("exec", outer, "docker", "--host", ENDPOINT, "pull", SILO_IMAGE, timeout=180, output="remote-silo-pull")
    docker("exec", outer, "docker", "--host", ENDPOINT, "image", "ls", "--no-trunc", "--digests", "--format", "{{json .}}", output="inner-images-before-test")
    runtime_args = ["docker", "--host", ENDPOINT, "exec", "--workdir", "/workspace/internal/api",
               "--env", "NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME=disposable-dind",
               "--env", "NEROCD_DATABASE_HTTP_APP_IMAGE=" + app_image,
               "--env", "NEROCD_TEST_DATABASE_URL=postgres://nerocd:isolated_fixture_only@127.0.0.1:5432/nerocd_database_runner?sslmode=disable"]
    if mysql_mode == "1":
        runtime_args += ["--env", "NEROCD_DATABASE_MYSQL_RUNTIME=1"]
    if backup_mode == "1":
        runtime_args += ["--env", "NEROCD_DATABASE_BACKUP_RUNTIME=1"]
        if notifications_mode == "1":
            runtime_args += ["--env", "NEROCD_DATABASE_BACKUP_NOTIFICATIONS_RUNTIME=1"]
        if remote_mode == "1":
            runtime_args += ["--env", "NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME=1"]
        if retention_mode == "1":
            runtime_args += ["--env", "NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME=1"]
        if schedule_mode == "1":
            runtime_args += ["--env", "NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME=1"]
    if browser_mode == "1":
        verified_outer(outer)
        if not browser_origin:
            raise Failure("browser bridge origin missing")
        runtime_args += ["--env", "NEROCD_DATABASE_CONNECTION_BROWSER=1", "--env", "NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN=" + browser_origin]
    if routes_mode == "1":
        runtime_args += ["--env", "NEROCD_DATABASE_CONNECTION_ROUTES=1"]
    if storage_mode == "1":
        runtime_args += ["--env", "NEROCD_DATABASE_CONNECTION_STORAGE=1", "--env", "NEROCD_DATABASE_STORAGE_HTTP_APP_IMAGE=" + storage_image]
    pattern = "^TestHosting(Database(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)|ApplicationDatabaseCompiledRunner)$"
    if routes_mode == "1":
        pattern = "^TestHosting(Database(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)|ApplicationDatabaseCompiledRunner|ApplicationDatabaseRouteFailuresCompiledRunner)$"
    if storage_mode == "1":
        pattern = "^TestHosting(Database(CompiledRunner|CaptureCompiledRunner|UpgradeCompiledRunner)|ApplicationDatabaseCompiledRunner|ApplicationDatabaseRouteFailuresCompiledRunner|ApplicationDatabaseStorageCompiledRunner|ApplicationDatabaseStorageFailuresCompiledRunner)$"
    if gate_mode == "storage-cutover-diagnostic":
        pattern = STORAGE_CUTOVER_DIAGNOSTIC_SELECTOR
    if backup_mode == "1":
        pattern, _, _ = backup_runtime_profile(schedule_mode, remote_mode, retention_mode, notifications_mode, mysql_mode)
    if gate_mode == "retention-failures-diagnostic":
        pattern, _, _ = retention_failure_diagnostic_profile(backup_mode, remote_mode, retention_mode, schedule_mode, browser_mode, routes_mode, storage_mode)
    if mysql_mode == "1" and backup_mode == "0":
        pattern = "^TestHostingApplicationDatabaseStorage(CompiledRunner|FailuresCompiledRunner)$"
    runtime_args += [outer, "/fixture/api.test", "-test.v", "-test.run", pattern, "-test.timeout=40m"]
    if routes_mode == "1":
        runtime_args += ["-test.failfast"]
    if browser_mode == "1" and gate_mode == "full":
        runtime_output = run(["node", "acceptance/runtime-hosting-database-runner/browser-driver.mjs", browser_origin,
                              str(evidence / "browser-results"), "--", *runtime_args], timeout=2410, output="runtime-test")
    else:
        runtime_output = run(runtime_args, timeout=2410, output="runtime-test")
    say(runtime_output)
    if gate_mode == "retention-failures-diagnostic":
        verify_retention_failure_diagnostic_output(runtime_output)
        diagnostic_pass = True
        say("DATABASE_BACKUP_RETENTION_FAILURE_DIAGNOSTIC_PASS postgresql")
        return
    if backup_mode == "1":
        verify_backup_runtime_output(runtime_output, schedule_mode, remote_mode, retention_mode, notifications_mode, mysql_mode)
        full_workflow_pass = True
        say("hosted " + ("MySQL scheduled" if mysql_schedule else "PostgreSQL") + " logical backup and new-database restore fixture passed")
        return
    if mysql_mode == "1":
        verify_mysql_runtime_output(runtime_output)
        full_workflow_pass = True
        say("native MySQL connected application, uploads, replacement, trusted routing and local backup/restore passed")
        return
    if gate_mode == "storage-cutover-diagnostic":
        required = {"TestHostingApplicationDatabaseStorageFailuresCompiledRunner",
                    "TestHostingApplicationDatabaseStorageFailuresCompiledRunner/postgresql/cutover"}
        passed = re.findall(r"^\s*--- PASS: ([^ ]+) ", runtime_output, re.MULTILINE)
        if set(passed) != required or len(passed) != len(required) or re.search(r"^\s*--- (?:SKIP|FAIL): ", runtime_output, re.MULTILINE):
            raise Failure("required diagnostic cutover case did not pass exclusively")
        for marker in ("DATABASE_CONNECTION_STORAGE_RECOVERY_PASS postgresql cutover",
                       "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS postgresql cutover"):
            if runtime_output.splitlines().count(marker) != 1:
                raise Failure("required diagnostic cutover result or cleanup proof missing or repeated")
        allowed = {"DATABASE_CONNECTION_STORAGE_RECOVERY_PASS postgresql cutover",
                   "DATABASE_CONNECTION_STORAGE_CAPACITY_PASS postgresql cutover"}
        if any(line.startswith("DATABASE_CONNECTION_") and line not in allowed for line in runtime_output.splitlines()):
            raise Failure("diagnostic output contains unrelated qualification markers")
        diagnostic_pass = True
        say("DATABASE_CONNECTION_STORAGE_CUTOVER_DIAGNOSTIC_PASS postgresql")
        return
    required = ["TestHostingDatabaseCompiledRunner", "TestHostingDatabaseCaptureCompiledRunner", "TestHostingDatabaseUpgradeCompiledRunner", "TestHostingApplicationDatabaseCompiledRunner"]
    if routes_mode == "1":
        required.append("TestHostingApplicationDatabaseRouteFailuresCompiledRunner")
    if storage_mode == "1":
        required.append("TestHostingApplicationDatabaseStorageCompiledRunner")
        required.append("TestHostingApplicationDatabaseStorageFailuresCompiledRunner")
    for test in required:
        if not re.search(r"^--- PASS: " + test + r" ", runtime_output, re.MULTILINE):
            raise Failure("required physical test did not pass: " + test)
        if re.search(r"^--- SKIP: " + test + r" ", runtime_output, re.MULTILINE):
            raise Failure("required physical test skipped: " + test)
    if browser_mode == "1":
        for engine in ("postgresql", "redis"):
            if runtime_output.splitlines().count("DATABASE_CONNECTION_BROWSER_PASS " + engine) != 1:
                raise Failure("required live browser result missing or repeated: " + engine)
    if storage_mode == "1":
        for engine in ("postgresql", "redis"):
            for marker in ("DATABASE_CONNECTION_STORAGE_PASS ", "DATABASE_CONNECTION_STORAGE_BROWSER_PASS "):
                if runtime_output.splitlines().count(marker + engine) != 1:
                    raise Failure("required connected storage result missing or repeated: " + marker + engine)
            if runtime_output.splitlines().count("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS " + engine + " success") != 1:
                raise Failure("required post-cleanup storage capacity proof missing or repeated: " + engine)
            for case in ("readiness", "cutover"):
                if runtime_output.splitlines().count("DATABASE_CONNECTION_STORAGE_RECOVERY_PASS " + engine + " " + case) != 1:
                    raise Failure("required actual connected storage recovery proof missing or repeated: " + engine + " " + case)
                if runtime_output.splitlines().count("DATABASE_CONNECTION_STORAGE_CAPACITY_PASS " + engine + " " + case) != 1:
                    raise Failure("required post-recovery storage capacity proof missing or repeated: " + engine + " " + case)
        for engine, action in (("postgresql", "resume_predecessor"), ("redis", "complete_replacement")):
            for marker in ("DATABASE_CONNECTION_STORAGE_RECOVERY_BROWSER_PASS ", "DATABASE_CONNECTION_STORAGE_RECOVERY_UI_PASS "):
                if runtime_output.splitlines().count(marker + engine + " " + action) != 1:
                    raise Failure("required live storage recovery UI proof missing or repeated: " + engine + " " + action)
    if routes_mode == "1":
        for engine in ("postgresql", "redis"):
            if runtime_output.splitlines().count("DATABASE_CONNECTION_ROUTE_PASS " + engine) != 1:
                raise Failure("required trusted HTTPS route result missing or repeated: " + engine)
            for case in ("readiness", "cutover"):
                if runtime_output.splitlines().count("DATABASE_CONNECTION_ROUTE_FAILURE_PASS " + engine + " " + case) != 1:
                    raise Failure("required trusted HTTPS failure result missing or repeated: " + engine + " " + case)
            for case in ("success", "readiness", "cutover"):
                if runtime_output.splitlines().count("DATABASE_CONNECTION_ROUTE_CAPACITY_PASS " + engine + " " + case) != 1:
                    raise Failure("required post-cleanup route capacity proof missing or repeated: " + engine + " " + case)
    full_workflow_pass = True
    say("compiled database, capture, upgrade and connected HTTP app runners / authenticated API / River / private PostgreSQL and Redis gate passed")


GUEST_JOIN = r"""
for exe in /proc/[0-9]*/exe; do
 case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd|/usr/local/bin/docker|/client)
  pid=$(basename "$(dirname "$exe")"); kill -TERM "$pid";; esac
done
for tick in $(seq 1 100); do
 live=false
 for exe in /proc/[0-9]*/exe; do
  case $(readlink "$exe" 2>/dev/null || true) in /fixture/api.test|/fixture/nerocd|/usr/local/bin/docker|/client) live=true;; esac
 done
 if [ "$live" = false ]; then exit 0; fi
 sleep 0.1
done
exit 1
"""


def cleanup():
    global cleaning
    cleaning = True
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
                    (evidence / "process-absence").write_text("owned guest API, runner, client and Docker CLI processes absent\n")
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
        backup_baseline_verify()
        if head is not None and (run(["git", "rev-parse", "HEAD"]).strip() != head or
                                 run(["git", "rev-parse", "HEAD^{tree}"]).strip() != tree or
                                 run(["git", "status", "--porcelain"], output="status-after").strip()):
            raise Failure("checkout changed during gate")
    except Exception as error:
        say("final inventory/check failed: " + str(error))
        status = status or 1
    receipt_mode = gate_mode if gate_mode in ("full", "storage-cutover-diagnostic", "retention-failures-diagnostic") else "invalid"
    result_name, exit_key = ("result", "gate_exit") if gate_mode == "full" else ("diagnostic-result", "diagnostic_exit")
    (evidence / result_name).write_text(exit_key + "=" + str(status) + "\ngate_mode=" + receipt_mode +
                                   "\nfull_workflow_pass=" + str(int(full_workflow_pass and gate_mode == "full" and status == 0)) +
                                   "\ndiagnostic_pass=" + str(int(diagnostic_pass and status == 0)) + "\nouter_id=" + outer +
                                   "\nouter_volume=" + VOLUME + "\ncleanup_failure=" + cleanup_failure + "\n")
    say(("gate exit=" if gate_mode == "full" else "diagnostic exit=") + str(status) + " evidence=" + str(evidence))
    log.close()
sys.exit(status)
PY
