#!/usr/bin/env python3
"""Prepare an offline linux/arm64 scratch context. Never invoke Docker."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import selectors
import shutil
import signal
import stat
import struct
import subprocess
import tarfile
import tempfile
import time


RECIPE = "scratch-v1"
SOURCE_PATHS = ("go.mod", "go.sum", "cmd/nerocd-storage-archive", "internal/storagearchive")
MAX_SOURCE_BYTES = 2 * 1024 * 1024
MAX_EXECUTABLE_BYTES = 16 * 1024 * 1024
MAX_MEMBERS = 128


class PreparationError(Exception):
    pass


def run(command, cwd, env, timeout=60, limit=65536):
    """Bound output and lifetime; kill and join only this new process group."""
    process = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                               start_new_session=True)
    data = bytearray()
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise PreparationError("preparation subprocess timed out")
                for key, _ in selector.select(min(remaining, 0.1)):
                    chunk = os.read(key.fd, 65536)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    if len(data) + len(chunk) > limit:
                        raise PreparationError("preparation subprocess exceeded output budget")
                    data.extend(chunk)
        status = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        if status:
            raise PreparationError("preparation subprocess failed")
        return bytes(data)
    except BaseException as error:
        # start_new_session gives this invocation its own PGID. The unreaped
        # process identity is retained until this exact group is terminated.
        try:
            if process.returncode is None:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            process.wait(timeout=5)
        except (OSError, subprocess.TimeoutExpired) as cleanup_error:
            raise PreparationError("preparation failed and subprocess cleanup failed") from cleanup_error
        if isinstance(error, subprocess.TimeoutExpired):
            raise PreparationError("preparation subprocess timed out") from error
        raise
    finally:
        process.stdout.close()


def clean_environment(temporary):
    # Do not inherit GOFLAGS/-overlay, GOENV, alternate compilers, cache helpers,
    # toolchain downloads or proxy settings from the caller.
    home = Path.home()
    return {"PATH": os.defpath, "HOME": str(home), "LANG": "C", "LC_ALL": "C",
            "TMPDIR": str(temporary), "GOWORK": "off", "GOENV": "off",
            "GOTOOLCHAIN": "local", "GOFLAGS": "", "GOPROXY": "off", "GOSUMDB": "off",
            "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "arm64", "GOARM64": "v8.0",
            "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_NO_REPLACE_OBJECTS": "1", "GIT_NO_LAZY_FETCH": "1", "GIT_TERMINAL_PROMPT": "0",
            "GOPATH": str(home / "go"), "GOMODCACHE": str(home / "go/pkg/mod"),
            "GOCACHE": str(temporary / "go-cache")}


def git(repo, env, *args, limit=65536):
    return run(["/usr/bin/git", "-c", "core.fsmonitor=false", "-C", str(repo), *args],
               repo, env, limit=limit)


def check_source(repo, env, revision=None):
    if git(repo, env, "rev-parse", "--show-toplevel").decode().strip() != str(repo):
        raise PreparationError("script must belong to the repository root")
    current = git(repo, env, "rev-parse", "HEAD").decode().strip()
    if len(current) != 40 or any(c not in "0123456789abcdef" for c in current):
        raise PreparationError("source must have a full commit identity")
    if revision is not None and revision != current:
        raise PreparationError("source revision changed during preparation")
    if git(repo, env, "status", "--porcelain=v1", "--untracked-files=no"):
        raise PreparationError("tracked source must be clean")
    return current


def output_path(value, repo):
    path = Path(value)
    if (not path.is_absolute() or ".." in path.parts or str(path) != value
            or any(ord(c) < 32 for c in value) or path.resolve() != path):
        raise PreparationError("output must be an absolute canonical path without traversal or symlinks")
    if path == repo or repo in path.parents:
        raise PreparationError("output must be outside the source checkout")
    if path.exists() or path.is_symlink():
        raise PreparationError("output must not already exist")
    parent = path.parent.stat()
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid() or parent.st_mode & 0o022:
        raise PreparationError("output parent must be owned and not writable by group or others")
    return path


def export_source(archive, target):
    if len(archive) > MAX_SOURCE_BYTES:
        raise PreparationError("tracked source exceeds archive budget")
    seen = set()
    total = 0
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as source:
        for member in source:
            path = PurePosixPath(member.name.rstrip("/"))
            if (str(path) != member.name.rstrip("/") or path.is_absolute() or ".." in path.parts
                    or member.name in seen or len(seen) >= MAX_MEMBERS
                    or not any(str(path) == p or str(path).startswith(p + "/")
                               or p.startswith(str(path) + "/") for p in SOURCE_PATHS)):
                raise PreparationError("invalid tracked source member")
            seen.add(member.name)
            destination = target.joinpath(*path.parts)
            if member.isdir():
                destination.mkdir(parents=True, exist_ok=True)
                continue
            if not member.isfile() or member.size < 0 or member.size > MAX_SOURCE_BYTES:
                raise PreparationError("source must contain only bounded regular files and directories")
            total += member.size
            if total > MAX_SOURCE_BYTES:
                raise PreparationError("tracked source exceeds payload budget")
            destination.parent.mkdir(parents=True, exist_ok=True)
            with source.extractfile(member) as reader, destination.open("xb") as writer:
                shutil.copyfileobj(reader, writer, length=65536)


def validate_executable(data):
    if not 64 <= len(data) <= MAX_EXECUTABLE_BYTES or data[:7] != b"\x7fELF\x02\x01\x01":
        raise PreparationError("helper must be a bounded little-endian ELF64 executable")
    kind, machine = struct.unpack_from("<HH", data, 16)
    offset = struct.unpack_from("<Q", data, 32)[0]
    size, count = struct.unpack_from("<HH", data, 54)
    if kind != 2 or machine != 183 or size != 56 or not 1 <= count <= 128 or offset + size * count > len(data):
        raise PreparationError("helper must be a linux/arm64 executable with bounded program headers")
    types = {struct.unpack_from("<I", data, offset + size * i)[0] for i in range(count)}
    if 1 not in types or types & {2, 3}:
        raise PreparationError("helper must be static, without dynamic headers or interpreter")


def prepare(repo, output, go):
    repo = repo.resolve(strict=True)
    output = output_path(output, repo)
    go = Path(go)
    if not go.is_absolute() or not go.is_file() or not os.access(go, os.X_OK):
        raise PreparationError("go must name an absolute executable path")
    go = go.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="nerocd-storage-archive-") as directory:
        temporary = Path(directory)
        env = clean_environment(temporary)
        revision = check_source(repo, env)
        recipe = git(repo, env, "show", f"{revision}:Dockerfile.storage-archive")
        exported = temporary / "source"
        exported.mkdir()
        archive = git(repo, env, "archive", "--format=tar", revision, *SOURCE_PATHS, limit=MAX_SOURCE_BYTES)
        export_source(archive, exported)
        toolchain = [line.split()[1] for line in (exported / "go.mod").read_text().splitlines()
                     if line.startswith("toolchain ")]
        if len(toolchain) != 1:
            raise PreparationError("source must pin exactly one Go toolchain")
        version = run([str(go), "version"], exported, env, limit=1024).decode().strip()
        if version.split()[:3] != ["go", "version", toolchain[0]]:
            raise PreparationError("selected Go executable does not match the required version")
        epoch = git(repo, env, "show", "-s", "--format=%ct", revision).decode().strip()
        if not epoch.isdecimal():
            raise PreparationError("invalid source timestamp")
        env["SOURCE_DATE_EPOCH"] = epoch
        executable = temporary / "nerocd-storage-archive"
        run([str(go), "build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags=-buildid=",
             "-o", str(executable), "./cmd/nerocd-storage-archive"], exported, env)
        with executable.open("rb") as file:
            data = file.read(MAX_EXECUTABLE_BYTES + 1)
        validate_executable(data)
        digest = hashlib.sha256(data).hexdigest()
        rendered = recipe
        for token, value in [(b"@SOURCE_COMMIT@", revision), (b"@EXECUTABLE_SHA256@", digest)]:
            if rendered.count(token) != 1:
                raise PreparationError("invalid fixed image recipe placeholder")
            rendered = rendered.replace(token, value.encode())
        check_source(repo, env, revision)
        metadata = {"schema_version": 1, "recipe": RECIPE, "platform": "linux/arm64",
                    "source_commit": revision, "source_date_epoch": int(epoch), "go_version": version,
                    "executable_sha256": digest, "recipe_sha256": hashlib.sha256(recipe).hexdigest(),
                    "dockerfile_sha256": hashlib.sha256(rendered).hexdigest(),
                    "status": "prepared_only", "image_config_id": None}
        # mkdir is the exclusive publication boundary. Never replace an existing
        # directory; a partial output is left for inspection if a write fails.
        output.mkdir(mode=0o700)
        context = output / "context"
        context.mkdir(mode=0o700)
        with (context / "nerocd-storage-archive").open("xb") as file:
            file.write(data)
        (context / "nerocd-storage-archive").chmod(0o555)
        with (context / "Dockerfile").open("xb") as file:
            file.write(rendered)
        with (output / "provenance.json").open("x", encoding="utf-8") as file:
            json.dump(metadata, file, sort_keys=True, indent=2)
            file.write("\n")
        return metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--go", default=shutil.which("go"))
    args = parser.parse_args()
    try:
        if args.go is None:
            raise PreparationError("a local Go executable is required")
        prepare(Path(__file__).resolve().parent.parent, args.output, args.go)
    except (PreparationError, OSError, ValueError, tarfile.TarError) as error:
        parser.exit(1, f"storage archive image preparation failed: {error}\n")


if __name__ == "__main__":
    main()
