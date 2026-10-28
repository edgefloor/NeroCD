#!/usr/bin/env python3
"""Run independent CI lanes without dropping any make-check gates."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess


ROOT = Path(__file__).resolve().parents[1]
RUNTIME = "nerocd/internal/runnerruntime"
STORE = "nerocd/internal/store"
SHARDS = 6
LANES = ["go", "race", *[f"runtime-{n}" for n in range(SHARDS)],
         *[f"postgres-{n}" for n in range(SHARDS)], "web"]
GO_GATES = ["ci-release-policy-gate", "verify"]
WEB_GATES = ["web-test", "build", "browser-smoke", "web-policy", "contract",
             "check-generated", "production-compose-policy-gate",
             "local-image-registry-gate", "docker-build"]


def discover_tests(output):
    names = [line for line in output.splitlines()
             if re.fullmatch(r"(?:Test|Example|Fuzz)\w*", line)]
    if not names or len(names) != len(set(names)):
        raise ValueError("test inventory is empty or contains duplicates")
    return sorted(names)


def shard_tests(names, index):
    if index not in range(SHARDS):
        raise ValueError("invalid test shard")
    selected = names[index::SHARDS]
    if not selected:
        raise ValueError("test shard is empty")
    return selected


def run(command, capture=False):
    print("+ " + " ".join(command), flush=True)
    env = {**os.environ, "GOCACHE": str(ROOT / ".cache/go-build")}
    return subprocess.run(command, cwd=ROOT, env=env, check=True,
                          text=True, stdout=subprocess.PIPE if capture else None)


def check_lane(lane):
    if lane == "go":
        run(["make", *GO_GATES])
    elif lane == "web":
        run(["make", *WEB_GATES])
    elif lane == "race":
        packages = run(["go", "list", "./cmd/...", "./internal/..."], capture=True).stdout.splitlines()
        if packages.count(RUNTIME) != 1:
            raise ValueError("runtime package missing or duplicated in race inventory")
        packages.remove(RUNTIME)
        if not packages:
            raise ValueError("non-runtime race inventory is empty")
        run(["go", "test", "-race", "-timeout=10m", *packages])
    elif lane in LANES:
        group, number = lane.rsplit("-", 1)
        index = int(number)
        package = RUNTIME if group == "runtime" else STORE
        flags = ["-race"] if group == "runtime" else ["-count=1"]
        if group == "postgres" and not os.environ.get("NEROCD_TEST_DATABASE_URL", "").strip():
            raise ValueError("PostgreSQL shards require NEROCD_TEST_DATABASE_URL; skipping is forbidden")
        # Build and discover with the same tags/instrumentation as execution.
        output = run(["go", "test", *flags, "-list", ".", package], capture=True).stdout
        names = discover_tests(output)
        selected = shard_tests(names, index)
        print(f"{group} shard {index + 1}/{SHARDS}: {len(selected)}/{len(names)} tests", flush=True)
        pattern = "^(?:" + "|".join(re.escape(name) for name in selected) + ")$"
        timeout = "10m" if group == "runtime" else "15m"
        run(["go", "test", *flags, f"-timeout={timeout}", "-run", pattern, package])
    else:
        raise ValueError("unknown CI lane")


def validate_workflow(root):
    """Fail closed if the fixed CI matrix or required aggregate is weakened."""
    source = (root / ".github/workflows/check.yml").read_text()
    def step(name):
        match = re.search(r"^      - name: " + re.escape(name) + r"\n(.*?)(?=^      - name:|^  \w+:|\Z)", source, re.M | re.S)
        if not match:
            raise ValueError(f"missing CI step: {name}")
        return match[1]

    match = re.search(r"^        lane: (\[.*\])$", source, re.M)
    if not match or json.loads(match[1]) != LANES:
        raise ValueError("check matrix must include every CI lane exactly once")
    bun = re.search(r"      - name: Set up Bun\n(.*?)(?=\n      - name:)", source, re.S)
    if not bun or "if: matrix.lane == 'go' || matrix.lane == 'race' || matrix.lane == 'web'" not in bun[1]:
        raise ValueError("Go and web lanes require pinned Bun for enrollment contract coverage")
    verify, aggregate = source.split("\n  check:\n", 1)
    for required in ["  verify:\n", "      fail-fast: false\n",
                     '        run: python3 -B scripts/ci-check.py "${{ matrix.lane }}"\n']:
        if required not in verify:
            raise ValueError("check matrix must execute all lanes without fail-fast")
    if re.search(r"^    (if:|continue-on-error:)", verify, re.M) or "continue-on-error:" in source:
        raise ValueError("check matrix cannot skip jobs or tolerate failure")
    for name in ["Run checks", "Require every verification lane"]:
        if re.search(r"^        if:", step(name), re.M):
            raise ValueError(f"CI step must be unconditional: {name}")
    database_env = "          NEROCD_TEST_DATABASE_URL: ${{ startsWith(matrix.lane, 'postgres-') && 'postgres://nerocd:nerocd_dev@127.0.0.1:5432/nerocd?sslmode=disable' || '' }}\n"
    if database_env not in step("Run checks"):
        raise ValueError("PostgreSQL shards must use their isolated service database")
    for name, lane, command in [
        ("Generate release artifacts", "web", "make release-artifacts"),
    ]:
        block = step(name)
        if f"        if: matrix.lane == '{lane}'\n" not in block or f"        run: {command}\n" not in block:
            raise ValueError(f"CI step must retain its lane and command: {name}")
    for required in ["    needs: [verify]\n", "    if: ${{ always() }}\n",
                     "          VERIFY_RESULT: ${{ needs.verify.result }}\n",
                     '        run: test "$VERIFY_RESULT" = success\n']:
        if required not in aggregate:
            raise ValueError("required check must fail unless every lane succeeds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("lane", nargs="?", choices=LANES)
    parser.add_argument("--validate-workflow", type=Path)
    args = parser.parse_args()
    if args.validate_workflow:
        validate_workflow(args.validate_workflow)
    elif args.lane:
        check_lane(args.lane)
    else:
        parser.error("a lane or --validate-workflow is required")


if __name__ == "__main__":
    main()
