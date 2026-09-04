#!/usr/bin/env python3
"""Validate delegation packets against an explicit runtime attestation."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from orchestration.protocol import ProtocolError, load_json, preflight, run_skills_validator


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--capabilities", required=True, type=Path)
    parser.add_argument("--packet", required=True, action="append", type=Path)
    parser.add_argument(
        "--repo-root", action="append", type=Path,
        help="one clean checkout per packet, in the same order (defaults to cwd for one packet)",
    )
    parser.add_argument("--skills-validator", type=Path)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    try:
        packets = [load_json(path) for path in args.packet]
        repos = args.repo_root
        if repos is None:
            if len(packets) != 1:
                raise ProtocolError("multiple packets require one --repo-root per packet")
            repos = [Path.cwd()]
        installed_skills = run_skills_validator(args.skills_validator) if args.skills_validator else None
        preflight(packets, load_json(args.capabilities), repos, installed_skills)
    except ProtocolError as exc:
        payload = {"ok": False, "error": str(exc)}
        print(json.dumps(payload) if args.json else f"preflight failed: {exc}")
        return 1
    payload = {"ok": True, "packets": [packet["task_id"] for packet in packets]}
    print(json.dumps(payload) if args.json else "preflight passed: " + ", ".join(payload["packets"]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
