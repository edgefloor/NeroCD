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
    if host.get("PortBindings") != {"18190/tcp": [{"HostIp": "127.0.0.1", "HostPort": ""}]}:
        raise ValueError("remote browser publication request mismatch")
    ports = value.get("NetworkSettings", {}).get("Ports") or {}
    if not isinstance(ports, dict):
        raise ValueError("malformed publication")
    assigned = {key: bindings for key, bindings in ports.items() if bindings}
    bindings = assigned.get("18190/tcp")
    if set(assigned) != {"18190/tcp"} or not isinstance(bindings, list) or len(bindings) != 1:
        raise ValueError("extra or missing published port")
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
    return {key: result.get(key) for key in ("Id", "Image", "Config", "HostConfig", "Mounts", "State", "NetworkSettings")}


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
        passed = before == after
        write(root / "preservation-safe.json", {"status": "passed" if passed else "failed",
              "baseline_containers": len(before["containers"]), "baseline_volumes": len(before["volumes"]),
              "user_ui_identity_unchanged": before["ui_files"] == after["ui_files"] and before["ui_root"] == after["ui_root"] and before["ui_listener"] == after["ui_listener"],
              "user_ui_http_healthy": all(value["status"] == 200 for value in after["ui_health"].values())})
        if not passed:
            raise ValueError("baseline identity/configuration/state/network or user UI changed")
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
