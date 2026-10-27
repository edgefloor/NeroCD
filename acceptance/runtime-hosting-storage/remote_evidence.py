"""Closed-profile checks and safe evidence for the opt-in remote storage gate."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import urllib.request

ENDPOINT = "unix:///var/run/docker.sock"
UI = Path("/Users/bubble/.local/share/nerocd/user-ui-20261025")


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def docker(*args):
    return subprocess.check_output(["docker", "--host", ENDPOINT, *args], timeout=30)


def publication(value):
    """Require the one explicit loopback publication and its dynamic assignment."""
    host = value.get("HostConfig", {})
    requested = {"18190/tcp": [{"HostIp": "127.0.0.1", "HostPort": ""}]}
    application_owner = value.get("Config", {}).get("Labels", {}).get("io.nerocd.storage-runtime.owner")
    application = application_owner in {"nerocd-compose-vikunja-runtime-20261027", "nerocd-catalog-uptime-kuma-runtime-20261027"} and value.get("Name") == "/" + application_owner + "-dind"
    if application:
        requested["443/tcp"] = [{"HostIp": "127.0.0.1", "HostPort": "25443"}]
    if host.get("PortBindings") != requested:
        raise ValueError("remote browser publication request mismatch")
    ports = value.get("NetworkSettings", {}).get("Ports") or {}
    if not isinstance(ports, dict):
        raise ValueError("malformed publication")
    assigned = {key: bindings for key, bindings in ports.items() if bindings}
    bindings = assigned.get("18190/tcp")
    if set(assigned) != set(requested) or not isinstance(bindings, list) or len(bindings) != 1:
        raise ValueError("extra or missing published port")
    if application and assigned["443/tcp"] != requested["443/tcp"]:
        raise ValueError("Catalog application publication changed")
    binding = bindings[0]
    if (not isinstance(binding, dict) or set(binding) != {"HostIp", "HostPort"}
            or binding["HostIp"] != "127.0.0.1"
            or not isinstance(binding["HostPort"], str)
            or not re.fullmatch(r"[1-9][0-9]{3,4}", binding["HostPort"])
            or not 1024 <= int(binding["HostPort"]) <= 65535):
        raise ValueError("non-loopback or invalid published port")
    return "https://127.0.0.1:" + binding["HostPort"]


def selected(value):
    # Qualified PG gate comparison, retaining all state except rotating health logs.
    result = json.loads(json.dumps(value))
    result["Mounts"] = sorted(result.get("Mounts", []), key=lambda v: json.dumps(v, sort_keys=True))
    health = result.get("State", {}).get("Health")
    if isinstance(health, dict):
        health.pop("Log", None)
    return {key: result.get(key) for key in ("Id", "Name", "Image", "Config", "HostConfig", "Mounts", "State", "NetworkSettings")}


def snapshot():
    ids = docker("ps", "-aq", "--no-trunc").decode().splitlines()
    if any(not re.fullmatch(r"[a-f0-9]{64}", value) for value in ids):
        raise ValueError("invalid baseline IDs")
    containers = json.loads(docker("inspect", *ids)) if ids else []
    volumes = docker("volume", "ls", "--format", "{{.Name}}").decode().splitlines()
    profiles = json.loads(docker("volume", "inspect", *volumes)) if volumes else []
    stable = ["nerocd", "runtime-config.json", "service.plist", "launch-server.py", "admin-password", "postgres.env"]
    files = {}
    for name in stable:
        path = UI / name
        stat = path.lstat()
        if path.is_symlink() or not path.is_file():
            raise ValueError("user UI stable file identity unavailable")
        files[name] = {"sha256": digest(path.read_bytes()), "mode": stat.st_mode, "uid": stat.st_uid,
                       "gid": stat.st_gid, "inode": stat.st_ino, "device": stat.st_dev}
    root = UI.lstat()
    if UI.is_symlink() or not UI.is_dir():
        raise ValueError("user UI root identity unavailable")
    # Query the existing UI without authentication or mutations. Never save bodies.
    health = {}
    for path in ("/", "/api/v1/health"):
        with urllib.request.urlopen("http://127.0.0.1:18081" + path, timeout=5) as response:
            body = response.read((2 << 20) + 1)
            if response.status != 200 or len(body) > 2 << 20:
                raise ValueError("user UI response health failed")
            health[path] = {"status": response.status, "sha256": digest(body)}
    listeners = subprocess.check_output(["lsof", "-nP", "-iTCP:18081", "-sTCP:LISTEN", "-Fpct"], timeout=5).decode()
    if not re.search(r"^p[1-9][0-9]*$", listeners, re.M):
        raise ValueError("user UI listener identity unavailable")
    return {"containers": {value["Id"]: selected(value) for value in containers},
            "volumes": {value["Name"]: value for value in profiles}, "ui_files": files,
            "ui_root": [root.st_dev, root.st_ino, root.st_mode, root.st_uid, root.st_gid],
            "ui_listener": listeners, "ui_health": health}


def preservation(before, after, owner, name, volume):
    """Preserve baseline objects exactly; report concurrent non-task additions."""
    if any(not isinstance(v, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", v)
           for v in (owner, name, volume)):
        raise ValueError("task preservation identity unavailable")
    old = before["containers"]
    current = after["containers"]
    additions = sorted(set(current) - set(old))
    blocked = []
    uncertain = []
    for identity in additions:
        value = current[identity]
        if not isinstance(value, dict):
            uncertain.append(identity)
            continue
        config = value.get("Config")
        host = value.get("HostConfig")
        mounts = value.get("Mounts")
        if (value.get("Id") != identity or not isinstance(value.get("Name"), str) or not re.fullmatch(r"/[A-Za-z0-9][A-Za-z0-9_.-]*", value["Name"])
                or not isinstance(config, dict) or not isinstance(host, dict)
                or not isinstance(mounts, list) or any(not isinstance(m, dict) for m in mounts)):
            uncertain.append(identity)
            continue
        labels = config.get("Labels")
        requested = host.get("Mounts")
        labels = {} if labels is None else labels
        requested = [] if requested is None else requested
        if not isinstance(labels, dict) or not isinstance(requested, list) or any(not isinstance(m, dict) for m in requested):
            uncertain.append(identity)
            continue
        volume_user = any(m.get("Name") == volume or m.get("Source") == volume for m in mounts + requested)
        if labels.get("io.nerocd.storage-runtime.owner") == owner or value.get("Name") == "/" + name or volume_user:
            blocked.append(identity)
    containers_unchanged = all(identity in current and current[identity] == value for identity, value in old.items())
    volumes_unchanged = before["volumes"] == after["volumes"]
    baseline_volumes_unchanged = all(key in after["volumes"] and after["volumes"][key] == value
                                     for key, value in before["volumes"].items())
    added_volumes = sorted(set(after["volumes"]) - set(before["volumes"]))
    classified_volumes = []
    unclassified_volumes = []
    task_volumes = []
    classified_containers = set(additions) - set(blocked) - set(uncertain)
    for key in added_volumes:
        value = after["volumes"][key]
        if key == volume or key == owner or key.startswith(owner + "-") or key == name:
            task_volumes.append(key)
            continue
        if not isinstance(value, dict):
            unclassified_volumes.append(key)
            continue
        labels = value.get("Labels")
        labels = {} if labels is None else labels
        if isinstance(labels, dict) and labels.get("io.nerocd.storage-runtime.owner") == owner:
            task_volumes.append(key)
            continue
        mountpoint = "/var/lib/docker/volumes/" + key + "/_data"
        valid = (isinstance(key, str) and re.fullmatch(r"[a-f0-9]{64}", key) is not None
                 and set(value) == {"CreatedAt", "Driver", "Labels", "Mountpoint", "Name", "Options", "Scope"}
                 and isinstance(value.get("CreatedAt"), str) and bool(value["CreatedAt"])
                 and value.get("Name") == key and value.get("Driver") == "local" and value.get("Scope") == "local"
                 and value.get("Mountpoint") == mountpoint and value.get("Options") in (None, {})
                 and labels == {"com.docker.volume.anonymous": ""})
        users = []
        for identity, container in current.items():
            if not isinstance(container, dict):
                valid = False
                continue
            mounts = container.get("Mounts")
            if not isinstance(mounts, list) or any(not isinstance(m, dict) for m in mounts):
                valid = False
                continue
            actual = [m for m in mounts if m.get("Name") == key or m.get("Source") == mountpoint]
            for mount in actual:
                users.append(identity)
                valid = valid and (identity in classified_containers and mount.get("Type") == "volume"
                                   and mount.get("Name") == key and mount.get("Source") == mountpoint
                                   and mount.get("Driver") == "local" and isinstance(mount.get("Destination"), str)
                                   and mount["Destination"].startswith("/") and isinstance(mount.get("RW"), bool)
                                   and isinstance(container.get("Config", {}).get("Volumes"), dict)
                                   and container["Config"]["Volumes"].get(mount["Destination"]) == {}
                                   and container.get("HostConfig", {}).get("Binds") in (None, [])
                                   and container.get("HostConfig", {}).get("Mounts") in (None, []))
            host = container.get("HostConfig")
            requested = host.get("Mounts") if isinstance(host, dict) else None
            requested = [] if requested is None else requested
            if not isinstance(requested, list) or any(not isinstance(m, dict) for m in requested):
                valid = False
                continue
            for mount in requested:
                if mount.get("Source") in (key, mountpoint):
                    valid = valid and (identity in classified_containers and mount.get("Type") == "volume"
                                       and mount.get("Source") == key
                                       and any(m.get("Destination") == mount.get("Target") for m in actual))
        if valid and users:
            classified_volumes.append(key)
        else:
            unclassified_volumes.append(key)
    # Inventories are checked above. Retain every other field, including unknown
    # snapshot fields, in the exact user identity/content comparison.
    other_unchanged = ({k: v for k, v in before.items() if k not in ("containers", "volumes")} ==
                       {k: v for k, v in after.items() if k not in ("containers", "volumes")})
    healthy = bool(after["ui_health"]) and all(value["status"] == 200 for value in after["ui_health"].values())
    passed = (containers_unchanged and baseline_volumes_unchanged and other_unchanged and healthy
              and not blocked and not uncertain and not task_volumes and not unclassified_volumes)
    return {"status": "passed" if passed else "failed",
            "baseline_containers": len(old), "baseline_volumes": len(before["volumes"]),
            "baseline_container_profiles_unchanged": containers_unchanged,
            "volume_inventory_and_profiles_unchanged": volumes_unchanged,
            "baseline_volume_profiles_unchanged": baseline_volumes_unchanged,
            "non_inventory_snapshot_unchanged": other_unchanged,
            "non_task_added_volume_names": classified_volumes,
            "task_owned_added_volume_names": task_volumes,
            "unclassified_added_volume_names": unclassified_volumes,
            "non_task_added_container_ids": [identity for identity in additions if identity not in blocked and identity not in uncertain],
            "task_owned_or_task_volume_addition_ids": blocked,
            "unclassified_added_container_ids": uncertain,
            "user_ui_identity_unchanged": all(before[k] == after[k] for k in ("ui_files", "ui_root", "ui_listener")),
            "user_ui_http_content_unchanged": before["ui_health"] == after["ui_health"],
            "user_ui_http_healthy": healthy}


def write(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + "\n")
    path.chmod(0o600)


def main():
    mode, directory = sys.argv[1:3]
    root = Path(directory)
    if mode == "profile":
        values = json.loads((root / "outer-current-inspect").read_text())
        if len(values) != 1:
            raise ValueError("ambiguous outer profile")
        origin = publication(values[0])
        pin = root / "browser-origin"
        if pin.exists() and pin.read_text().strip() != origin:
            raise ValueError("browser publication changed")
        pin.write_text(origin + "\n")
        return
    if mode == "before":
        write(root / "preservation-before.private.json", snapshot())
        return
    if mode == "after":
        before = json.loads((root / "preservation-before.private.json").read_text())
        after = snapshot()
        write(root / "preservation-after.private.json", after)
        owner, name, volume = sys.argv[3:6]
        result = preservation(before, after, owner, name, volume)
        write(root / "preservation-safe.json", result)
        if result["status"] != "passed":
            raise ValueError("baseline profile/user UI changed or task-owned addition remains")
        return
    if mode == "receipt":
        # Explicit allowlist, never recurse over private evidence or inventories.
        names = ["result", "binary-sha256", "archive-helper-provenance", "image-store-scope",
                 "preservation-safe.json", "runtime/remote-markers.json", "runtime/retention-markers.json", "runtime/deletion-markers.json", "runtime/owned-upload.tar",
                 "browser-safe.json", "browser-ui-manifest.json"]
        with tarfile.open(root / "safe-receipt.tar.gz", "w:gz") as archive:
            for name in names:
                path = root / name
                if path.exists():
                    if path.is_symlink() or not path.is_file():
                        raise ValueError("unsafe receipt member")
                    archive.add(path, arcname=name, recursive=False)
        return
    raise ValueError("unknown evidence action")


if __name__ == "__main__":
    main()
